package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"namo/internal/auth"
	"namo/internal/config"
	"namo/internal/digest"
	"namo/internal/jobs"
	"namo/internal/procurement"
	"namo/internal/store"
	webui "namo/internal/web"
	"namo/migrations"
)

// CommandOperation is the narrow boundary between CLI argument handling and a
// command implementation. Keeping it injectable lets commands be tested
// without opening sockets or contacting external services.
type CommandOperation func(context.Context, config.Config, []string) error

type RuntimeOperations struct {
	Serve, Migrate, CreateAdmin, CollectOnce, SendTestMail CommandOperation
}

// UIFactory and DigestJobFactory are integration points for the tenant-aware
// web read/write service and PostgreSQL digest repository.
type UIFactory func(context.Context, *PostgresRepository, config.Config) (http.Handler, error)
type ScheduledJob func(context.Context, time.Time) error
type DigestJobFactory func(*PostgresRepository, config.Config) (ScheduledJob, error)
type CollectionRunner func(context.Context) (CollectionResult, error)
type CollectionRunnerFactory func(*PostgresRepository, config.Config) (CollectionRunner, error)

// Runtime composes concrete free/open-source adapters while keeping the two
// larger application services replaceable during staged implementation.
type Runtime struct {
	Input          io.Reader
	Output         io.Writer
	ErrorOutput    io.Writer
	BuildUI        UIFactory
	BuildDigestJob DigestJobFactory
	BuildCollector CollectionRunnerFactory
	Operations     RuntimeOperations
}

// NewRuntime returns the production command executor. Streams are explicit so
// create-admin can receive a password over stdin instead of the process list.
func NewRuntime(input io.Reader, output, errorOutput io.Writer) *Runtime {
	if input == nil {
		input = strings.NewReader("")
	}
	if output == nil {
		output = io.Discard
	}
	if errorOutput == nil {
		errorOutput = io.Discard
	}
	runtime := &Runtime{
		BuildUI:        defaultUIFactory,
		BuildDigestJob: defaultDigestJobFactory,
		BuildCollector: defaultCollectionRunnerFactory,
	}
	runtime.Input = input
	runtime.Output = output
	runtime.ErrorOutput = errorOutput
	runtime.Operations = RuntimeOperations{
		Serve: runtime.serve, Migrate: runtime.migrate, CreateAdmin: runtime.createAdmin,
		CollectOnce: runtime.collectOnce, SendTestMail: runtime.sendTestMail,
	}
	return runtime
}

func (r *Runtime) Execute(ctx context.Context, command string, cfg config.Config, args []string) error {
	if r == nil {
		return errors.New("runtime is not configured")
	}
	var operation CommandOperation
	switch command {
	case "serve":
		operation = r.Operations.Serve
	case "migrate":
		operation = r.Operations.Migrate
	case "create-admin":
		operation = r.Operations.CreateAdmin
	case "collect-once":
		operation = r.Operations.CollectOnce
	case "send-test-mail":
		operation = r.Operations.SendTestMail
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
	if operation == nil {
		return fmt.Errorf("%s command handler is not configured", command)
	}
	return operation(ctx, cfg, args)
}

func (r *Runtime) migrate(ctx context.Context, cfg config.Config, args []string) error {
	if err := rejectCommandArguments("migrate", args); err != nil {
		return err
	}
	pool, err := OpenOwnerPool(ctx, cfg.MigrationDatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	assets, err := migrations.All()
	if err != nil {
		return err
	}
	if err := store.ApplyMigrations(ctx, store.PgxMigrationBeginner{DB: pool}, assets); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	return nil
}

type CreateAdminOptions struct {
	Email, DisplayName string
}

type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *Runtime) createAdmin(ctx context.Context, cfg config.Config, args []string) error {
	options, password, err := parseCreateAdminOptions(args, r.Input, func(path string) (io.ReadCloser, error) {
		return os.Open(path)
	})
	if err != nil {
		return err
	}
	pool, err := OpenOwnerPool(ctx, cfg.MigrationDatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return insertPlatformAdmin(ctx, pool, options, password)
}

func parseCreateAdminOptions(args []string, input io.Reader, openFile func(string) (io.ReadCloser, error)) (CreateAdminOptions, string, error) {
	set := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var options CreateAdminOptions
	var passwordFile string
	var passwordStdin bool
	set.StringVar(&options.Email, "email", "", "administrator email")
	set.StringVar(&options.DisplayName, "name", "", "administrator display name")
	set.StringVar(&passwordFile, "password-file", "", "read password from file")
	set.BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin")
	if err := set.Parse(args); err != nil {
		return CreateAdminOptions{}, "", fmt.Errorf("create-admin options: %w", err)
	}
	if set.NArg() != 0 {
		return CreateAdminOptions{}, "", errors.New("create-admin does not accept positional arguments")
	}
	email, err := normalizeMailbox(options.Email)
	if err != nil {
		return CreateAdminOptions{}, "", fmt.Errorf("email: %w", err)
	}
	options.Email = email
	options.DisplayName = strings.TrimSpace(options.DisplayName)
	if utf8.RuneCountInString(options.DisplayName) > 120 {
		return CreateAdminOptions{}, "", errors.New("name must contain at most 120 characters")
	}
	if passwordStdin == (strings.TrimSpace(passwordFile) != "") {
		return CreateAdminOptions{}, "", errors.New("exactly one of --password-stdin or --password-file is required")
	}
	var source io.ReadCloser
	if passwordStdin {
		if input == nil {
			return CreateAdminOptions{}, "", errors.New("password stdin is unavailable")
		}
		source = io.NopCloser(input)
	} else {
		if openFile == nil {
			return CreateAdminOptions{}, "", errors.New("password file reader is unavailable")
		}
		source, err = openFile(passwordFile)
		if err != nil {
			return CreateAdminOptions{}, "", fmt.Errorf("open password file: %w", err)
		}
	}
	defer source.Close()
	password, err := readPassword(source)
	if err != nil {
		return CreateAdminOptions{}, "", err
	}
	return options, password, nil
}

func readPassword(reader io.Reader) (string, error) {
	const maximumInput = 1024
	raw, err := io.ReadAll(io.LimitReader(reader, maximumInput+1))
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(raw) > maximumInput {
		return "", errors.New("password input is too large")
	}
	password := strings.TrimSuffix(string(raw), "\n")
	password = strings.TrimSuffix(password, "\r")
	if strings.ContainsAny(password, "\r\n") {
		return "", errors.New("password must be a single line")
	}
	if utf8.RuneCountInString(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	if len([]byte(password)) > 72 {
		return "", errors.New("password must contain at most 72 UTF-8 bytes")
	}
	return password, nil
}

func insertPlatformAdmin(ctx context.Context, database sqlExecer, options CreateAdminOptions, password string) error {
	if database == nil {
		return errors.New("administrator database is not configured")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO public.users (email, display_name, password_hash, role, tenant_id)
		VALUES ($1, $2, $3, 'platform_admin', NULL)`, options.Email, options.DisplayName, hash); err != nil {
		return fmt.Errorf("create platform administrator: %w", err)
	}
	return nil
}

func (r *Runtime) collectOnce(ctx context.Context, cfg config.Config, args []string) error {
	if err := rejectCommandArguments("collect-once", args); err != nil {
		return err
	}
	pool, err := OpenRuntimePool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if r.BuildCollector == nil {
		return errors.New("collection runner factory is not configured")
	}
	collector, err := r.BuildCollector(&PostgresRepository{Pool: pool}, cfg)
	if err != nil {
		return fmt.Errorf("build collection runner: %w", err)
	}
	if collector == nil {
		return errors.New("collection runner factory returned no runner")
	}
	result, err := collector(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Output, "수집 %d건, 변경 %d건, 매칭 %d건, 경고 %d건\n", result.Fetched, result.Changed, result.Matched, result.Warnings)
	return nil
}

func runCollector(ctx context.Context, repository CollectorRepository, cfg config.Config, callBudget procurement.CallBudget) (CollectionResult, error) {
	collector := Collector{
		Source:  &ProcurementSource{ServiceKey: cfg.G2BAPIKey, LookupBudget: 500, CallBudget: callBudget},
		Matcher: RuleMatcher{}, Repository: repository,
	}
	return collector.Run(ctx)
}

func defaultCollectionRunnerFactory(repository *PostgresRepository, cfg config.Config) (CollectionRunner, error) {
	if repository == nil || repository.Pool == nil {
		return nil, errors.New("PostgreSQL collector repository is not available")
	}
	callBudget := &PostgresDailyCallBudget{DB: repository.Pool}
	job := CollectionJob{
		Acquire: func(ctx context.Context) (AdvisorySession, error) {
			return repository.Pool.Acquire(ctx)
		},
		Run: func(ctx context.Context) (CollectionResult, error) {
			// The region cache is per collection window. The PostgreSQL budget is
			// shared by every process and retry for the current Seoul calendar day.
			return runCollector(ctx, repository, cfg, callBudget)
		},
	}
	return job.RunLocked, nil
}

func (r *Runtime) sendTestMail(ctx context.Context, cfg config.Config, args []string) error {
	if err := cfg.ValidateCommand("send-test-mail"); err != nil {
		return err
	}
	to, err := parseTestMailOptions(args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.SMTPFrom) == "" {
		return errors.New("SMTP_FROM is required for send-test-mail")
	}
	mailer := SMTPMailer{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser,
		Password: cfg.SMTPPassword,
	}
	if err := sendTestMail(ctx, mailer, cfg.SMTPFrom, to); err != nil {
		return err
	}
	fmt.Fprintf(r.Output, "테스트 메일 발송 완료: %s\n", to)
	return nil
}

func parseTestMailOptions(args []string) (string, error) {
	set := flag.NewFlagSet("send-test-mail", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var to string
	set.StringVar(&to, "to", "", "test recipient")
	if err := set.Parse(args); err != nil {
		return "", fmt.Errorf("send-test-mail options: %w", err)
	}
	if set.NArg() != 0 {
		return "", errors.New("send-test-mail does not accept positional arguments")
	}
	to, err := normalizeMailbox(to)
	if err != nil {
		return "", fmt.Errorf("recipient: %w", err)
	}
	return to, nil
}

func normalizeMailbox(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("a plain email address is required")
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || !strings.EqualFold(address.Address, raw) {
		return "", errors.New("a plain email address is required")
	}
	return strings.ToLower(address.Address), nil
}

func sendTestMail(ctx context.Context, mailer Mailer, from, to string) error {
	return sendTestMailWithPolicy(ctx, mailer, from, to, interactiveMailRetryPolicy)
}

func sendTestMailWithPolicy(ctx context.Context, mailer Mailer, from, to string, policy mailRetryPolicy) error {
	if mailer == nil {
		return errors.New("mailer is not configured")
	}
	message, err := digest.BuildSMTPMessage(from, []string{to}, "나라장터 모니터링 테스트 메일", nil)
	if err != nil {
		return fmt.Errorf("build test mail: %w", err)
	}
	if err := sendMailWithRetry(ctx, mailer, from, to, message, policy); err != nil {
		return fmt.Errorf("send test mail after up to %d attempts: %w", policy.Attempts, err)
	}
	return nil
}

func (r *Runtime) serve(ctx context.Context, cfg config.Config, args []string) error {
	if err := rejectCommandArguments("serve", args); err != nil {
		return err
	}
	if err := cfg.ValidateCommand("serve"); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.SMTPFrom) == "" {
		return errors.New("SMTP_FROM is required for serve")
	}
	pool, err := OpenRuntimePool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository := &PostgresRepository{Pool: pool}
	runCtx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	if r.BuildUI == nil || r.BuildDigestJob == nil || r.BuildCollector == nil {
		return errors.New("serve integration factories are not configured")
	}
	ui, err := r.BuildUI(runCtx, repository, cfg)
	if err != nil {
		return fmt.Errorf("build web UI: %w", err)
	}
	protected, err := NewAuthHandler(ui, repository, []byte(cfg.SessionKey), true, nil)
	if err != nil {
		return fmt.Errorf("build authentication handler: %w", err)
	}
	digestJob, err := r.BuildDigestJob(repository, cfg)
	if err != nil {
		return fmt.Errorf("build digest job: %w", err)
	}
	if digestJob == nil {
		return errors.New("digest job factory returned no job")
	}
	collectionRunner, err := r.BuildCollector(repository, cfg)
	if err != nil {
		return fmt.Errorf("build collection runner: %w", err)
	}
	if collectionRunner == nil {
		return errors.New("collection runner factory returned no runner")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	defer listener.Close()
	fmt.Fprintf(r.Output, "나라장터 모니터링 수신 대기: %s\n", listener.Addr())

	scheduler := jobs.NewScheduler(time.Hour,
		func(jobCtx context.Context, _ time.Time) error {
			_, err := collectionRunner(jobCtx)
			return err
		},
		jobs.Job(digestJob),
	)
	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		runScheduler(runCtx, scheduler, r.ErrorOutput)
	}()
	serveErr := ServeHTTP(runCtx, listener, NewHTTPHandler(protected, repository))
	stopJobs()
	<-jobsDone
	return serveErr
}

type schedulerTicker interface {
	Tick(context.Context, time.Time) error
}

func runScheduler(ctx context.Context, scheduler schedulerTicker, errorsOut io.Writer) {
	if errorsOut == nil {
		errorsOut = io.Discard
	}
	run := func(now time.Time) {
		if err := scheduler.Tick(ctx, now); err != nil && ctx.Err() == nil {
			fmt.Fprintf(errorsOut, "백그라운드 작업 실패: %v\n", err)
		}
	}
	run(time.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			run(now)
		}
	}
}

func defaultUIFactory(ctx context.Context, repository *PostgresRepository, cfg config.Config) (http.Handler, error) {
	if repository == nil || repository.Pool == nil {
		return nil, errors.New("PostgreSQL web repository is not available")
	}
	collectionRunner, err := defaultCollectionRunnerFactory(repository, cfg)
	if err != nil {
		return nil, err
	}
	collectionTrigger, err := newAsyncCollectionTrigger(ctx, collectionRunner)
	if err != nil {
		return nil, err
	}
	mailer := SMTPMailer{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword}
	service := &WebService{
		Repository:      repository,
		QueueCollection: collectionTrigger.Trigger,
		TestMail: func(ctx context.Context, to string) error {
			return sendTestMail(ctx, mailer, cfg.SMTPFrom, to)
		},
	}
	onboarding := InvitationService{
		Store: PostgresInvitationStore{DB: repository.Pool}, Mailer: mailer,
		From: cfg.SMTPFrom, BaseURL: cfg.BaseURL,
	}
	return webui.NewHandlerWithOptions(webui.Options{
		Backend: service, Actions: service, Onboarding: onboarding, MapContext: service.MapRequest,
	})
}

func defaultDigestJobFactory(repository *PostgresRepository, cfg config.Config) (ScheduledJob, error) {
	digestRepository, ok := any(repository).(DigestRepository)
	if !ok {
		return nil, errors.New("PostgreSQL digest repository is not available")
	}
	digestJournal, ok := any(repository).(DigestRunJournal)
	if !ok {
		return nil, errors.New("PostgreSQL digest run journal is not available")
	}
	// Digest delivery is a background job, so it keeps SMTPMailer's independent
	// connection timeout. Only request-driven test and invitation mail uses the
	// shorter interactive retry budget.
	mailer := SMTPMailer{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword}
	return func(ctx context.Context, now time.Time) error {
		runner := DigestRunner{
			Repository: digestRepository, Mailer: mailer, From: cfg.SMTPFrom,
			Now: time.Now,
		}
		return runScheduledDigest(ctx, now, runner, digestJournal, time.Now)
	}, nil
}

func rejectCommandArguments(command string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%s does not accept arguments", command)
	}
	return nil
}
