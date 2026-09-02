package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"namo/internal/matcher"
	"namo/internal/model"
	appweb "namo/internal/web"
)

type WebService struct {
	Repository      *PostgresRepository
	QueueCollection func() error
	TestMail        func(context.Context, string) error
}

var _ appweb.Backend = (*WebService)(nil)
var _ appweb.Actions = (*WebService)(nil)

func (s *WebService) MapRequest(r *http.Request) (appweb.RequestContext, error) {
	requestContext := appweb.RequestContext{CSRFToken: CSRFTokenFromContext(r.Context())}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		if r.URL.Path == "/login" || r.URL.Path == "/accept-invite" {
			return requestContext, nil
		}
		return appweb.RequestContext{}, ErrUnauthenticated
	}
	requestContext.UserID = principal.UserID
	requestContext.UserName = principal.Email
	requestContext.Email = principal.Email
	requestContext.TenantID = principal.TenantID
	requestContext.Role = string(principal.Role)
	if principal.TenantID == "" {
		return requestContext, nil
	}
	if s == nil || s.Repository == nil {
		return appweb.RequestContext{}, errors.New("web repository is not configured")
	}
	err := s.Repository.withTenant(r.Context(), principal.TenantID, func(tx pgx.Tx) error {
		var displayName, tenantName string
		if err := tx.QueryRow(r.Context(), `SELECT u.display_name, t.name
FROM public.users u JOIN public.tenants t ON t.id=u.tenant_id
WHERE u.id=$1::uuid AND u.tenant_id=$2::uuid`, principal.UserID, principal.TenantID).Scan(&displayName, &tenantName); err != nil {
			return fmt.Errorf("load web identity: %w", err)
		}
		if strings.TrimSpace(displayName) != "" {
			requestContext.UserName = displayName
		}
		requestContext.TenantName = tenantName
		return nil
	})
	return requestContext, err
}

func (s *WebService) Load(ctx context.Context, requestContext appweb.RequestContext, _ appweb.PageRequest) (appweb.AppData, error) {
	if s == nil || s.Repository == nil || s.Repository.Pool == nil {
		return appweb.AppData{}, errors.New("web repository is not configured")
	}
	data, state, err := s.loadGlobalState(ctx)
	if err != nil {
		return appweb.AppData{}, err
	}
	if requestContext.Role == "platform_admin" {
		if err := s.loadPlatformData(ctx, &data, state); err != nil {
			return appweb.AppData{}, err
		}
		return data, nil
	}
	if requestContext.TenantID == "" {
		return appweb.AppData{}, ErrUnauthenticated
	}
	err = s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		return loadTenantWebData(ctx, tx, requestContext.TenantID, &data)
	})
	return data, err
}

func (s *WebService) loadGlobalState(ctx context.Context) (appweb.AppData, CollectionResult, error) {
	var lastSuccess *time.Time
	var resultJSON []byte
	var lastError *string
	if err := s.Repository.Pool.QueryRow(ctx, `SELECT last_success_at, last_result, last_error
FROM public.collection_state WHERE singleton`).Scan(&lastSuccess, &resultJSON, &lastError); err != nil {
		return appweb.AppData{}, CollectionResult{}, fmt.Errorf("load collection dashboard: %w", err)
	}
	var result CollectionResult
	if len(resultJSON) != 0 {
		_ = json.Unmarshal(resultJSON, &result)
	}
	data := appweb.AppData{Demo: false}
	data.Dashboard.NewNotices = result.Changed
	data.Dashboard.Collected = result.Fetched
	data.Dashboard.Healthy = lastError == nil || *lastError == ""
	data.Dashboard.LastCollected = "아직 없음"
	if lastSuccess != nil {
		data.Dashboard.LastCollected = formatKoreanTime(*lastSuccess)
	}
	data.Admin = appweb.AdminView{
		Healthy: data.Dashboard.Healthy, LastCollected: data.Dashboard.LastCollected,
		CollectedCount: result.Fetched,
	}
	return data, result, nil
}

func loadTenantWebData(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	if err := tx.QueryRow(ctx, `SELECT contact_email FROM public.tenants WHERE id=$1::uuid`, tenantID).Scan(&data.ContactEmail); err != nil {
		return fmt.Errorf("load tenant settings: %w", err)
	}
	if err := loadTenantSchedule(ctx, tx, tenantID, data); err != nil {
		return err
	}
	if err := loadTenantNotices(ctx, tx, tenantID, data); err != nil {
		return err
	}
	if err := loadTenantFilters(ctx, tx, tenantID, data); err != nil {
		return err
	}
	if err := loadTenantRecipients(ctx, tx, tenantID, data); err != nil {
		return err
	}
	if err := loadTenantMembers(ctx, tx, tenantID, data); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.matches
WHERE tenant_id=$1::uuid AND created_at >= now()-interval '24 hours'`, tenantID).Scan(&data.Dashboard.Matches); err != nil {
		return fmt.Errorf("count recent matches: %w", err)
	}
	return nil
}

func loadTenantSchedule(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	data.DeliveryTime, data.Timezone = "07:00", "Asia/Seoul"
	data.DeliveryDays = []int{0, 1, 2, 3, 4, 5, 6}
	var hour, minute int
	var weekdays []int16
	err := tx.QueryRow(ctx, `SELECT hour, minute, timezone, weekdays FROM public.schedules
WHERE tenant_id=$1::uuid AND enabled ORDER BY created_at LIMIT 1`, tenantID).Scan(&hour, &minute, &data.Timezone, &weekdays)
	if errors.Is(err, pgx.ErrNoRows) {
		data.Dashboard.RunTime = data.DeliveryTime
		if next, ok := nextDeliveryAt(time.Now(), 7, 0, data.DeliveryDays); ok {
			data.Dashboard.NextDelivery = next.Format("01.02 15:04")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("load digest schedule: %w", err)
	}
	data.DeliveryTime = fmt.Sprintf("%02d:%02d", hour, minute)
	data.Dashboard.RunTime = data.DeliveryTime
	data.DeliveryDays = make([]int, len(weekdays))
	for i, day := range weekdays {
		data.DeliveryDays[i] = int(day)
	}
	if next, ok := nextDeliveryAt(time.Now(), hour, minute, data.DeliveryDays); ok {
		data.Dashboard.NextDelivery = next.Format("01.02 15:04")
	}
	return nil
}

const tenantNoticesSQL = `SELECT n.id::text, n.payload, m.reasons
FROM public.matches m
JOIN public.filters f ON f.tenant_id=m.tenant_id AND f.id=m.filter_id AND f.enabled
JOIN public.notices n ON n.id=m.notice_id
WHERE m.tenant_id=$1::uuid ORDER BY m.created_at DESC LIMIT 300`

func loadTenantNotices(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	rows, err := tx.Query(ctx, tenantNoticesSQL, tenantID)
	if err != nil {
		return fmt.Errorf("load matched notices: %w", err)
	}
	defer rows.Close()
	index := make(map[string]int)
	for rows.Next() {
		var id string
		var noticeJSON, reasonsJSON []byte
		if err := rows.Scan(&id, &noticeJSON, &reasonsJSON); err != nil {
			return fmt.Errorf("scan matched notice: %w", err)
		}
		position, exists := index[id]
		if !exists {
			var notice model.Notice
			if err := json.Unmarshal(noticeJSON, &notice); err != nil {
				return fmt.Errorf("decode matched notice: %w", err)
			}
			view := appweb.NoticeView{
				ID: id, Title: notice.Title, Category: categoryLabel(notice.Category), Agency: notice.Agency,
				Region: notice.Region, Amount: formatWon(notice.Amount), Deadline: formatKoreanTime(notice.Deadline), SourceURL: notice.SourceURL,
			}
			data.Notices = append(data.Notices, view)
			position = len(data.Notices) - 1
			index[id] = position
		}
		var matched struct {
			Reasons []matcher.Reason `json:"reasons"`
			Details []matcher.Detail `json:"details"`
		}
		if err := json.Unmarshal(reasonsJSON, &matched); err != nil {
			return fmt.Errorf("decode match reasons: %w", err)
		}
		for _, detail := range matched.Details {
			data.Notices[position].Reasons = appendUnique(data.Notices[position].Reasons, reasonText(detail))
		}
		if len(matched.Details) == 0 {
			for _, reason := range matched.Reasons {
				data.Notices[position].Reasons = appendUnique(data.Notices[position].Reasons, reasonText(matcher.Detail{Code: reason}))
			}
		}
	}
	return rows.Err()
}

func loadTenantFilters(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	rows, err := tx.Query(ctx, `SELECT f.id::text, f.name, f.rules, f.enabled, count(m.id)
FROM public.filters f LEFT JOIN public.matches m ON m.tenant_id=f.tenant_id AND m.filter_id=f.id
WHERE f.tenant_id=$1::uuid GROUP BY f.id ORDER BY f.created_at`, tenantID)
	if err != nil {
		return fmt.Errorf("load filters: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var view appweb.FilterView
		var raw []byte
		if err := rows.Scan(&view.ID, &view.Name, &raw, &view.Enabled, &view.Matches); err != nil {
			return err
		}
		var rule matcher.Rule
		if err := json.Unmarshal(raw, &rule); err != nil {
			return fmt.Errorf("decode filter rule: %w", err)
		}
		view.Summary = filterSummary(rule)
		data.Filters = append(data.Filters, view)
		if view.Enabled {
			data.Dashboard.ActiveFilters++
		}
	}
	return rows.Err()
}

func loadTenantRecipients(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	rows, err := tx.Query(ctx, `SELECT name, email, enabled FROM public.recipients WHERE tenant_id=$1::uuid ORDER BY created_at`, tenantID)
	if err != nil {
		return fmt.Errorf("load recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var view appweb.RecipientView
		var enabled bool
		if err := rows.Scan(&view.Name, &view.Email, &enabled); err != nil {
			return err
		}
		if enabled {
			view.State = "수신"
		} else {
			view.State = "중지"
		}
		data.Recipients = append(data.Recipients, view)
	}
	return rows.Err()
}

func loadTenantMembers(ctx context.Context, tx pgx.Tx, tenantID string, data *appweb.AppData) error {
	rows, err := tx.Query(ctx, `SELECT display_name, email, role FROM public.users WHERE tenant_id=$1::uuid ORDER BY created_at`, tenantID)
	if err != nil {
		return fmt.Errorf("load members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var view appweb.MemberView
		var role string
		if err := rows.Scan(&view.Name, &view.Email, &role); err != nil {
			return err
		}
		view.Role = map[string]string{"tenant_admin": "테넌트 관리자", "member": "담당자"}[role]
		data.Members = append(data.Members, view)
	}
	return rows.Err()
}

func (s *WebService) loadPlatformData(ctx context.Context, data *appweb.AppData, _ CollectionResult) error {
	tenants, err := s.Repository.tenantCatalog(ctx)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		view := appweb.TenantView{Name: tenant.Name, LastDigest: "발송 전", State: "정상"}
		err := s.Repository.withTenant(ctx, tenant.ID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.users WHERE tenant_id=$1::uuid`, tenant.ID).Scan(&view.Members); err != nil {
				return err
			}
			var sentAt *time.Time
			if err := tx.QueryRow(ctx, `SELECT max(sent_at) FROM public.deliveries WHERE tenant_id=$1::uuid AND status='sent'`, tenant.ID).Scan(&sentAt); err != nil {
				return err
			}
			if sentAt != nil {
				view.LastDigest = formatKoreanTime(*sentAt)
			}
			failures, err := loadTenantFailureCount(ctx, tx, tenant.ID)
			if err != nil {
				return err
			}
			applyTenantFailures(data, &view, failures)
			return nil
		})
		if err != nil {
			return fmt.Errorf("load platform tenant %s: %w", tenant.ID, err)
		}
		data.Tenants = append(data.Tenants, view)
	}
	return nil
}

func applyTenantFailures(data *appweb.AppData, view *appweb.TenantView, failures int) {
	data.Admin.FailedJobs += failures
	if failures > 0 {
		data.Admin.Healthy = false
		view.State = "점검"
	}
}

type failureCountQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadTenantFailureCount(ctx context.Context, queryer failureCountQueryer, tenantID string) (int, error) {
	var failures int
	err := queryer.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM public.job_runs
   WHERE tenant_id=$1::uuid AND status='failed' AND started_at >= now()-interval '24 hours')
  +
  (SELECT count(*) FROM public.deliveries
   WHERE tenant_id=$1::uuid AND status='failed' AND claimed_at >= now()-interval '24 hours'
     AND position($2 in COALESCE(last_error,'')) = 0)`, tenantID, expiredDigestTerminalReason).Scan(&failures)
	if err != nil {
		return 0, fmt.Errorf("count tenant job and delivery failures: %w", err)
	}
	return failures, nil
}

func (s *WebService) SaveFilter(ctx context.Context, requestContext appweb.RequestContext, command appweb.FilterCommand) error {
	if err := requireTenantAdmin(requestContext); err != nil {
		return err
	}
	raw, err := json.Marshal(filterRuleFromWebCommand(command))
	if err != nil {
		return err
	}
	return s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		return saveFilter(ctx, tx, requestContext.TenantID, command.Name, raw)
	})
}

type webQueryExecer interface {
	webExecer
	QueryRow(context.Context, string, ...any) pgx.Row
}

func saveFilter(ctx context.Context, tx webQueryExecer, tenantID, name string, rules []byte) error {
	var filterID string
	err := tx.QueryRow(ctx, `INSERT INTO public.filters (tenant_id, name, rules)
VALUES ($1::uuid, $2, $3::jsonb)
ON CONFLICT (tenant_id, name) DO NOTHING
RETURNING id::text`, tenantID, name, rules).Scan(&filterID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("insert filter: %w", err)
	}

	var rulesChanged, enabled bool
	err = tx.QueryRow(ctx, `SELECT id::text, rules IS DISTINCT FROM $3::jsonb, enabled
FROM public.filters
WHERE tenant_id=$1::uuid AND name=$2
FOR UPDATE`, tenantID, name, rules).Scan(&filterID, &rulesChanged, &enabled)
	if err != nil {
		return fmt.Errorf("lock existing filter: %w", err)
	}
	if !rulesChanged && enabled {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE public.filters
SET rules=$3::jsonb, enabled=true, updated_at=now()
WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, filterID, rules)
	if err != nil {
		return fmt.Errorf("update filter: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if !rulesChanged {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM public.matches
WHERE tenant_id=$1::uuid AND filter_id=$2::uuid`, tenantID, filterID); err != nil {
		return fmt.Errorf("delete stale filter matches: %w", err)
	}
	return nil
}

func (s *WebService) ToggleFilter(ctx context.Context, requestContext appweb.RequestContext, command appweb.ToggleFilterCommand) error {
	if err := requireTenantAdmin(requestContext); err != nil {
		return err
	}
	return s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		return toggleFilter(ctx, tx, requestContext.TenantID, command)
	})
}

type webExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func toggleFilter(ctx context.Context, tx webExecer, tenantID string, command appweb.ToggleFilterCommand) error {
	tag, err := tx.Exec(ctx, `UPDATE public.filters SET enabled=$3, updated_at=now()
WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, command.FilterID, command.Enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if command.Enabled {
		return nil
	}
	_, err = tx.Exec(ctx, `DELETE FROM public.matches
WHERE tenant_id=$1::uuid AND filter_id=$2::uuid`, tenantID, command.FilterID)
	return err
}

func (s *WebService) SaveNotification(ctx context.Context, requestContext appweb.RequestContext, command appweb.NotificationCommand) error {
	if err := requireTenantAdmin(requestContext); err != nil {
		return err
	}
	parsed, err := time.Parse("15:04", command.DeliveryTime)
	if err != nil || command.Timezone != "Asia/Seoul" || len(command.DeliveryDays) == 0 {
		return errors.New("invalid notification schedule")
	}
	days := make([]int16, len(command.DeliveryDays))
	for i, day := range command.DeliveryDays {
		if day < 0 || day > 6 {
			return errors.New("invalid notification weekday")
		}
		days[i] = int16(day)
	}
	return s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO public.schedules (tenant_id, name, hour, minute, timezone, weekdays)
VALUES ($1::uuid, '기본 알림', $2, $3, $4, $5)
ON CONFLICT (tenant_id, name) DO UPDATE SET hour=EXCLUDED.hour, minute=EXCLUDED.minute,
timezone=EXCLUDED.timezone, weekdays=EXCLUDED.weekdays, enabled=true`,
			requestContext.TenantID, parsed.Hour(), parsed.Minute(), command.Timezone, days)
		return err
	})
}

func (s *WebService) AddRecipient(ctx context.Context, requestContext appweb.RequestContext, command appweb.RecipientCommand) error {
	if err := requireTenantAdmin(requestContext); err != nil {
		return err
	}
	command.Name = strings.TrimSpace(command.Name)
	command.Email = strings.ToLower(strings.TrimSpace(command.Email))
	address, err := mail.ParseAddress(command.Email)
	if command.Name == "" || err != nil || address.Address != command.Email {
		return errors.New("invalid recipient")
	}
	return s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO public.recipients (tenant_id, name, email)
VALUES ($1::uuid, $2, $3) ON CONFLICT (tenant_id, (lower(email)))
DO UPDATE SET name=EXCLUDED.name, enabled=true`, requestContext.TenantID, command.Name, command.Email)
		return err
	})
}

func (s *WebService) SaveSettings(ctx context.Context, requestContext appweb.RequestContext, command appweb.SettingsCommand) error {
	if err := requireTenantAdmin(requestContext); err != nil {
		return err
	}
	return s.Repository.withTenant(ctx, requestContext.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.tenants SET name=$2, contact_email=$3 WHERE id=$1::uuid`,
			requestContext.TenantID, command.TenantName, command.ContactEmail)
		return err
	})
}

func (s *WebService) RunCollection(_ context.Context, requestContext appweb.RequestContext) error {
	if requestContext.Role != "platform_admin" || s.QueueCollection == nil {
		return errors.New("platform collection action is unavailable")
	}
	return s.QueueCollection()
}

func (s *WebService) SendTestMail(ctx context.Context, requestContext appweb.RequestContext) error {
	if requestContext.Role != "platform_admin" || s.TestMail == nil || requestContext.Email == "" {
		return errors.New("platform test-mail action is unavailable")
	}
	return s.TestMail(ctx, requestContext.Email)
}

func requireTenantAdmin(requestContext appweb.RequestContext) error {
	if requestContext.TenantID == "" || (requestContext.Role != "tenant_admin" && requestContext.Role != "platform_admin") {
		return errors.New("tenant administrator role is required")
	}
	return nil
}

func filterRuleFromWebCommand(command appweb.FilterCommand) matcher.Rule {
	rule := matcher.Rule{
		Exclude:            splitTerms(command.ExcludeKeywords),
		Agencies:           splitTerms(command.Agency),
		Regions:            splitTerms(command.Region),
		MinAmount:          command.MinimumAmount,
		DeadlineWithinDays: &command.DeadlineDays,
	}
	terms := splitTerms(command.IncludeKeywords)
	if command.IncludeMode == "all" {
		rule.IncludeAll = terms
	} else {
		rule.IncludeAny = terms
	}
	if category := categoryFromLabel(command.Category); category != "" {
		rule.Categories = []model.Category{category}
	}
	return rule
}

func splitTerms(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := model.NormalizeText(field); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func categoryFromLabel(value string) model.Category {
	return map[string]model.Category{"공사": model.CategoryConstruction, "용역": model.CategoryService, "물품": model.CategoryGoods, "외자": model.CategoryForeign}[value]
}

func categoryLabel(value model.Category) string {
	return map[model.Category]string{model.CategoryConstruction: "공사", model.CategoryService: "용역", model.CategoryGoods: "물품", model.CategoryForeign: "외자"}[value]
}

func formatKoreanTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	location, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		location = time.FixedZone("KST", 9*60*60)
	}
	return value.In(location).Format("2006.01.02 15:04")
}

func nextDeliveryAt(now time.Time, hour, minute int, weekdays []int) (time.Time, bool) {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || len(weekdays) == 0 {
		return time.Time{}, false
	}
	location, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		location = time.FixedZone("KST", 9*60*60)
	}
	allowed := make(map[time.Weekday]bool, len(weekdays))
	for _, day := range weekdays {
		if day < 0 || day > 6 {
			return time.Time{}, false
		}
		allowed[time.Weekday(day)] = true
	}
	localNow := now.In(location)
	for offset := 0; offset <= 7; offset++ {
		date := localNow.AddDate(0, 0, offset)
		candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, location)
		if allowed[candidate.Weekday()] && candidate.After(localNow) {
			return candidate, true
		}
	}
	return time.Time{}, false
}

func formatWon(value int64) string {
	digits := strconv.FormatInt(value, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits + "원"
}

func reasonText(detail matcher.Detail) string {
	label := map[matcher.Reason]string{
		matcher.ReasonIncludeAny: "포함 키워드", matcher.ReasonIncludeAll: "필수 키워드",
		matcher.ReasonCategory: "업무구분", matcher.ReasonAgency: "기관", matcher.ReasonRegion: "지역",
		matcher.ReasonMinAmount: "최소 금액", matcher.ReasonMaxAmount: "최대 금액",
		matcher.ReasonDeadlineWeekday: "마감 요일", matcher.ReasonDeadlineWithinDays: "마감 잔여일",
	}[detail.Code]
	if label == "" {
		label = string(detail.Code)
	}
	if detail.RuleValue == "" {
		return label + " 일치"
	}
	text := fmt.Sprintf("%s ‘%s’ 일치", label, detail.RuleValue)
	if detail.NoticeValue != "" {
		text += fmt.Sprintf(" (공고: %s)", detail.NoticeValue)
	}
	return text
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func filterSummary(rule matcher.Rule) string {
	parts := make([]string, 0, 5)
	if len(rule.IncludeAny) > 0 {
		parts = append(parts, "ANY: "+strings.Join(rule.IncludeAny, ", "))
	}
	if len(rule.IncludeAll) > 0 {
		parts = append(parts, "ALL: "+strings.Join(rule.IncludeAll, ", "))
	}
	if len(rule.Exclude) > 0 {
		parts = append(parts, "제외: "+strings.Join(rule.Exclude, ", "))
	}
	if len(rule.Regions) > 0 {
		parts = append(parts, strings.Join(rule.Regions, ", "))
	}
	if rule.MinAmount != nil {
		parts = append(parts, formatWon(*rule.MinAmount)+" 이상")
	}
	if len(parts) == 0 {
		return "전체 공고"
	}
	return strings.Join(parts, " · ")
}
