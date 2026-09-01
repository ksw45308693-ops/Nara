package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"g2b-monitor/internal/auth"
	"g2b-monitor/internal/digest"
	"g2b-monitor/internal/model"
	"g2b-monitor/internal/store"
	appweb "g2b-monitor/internal/web"
	"g2b-monitor/migrations"
)

const inPlaceTestDatabasePrefix = "g2b_monitor_test_"

type releasePostgresHarness struct {
	ownerPool   *pgxpool.Pool
	runtimePool *pgxpool.Pool
	control     *pgx.Conn
	database    string
	created     bool
}

func TestPostgresReleaseContracts(t *testing.T) {
	ownerURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_OWNER_URL"))
	runtimeURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_RUNTIME_URL"))
	if ownerURL == "" || runtimeURL == "" {
		t.Skip("TEST_POSTGRES_OWNER_URL and TEST_POSTGRES_RUNTIME_URL are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	harness := newReleasePostgresHarness(t, ctx, ownerURL, runtimeURL)
	defer harness.close(t)

	assets, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}
	migrator := store.PgxMigrationBeginner{DB: harness.ownerPool}
	preHardening := migrationsBefore(t, assets, 4)
	if err := store.ApplyMigrations(ctx, migrator, preHardening); err != nil {
		t.Fatalf("apply migrations before 0004: %v", err)
	}
	legacyRecipients := seedLegacyRecipientDuplicates(t, ctx, harness.ownerPool)
	if err := store.ApplyMigrations(ctx, migrator, assets); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := store.ApplyMigrations(ctx, migrator, assets); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	assertMigrationLedger(t, ctx, harness.ownerPool, assets)
	t.Run("0005 replaces legacy onboarding functions", func(t *testing.T) {
		assertInvitationUpgradeFunctions(t, ctx, harness.ownerPool)
	})
	t.Run("0004 merges legacy case-duplicate recipients and references", func(t *testing.T) {
		assertLegacyRecipientMerge(t, ctx, harness.ownerPool, legacyRecipients)
	})

	changed := append([]store.Migration(nil), assets...)
	changed[len(changed)-1].SQL += "\n-- deliberate integration-test checksum mutation\n"
	if err := store.ApplyMigrations(ctx, migrator, changed); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("changed applied migration was not rejected: %v", err)
	}
	assertMigrationLedger(t, ctx, harness.ownerPool, assets)

	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_RUNTIME_URL: %v", err)
	}
	runtimeConfig.Database = harness.database
	harness.runtimePool, err = OpenRuntimePool(ctx, runtimeConfig.ConnString())
	if err != nil {
		t.Fatalf("open migrated database with runtime role: %v", err)
	}

	t.Run("force RLS blocks cross-tenant reads and writes", func(t *testing.T) {
		testForceRLS(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("daily API budget is atomic across two pools", func(t *testing.T) {
		testDailyAPIBudgetAcrossPools(t, ctx, harness.ownerPool, harness.runtimePool, runtimeConfig.ConnString())
	})
	t.Run("legacy recipient delivery resumes with survivor key", func(t *testing.T) {
		testLegacyRecipientDeliveryReclaim(t, ctx, harness.ownerPool, harness.runtimePool, legacyRecipients)
	})
	t.Run("notice region lookup state and active query persist", func(t *testing.T) {
		testNoticeRegionLookupState(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("runtime cannot mutate invitations directly", func(t *testing.T) {
		testRuntimeInvitationPrivileges(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("delivery claim token fences an expired lease", func(t *testing.T) {
		testDeliveryClaimFencing(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("expired partial digest closes without retry and preserves sent history", func(t *testing.T) {
		testExpiredPartialDigestTerminalization(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("only one concurrent invitation acceptance succeeds", func(t *testing.T) {
		testConcurrentInvitationAcceptance(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("accept and reinvite serialize to one valid state", func(t *testing.T) {
		testConcurrentAcceptAndReinvite(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("initial administrator replacement revokes old bearer only", func(t *testing.T) {
		testInitialAdministratorReplacement(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("expired invitation email is reusable but live bearer still conflicts", func(t *testing.T) {
		testExpiredInvitationEmailReuse(t, ctx, harness.ownerPool, harness.runtimePool)
	})
	t.Run("concurrent invitation email claims serialize", func(t *testing.T) {
		testConcurrentInvitationEmailClaims(t, ctx, harness.ownerPool, harness.runtimePool)
	})
}

func assertInvitationUpgradeFunctions(t *testing.T, ctx context.Context, owner *pgxpool.Pool) {
	t.Helper()
	for _, signature := range []string{
		"public.onboarding_create_tenant(uuid,text,text,text,text,text,timestamp with time zone)",
		"public.onboarding_invite_member(uuid,uuid,text,text,text,text,timestamp with time zone)",
		"public.onboarding_accept_invitation(text,text,text)",
	} {
		var definition, functionOwner string
		var runtimeCanExecute, publicCanExecute bool
		err := owner.QueryRow(ctx, `SELECT pg_catalog.pg_get_functiondef($1::regprocedure), function_owner.rolname,
       pg_catalog.has_function_privilege('g2b_runtime', $1, 'EXECUTE'),
       EXISTS (
           SELECT 1
           FROM pg_catalog.aclexplode(coalesce(proc.proacl, pg_catalog.acldefault('f', proc.proowner))) acl
           WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
       )
FROM pg_catalog.pg_proc proc
JOIN pg_catalog.pg_roles function_owner ON function_owner.oid = proc.proowner
WHERE proc.oid = $1::regprocedure`, signature).Scan(&definition, &functionOwner, &runtimeCanExecute, &publicCanExecute)
		if err != nil {
			t.Fatalf("read upgraded function %s: %v", signature, err)
		}
		if !strings.Contains(definition, "g2b-tenant-onboarding:") ||
			!strings.Contains(definition, "g2b-invitation:") ||
			!strings.Contains(definition, "email already belongs to an account") {
			t.Fatalf("function %s does not include tenant/email locks and account recheck", signature)
		}
		if strings.Index(definition, "g2b-tenant-onboarding:") > strings.Index(definition, "g2b-invitation:") {
			t.Fatalf("function %s does not lock tenant before email", signature)
		}
		if !strings.Contains(signature, "onboarding_accept_invitation") &&
			!strings.Contains(definition, "expires_at <= clock_timestamp()") {
			t.Fatalf("function %s does not close expired pending email rows", signature)
		}
		if functionOwner != "g2b_onboarding_definer" {
			t.Fatalf("function %s owner=%q", signature, functionOwner)
		}
		if !runtimeCanExecute || publicCanExecute {
			t.Fatalf("function %s runtime execute=%t public execute=%t", signature, runtimeCanExecute, publicCanExecute)
		}
	}
}

func migrationsBefore(t *testing.T, assets []store.Migration, version int) []store.Migration {
	t.Helper()
	var before []store.Migration
	found := false
	for _, migration := range assets {
		if migration.Version == version {
			found = true
		}
		if migration.Version < version {
			before = append(before, migration)
		}
	}
	if !found {
		t.Fatalf("required migration %04d is missing", version)
	}
	return before
}

type legacyRecipientFixture struct {
	tenantID, scheduleID, keptRecipientID, legacyKey string
	dueAt, windowEnd                                 time.Time
}

func seedLegacyRecipientDuplicates(t *testing.T, ctx context.Context, owner *pgxpool.Pool) legacyRecipientFixture {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Legacy Recipient")
	var scheduleID, keptRecipientID, duplicateRecipientID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.schedules (tenant_id,name,hour,minute,timezone)
VALUES ($1::uuid,'레거시 병합',7,0,'Asia/Seoul') RETURNING id::text`, tenantID).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.recipients (tenant_id,email,name,created_at)
VALUES ($1::uuid,'Legacy.Duplicate@example.com','기존',now()-interval '2 hours') RETURNING id::text`, tenantID).Scan(&keptRecipientID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.recipients (tenant_id,email,name,created_at)
VALUES ($1::uuid,' legacy.duplicate@EXAMPLE.com ','중복',now()-interval '1 hour') RETURNING id::text`, tenantID).Scan(&duplicateRecipientID); err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	windowEnd := dueAt.Add(30 * time.Minute)
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_windows
(tenant_id,schedule_id,due_at,window_end_at,status) VALUES ($1::uuid,$2::uuid,$3,$4,'pending')`, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []struct{ id, email string }{
		{keptRecipientID, "Legacy.Duplicate@example.com"},
		{duplicateRecipientID, " legacy.duplicate@EXAMPLE.com "},
	} {
		if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_recipients
(tenant_id,schedule_id,due_at,window_end_at,recipient_id,email)
VALUES ($1::uuid,$2::uuid,$3,$4,$5::uuid,$6)`, tenantID, scheduleID, dueAt, windowEnd, recipient.id, recipient.email); err != nil {
			t.Fatal(err)
		}
	}
	const legacyKey = "removed-recipient-delivery-key"
	if _, err := owner.Exec(ctx, `INSERT INTO public.deliveries
(tenant_id,schedule_id,recipient_id,idempotency_key,due_at,window_end_at,status,attempts,sent_at)
	VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,'failed',1,NULL)`, tenantID, scheduleID, duplicateRecipientID, legacyKey, dueAt, windowEnd); err != nil {
		t.Fatal(err)
	}
	return legacyRecipientFixture{tenantID: tenantID, scheduleID: scheduleID, keptRecipientID: keptRecipientID, legacyKey: legacyKey, dueAt: dueAt, windowEnd: windowEnd}
}

func assertLegacyRecipientMerge(t *testing.T, ctx context.Context, owner *pgxpool.Pool, fixture legacyRecipientFixture) {
	t.Helper()
	var recipientCount, deliveryCount, snapshotCount int
	var recipientID, email, deliveryStatus, deliveryKey string
	err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.recipients WHERE tenant_id=$1::uuid),
(SELECT id::text FROM public.recipients WHERE tenant_id=$1::uuid),
(SELECT email FROM public.recipients WHERE tenant_id=$1::uuid),
(SELECT count(*) FROM public.deliveries WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3),
(SELECT status FROM public.deliveries WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3),
(SELECT idempotency_key FROM public.deliveries WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3),
(SELECT count(*) FROM public.digest_window_recipients WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3 AND window_end_at=$4)`,
		fixture.tenantID, fixture.scheduleID, fixture.dueAt, fixture.windowEnd,
	).Scan(&recipientCount, &recipientID, &email, &deliveryCount, &deliveryStatus, &deliveryKey, &snapshotCount)
	if err != nil {
		t.Fatal(err)
	}
	if recipientCount != 1 || recipientID != fixture.keptRecipientID || email != "legacy.duplicate@example.com" {
		t.Fatalf("merged recipient count=%d id=%s email=%q", recipientCount, recipientID, email)
	}
	if deliveryCount != 1 || deliveryStatus != "failed" || deliveryKey != fixture.legacyKey || snapshotCount != 1 {
		t.Fatalf("merged delivery count=%d status=%s key=%q snapshot count=%d", deliveryCount, deliveryStatus, deliveryKey, snapshotCount)
	}
	var deliveryRecipient, snapshotRecipient, snapshotEmail string
	if err := owner.QueryRow(ctx, `SELECT d.recipient_id::text,s.recipient_id::text,s.email
FROM public.deliveries d JOIN public.digest_window_recipients s
ON s.tenant_id=d.tenant_id AND s.schedule_id=d.schedule_id AND s.due_at=d.due_at
AND s.window_end_at=d.window_end_at AND s.recipient_id=d.recipient_id
WHERE d.tenant_id=$1::uuid AND d.schedule_id=$2::uuid AND d.due_at=$3`,
		fixture.tenantID, fixture.scheduleID, fixture.dueAt,
	).Scan(&deliveryRecipient, &snapshotRecipient, &snapshotEmail); err != nil {
		t.Fatal(err)
	}
	if deliveryRecipient != fixture.keptRecipientID || snapshotRecipient != fixture.keptRecipientID || snapshotEmail != email {
		t.Fatalf("merged references delivery=%s snapshot=%s email=%q", deliveryRecipient, snapshotRecipient, snapshotEmail)
	}
}

func testLegacyRecipientDeliveryReclaim(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool, fixture legacyRecipientFixture) {
	t.Helper()
	key := digest.DeliveryKey(fixture.tenantID, fixture.scheduleID, fixture.keptRecipientID, fixture.dueAt)
	claim := DeliveryClaim{
		TenantID: fixture.tenantID, ScheduleID: fixture.scheduleID, RecipientID: fixture.keptRecipientID,
		IdempotencyKey: key, DueAt: fixture.dueAt, WindowEnd: fixture.windowEnd,
	}
	reservation, err := (&PostgresRepository{Pool: runtime}).ClaimDelivery(ctx, claim)
	if err != nil || !reservation.Claimed || reservation.Attempts != 2 || reservation.ClaimToken == "" {
		t.Fatalf("legacy delivery reservation=%+v error=%v", reservation, err)
	}
	var storedKey, status, recipientID string
	if err := owner.QueryRow(ctx, `SELECT idempotency_key,status,recipient_id::text
		FROM public.deliveries WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid
		AND due_at=$3 AND window_end_at=$4`, fixture.tenantID, fixture.scheduleID, fixture.dueAt, fixture.windowEnd).Scan(&storedKey, &status, &recipientID); err != nil {
		t.Fatal(err)
	}
	if storedKey != key || status != "sending" || recipientID != fixture.keptRecipientID {
		t.Fatalf("normalized legacy delivery key=%q status=%s recipient=%s", storedKey, status, recipientID)
	}
}

func newReleasePostgresHarness(t *testing.T, ctx context.Context, ownerURL, runtimeURL string) *releasePostgresHarness {
	t.Helper()
	ownerConfig, err := pgx.ParseConfig(ownerURL)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_OWNER_URL: %v", err)
	}
	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_RUNTIME_URL: %v", err)
	}
	if ownerConfig.Host != runtimeConfig.Host || ownerConfig.Port != runtimeConfig.Port {
		t.Fatal("owner and runtime URLs must point to the same PostgreSQL server")
	}
	if ownerConfig.Database == "" || runtimeConfig.Database == "" {
		t.Fatal("owner and runtime URLs must name a database")
	}

	control, err := pgx.ConnectConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatalf("connect migration owner: %v", err)
	}
	harness := &releasePostgresHarness{control: control}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		_ = control.Close(ctx)
		t.Fatalf("create temporary database name: %v", err)
	}
	harness.database = inPlaceTestDatabasePrefix + hex.EncodeToString(suffix)
	_, createErr := control.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{harness.database}.Sanitize())
	if createErr == nil {
		harness.created = true
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(createErr, &postgresError) || postgresError.Code != "42501" {
			_ = control.Close(ctx)
			t.Fatalf("create disposable PostgreSQL database: %v", createErr)
		}
		allowedDatabase := strings.TrimSpace(os.Getenv("TEST_POSTGRES_ALLOW_IN_PLACE"))
		if ownerConfig.Database != runtimeConfig.Database || allowedDatabase != ownerConfig.Database || !strings.HasPrefix(ownerConfig.Database, inPlaceTestDatabasePrefix) {
			_ = control.Close(ctx)
			t.Skipf("owner lacks CREATEDB; to erase and reuse a disposable database named %s, set TEST_POSTGRES_ALLOW_IN_PLACE to that exact name", inPlaceTestDatabasePrefix+"...")
		}
		harness.database = ownerConfig.Database
		if err := resetPublicSchema(ctx, control); err != nil {
			_ = control.Close(ctx)
			t.Fatalf("reset explicitly authorized disposable database: %v", err)
		}
	}

	ownerTarget := ownerConfig.Copy()
	ownerTarget.Database = harness.database
	harness.ownerPool, err = OpenOwnerPool(ctx, ownerTarget.ConnString())
	if err != nil {
		harness.close(t)
		t.Fatalf("open disposable migration database: %v", err)
	}
	return harness
}

func (h *releasePostgresHarness) close(t *testing.T) {
	t.Helper()
	if h == nil || h.control == nil {
		return
	}
	if h.runtimePool != nil {
		h.runtimePool.Close()
		h.runtimePool = nil
	}
	if h.ownerPool != nil {
		h.ownerPool.Close()
		h.ownerPool = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if h.created {
		if _, err := h.control.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{h.database}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable PostgreSQL database %s: %v", h.database, err)
		}
	} else if err := resetPublicSchema(ctx, h.control); err != nil {
		t.Errorf("clean explicitly authorized disposable database %s: %v", h.database, err)
	}
	if err := h.control.Close(ctx); err != nil {
		t.Errorf("close migration owner connection: %v", err)
	}
	h.control = nil
}

func resetPublicSchema(ctx context.Context, conn *pgx.Conn) error {
	var owner string
	if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&owner); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA public AUTHORIZATION "+pgx.Identifier{owner}.Sanitize()); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, `GRANT USAGE ON SCHEMA public TO PUBLIC`)
	return err
}

func assertMigrationLedger(t *testing.T, ctx context.Context, owner *pgxpool.Pool, assets []store.Migration) {
	t.Helper()
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migration ledger: %v", err)
	}
	if count != len(assets) {
		t.Fatalf("migration ledger rows=%d, want %d", count, len(assets))
	}
	for _, migration := range assets {
		var checksum string
		if err := owner.QueryRow(ctx, `SELECT checksum FROM public.schema_migrations WHERE version=$1`, migration.Version).Scan(&checksum); err != nil {
			t.Fatalf("read migration %d checksum: %v", migration.Version, err)
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(migration.SQL)))
		if checksum != want {
			t.Fatalf("migration %d checksum=%q, want %q", migration.Version, checksum, want)
		}
	}
}

func testDailyAPIBudgetAcrossPools(t *testing.T, ctx context.Context, owner, firstPool *pgxpool.Pool, runtimeURL string) {
	t.Helper()
	secondPool, err := OpenRuntimePool(ctx, runtimeURL)
	if err != nil {
		t.Fatalf("open second runtime pool: %v", err)
	}
	defer secondPool.Close()

	const limit = 17
	const attempts = 40
	fixedNow := time.Date(2042, time.March, 4, 12, 0, 0, 0, time.UTC)
	budgets := []PostgresDailyCallBudget{
		{DB: firstPool, Limit: limit, Now: func() time.Time { return fixedNow }},
		{DB: secondPool, Limit: limit, Now: func() time.Time { return fixedNow }},
	}
	results := make(chan error, attempts)
	for attempt := range attempts {
		budget := budgets[attempt%len(budgets)]
		go func() { results <- budget.Take(ctx) }()
	}
	succeeded, exhausted := 0, 0
	for range attempts {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDailyAPICallBudget):
			exhausted++
		default:
			t.Fatalf("consume concurrent API budget: %v", err)
		}
	}
	if succeeded != limit || exhausted != attempts-limit {
		t.Fatalf("budget successes=%d exhausted=%d, want %d/%d", succeeded, exhausted, limit, attempts-limit)
	}
	if err := budgets[1].Take(ctx); !errors.Is(err, ErrDailyAPICallBudget) {
		t.Fatalf("post-cap budget error=%v", err)
	}
	seoulDay := fixedNow.In(time.FixedZone("Asia/Seoul", 9*60*60)).Format("2006-01-02")
	var calls int
	if err := owner.QueryRow(ctx, `SELECT calls FROM public.api_daily_usage WHERE usage_day=$1::date`, seoulDay).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != limit {
		t.Fatalf("persisted API calls=%d, want %d", calls, limit)
	}
}

func testNoticeRegionLookupState(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2041, time.June, 2, 0, 0, 0, 0, time.UTC)
	activeNotice := model.Notice{
		Category: model.CategoryGoods, BidNumber: "REGION-2041-1", BidSequence: "00",
		Title: "지역 조회 대기 공고", Agency: "테스트 기관", PostedAt: now.Add(-time.Hour), Deadline: now.Add(24 * time.Hour),
	}
	noticeID, defaultComplete := insertNoticeWithoutRegionState(t, ctx, owner, activeNotice)
	if defaultComplete {
		t.Fatal("region_lookup_complete defaulted to true for a notice without a region")
	}
	expiredNotice := activeNotice
	expiredNotice.BidNumber = "REGION-2041-EXPIRED"
	expiredNotice.Title = "마감된 공고"
	expiredNotice.Deadline = now.Add(-time.Minute)
	insertNoticeWithoutRegionState(t, ctx, owner, expiredNotice)

	repository := &PostgresRepository{Pool: runtime}
	active, err := repository.ActiveNotices(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != noticeID || active[0].Notice.BidNumber != activeNotice.BidNumber || active[0].RegionLookupComplete {
		t.Fatalf("active pending notices=%+v", active)
	}
	if err := repository.MarkRegionLookupComplete(ctx, noticeID); err != nil {
		t.Fatalf("mark region lookup complete: %v", err)
	}
	active, err = repository.ActiveNotices(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || !active[0].RegionLookupComplete {
		t.Fatalf("completed active notices=%+v", active)
	}
	var persisted bool
	if err := owner.QueryRow(ctx, `SELECT region_lookup_complete FROM public.notices WHERE id=$1::uuid`, noticeID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted {
		t.Fatal("completed region lookup state was not persisted")
	}
}

func insertNoticeWithoutRegionState(t *testing.T, ctx context.Context, owner *pgxpool.Pool, notice model.Notice) (string, bool) {
	t.Helper()
	identity, err := hex.DecodeString(notice.Identity())
	if err != nil {
		t.Fatal(err)
	}
	revision, err := hex.DecodeString(notice.Revision())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	var noticeID string
	var regionLookupComplete bool
	err = owner.QueryRow(ctx, `INSERT INTO public.notices
(identity_hash,revision_hash,source_id,title,published_at,deadline_at,payload)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,region_lookup_complete`,
		identity, revision, notice.BidNumber, notice.Title, notice.PostedAt, notice.Deadline, payload,
	).Scan(&noticeID, &regionLookupComplete)
	if err != nil {
		t.Fatal(err)
	}
	return noticeID, regionLookupComplete
}

func testForceRLS(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantA := insertTenant(t, ctx, owner, "RLS A")
	tenantB := insertTenant(t, ctx, owner, "RLS B")
	if _, err := owner.Exec(ctx, `INSERT INTO public.filters (tenant_id,name,rules) VALUES
($1::uuid,'A filter','{}'::jsonb),($2::uuid,'B filter','{}'::jsonb)`, tenantA, tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.job_runs
(tenant_id,kind,status,started_at,finished_at,detail) VALUES
($1::uuid,'digest','failed',now(),now(),'{}'::jsonb),
($2::uuid,'digest','failed',now(),now(),'{}'::jsonb)`, tenantA, tenantB); err != nil {
		t.Fatal(err)
	}
	var forced bool
	if err := owner.QueryRow(ctx, `SELECT relforcerowsecurity FROM pg_catalog.pg_class WHERE oid='public.filters'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("filters FORCE RLS=%t err=%v", forced, err)
	}
	if err := owner.QueryRow(ctx, `SELECT relforcerowsecurity FROM pg_catalog.pg_class WHERE oid='public.job_runs'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("job_runs FORCE RLS=%t err=%v", forced, err)
	}

	tx, err := runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('app.tenant_id',$1,true)`, tenantA); err != nil {
		t.Fatal(err)
	}
	var own, cross int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.filters`).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.filters WHERE tenant_id=$1::uuid`, tenantB).Scan(&cross); err != nil {
		t.Fatal(err)
	}
	if own != 1 || cross != 0 {
		t.Fatalf("tenant A sees total=%d cross-tenant=%d", own, cross)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.job_runs`).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.job_runs WHERE tenant_id=$1::uuid`, tenantB).Scan(&cross); err != nil {
		t.Fatal(err)
	}
	if own != 1 || cross != 0 {
		t.Fatalf("tenant A sees job runs total=%d cross-tenant=%d", own, cross)
	}
	jobAttempt, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = jobAttempt.Exec(ctx, `INSERT INTO public.job_runs (tenant_id,kind,status,finished_at)
VALUES ($1::uuid,'digest','failed',now())`, tenantB)
	assertPostgresCode(t, err, "42501")
	if err := jobAttempt.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.filters (tenant_id,name,rules) VALUES ($1::uuid,'cross write','{}'::jsonb)`, tenantB)
	assertPostgresCode(t, err, "42501")
}

func testRuntimeInvitationPrivileges(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Privilege")
	hash := integrationHash(t.Name() + "-existing")
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at)
VALUES ($1::uuid,'privilege@example.com','권한','member',$2,now()+interval '1 hour')`, tenantID, hash); err != nil {
		t.Fatal(err)
	}
	for _, privilege := range []string{"INSERT", "UPDATE", "DELETE"} {
		var allowed bool
		if err := runtime.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege(current_user,'public.invitations',$1)`, privilege).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Fatalf("runtime unexpectedly has %s on invitations", privilege)
		}
	}

	mutations := []struct {
		name string
		sql  string
		args []any
	}{
		{"insert", `INSERT INTO public.invitations (tenant_id,email,display_name,role,token_hash,expires_at) VALUES ($1::uuid,'direct@example.com','직접','member',$2,now()+interval '1 hour')`, []any{tenantID, integrationHash(t.Name() + "-insert")}},
		{"update", `UPDATE public.invitations SET display_name='변조' WHERE tenant_id=$1::uuid`, []any{tenantID}},
		{"delete", `DELETE FROM public.invitations WHERE tenant_id=$1::uuid`, []any{tenantID}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			tx, err := runtime.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('app.tenant_id',$1,true)`, tenantID); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, mutation.sql, mutation.args...)
			assertPostgresCode(t, err, "42501")
		})
	}
}

func testDeliveryClaimFencing(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Delivery")
	var scheduleID, recipientID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.schedules (tenant_id,name,hour,minute,timezone)
VALUES ($1::uuid,'배송',7,0,'Asia/Seoul') RETURNING id::text`, tenantID).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.recipients (tenant_id,email,name)
VALUES ($1::uuid,'delivery@example.com','수신자') RETURNING id::text`, tenantID).Scan(&recipientID); err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	windowEnd := dueAt.Add(30 * time.Minute)
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_windows
(tenant_id,schedule_id,due_at,window_end_at,status) VALUES ($1::uuid,$2::uuid,$3,$4,'pending')`, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_recipients
(tenant_id,schedule_id,due_at,window_end_at,recipient_id,email)
VALUES ($1::uuid,$2::uuid,$3,$4,$5::uuid,'delivery@example.com')`, tenantID, scheduleID, dueAt, windowEnd, recipientID); err != nil {
		t.Fatal(err)
	}

	repository := &PostgresRepository{Pool: runtime}
	claim := DeliveryClaim{
		TenantID: tenantID, ScheduleID: scheduleID, RecipientID: recipientID,
		IdempotencyKey: "release-fencing", DueAt: dueAt, WindowEnd: windowEnd,
	}
	first, err := repository.ClaimDelivery(ctx, claim)
	if err != nil || !first.Claimed || first.ClaimToken == "" || first.Attempts != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE public.deliveries SET claimed_at=now()-interval '16 minutes'
WHERE tenant_id=$1::uuid AND idempotency_key=$2`, tenantID, claim.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	second, err := repository.ClaimDelivery(ctx, claim)
	if err != nil || !second.Claimed || second.ClaimToken == "" || second.ClaimToken == first.ClaimToken || second.Attempts != 2 {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	stale := claim
	stale.ClaimToken = first.ClaimToken
	if err := repository.FinalizeSent(ctx, stale, second.Attempts, time.Now().UTC()); !errors.Is(err, store.ErrDeliveryNotClaimed) {
		t.Fatalf("stale claim finalize error=%v", err)
	}
	claim.ClaimToken = second.ClaimToken
	if err := repository.FinalizeSent(ctx, claim, second.Attempts, time.Now().UTC()); err != nil {
		t.Fatalf("current claim finalize: %v", err)
	}
	var deliveryStatus, windowStatus string
	if err := owner.QueryRow(ctx, `SELECT d.status,w.status FROM public.deliveries d
JOIN public.digest_windows w ON w.tenant_id=d.tenant_id AND w.schedule_id=d.schedule_id AND w.due_at=d.due_at
WHERE d.tenant_id=$1::uuid AND d.idempotency_key=$2`, tenantID, claim.IdempotencyKey).Scan(&deliveryStatus, &windowStatus); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "sent" || windowStatus != "completed" {
		t.Fatalf("delivery=%s window=%s", deliveryStatus, windowStatus)
	}
}

func testExpiredPartialDigestTerminalization(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Expired Partial Digest")
	var scheduleID, sentRecipientID, failedRecipientID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.schedules (tenant_id,name,hour,minute,timezone)
VALUES ($1::uuid,'만료 부분 발송',7,0,'Asia/Seoul') RETURNING id::text`, tenantID).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.recipients (tenant_id,email,name)
VALUES ($1::uuid,'sent.expired@example.com','발송 완료') RETURNING id::text`, tenantID).Scan(&sentRecipientID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO public.recipients (tenant_id,email,name)
VALUES ($1::uuid,'failed.expired@example.com','발송 실패') RETURNING id::text`, tenantID).Scan(&failedRecipientID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	dueAt := now.Add(-time.Hour)
	windowEnd := now.Add(-10 * time.Minute)
	sentAt := windowEnd.Add(time.Minute)
	notice := model.Notice{
		Category: model.CategoryService, BidNumber: "EXPIRED-PARTIAL-1", BidSequence: "00",
		Title: "마감된 부분 발송 공고", Deadline: now.Add(-time.Minute),
	}
	repository := &PostgresRepository{Pool: runtime}
	stored, err := repository.StoreNotice(ctx, notice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_windows
(tenant_id,schedule_id,due_at,window_end_at,status) VALUES ($1::uuid,$2::uuid,$3,$4,'pending')`, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_recipients
(tenant_id,schedule_id,due_at,window_end_at,recipient_id,email) VALUES
($1::uuid,$2::uuid,$3,$4,$5::uuid,'sent.expired@example.com'),
($1::uuid,$2::uuid,$3,$4,$6::uuid,'failed.expired@example.com')`, tenantID, scheduleID, dueAt, windowEnd, sentRecipientID, failedRecipientID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_items
(tenant_id,schedule_id,due_at,window_end_at,match_id,notice_id,title,source_url,reasons,matched_at)
VALUES ($1::uuid,$2::uuid,$3,$4,gen_random_uuid(),$5::uuid,$6,'',
        '{"reasons":["include_any"]}'::jsonb,$4)`, tenantID, scheduleID, dueAt, windowEnd, stored.ID, notice.Title); err != nil {
		t.Fatal(err)
	}
	sentKey := digest.DeliveryKey(tenantID, scheduleID, sentRecipientID, dueAt)
	failedKey := digest.DeliveryKey(tenantID, scheduleID, failedRecipientID, dueAt)
	if _, err := owner.Exec(ctx, `INSERT INTO public.deliveries
(tenant_id,schedule_id,recipient_id,idempotency_key,due_at,window_end_at,status,attempts,claimed_at,sent_at,last_error) VALUES
($1::uuid,$2::uuid,$3::uuid,$5,$7,$8,'sent',1,$9,$10,NULL),
($1::uuid,$2::uuid,$4::uuid,$6,$7,$8,'failed',1,$9,NULL,'SMTP rejected')`,
		tenantID, scheduleID, sentRecipientID, failedRecipientID, sentKey, failedKey, dueAt, windowEnd, windowEnd, sentAt); err != nil {
		t.Fatal(err)
	}

	var eligible []digestNoticeRow
	if err := repository.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var loadErr error
		eligible, loadErr = loadDigestNoticeRows(ctx, tx, tenantID, scheduleID, dueAt, windowEnd, now)
		return loadErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("expired digest rows remained eligible: %+v", eligible)
	}
	if err := repository.CompleteNoop(ctx, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		t.Fatal(err)
	}

	var windowStatus string
	var completedAt, lastSuccess time.Time
	if err := owner.QueryRow(ctx, `SELECT w.status,w.completed_at,s.last_success_at
FROM public.digest_windows w JOIN public.schedules s
  ON s.tenant_id=w.tenant_id AND s.id=w.schedule_id
WHERE w.tenant_id=$1::uuid AND w.schedule_id=$2::uuid AND w.due_at=$3`, tenantID, scheduleID, dueAt).Scan(&windowStatus, &completedAt, &lastSuccess); err != nil {
		t.Fatal(err)
	}
	var sentStatus, failedStatus, failedError string
	var persistedSentAt time.Time
	if err := owner.QueryRow(ctx, `SELECT sent.status,sent.sent_at,failed.status,failed.last_error
FROM public.deliveries sent, public.deliveries failed
WHERE sent.tenant_id=$1::uuid AND sent.idempotency_key=$2
  AND failed.tenant_id=$1::uuid AND failed.idempotency_key=$3`, tenantID, sentKey, failedKey).Scan(&sentStatus, &persistedSentAt, &failedStatus, &failedError); err != nil {
		t.Fatal(err)
	}
	var deliveryCount, itemCount int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.deliveries WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3),
(SELECT count(*) FROM public.digest_window_items WHERE tenant_id=$1::uuid AND schedule_id=$2::uuid AND due_at=$3)`, tenantID, scheduleID, dueAt).Scan(&deliveryCount, &itemCount); err != nil {
		t.Fatal(err)
	}
	if windowStatus != "completed" || !lastSuccess.Equal(windowEnd) || sentStatus != "sent" || !persistedSentAt.Equal(sentAt) || failedStatus != "failed" || !strings.Contains(failedError, "SMTP rejected") || !strings.Contains(failedError, expiredDigestTerminalReason) {
		t.Fatalf("window=%s last=%v sent=%s/%v failed=%s/%q", windowStatus, lastSuccess, sentStatus, persistedSentAt, failedStatus, failedError)
	}
	if deliveryCount != 2 || itemCount != 1 {
		t.Fatalf("history was deleted: deliveries=%d items=%d", deliveryCount, itemCount)
	}
	var visibleFailures int
	if err := repository.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var countErr error
		visibleFailures, countErr = loadTenantFailureCount(ctx, tx, tenantID)
		return countErr
	}); err != nil {
		t.Fatal(err)
	}
	if visibleFailures != 0 {
		t.Fatalf("normal expiry cancellation appeared as an operational failure: %d", visibleFailures)
	}

	if err := repository.CompleteNoop(ctx, tenantID, scheduleID, dueAt, windowEnd); err != nil {
		t.Fatalf("idempotent terminalization: %v", err)
	}
	var repeatedCompletedAt, repeatedLastSuccess time.Time
	var repeatedError string
	if err := owner.QueryRow(ctx, `SELECT w.completed_at,s.last_success_at,
(SELECT last_error FROM public.deliveries WHERE tenant_id=$1::uuid AND idempotency_key=$4)
FROM public.digest_windows w JOIN public.schedules s
  ON s.tenant_id=w.tenant_id AND s.id=w.schedule_id
WHERE w.tenant_id=$1::uuid AND w.schedule_id=$2::uuid AND w.due_at=$3`, tenantID, scheduleID, dueAt, failedKey).Scan(&repeatedCompletedAt, &repeatedLastSuccess, &repeatedError); err != nil {
		t.Fatal(err)
	}
	if !repeatedCompletedAt.Equal(completedAt) || !repeatedLastSuccess.Equal(lastSuccess) || repeatedError != failedError {
		t.Fatalf("rerun mutated terminal state: completed=%v/%v last=%v/%v error=%q/%q", completedAt, repeatedCompletedAt, lastSuccess, repeatedLastSuccess, failedError, repeatedError)
	}

	activeDueAt := now.Add(-5 * time.Minute)
	activeWindowEnd := now.Add(-2 * time.Minute)
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_windows
(tenant_id,schedule_id,due_at,window_end_at,status) VALUES ($1::uuid,$2::uuid,$3,$4,'pending')`, tenantID, scheduleID, activeDueAt, activeWindowEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_recipients
(tenant_id,schedule_id,due_at,window_end_at,recipient_id,email) VALUES
($1::uuid,$2::uuid,$3,$4,$5::uuid,'sent.expired@example.com'),
($1::uuid,$2::uuid,$3,$4,$6::uuid,'failed.expired@example.com')`, tenantID, scheduleID, activeDueAt, activeWindowEnd, sentRecipientID, failedRecipientID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO public.digest_window_items
(tenant_id,schedule_id,due_at,window_end_at,match_id,notice_id,title,source_url,reasons,matched_at)
VALUES ($1::uuid,$2::uuid,$3,$4,gen_random_uuid(),$5::uuid,$6,'',
        '{"reasons":["include_any"]}'::jsonb,$4)`, tenantID, scheduleID, activeDueAt, activeWindowEnd, stored.ID, notice.Title); err != nil {
		t.Fatal(err)
	}

	failedClaim := DeliveryClaim{
		TenantID: tenantID, ScheduleID: scheduleID, RecipientID: failedRecipientID,
		IdempotencyKey: digest.DeliveryKey(tenantID, scheduleID, failedRecipientID, activeDueAt),
		DueAt:          activeDueAt, WindowEnd: activeWindowEnd,
	}
	failedReservation, err := repository.ClaimDelivery(ctx, failedClaim)
	if err != nil || !failedReservation.Claimed {
		t.Fatalf("claim failed recipient=%+v err=%v", failedReservation, err)
	}
	failedClaim.ClaimToken = failedReservation.ClaimToken
	if err := repository.FinalizeFailure(ctx, failedClaim, failedReservation.Attempts, errors.New("SMTP rejected before expiry")); err != nil {
		t.Fatal(err)
	}
	activeClaim := DeliveryClaim{
		TenantID: tenantID, ScheduleID: scheduleID, RecipientID: sentRecipientID,
		IdempotencyKey: digest.DeliveryKey(tenantID, scheduleID, sentRecipientID, activeDueAt),
		DueAt:          activeDueAt, WindowEnd: activeWindowEnd,
	}
	activeReservation, err := repository.ClaimDelivery(ctx, activeClaim)
	if err != nil || !activeReservation.Claimed {
		t.Fatalf("claim active recipient=%+v err=%v", activeReservation, err)
	}
	activeClaim.ClaimToken = activeReservation.ClaimToken

	if err := repository.CompleteNoop(ctx, tenantID, scheduleID, activeDueAt, activeWindowEnd); err != nil {
		t.Fatalf("active lease deferral: %v", err)
	}
	var deferredWindow, deferredActive, deferredFailed, deferredError string
	var deferredLastSuccess *time.Time
	if err := owner.QueryRow(ctx, `SELECT w.status,s.last_success_at,active.status,failed.status,failed.last_error
FROM public.digest_windows w
JOIN public.schedules s ON s.tenant_id=w.tenant_id AND s.id=w.schedule_id
JOIN public.deliveries active ON active.tenant_id=w.tenant_id AND active.idempotency_key=$4
JOIN public.deliveries failed ON failed.tenant_id=w.tenant_id AND failed.idempotency_key=$5
WHERE w.tenant_id=$1::uuid AND w.schedule_id=$2::uuid AND w.due_at=$3`, tenantID, scheduleID, activeDueAt, activeClaim.IdempotencyKey, failedClaim.IdempotencyKey).Scan(&deferredWindow, &deferredLastSuccess, &deferredActive, &deferredFailed, &deferredError); err != nil {
		t.Fatal(err)
	}
	if deferredWindow != "pending" || deferredLastSuccess == nil || !deferredLastSuccess.Equal(windowEnd) || deferredActive != "sending" || deferredFailed != "failed" || strings.Contains(deferredError, expiredDigestTerminalReason) {
		t.Fatalf("active lease was mutated: window=%s last=%v active=%s failed=%s/%q", deferredWindow, deferredLastSuccess, deferredActive, deferredFailed, deferredError)
	}

	activeSentAt := time.Now().UTC().Truncate(time.Second)
	if err := repository.FinalizeSent(ctx, activeClaim, activeReservation.Attempts, activeSentAt); err != nil {
		t.Fatalf("active sender could not finalize after expiry deferral: %v", err)
	}
	staleReservation, err := repository.ClaimDelivery(ctx, failedClaim)
	if err != nil || !staleReservation.Claimed || staleReservation.Attempts != 2 {
		t.Fatalf("reclaim failed recipient=%+v err=%v", staleReservation, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE public.deliveries
SET claimed_at=clock_timestamp()-interval '16 minutes'
WHERE tenant_id=$1::uuid AND idempotency_key=$2`, tenantID, failedClaim.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteNoop(ctx, tenantID, scheduleID, activeDueAt, activeWindowEnd); err != nil {
		t.Fatalf("stale lease terminalization: %v", err)
	}

	var staleWindow, preservedSentStatus, staleStatus, staleError string
	var staleCompletedAt, staleLastSuccess, preservedActiveSentAt time.Time
	var staleAttempts int
	if err := owner.QueryRow(ctx, `SELECT w.status,w.completed_at,s.last_success_at,
active.status,active.sent_at,stale.status,stale.attempts,stale.last_error
FROM public.digest_windows w
JOIN public.schedules s ON s.tenant_id=w.tenant_id AND s.id=w.schedule_id
JOIN public.deliveries active ON active.tenant_id=w.tenant_id AND active.idempotency_key=$4
JOIN public.deliveries stale ON stale.tenant_id=w.tenant_id AND stale.idempotency_key=$5
WHERE w.tenant_id=$1::uuid AND w.schedule_id=$2::uuid AND w.due_at=$3`, tenantID, scheduleID, activeDueAt, activeClaim.IdempotencyKey, failedClaim.IdempotencyKey).Scan(
		&staleWindow, &staleCompletedAt, &staleLastSuccess, &preservedSentStatus, &preservedActiveSentAt, &staleStatus, &staleAttempts, &staleError,
	); err != nil {
		t.Fatal(err)
	}
	if staleWindow != "completed" || !staleLastSuccess.Equal(activeWindowEnd) || preservedSentStatus != "sent" || !preservedActiveSentAt.Equal(activeSentAt) || staleStatus != "failed" || staleAttempts != 2 || !strings.Contains(staleError, expiredDigestTerminalReason) {
		t.Fatalf("stale terminal state: window=%s last=%v sent=%s/%v stale=%s/%d/%q", staleWindow, staleLastSuccess, preservedSentStatus, preservedActiveSentAt, staleStatus, staleAttempts, staleError)
	}
	if err := repository.CompleteNoop(ctx, tenantID, scheduleID, activeDueAt, activeWindowEnd); err != nil {
		t.Fatalf("stale terminal idempotent rerun: %v", err)
	}
	var repeatedStaleCompletedAt time.Time
	var repeatedStaleError string
	if err := owner.QueryRow(ctx, `SELECT w.completed_at,
(SELECT last_error FROM public.deliveries WHERE tenant_id=$1::uuid AND idempotency_key=$4)
FROM public.digest_windows w
WHERE w.tenant_id=$1::uuid AND w.schedule_id=$2::uuid AND w.due_at=$3`, tenantID, scheduleID, activeDueAt, failedClaim.IdempotencyKey).Scan(&repeatedStaleCompletedAt, &repeatedStaleError); err != nil {
		t.Fatal(err)
	}
	if !repeatedStaleCompletedAt.Equal(staleCompletedAt) || repeatedStaleError != staleError {
		t.Fatalf("stale rerun mutated state: completed=%v/%v error=%q/%q", staleCompletedAt, repeatedStaleCompletedAt, staleError, repeatedStaleError)
	}
}

func testConcurrentInvitationAcceptance(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Concurrent Accept")
	tokenHash := integrationHash(t.Name() + "-token")
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at)
VALUES ($1::uuid,'accept@example.com','초대','member',$2,now()+interval '1 hour')`, tenantID, tokenHash); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword("integration-password-123")
	if err != nil {
		t.Fatal(err)
	}
	invitationStore := PostgresInvitationStore{DB: runtime}
	input := AcceptedInvitationInput{TokenHash: tokenHash, DisplayName: "가입자", PasswordHash: passwordHash}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- invitationStore.AcceptInvitation(ctx, input)
		}()
	}
	close(start)
	first, second := <-results, <-results
	assertOneAcceptance(t, first, second)

	var users, accepted int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.users WHERE tenant_id=$1::uuid AND lower(email)='accept@example.com'),
(SELECT count(*) FROM public.invitations WHERE tenant_id=$1::uuid AND token_hash=$2 AND accepted_at IS NOT NULL)`, tenantID, tokenHash).Scan(&users, &accepted); err != nil {
		t.Fatal(err)
	}
	if users != 1 || accepted != 1 {
		t.Fatalf("accepted users=%d accepted invitations=%d", users, accepted)
	}
}

func testConcurrentAcceptAndReinvite(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	tenantID := insertTenant(t, ctx, owner, "Accept Reinvite")
	var actorID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.users
(tenant_id,email,display_name,password_hash,role)
VALUES ($1::uuid,'actor@example.com','관리자','$2a$10$01234567890123456789012345678901234567890123456789012','tenant_admin')
RETURNING id::text`, tenantID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	oldHash := integrationHash(t.Name() + "-old")
	newHash := integrationHash(t.Name() + "-new")
	invitationStore := PostgresInvitationStore{DB: runtime}
	oldInvite := MemberInvitationInput{
		ActorUserID: actorID, TenantID: tenantID, Name: "경합 사용자", Email: "race@example.com",
		Role: auth.Member, TokenHash: oldHash, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := invitationStore.CreateMemberInvitation(ctx, oldInvite); err != nil {
		t.Fatalf("create initial invitation: %v", err)
	}
	passwordHash, err := auth.HashPassword("integration-password-456")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	acceptResult := make(chan error, 1)
	reinviteResult := make(chan error, 1)
	go func() {
		<-start
		acceptResult <- invitationStore.AcceptInvitation(ctx, AcceptedInvitationInput{
			TokenHash: oldHash, DisplayName: "가입 경합", PasswordHash: passwordHash,
		})
	}()
	go func() {
		<-start
		reinvite := oldInvite
		reinvite.TokenHash = newHash
		reinvite.ExpiresAt = time.Now().Add(2 * time.Hour)
		reinviteResult <- invitationStore.CreateMemberInvitation(ctx, reinvite)
	}()
	close(start)
	acceptErr, reinviteErr := <-acceptResult, <-reinviteResult
	if (acceptErr == nil) == (reinviteErr == nil) {
		t.Fatalf("exactly one accept/reinvite operation must succeed: accept=%v reinvite=%v", acceptErr, reinviteErr)
	}

	var users, pending int
	var pendingHash *string
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.users WHERE tenant_id=$1::uuid AND lower(email)='race@example.com'),
(SELECT count(*) FROM public.invitations WHERE tenant_id=$1::uuid AND lower(email)='race@example.com' AND accepted_at IS NULL),
(SELECT token_hash FROM public.invitations WHERE tenant_id=$1::uuid AND lower(email)='race@example.com' AND accepted_at IS NULL LIMIT 1)`, tenantID).Scan(&users, &pending, &pendingHash); err != nil {
		t.Fatal(err)
	}
	if acceptErr == nil {
		assertPostgresCode(t, reinviteErr, "23505")
		if users != 1 || pending != 0 || pendingHash != nil {
			t.Fatalf("accepted race state users=%d pending=%d hash=%v", users, pending, pendingHash)
		}
		return
	}
	if !errors.Is(acceptErr, appweb.ErrInvitationUnavailable) {
		t.Fatalf("old acceptance error=%v", acceptErr)
	}
	if users != 0 || pending != 1 || pendingHash == nil || *pendingHash != newHash {
		t.Fatalf("reinvited race state users=%d pending=%d hash=%v", users, pending, pendingHash)
	}
}

func testInitialAdministratorReplacement(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	var platformAdminID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.users
(tenant_id,email,display_name,password_hash,role)
VALUES (NULL,'replacement-platform@example.com','플랫폼 관리자','$2a$10$01234567890123456789012345678901234567890123456789012','platform_admin')
RETURNING id::text`).Scan(&platformAdminID); err != nil {
		t.Fatal(err)
	}

	store := PostgresInvitationStore{DB: runtime}
	firstHash := integrationHash(t.Name() + "-first-admin")
	secondHash := integrationHash(t.Name() + "-second-admin")
	first := TenantInvitationInput{
		ActorUserID: platformAdminID, TenantName: "Initial Admin Replacement", ContactEmail: "contact-replacement@example.com",
		AdminName: "초기 관리자 A", AdminEmail: "initial-a@example.com", Role: auth.TenantAdmin,
		TokenHash: firstHash, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.CreateTenantInvitation(ctx, first); err != nil {
		t.Fatalf("create first administrator invitation: %v", err)
	}

	var tenantID string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM public.tenants
WHERE lower(name)=lower($1) AND lower(contact_email)=lower($2)`, first.TenantName, first.ContactEmail).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	memberHash := integrationHash(t.Name() + "-unrelated-member")
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at)
VALUES ($1::uuid,'unrelated-member@example.com','일반 사용자','member',$2,now()+interval '1 hour')`, tenantID, memberHash); err != nil {
		t.Fatal(err)
	}

	second := first
	second.AdminName = "초기 관리자 B"
	second.AdminEmail = "initial-b@example.com"
	second.TokenHash = secondHash
	second.ExpiresAt = time.Now().Add(2 * time.Hour)
	if err := store.CreateTenantInvitation(ctx, second); err != nil {
		t.Fatalf("replace initial administrator invitation: %v", err)
	}

	passwordHash, err := auth.HashPassword("integration-password-789")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptInvitation(ctx, AcceptedInvitationInput{
		TokenHash: firstHash, DisplayName: "초기 관리자 A", PasswordHash: passwordHash,
	}); !errors.Is(err, appweb.ErrInvitationUnavailable) {
		t.Fatalf("revoked first invitation error=%v", err)
	}
	if err := store.AcceptInvitation(ctx, AcceptedInvitationInput{
		TokenHash: secondHash, DisplayName: "초기 관리자 B", PasswordHash: passwordHash,
	}); err != nil {
		t.Fatalf("accept replacement administrator: %v", err)
	}

	var firstConsumed, secondConsumed, memberPending, tenantAdmins int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.invitations WHERE token_hash=$2 AND accepted_at IS NOT NULL),
(SELECT count(*) FROM public.invitations WHERE token_hash=$3 AND accepted_at IS NOT NULL),
(SELECT count(*) FROM public.invitations WHERE token_hash=$4 AND accepted_at IS NULL),
(SELECT count(*) FROM public.users WHERE tenant_id=$1::uuid AND role='tenant_admin')`,
		tenantID, firstHash, secondHash, memberHash).Scan(&firstConsumed, &secondConsumed, &memberPending, &tenantAdmins); err != nil {
		t.Fatal(err)
	}
	if firstConsumed != 1 || secondConsumed != 1 || memberPending != 1 || tenantAdmins != 1 {
		t.Fatalf("replacement state first=%d second=%d member_pending=%d tenant_admins=%d",
			firstConsumed, secondConsumed, memberPending, tenantAdmins)
	}
}

func testExpiredInvitationEmailReuse(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	var platformAdminID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.users
(tenant_id,email,display_name,password_hash,role)
VALUES (NULL,'expiry-platform@example.com','만료 테스트 관리자','$2a$10$01234567890123456789012345678901234567890123456789012','platform_admin')
RETURNING id::text`).Scan(&platformAdminID); err != nil {
		t.Fatal(err)
	}
	store := PostgresInvitationStore{DB: runtime}

	expiredTenantID := insertTenant(t, ctx, owner, "Expired Email A")
	expiredHash := integrationHash(t.Name() + "-expired-admin")
	const reusableEmail = "expired-reusable@example.com"
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at,created_at)
VALUES ($1::uuid,$2,'만료 관리자','tenant_admin',$3,now()-interval '1 hour',now()-interval '2 hours')`,
		expiredTenantID, reusableEmail, expiredHash); err != nil {
		t.Fatal(err)
	}
	newHash := integrationHash(t.Name() + "-replacement-admin")
	if err := store.CreateTenantInvitation(ctx, TenantInvitationInput{
		ActorUserID: platformAdminID, TenantName: "Expired Email B", ContactEmail: "expired-b@example.com",
		AdminName: "신규 관리자", AdminEmail: reusableEmail, Role: auth.TenantAdmin,
		TokenHash: newHash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("reuse expired administrator email: %v", err)
	}
	if _, err := store.InvitationByHash(ctx, expiredHash); !errors.Is(err, appweb.ErrInvitationUnavailable) {
		t.Fatalf("expired bearer lookup error=%v", err)
	}
	expiredPasswordHash, err := auth.HashPassword("integration-expired-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptInvitation(ctx, AcceptedInvitationInput{
		TokenHash: expiredHash, DisplayName: "만료 관리자", PasswordHash: expiredPasswordHash,
	}); !errors.Is(err, appweb.ErrInvitationUnavailable) {
		t.Fatalf("expired bearer acceptance error=%v", err)
	}
	var expiredClosed, replacementPending int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.invitations WHERE token_hash=$1 AND accepted_at IS NOT NULL),
(SELECT count(*) FROM public.invitations WHERE token_hash=$2 AND accepted_at IS NULL AND expires_at>now())`,
		expiredHash, newHash).Scan(&expiredClosed, &replacementPending); err != nil {
		t.Fatal(err)
	}
	if expiredClosed != 1 || replacementPending != 1 {
		t.Fatalf("expired closed=%d replacement pending=%d", expiredClosed, replacementPending)
	}

	memberTenantID := insertTenant(t, ctx, owner, "Expired Member Target")
	var tenantAdminID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.users
(tenant_id,email,display_name,password_hash,role)
VALUES ($1::uuid,'expiry-tenant-admin@example.com','회사 관리자','$2a$10$01234567890123456789012345678901234567890123456789012','tenant_admin')
RETURNING id::text`, memberTenantID).Scan(&tenantAdminID); err != nil {
		t.Fatal(err)
	}
	expiredMemberTenantID := insertTenant(t, ctx, owner, "Expired Member Source")
	expiredMemberHash := integrationHash(t.Name() + "-expired-member")
	const reusableMemberEmail = "expired-member-reusable@example.com"
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at,created_at)
VALUES ($1::uuid,$2,'만료 사용자','member',$3,now()-interval '1 hour',now()-interval '2 hours')`,
		expiredMemberTenantID, reusableMemberEmail, expiredMemberHash); err != nil {
		t.Fatal(err)
	}
	newMemberHash := integrationHash(t.Name() + "-replacement-member")
	if err := store.CreateMemberInvitation(ctx, MemberInvitationInput{
		ActorUserID: tenantAdminID, TenantID: memberTenantID, Name: "신규 사용자", Email: reusableMemberEmail,
		Role: auth.Member, TokenHash: newMemberHash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("reuse expired member email: %v", err)
	}
	var expiredMemberClosed, replacementMemberPending int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.invitations WHERE token_hash=$1 AND accepted_at IS NOT NULL),
(SELECT count(*) FROM public.invitations WHERE token_hash=$2 AND accepted_at IS NULL AND expires_at>now())`,
		expiredMemberHash, newMemberHash).Scan(&expiredMemberClosed, &replacementMemberPending); err != nil {
		t.Fatal(err)
	}
	if expiredMemberClosed != 1 || replacementMemberPending != 1 {
		t.Fatalf("expired member closed=%d replacement pending=%d", expiredMemberClosed, replacementMemberPending)
	}

	liveTenantID := insertTenant(t, ctx, owner, "Live Email A")
	liveHash := integrationHash(t.Name() + "-live-admin")
	const liveEmail = "live-invitation@example.com"
	if _, err := owner.Exec(ctx, `INSERT INTO public.invitations
(tenant_id,email,display_name,role,token_hash,expires_at)
VALUES ($1::uuid,$2,'유효 관리자','tenant_admin',$3,now()+interval '1 hour')`, liveTenantID, liveEmail, liveHash); err != nil {
		t.Fatal(err)
	}
	err = store.CreateTenantInvitation(ctx, TenantInvitationInput{
		ActorUserID: platformAdminID, TenantName: "Live Email B", ContactEmail: "live-b@example.com",
		AdminName: "충돌 관리자", AdminEmail: liveEmail, Role: auth.TenantAdmin,
		TokenHash: integrationHash(t.Name() + "-live-conflict"), ExpiresAt: time.Now().Add(time.Hour),
	})
	assertPostgresCode(t, err, "23505")
	var livePending int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM public.invitations
WHERE token_hash=$1 AND accepted_at IS NULL AND expires_at>now()`, liveHash).Scan(&livePending); err != nil {
		t.Fatal(err)
	}
	if livePending != 1 {
		t.Fatalf("live invitation pending=%d", livePending)
	}
}

func testConcurrentInvitationEmailClaims(t *testing.T, ctx context.Context, owner, runtime *pgxpool.Pool) {
	t.Helper()
	var platformAdminID string
	if err := owner.QueryRow(ctx, `INSERT INTO public.users
(tenant_id,email,display_name,password_hash,role)
VALUES (NULL,'concurrent-email-platform@example.com','동시성 관리자','$2a$10$01234567890123456789012345678901234567890123456789012','platform_admin')
RETURNING id::text`).Scan(&platformAdminID); err != nil {
		t.Fatal(err)
	}
	store := PostgresInvitationStore{DB: runtime}
	const sharedEmail = "concurrent-invitation@example.com"
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := range 2 {
		index := index
		go func() {
			<-start
			results <- store.CreateTenantInvitation(ctx, TenantInvitationInput{
				ActorUserID: platformAdminID,
				TenantName:  fmt.Sprintf("Concurrent Email %d", index), ContactEmail: fmt.Sprintf("concurrent-%d@example.com", index),
				AdminName: fmt.Sprintf("동시 관리자 %d", index), AdminEmail: sharedEmail, Role: auth.TenantAdmin,
				TokenHash: integrationHash(fmt.Sprintf("%s-%d", t.Name(), index)), ExpiresAt: time.Now().Add(time.Hour),
			})
		}()
	}
	close(start)
	var first, second error
	select {
	case first = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("first concurrent invitation timed out")
	}
	select {
	case second = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("second concurrent invitation timed out")
	}
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one shared-email invitation must succeed: first=%v second=%v", first, second)
	}
	failure := first
	if failure == nil {
		failure = second
	}
	assertPostgresCode(t, failure, "23505")
	var pending, tenants int
	if err := owner.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.invitations WHERE lower(email)=$1 AND accepted_at IS NULL),
(SELECT count(*) FROM public.tenants WHERE name IN ('Concurrent Email 0','Concurrent Email 1'))`, sharedEmail).Scan(&pending, &tenants); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || tenants != 1 {
		t.Fatalf("concurrent pending=%d committed tenants=%d", pending, tenants)
	}
}

func insertTenant(t *testing.T, ctx context.Context, owner *pgxpool.Pool, label string) string {
	t.Helper()
	emailLabel := strings.NewReplacer(" ", "-", "/", "-").Replace(strings.ToLower(label))
	var tenantID string
	err := owner.QueryRow(ctx, `INSERT INTO public.tenants (name,contact_email)
VALUES ($1,$2) RETURNING id::text`, label, emailLabel+"@example.com").Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant %q: %v", label, err)
	}
	return tenantID
}

func integrationHash(label string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(label)))
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("PostgreSQL error=%v, want SQLSTATE %s", err, code)
	}
}

func assertOneAcceptance(t *testing.T, first, second error) {
	t.Helper()
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one acceptance must succeed: first=%v second=%v", first, second)
	}
	failure := first
	if failure == nil {
		failure = second
	}
	if !errors.Is(failure, appweb.ErrInvitationUnavailable) {
		t.Fatalf("losing acceptance error=%v", failure)
	}
}
