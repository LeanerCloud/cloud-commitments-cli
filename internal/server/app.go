// Package server provides a cloud-agnostic server implementation for CUDly.
// It supports both AWS Lambda and standard HTTP server modes.
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/internal/analytics"
	"github.com/LeanerCloud/CUDly/internal/api"
	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/commitmentopts"
	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/credentials"
	"github.com/LeanerCloud/CUDly/internal/database"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/email"
	"github.com/LeanerCloud/CUDly/internal/oidc"
	"github.com/LeanerCloud/CUDly/internal/purchase"
	"github.com/LeanerCloud/CUDly/internal/runtime"
	"github.com/LeanerCloud/CUDly/internal/scheduler"
	"github.com/LeanerCloud/CUDly/internal/secrets"
	"github.com/LeanerCloud/CUDly/internal/server/scheduledauth"
	pkgladder "github.com/LeanerCloud/CUDly/pkg/ladder"
	"github.com/LeanerCloud/CUDly/pkg/logging"
	awsladder "github.com/LeanerCloud/CUDly/providers/aws/ladder"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Application holds all components of the CUDly server.
type Application struct {
	Config      config.StoreInterface
	API         *api.Handler
	Scheduler   SchedulerInterface
	Purchase    PurchaseManagerInterface
	Email       email.SenderInterface // Multi-cloud email sender (AWS SES, GCP SendGrid, Azure ACS)
	Auth        *auth.Service
	RateLimiter api.RateLimiterInterface // Distributed rate limiter (DB-backed for multi-instance)
	Analytics   AnalyticsStoreInterface  // Analytics store for savings data
	// AnalyticsCollector aggregates savings into snapshots on a schedule.
	// Nil until reinitializeAfterConnect wires it; the collect task no-ops
	// when nil so test builds without a DB stay quiet.
	AnalyticsCollector AnalyticsCollectorInterface
	Version            string
	DB                 *database.Connection // PostgreSQL database connection
	TaskLocker         TaskLocker           // Advisory lock for scheduled tasks (defaults to DB)

	// LadderCapabilityFactory constructs a LadderCapability for the given region
	// and accountID. It is called once per ladder_run task invocation.
	// Defaults to awsladder.NewFromAWSConfig in production; tests replace it
	// with a fake factory that returns a hermetic LadderCapability.
	LadderCapabilityFactory func(ctx context.Context, region, accountID string) (pkgladder.LadderCapability, error)

	// LadderAccountResolver resolves the Lambda's own AWS account ID and region
	// for the single-account ladder gate (Q1). It MUST fail loud when the
	// account cannot be determined: the account ID gates which configs run, so a
	// transient STS failure must abort the whole ladder_run rather than silently
	// skip every config as multi-account. Nil in production -> the default
	// STS-backed resolver (defaultLadderAccountResolver); tests inject a stub.
	LadderAccountResolver func(ctx context.Context) (accountID, region string, err error)

	// Static file serving directory (from STATIC_DIR env var)
	staticDir string

	// Lazy initialization fields for PostgreSQL (Lambda ENI readiness)
	dbConfig          *database.Config
	secretResolver    secrets.Resolver
	dbMu              sync.Mutex
	dbConnected       bool
	dbErr             error
	appConfig         ApplicationConfig
	signer            oidc.Signer
	scheduledAuth     *scheduledauth.Validator
	runMigrationsFunc func(ctx context.Context, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error
	migrationsTimeout time.Duration
	migrationMu       sync.Mutex

	// encKeySource is the env var name that resolved the credential encryption
	// key (e.g. "CREDENTIAL_ENCRYPTION_KEY_SECRET_NAME"). Set during
	// reinitializeAfterConnect; surfaced via /health.
	encKeySource string

	// State from the most recent migration attempt. Surfaced by /health so
	// ops can see failures. Protected by its OWN dedicated mutex -- NOT
	// dbMu, because ensureDB holds dbMu for its full duration. If /health
	// reached into dbMu it would block behind a long-running ensureDB,
	// which defeats the point of non-fatal migrations.
	migrationErr        error
	migrationFinishedAt time.Time
}

// ApplicationConfig holds all env-based configuration for the application.
type ApplicationConfig struct {
	ScheduledTaskSecret     string
	IssuerURL               string
	ScheduledTaskSecretName string
	DefaultPaymentOption    string
	Version                 string
	DefaultRampSchedule     string
	CORSAllowedOrigin       string
	DashboardURL            string
	DashboardBucket         string
	APIKeySecretARN         string
	Analytics               AnalyticsConfig
	NotificationDaysBefore  int
	DefaultCoverage         float64
	DefaultTerm             int
	EnableDashboard         bool
	IsLambda                bool
}

// ExternalDeps holds pre-built external dependencies that require infrastructure.
type ExternalDeps struct {
	EmailSender    email.SenderInterface
	ConfigStore    config.StoreInterface
	DBConfig       *database.Config
	SecretResolver secrets.Resolver
	STSClient      purchase.STSClient
}

// defaultMigrationsTimeout bounds how long ensureDB waits for migrations
// before giving up and proceeding. Set well above the time a normal index
// build / DDL takes (the prior 20s could be blown mid-run by a single index
// build on a growing table, leaving schema_migrations.dirty = true and
// fail-opening every later boot) yet still comfortably under the Lambda
// 300s hard limit, so a slow-but-legitimate migration completes rather than
// being killed inside ensureDB. Override per-environment with
// CUDLY_MIGRATION_TIMEOUT (e.g. "180s") for genuinely long migrations.
const defaultMigrationsTimeout = 120 * time.Second

// resolveMigrationsTimeout reads CUDLY_MIGRATION_TIMEOUT from the environment.
// It is called once in NewApplicationFromDeps to initialize
// Application.migrationsTimeout. Because the timeout lives on the struct
// (not a package-level var), tests can set it on a specific Application
// instance without serializing parallel tests (04-M3).
func resolveMigrationsTimeout() time.Duration {
	v := os.Getenv("CUDLY_MIGRATION_TIMEOUT")
	if v == "" {
		return defaultMigrationsTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("CUDLY_MIGRATION_TIMEOUT invalid (%q); using default %s", v, defaultMigrationsTimeout) // #nosec G706 -- env var value is operator-controlled configuration; logged for diagnostics
		return defaultMigrationsTimeout
	}
	return d
}

// recordMigrationResult stores the outcome of the most recent migration
// attempt. Takes migrationMu briefly. Called from inside ensureDB which
// holds dbMu — lock order is dbMu then migrationMu. Never take them the
// other way around.
func (app *Application) recordMigrationResult(err error) {
	app.migrationMu.Lock()
	defer app.migrationMu.Unlock()
	app.migrationErr = err
	app.migrationFinishedAt = time.Now()
}

// snapshotMigrationState returns a point-in-time copy of the migration
// state suitable for /health rendering.
func (app *Application) snapshotMigrationState() (err error, finishedAt time.Time) { //nolint:revive,staticcheck // error-return/ST1008: named return order matches struct field order for clarity
	app.migrationMu.Lock()
	defer app.migrationMu.Unlock()
	return app.migrationErr, app.migrationFinishedAt
}

// runMigrationsBounded runs app.runMigrationsFunc in a goroutine bounded by
// app.migrationsTimeout. The returned error is either the runner's own error,
// a panic wrapped as an error, or a timeout error -- never a
// nil-with-goroutine-still-alive. The goroutine is guaranteed to have exited
// before this function returns (the timeout branch waits on <-done after
// canceling the ctx), so no orphan goroutine survives past this call --
// critical on Lambda where goroutines freeze between invocations.
//
// Using instance fields (not package globals) makes it safe to call
// t.Parallel() in tests that override migrationsTimeout or runMigrationsFunc
// on a specific Application -- no shared mutable global state (04-M3).
func (app *Application) runMigrationsBounded(pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error {
	return runMigrationsBoundedWith(pool, migrationsPath, adminEmail, adminPassword, app.migrationsTimeout, app.runMigrationsFunc)
}

// runMigrationsBoundedWith is the underlying implementation used by the
// Application method and by the package-level tests that pre-date the
// instance-field migration (04-M3). Tests that want to exercise the
// timeout/panic/success paths can call this directly with a fake runner.
func runMigrationsBoundedWith(pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string, timeout time.Duration, runner func(ctx context.Context, pool *pgxpool.Pool, migrationsPath, adminEmail, adminPassword string) error) error {
	migCtx, cancelMig := context.WithTimeout(context.Background(), timeout)
	defer cancelMig()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("migration panic: %v", r)
			}
		}()
		done <- runner(migCtx, pool, migrationsPath, adminEmail, adminPassword)
	}()

	select {
	case err := <-done:
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("migration timed out after %s: %w", timeout, err)
		}
		return err
	case <-migCtx.Done():
		<-done
		return fmt.Errorf("migration timed out after %s: %w", timeout, migCtx.Err())
	}
}

// resolveOIDCIssuerURL picks the canonical OIDC issuer URL for the
// deployment. CUDLY_ISSUER_URL (set by the infra module to the
// Function URL / Container App URL / Cloud Run URL) wins; DashboardURL
// is the backstop.
func resolveOIDCIssuerURL(cfg ApplicationConfig) string {
	if cfg.IssuerURL != "" {
		return strings.TrimRight(cfg.IssuerURL, "/")
	}
	return strings.TrimRight(cfg.DashboardURL, "/")
}

// decorateSenderWithMute wires per-recipient mute suppression and the
// List-Unsubscribe base URL into the concrete SES (*email.Sender) or SMTP
// (*email.SMTPSender) sender. Other implementations (e.g. the no-op sender used
// when EMAIL_ENABLED=false) are returned unchanged. configStore satisfies
// email.MuteChecker via its IsNotificationMuted method. dashboardURL is the
// already-trimmed dashboard base URL; an empty value disables the
// List-Unsubscribe header (the mute checker is still wired in).
func decorateSenderWithMute(sender email.SenderInterface, mc email.MuteChecker, dashboardURL string) email.SenderInterface {
	switch s := sender.(type) {
	case *email.Sender:
		return s.WithMuteChecker(mc).WithUnsubscribeBaseURL(dashboardURL)
	case *email.SMTPSender:
		return s.WithMuteChecker(mc).WithUnsubscribeBaseURL(dashboardURL)
	default:
		return sender
	}
}

// LoadApplicationConfig reads all configuration from environment variables.
func LoadApplicationConfig() ApplicationConfig {
	version := os.Getenv("VERSION")
	if version == "" {
		version = "dev"
	}

	return ApplicationConfig{
		Version:                version,
		NotificationDaysBefore: getEnvInt("NOTIFICATION_DAYS_BEFORE", 3),
		DefaultTerm:            getEnvInt("DEFAULT_TERM", 3),
		DefaultPaymentOption:   os.Getenv("DEFAULT_PAYMENT_OPTION"),
		DefaultCoverage:        getEnvFloat("DEFAULT_COVERAGE", 80),
		DefaultRampSchedule:    os.Getenv("DEFAULT_RAMP_SCHEDULE"),
		APIKeySecretARN:        os.Getenv("API_KEY_SECRET_ARN"),
		EnableDashboard:        os.Getenv("ENABLE_DASHBOARD") == "true",
		DashboardBucket:        os.Getenv("DASHBOARD_BUCKET"),
		DashboardURL:           os.Getenv("DASHBOARD_URL"),
		IssuerURL:              os.Getenv("CUDLY_ISSUER_URL"),
		CORSAllowedOrigin:      os.Getenv("CORS_ALLOWED_ORIGIN"),
		// SCHEDULED_TASK_SECRET (plaintext) is the legacy dev-only path.
		// SCHEDULED_TASK_SECRET_NAME (secret-store name) is preferred in prod;
		// NewApplicationFromDeps resolves it via the SecretResolver at init.
		ScheduledTaskSecret:     os.Getenv("SCHEDULED_TASK_SECRET"),
		ScheduledTaskSecretName: os.Getenv("SCHEDULED_TASK_SECRET_NAME"),
		IsLambda:                runtime.IsLambda(),
		Analytics:               LoadAnalyticsConfig(),
	}
}

// validateAppConfigEnvDefaults validates the money-moving env-sourced defaults
// at the startup boundary. Both DEFAULT_PAYMENT_OPTION and DEFAULT_RAMP_SCHEDULE
// flow into the purchase manager as system-wide defaults; a typo propagates
// silently into every purchase unless we reject it here (issue #1026).
// Empty values are always valid (means "use the purchase manager's built-in default").
func validateAppConfigEnvDefaults(cfg ApplicationConfig) error {
	if err := config.ValidatePaymentOptionEnv(cfg.DefaultPaymentOption); err != nil {
		return fmt.Errorf("invalid DEFAULT_PAYMENT_OPTION: %w", err)
	}
	if err := config.ValidateRampScheduleEnv(cfg.DefaultRampSchedule); err != nil {
		return fmt.Errorf("invalid DEFAULT_RAMP_SCHEDULE: %w", err)
	}
	return nil
}

// resolveScheduledTaskSecret resolves SCHEDULED_TASK_SECRET_NAME to its real
// value via the configured SecretResolver (Azure Key Vault / AWS Secrets
// Manager) when possible. Falls back to cfg.ScheduledTaskSecret (plaintext
// SCHEDULED_TASK_SECRET env var) if the resolver is absent or the lookup
// fails.
//
// The second return value is non-nil only when a SecretName was configured
// AND the resolver failed. Callers in bearer mode MUST propagate this error
// so startup fails with the real cause (e.g. "failed to resolve
// scheduled-task secret from <name>: <err>") rather than the misleading
// downstream "bearer mode requires SCHEDULED_TASK_SECRET" that would
// otherwise surface when the empty fallback reaches buildScheduledAuth (04-M4).
//
// Security note: if both SCHEDULED_TASK_SECRET (plaintext) and
// SCHEDULED_TASK_SECRET_NAME (secret-store path) are set, we warn loudly
// because the plaintext value is visible in Lambda env / Terraform state.
// The secret-store path is always preferred when both are present.
func resolveScheduledTaskSecret(ctx context.Context, cfg ApplicationConfig, resolver secrets.Resolver) (string, error) {
	if cfg.ScheduledTaskSecretName != "" && cfg.ScheduledTaskSecret != "" {
		log.Printf("SECURITY WARNING: both SCHEDULED_TASK_SECRET (plaintext) and " +
			"SCHEDULED_TASK_SECRET_NAME are set. The plaintext value is visible in " +
			"Lambda environment variables and Terraform state. " +
			"Remove SCHEDULED_TASK_SECRET and rely on SCHEDULED_TASK_SECRET_NAME only.")
	}

	if cfg.ScheduledTaskSecretName == "" || resolver == nil {
		return cfg.ScheduledTaskSecret, nil
	}
	resolved, err := resolver.GetSecret(ctx, cfg.ScheduledTaskSecretName)
	if err != nil {
		log.Printf("scheduled task secret resolution failed for %q: %v (falling back to SCHEDULED_TASK_SECRET)", cfg.ScheduledTaskSecretName, err)
		return cfg.ScheduledTaskSecret, err
	}
	return resolved, nil
}

// envSourceOS implements scheduledauth.EnvSource against os.Getenv. The
// scheduledauth package owns the env var names and parse rules so they
// stay aligned with Terraform.
type envSourceOS struct{}

func (envSourceOS) Get(key string) string { return os.Getenv(key) }

// buildScheduledAuthFromConfig wires up the /api/scheduled/* validator from
// a pre-loaded scheduledauth.Config. The bearer secret is injected from cfg
// rather than re-read from env — in production cfg.ScheduledTaskSecret was
// already resolved from Key Vault / Secrets Manager by the caller.
func buildScheduledAuthFromConfig(cfg ApplicationConfig, saCfg scheduledauth.Config) (*scheduledauth.Validator, error) {
	// In bearer mode, override the env-supplied secret with the one
	// already resolved from KV / SM. LoadConfig reads SCHEDULED_TASK_SECRET
	// directly which is fine for local dev where the env carries the
	// plaintext, but in prod cfg.ScheduledTaskSecret is the authoritative
	// value (resolved by resolveScheduledTaskSecret upstream).
	if saCfg.Mode == scheduledauth.ModeBearer {
		saCfg.Bearer = cfg.ScheduledTaskSecret
	}
	return scheduledauth.New(saCfg)
}

// initScheduledAuth loads the scheduledauth config, resolves the bearer
// secret (failing fast in bearer mode if the resolver errors), builds the
// validator, and warms up JWKS. Extracted from NewApplicationFromDeps to
// keep its cyclomatic complexity within the project limit (04-M4).
func initScheduledAuth(ctx context.Context, cfg *ApplicationConfig, resolver secrets.Resolver) (*scheduledauth.Validator, error) {
	saCfg, err := scheduledauth.LoadConfig(envSourceOS{})
	if err != nil {
		return nil, fmt.Errorf("scheduled-task auth init: %w", err)
	}

	resolvedSecret, secretErr := resolveScheduledTaskSecret(ctx, *cfg, resolver)
	if secretErr != nil && saCfg.Mode == scheduledauth.ModeBearer {
		return nil, fmt.Errorf("failed to resolve scheduled-task secret from %q: %w", cfg.ScheduledTaskSecretName, secretErr)
	}
	cfg.ScheduledTaskSecret = resolvedSecret

	v, err := buildScheduledAuthFromConfig(*cfg, saCfg)
	if err != nil {
		return nil, fmt.Errorf("scheduled-task auth init: %w", err)
	}

	warmCtx, warmCancel := context.WithTimeout(ctx, 5*time.Second)
	v.Warmup(warmCtx)
	warmCancel()
	return v, nil
}

// NewApplicationFromDeps creates an Application from pre-built configuration and dependencies.
// This is the testable constructor - all external I/O is done before calling this.
func NewApplicationFromDeps(ctx context.Context, cfg ApplicationConfig, deps ExternalDeps) (*Application, error) {
	if deps.DBConfig == nil {
		return nil, fmt.Errorf("database configuration required: DBConfig must be provided")
	}

	// Validate money-moving env defaults at the startup boundary -- consistent with the
	// fail-fast posture used for scheduledauth mode and ADMIN_PASSWORD_SECRET (issue #1026).
	if err := validateAppConfigEnvDefaults(cfg); err != nil {
		return nil, err
	}

	// Wire up the /api/scheduled/* auth validator. initScheduledAuth loads
	// the mode config, resolves the bearer secret with fail-fast semantics
	// in bearer mode (04-M4), builds the validator, and warms up JWKS.
	scheduledAuth, err := initScheduledAuth(ctx, &cfg, deps.SecretResolver)
	if err != nil {
		return nil, err
	}

	// Construct the OIDC issuer signer once per deployment. Nil means
	// the deployment has not opted into the federated flow yet — all
	// OIDC-dependent paths (handler_oidc.go, purchase manager Azure
	// federated credential) fall back to their legacy behaviors.
	signer, signerErr := oidc.NewSignerFromEnv(ctx)
	if signerErr != nil {
		log.Printf("oidc signer init failed (federated flow disabled): %v", signerErr)
		signer = nil
	}

	// Prime the issuer URL cache. Priority order:
	//  1. CUDLY_ISSUER_URL / DASHBOARD_URL via resolveOIDCIssuerURL
	//  2. AWS Lambda self-lookup via lambda:GetFunctionUrlConfig
	//
	// The handler_oidc.go path is still a backstop (populates the
	// cache from the first inbound request's DomainName), but doing
	// it here means scheduled-task cold starts don't race the first
	// inbound HTTP request.
	if issuer := resolveOIDCIssuerURL(cfg); issuer != "" {
		if err := oidc.SetIssuerURL(issuer); err != nil {
			log.Printf("WARN: oidc issuer URL invalid, ignoring: %v", err)
		}
	} else if cfg.IsLambda {
		primeCtx, primeCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := oidc.PrimeIssuerURLFromLambda(primeCtx); err != nil {
			log.Printf("oidc issuer cache not primed from lambda: %v (falling back to request-driven populate)", err)
		}
		primeCancel()
	}

	// Initialize purchase manager
	purchaseManager := purchase.NewManager(purchase.ManagerConfig{
		ConfigStore:            deps.ConfigStore,
		EmailSender:            deps.EmailSender,
		STSClient:              deps.STSClient,
		NotificationDaysBefore: cfg.NotificationDaysBefore,
		DefaultTerm:            cfg.DefaultTerm,
		DefaultPaymentOption:   cfg.DefaultPaymentOption,
		DefaultCoverage:        cfg.DefaultCoverage,
		DefaultRampSchedule:    cfg.DefaultRampSchedule,
		OIDCSigner:             signer,
		OIDCIssuerURL:          resolveOIDCIssuerURL(cfg),
	})

	// Initialize scheduler
	sched := scheduler.NewScheduler(scheduler.SchedulerConfig{
		ConfigStore:     deps.ConfigStore,
		PurchaseManager: purchaseManager,
		EmailSender:     deps.EmailSender,
		STSClient:       deps.STSClient,
		IsLambda:        runtime.IsLambda(),
	})

	// Auth store will be initialized lazily after DB connection
	var authStore auth.StoreInterface
	log.Println("PostgreSQL auth store will be initialized on first request")

	// Initialize auth service
	authService := auth.NewService(auth.ServiceConfig{
		Store:           authStore,
		EmailSender:     deps.EmailSender,
		SessionDuration: 24 * time.Hour,
		DashboardURL:    cfg.DashboardURL,
	})

	// Initialize rate limiter based on runtime environment.
	// Lambda: start with an in-memory limiter immediately so the first cold-start
	// request is protected. ensureDB() swaps it for the DB-backed limiter once the
	// database connection is established (distributed state across warm containers).
	// Fargate/containers: in-memory is the permanent implementation because the
	// process is long-lived and single-instance.
	rateLimiter := api.RateLimiterInterface(api.NewInMemoryRateLimiter())
	if !cfg.IsLambda {
		log.Println("Initialized in-memory rate limiter for single-instance deployment (Fargate/Container)")
	} else {
		log.Println("Initialized in-memory rate limiter for Lambda cold-start (will be upgraded to DB-backed on first DB connect)")
	}

	// Initialize API handler
	apiHandler := api.NewHandler(api.HandlerConfig{
		ConfigStore:       deps.ConfigStore,
		PurchaseManager:   purchaseManager,
		Scheduler:         sched,
		AuthService:       newAuthServiceAdapter(authService),
		APIKeySecretARN:   cfg.APIKeySecretARN,
		EnableDashboard:   cfg.EnableDashboard,
		DashboardBucket:   cfg.DashboardBucket,
		CORSAllowedOrigin: cfg.CORSAllowedOrigin,
		RateLimiter:       rateLimiter,
		EmailNotifier:     deps.EmailSender,
		DashboardURL:      cfg.DashboardURL,
		OIDCSigner:        signer,
		OIDCIssuerURL:     resolveOIDCIssuerURL(cfg),
	})

	log.Printf("CUDly Server initialization complete")

	return &Application{
		Config:            deps.ConfigStore,
		API:               apiHandler,
		Scheduler:         sched,
		Purchase:          purchaseManager,
		Email:             deps.EmailSender,
		Auth:              authService,
		RateLimiter:       rateLimiter,
		Version:           cfg.Version,
		DB:                nil, // Will be initialized lazily on first request
		staticDir:         staticDirFromEnv(),
		dbConfig:          deps.DBConfig,
		secretResolver:    deps.SecretResolver,
		appConfig:         cfg,
		signer:            signer,
		scheduledAuth:     scheduledAuth,
		migrationsTimeout: resolveMigrationsTimeout(),
		runMigrationsFunc: migrations.RunMigrations,
	}, nil
}

// NewApplication creates and initializes a new Application instance.
// version overrides the VERSION env var when non-empty, so cmd entrypoints
// can pass the ldflags-stamped value directly instead of round-tripping
// through os.Setenv / os.Getenv (04-N1). Pass "" to fall back to the env.
func NewApplication(ctx context.Context, version string) (*Application, error) {
	cfg := LoadApplicationConfig()
	if version != "" {
		cfg.Version = version
	}

	if err := cfg.Analytics.Validate(); err != nil {
		return nil, fmt.Errorf("invalid analytics configuration: %w", err)
	}

	log.Printf("CUDly Server initializing, version: %s", cfg.Version)

	// Initialize configuration store (PostgreSQL). The store connects lazily on
	// first request, so ConfigStore is wired as nil here (see initConfigStore).
	dbConfig, secretResolver, err := initConfigStore(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config store: %w", err)
	}

	// Initialize email sender (auto-detects cloud provider from SECRET_PROVIDER env var)
	emailSender, err := email.NewSenderFromEnvironment(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize email sender: %w", err)
	}
	// Initialize AWS config for STS client
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	stsClient := sts.NewFromConfig(awsCfg)

	deps := ExternalDeps{
		EmailSender:    emailSender,
		ConfigStore:    nil, // created lazily on first request (see initConfigStore)
		DBConfig:       dbConfig,
		SecretResolver: secretResolver,
		STSClient:      stsClient,
	}

	app, err := NewApplicationFromDeps(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	app.LadderCapabilityFactory = awsladder.NewFromAWSConfig
	return app, nil
}

// ensureDB ensures the database connection is established (lazy initialization).
// This is called on first request to ensure Lambda ENI is ready.
// Unlike sync.Once, transient failures allow retry on subsequent requests.
func (app *Application) ensureDB(ctx context.Context) error {
	// Only attempt lazy init if we have a dbConfig
	if app.dbConfig == nil {
		return nil // Not using PostgreSQL
	}

	app.dbMu.Lock()
	defer app.dbMu.Unlock()

	// Already connected successfully
	if app.dbConnected {
		return nil
	}

	// If a previous error was set (e.g. in tests), return it
	if app.dbErr != nil {
		return app.dbErr
	}

	log.Println("Establishing PostgreSQL connection (lazy initialization)...")

	// Connect to PostgreSQL
	dbConn, err := database.NewConnection(ctx, app.dbConfig, app.secretResolver)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Store the connection
	app.DB = dbConn
	log.Println("PostgreSQL connection established successfully")

	// Run migrations if AutoMigrate is enabled. Failures are non-fatal:
	// we log, surface via /health's migrations check, and proceed. The app
	// stays up; handlers that need the missing schema error at query time.
	// See specs/migration-resilience.md (or the plan) for the rationale.
	if app.dbConfig.AutoMigrate {
		log.Println("Running database migrations...")

		adminEmail := os.Getenv("ADMIN_EMAIL")
		adminPassword, err := app.resolveAdminPassword(ctx)
		if err != nil {
			return err // secret-resolution failure is still fatal — env/config, not a migration runtime error
		}

		migErr := app.runMigrationsBounded(dbConn.Pool(), app.dbConfig.MigrationsPath, adminEmail, adminPassword)
		app.recordMigrationResult(migErr)

		if migErr != nil {
			log.Printf("⚠️ Migration failed — app continuing with existing schema: %v", migErr)
			// Intentionally do NOT return, do NOT close dbConn, do NOT clear app.DB.
		} else {
			log.Println("Database migrations completed successfully")
		}
	}

	// Re-initialize all stores and services with the live DB connection
	if err := app.reinitializeAfterConnect(ctx, dbConn); err != nil {
		dbConn.Close()
		app.DB = nil
		return fmt.Errorf("failed to reinitialize after DB connect: %w", err)
	}
	app.dbConnected = true

	return nil
}

// resolveAdminPassword returns the admin password from Secrets Manager.
// It requires ADMIN_PASSWORD_SECRET to be set to a valid secret ARN/name.
// If absent, it returns an error to prevent startup with no secret source.
func (app *Application) resolveAdminPassword(ctx context.Context) (string, error) {
	secret := os.Getenv("ADMIN_PASSWORD_SECRET")
	if secret == "" {
		return "", fmt.Errorf("ADMIN_PASSWORD_SECRET environment variable is required but not set; refusing to start without a Secrets Manager ARN")
	}
	if app.secretResolver == nil {
		return "", fmt.Errorf("secret resolver is not configured; cannot resolve ADMIN_PASSWORD_SECRET")
	}
	resolved, err := app.secretResolver.GetSecret(ctx, secret)
	if err != nil {
		return "", fmt.Errorf("failed to resolve admin password secret: %w", err)
	}
	return resolved, nil
}

// loadAndGuardEncryptionKey loads the credential-encryption key via the
// shared secrets.Resolver and refuses to return the all-zero dev key unless
// CREDENTIAL_ENCRYPTION_ALLOW_DEV_KEY=1 is explicitly set. Logs which env
// var resolved the key (name only — never the key value). The returned
// keySource is propagated to the API handler so /health can surface it.
func loadAndGuardEncryptionKey(ctx context.Context, resolver secrets.Resolver) (key []byte, keySource string, err error) {
	encKey, source, err := credentials.LoadKey(ctx, resolver)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load credential encryption key: %w", err)
	}
	// Defense in depth: LoadKey already gates this, but a guard here protects
	// against a future regression where LoadKey is changed to silently fall
	// back again.
	if bytes.Equal(encKey, credentials.DevKey()) && os.Getenv(credentials.EnvAllowDev) != "1" {
		return nil, "", fmt.Errorf("credentials: refusing to start with all-zero dev key (set %s=1 for local dev only)", credentials.EnvAllowDev)
	}
	log.Printf("credentials: loaded encryption key via %s", source)
	return encKey, source, nil
}

// initAuthService derives a stable CSRF key from encKey via HKDF and
// constructs the auth.Service. Extracted from reinitializeAfterConnect to keep
// cyclomatic complexity within the project limit. Returns an error if CSRF key
// derivation fails or if NewService returns nil (fail-closed).
func (app *Application) initAuthService(authStore *auth.PostgresStore, encKey []byte) (*auth.Service, error) {
	// Derive a STABLE CSRF key from the encryption key so every instance and
	// every Lambda cold-start uses the same key (closes the cross-instance CSRF
	// failure: a token minted on one instance must validate on another). Without
	// an explicit CSRFKey, auth.NewService would mint a random per-process key,
	// invalidating tokens across the fleet and on every cold-start.
	csrfKey, err := auth.DeriveCSRFKey(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive CSRF key: %w", err)
	}
	svc := auth.NewService(auth.ServiceConfig{
		Store:            authStore,
		EmailSender:      app.Email,
		SessionDuration:  24 * time.Hour,
		DashboardURL:     app.appConfig.DashboardURL,
		OnPasswordChange: buildAdminPasswordSyncCallback(authStore, app.secretResolver),
		CSRFKey:          csrfKey,
	})
	if svc == nil {
		return nil, fmt.Errorf("failed to create auth service")
	}
	return svc, nil
}

// reinitializeAfterConnect re-creates all stores and services that depend on the
// database connection. This is called after the lazy DB connect succeeds.
// Returns an error if any store or service initialization fails.
func (app *Application) reinitializeAfterConnect(ctx context.Context, dbConn *database.Connection) error {
	// Initialize config store with the connection
	pgStore := config.NewPostgresStore(dbConn)
	if pgStore == nil {
		return fmt.Errorf("failed to create PostgreSQL config store")
	}
	app.Config = pgStore
	// Wire per-recipient mute suppression + List-Unsubscribe now that the config
	// store (which implements email.MuteChecker via IsNotificationMuted) is live.
	// Moving this here from NewApplication is correct: mute lookups require DB,
	// so decorating before the store is available would panic on the first call.
	app.Email = decorateSenderWithMute(app.Email, pgStore, strings.TrimRight(app.appConfig.DashboardURL, "/"))

	// Initialize auth store with the connection
	authStore := auth.NewPostgresStore(dbConn)
	if authStore == nil {
		return fmt.Errorf("failed to create PostgreSQL auth store")
	}

	// Load the credential encryption key (deploy-provided, stable across
	// instances and cold-starts) up front: it seeds the CSRF key below and is
	// reused for the credential store further down. loadAndGuardEncryptionKey
	// memoizes via sync.Once, so the later credential-store call is free.
	encKey, encKeySource, err := loadAndGuardEncryptionKey(ctx, app.secretResolver)
	if err != nil {
		return err
	}

	// Update auth service with PostgreSQL auth store. The CSRF key is derived
	// from the encryption key so every instance uses the same stable key.
	authSvc, err := app.initAuthService(authStore, encKey)
	if err != nil {
		return err
	}
	app.Auth = authSvc

	// Initialize distributed rate limiter for Lambda (multi-instance)
	// For Fargate/containers, we already have in-memory rate limiter from startup
	if app.appConfig.IsLambda {
		dbRL := api.NewDBRateLimiter(dbConn.Pool())
		// Start the scheduled cleanup worker so perpetually-denied keys (whose
		// count never resets to 1) are still evicted on a fixed schedule (02-M2).
		dbRL.StartCleanupWorker(ctx)
		app.RateLimiter = dbRL
		log.Println("Initialized database-backed rate limiter for Lambda (distributed state)")
	}

	// Initialize analytics store for savings data and materialized views, plus
	// the snapshot collector behind the scheduled analytics_collect task.
	app.Analytics = analytics.NewPostgresAnalyticsStore(dbConn)
	collector, err := newAnalyticsCollector(dbConn, app.Config)
	if err != nil {
		return fmt.Errorf("failed to create analytics collector: %w", err)
	}
	app.AnalyticsCollector = collector
	log.Println("Initialized PostgreSQL analytics store and snapshot collector")

	// Initialize credential store (AES-256-GCM encrypted credential blobs).
	// encKey/encKeySource were loaded above (memoized) to seed the CSRF key.
	credStore := credentials.NewCredentialStore(dbConn.Pool(), encKey)
	app.encKeySource = encKeySource
	log.Println("Initialized encrypted credential store")

	// Re-initialize purchase manager with multi-account deps now that credStore is available.
	// The initial manager (created before DB connect) lacks CredentialStore and AssumeRoleSTS,
	// so the multi-account fan-out guard (m.credStore != nil) would always be false without this.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config for cross-account STS: %w", err)
	}
	app.Purchase = purchase.NewManager(purchase.ManagerConfig{
		ConfigStore:            app.Config,
		EmailSender:            app.Email,
		STSClient:              sts.NewFromConfig(awsCfg),
		AssumeRoleSTS:          sts.NewFromConfig(awsCfg),
		AmbientAWSCreds:        awsCfg.Credentials,
		CredentialStore:        credStore,
		NotificationDaysBefore: app.appConfig.NotificationDaysBefore,
		DefaultTerm:            app.appConfig.DefaultTerm,
		DefaultPaymentOption:   app.appConfig.DefaultPaymentOption,
		DefaultCoverage:        app.appConfig.DefaultCoverage,
		DefaultRampSchedule:    app.appConfig.DefaultRampSchedule,
		DashboardURL:           app.appConfig.DashboardURL,
		OIDCSigner:             app.signer,
		OIDCIssuerURL:          resolveOIDCIssuerURL(app.appConfig),
	})
	log.Println("Re-initialized purchase manager with credential store and cross-account STS")

	// Re-initialize scheduler with per-account credential resolution.
	app.Scheduler = scheduler.NewScheduler(scheduler.SchedulerConfig{
		ConfigStore:     app.Config,
		PurchaseManager: app.Purchase,
		EmailSender:     app.Email,
		CredentialStore: credStore,
		OIDCSigner:      app.signer,
		OIDCIssuerURL:   resolveOIDCIssuerURL(app.appConfig),
		AssumeRoleSTS:   sts.NewFromConfig(awsCfg),
		STSClient:       sts.NewFromConfig(awsCfg),
		IsLambda:        app.appConfig.IsLambda,
	})

	// Build the commitment-options probe/cache service. It lazily probes
	// AWS reserved-offerings APIs through the first enabled AWS account and
	// persists the result so subsequent calls come from the DB. Failures
	// anywhere along the chain (no account connected, probe denied) collapse
	// to ErrNoData; the API handler and save-side validator both degrade
	// gracefully, so this is wired unconditionally.
	commitmentOpts := commitmentopts.New(
		commitmentopts.NewPostgresStore(dbConn),
		app.Config,
		func(ctx context.Context, acct *config.CloudAccount) (aws.Config, error) {
			stsClient := sts.NewFromConfig(awsCfg)
			prov, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, acct, credStore, stsClient,
				credentials.AWSResolveOptions{AmbientProvider: awsCfg.Credentials})
			if err != nil {
				return aws.Config{}, err
			}
			// us-east-1 hardcoded because reserved offerings are global
			// facts (not AZ-scoped), and us-east-1 has the widest
			// instance-type coverage. This fails silently for GovCloud
			// / China-partition accounts — those return ErrNoData from
			// the probe and the frontend falls back to hardcoded rules,
			// which is acceptable since those partitions rarely need
			// dynamic commitment detection.
			return awsconfig.LoadDefaultConfig(ctx,
				awsconfig.WithCredentialsProvider(prov),
				awsconfig.WithRegion("us-east-1"),
			)
		},
		commitmentopts.DefaultProbers(),
	)

	// Update API handler with new config store, scheduler, and rate limiter.
	// AnalyticsClient is Postgres-backed (see api.NewPostgresAnalyticsClient) —
	// it aggregates purchase_history on demand so the History UI charts work
	// without requiring a separate S3/Athena deployment.
	app.API = api.NewHandler(api.HandlerConfig{
		ConfigStore:         app.Config,
		CredentialStore:     credStore,
		PurchaseManager:     app.Purchase,
		Scheduler:           app.Scheduler,
		AuthService:         newAuthServiceAdapter(app.Auth),
		APIKeySecretARN:     app.appConfig.APIKeySecretARN,
		EnableDashboard:     app.appConfig.EnableDashboard,
		DashboardBucket:     app.appConfig.DashboardBucket,
		CORSAllowedOrigin:   app.appConfig.CORSAllowedOrigin,
		RateLimiter:         app.RateLimiter,
		EmailNotifier:       app.Email,
		DashboardURL:        app.appConfig.DashboardURL,
		AnalyticsClient:     api.NewPostgresAnalyticsClient(dbConn),
		AnalyticsCollector:  app.AnalyticsCollector,
		AnalyticsSnapshots:  analytics.NewPostgresAnalyticsStore(dbConn),
		OIDCSigner:          app.signer,
		OIDCIssuerURL:       resolveOIDCIssuerURL(app.appConfig),
		CommitmentOpts:      commitmentOpts,
		EncryptionKeySource: app.encKeySource,
	})
	if app.API == nil {
		return fmt.Errorf("failed to create API handler")
	}

	return nil
}

// buildAdminPasswordSyncCallback returns a callback that syncs the admin user's
// password to the secret manager when it changes. Returns nil if the required
// environment variables (ADMIN_PASSWORD_SECRET, ADMIN_EMAIL) are not set or
// the secret resolver is nil.
func buildAdminPasswordSyncCallback(store auth.StoreInterface, resolver secrets.Resolver) func(ctx context.Context, userID, newPassword string) {
	adminPasswordSecret := os.Getenv("ADMIN_PASSWORD_SECRET")
	adminEmail := os.Getenv("ADMIN_EMAIL")

	if adminPasswordSecret == "" || resolver == nil || adminEmail == "" {
		return nil
	}

	return func(ctx context.Context, userID, newPassword string) {
		user, err := store.GetUserByID(ctx, userID)
		if err != nil || user == nil || user.Email != adminEmail {
			return
		}
		if err := resolver.PutSecret(ctx, adminPasswordSecret, newPassword); err != nil {
			logging.Warnf("Failed to sync admin password to secret manager: %v", err)
		} else {
			logging.Infof("Admin password synced to secret manager")
		}
	}
}

// Close gracefully shuts down the application.
func (app *Application) Close() error {
	log.Println("Shutting down CUDly Server...")

	// Close database connection if using PostgreSQL
	if app.DB != nil {
		log.Println("Closing database connection...")
		app.DB.Close()
		log.Println("Database connection closed successfully")
	}

	return nil
}

// initConfigStore initializes the configuration store using PostgreSQL
// Connection is deferred (lazy init) until first request to avoid Lambda ENI issues.
func initConfigStore(ctx context.Context) (*database.Config, secrets.Resolver, error) {
	// Require PostgreSQL configuration
	if os.Getenv("DB_HOST") == "" {
		return nil, nil, fmt.Errorf("database configuration required: DB_HOST must be set")
	}

	log.Println("Preparing PostgreSQL configuration store (lazy initialization)...")

	// Initialize secret resolver
	secretResolver, err := secrets.NewResolver(ctx, secrets.LoadConfigFromEnv())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create secret resolver: %w", err)
	}

	// Load database config from environment
	dbConfig, err := database.LoadFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load database config: %w", err)
	}

	log.Printf("PostgreSQL config loaded (will connect on first request): %s:%d", dbConfig.Host, dbConfig.Port)

	// The config store itself is created lazily on first request (not here), so
	// callers wire a nil ConfigStore into ExternalDeps. Deferring the connection
	// avoids connecting during Lambda init when the ENI isn't ready.
	return dbConfig, secretResolver, nil
}

// Helper functions for environment variable parsing

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		result, err := strconv.Atoi(val)
		if err != nil {
			log.Printf("WARNING: %s=%q is not a valid integer; using default %d", key, val, defaultVal) // #nosec G706 -- env var value is operator-controlled configuration; logged for diagnostics
			return defaultVal
		}
		return result
	}
	return defaultVal
}

// getEnvFloat mirrors getEnvInt for float-valued env vars. defaultVal is kept
// parameterized (rather than inlined) to stay symmetric with getEnvInt even
// though every current caller passes the same coverage default.
//
//nolint:unparam // general-purpose env parser; default kept parameterized for symmetry with getEnvInt
func getEnvFloat(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		result, err := strconv.ParseFloat(val, 64)
		if err != nil {
			log.Printf("WARNING: %s=%q is not a valid float; using default %g", key, val, defaultVal) // #nosec G706 -- env var value is operator-controlled configuration; logged for diagnostics
			return defaultVal
		}
		return result
	}
	return defaultVal
}

// authServiceAdapter adapts auth.Service to api.AuthServiceInterface.
type authServiceAdapter struct {
	service *auth.Service
}

func newAuthServiceAdapter(service *auth.Service) *authServiceAdapter {
	return &authServiceAdapter{service: service}
}

func (a *authServiceAdapter) Login(ctx context.Context, req api.LoginRequest) (*api.LoginResponse, error) {
	authReq := auth.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
		MFACode:  req.MFACode,
	}
	resp, err := a.service.Login(ctx, authReq)
	if err != nil {
		return nil, err
	}
	return &api.LoginResponse{
		Token:     resp.Token,
		ExpiresAt: resp.ExpiresAt.Format(time.RFC3339),
		User: &api.UserInfo{
			ID:         resp.User.ID,
			Email:      resp.User.Email,
			Groups:     resp.User.Groups,
			MFAEnabled: resp.User.MFAEnabled,
		},
		CSRFToken: resp.CSRFToken,
	}, nil
}

func (a *authServiceAdapter) Logout(ctx context.Context, token string) error {
	return a.service.Logout(ctx, token)
}

func (a *authServiceAdapter) ValidateSession(ctx context.Context, token string) (*api.Session, error) {
	sess, err := a.service.ValidateSession(ctx, token)
	if err != nil {
		return nil, err
	}
	return &api.Session{
		UserID: sess.UserID,
		Email:  sess.Email,
	}, nil
}

func (a *authServiceAdapter) SetupAdmin(ctx context.Context, req api.SetupAdminRequest) (*api.LoginResponse, error) {
	authReq := auth.SetupAdminRequest{
		Email:    req.Email,
		Password: req.Password,
	}
	resp, err := a.service.SetupAdmin(ctx, authReq)
	if err != nil {
		return nil, err
	}
	return &api.LoginResponse{
		Token:     resp.Token,
		ExpiresAt: resp.ExpiresAt.Format(time.RFC3339),
		User: &api.UserInfo{
			ID:         resp.User.ID,
			Email:      resp.User.Email,
			Groups:     resp.User.Groups,
			MFAEnabled: resp.User.MFAEnabled,
		},
		CSRFToken: resp.CSRFToken,
	}, nil
}

func (a *authServiceAdapter) CheckAdminExists(ctx context.Context) (bool, error) {
	return a.service.CheckAdminExists(ctx)
}

func (a *authServiceAdapter) RequestPasswordReset(ctx context.Context, emailAddr string) error {
	return a.service.RequestPasswordReset(ctx, emailAddr)
}

func (a *authServiceAdapter) ConfirmPasswordReset(ctx context.Context, req api.PasswordResetConfirm) error {
	authReq := auth.PasswordResetConfirm{
		Token:       req.Token,
		NewPassword: req.NewPassword,
	}
	return a.service.ConfirmPasswordReset(ctx, authReq)
}

func (a *authServiceAdapter) ResetTokenStatus(ctx context.Context, token string) (string, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	state, flow, err := a.service.ResetTokenStatus(ctx, token)
	return string(state), string(flow), err
}

func (a *authServiceAdapter) GetUser(ctx context.Context, userID string) (*api.User, error) {
	user, err := a.service.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}
	return &api.User{
		ID:         user.ID,
		Email:      user.Email,
		Groups:     user.GroupIDs,
		MFAEnabled: user.MFAEnabled,
	}, nil
}

func (a *authServiceAdapter) UpdateUserProfile(ctx context.Context, userID, emailAddr, currentPassword, newPassword string) error {
	return a.service.UpdateUserProfile(ctx, userID, emailAddr, currentPassword, newPassword)
}

// User management methods - delegate to auth service API methods.
func (a *authServiceAdapter) CreateUserAPI(ctx context.Context, req any) (any, error) {
	return a.service.CreateUserAPI(ctx, req)
}

func (a *authServiceAdapter) UpdateUserAPI(ctx context.Context, actorUserID, userID string, req any) (any, error) {
	return a.service.UpdateUserAPI(ctx, actorUserID, userID, req)
}

func (a *authServiceAdapter) DeleteUser(ctx context.Context, userID string) error {
	return a.service.DeleteUser(ctx, userID)
}

func (a *authServiceAdapter) ListUsersAPI(ctx context.Context) (any, error) {
	return a.service.ListUsersAPI(ctx)
}

func (a *authServiceAdapter) ChangePasswordAPI(ctx context.Context, userID, currentPassword, newPassword string) error {
	return a.service.ChangePasswordAPI(ctx, userID, currentPassword, newPassword)
}

// MFA lifecycle (issue #497).
func (a *authServiceAdapter) MFASetupAPI(ctx context.Context, userID, password string) (string, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	return a.service.MFASetupAPI(ctx, userID, password)
}

func (a *authServiceAdapter) MFAEnableAPI(ctx context.Context, userID, code string) ([]string, error) {
	return a.service.MFAEnableAPI(ctx, userID, code)
}

func (a *authServiceAdapter) MFADisableAPI(ctx context.Context, userID, password, codeOrRecovery string) error {
	return a.service.MFADisableAPI(ctx, userID, password, codeOrRecovery)
}

func (a *authServiceAdapter) MFARegenerateRecoveryCodesAPI(ctx context.Context, userID, code string) ([]string, error) {
	return a.service.MFARegenerateRecoveryCodesAPI(ctx, userID, code)
}

// Group management methods - delegate to auth service API methods.
func (a *authServiceAdapter) CreateGroupAPI(ctx context.Context, req any) (any, error) {
	return a.service.CreateGroupAPI(ctx, req)
}

func (a *authServiceAdapter) UpdateGroupAPI(ctx context.Context, groupID string, req any) (any, error) {
	return a.service.UpdateGroupAPI(ctx, groupID, req)
}

func (a *authServiceAdapter) DeleteGroup(ctx context.Context, groupID string) error {
	return a.service.DeleteGroup(ctx, groupID)
}

func (a *authServiceAdapter) GetGroupAPI(ctx context.Context, groupID string) (any, error) {
	return a.service.GetGroupAPI(ctx, groupID)
}

func (a *authServiceAdapter) ListGroupsAPI(ctx context.Context) (any, error) {
	return a.service.ListGroupsAPI(ctx)
}

// Permission checking.
func (a *authServiceAdapter) HasPermissionAPI(ctx context.Context, userID, action, resource string) (bool, error) {
	return a.service.HasPermissionAPI(ctx, userID, action, resource)
}

func (a *authServiceAdapter) HasPermissionForConstraintsAPI(ctx context.Context, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	return a.service.HasPermissionForConstraintsAPI(ctx, userID, action, resource, constraintSets)
}

func (a *authServiceAdapter) GetUserPermissionsAPI(ctx context.Context, userID string) (any, error) {
	return a.service.GetUserPermissionsAPI(ctx, userID)
}

// Account access.
func (a *authServiceAdapter) GetAllowedAccountsAPI(ctx context.Context, userID string) ([]string, error) {
	authCtx, err := a.service.BuildAuthContext(ctx, userID)
	if err != nil {
		return nil, err
	}
	return authCtx.AllowedAccounts, nil
}

// CSRF validation.
func (a *authServiceAdapter) ValidateCSRFToken(ctx context.Context, sessionToken, csrfToken string) error {
	return a.service.ValidateCSRFToken(ctx, sessionToken, csrfToken)
}

// API Key management.
func (a *authServiceAdapter) CreateAPIKeyAPI(ctx context.Context, userID string, req any) (any, error) {
	return a.service.CreateAPIKeyAPI(ctx, userID, req)
}

func (a *authServiceAdapter) ListUserAPIKeysAPI(ctx context.Context, userID string) (any, error) {
	return a.service.ListUserAPIKeysAPI(ctx, userID)
}

func (a *authServiceAdapter) GetAPIKeysUsageStatsAPI(ctx context.Context, userID string) (any, error) {
	return a.service.GetAPIKeysUsageStatsAPI(ctx, userID)
}

func (a *authServiceAdapter) DeleteAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	return a.service.DeleteAPIKeyAPI(ctx, userID, keyID)
}

func (a *authServiceAdapter) RevokeAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	return a.service.RevokeAPIKeyAPI(ctx, userID, keyID)
}

func (a *authServiceAdapter) ValidateUserAPIKeyAPI(ctx context.Context, apiKey string) (any, any, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	return a.service.ValidateUserAPIKeyAPI(ctx, apiKey)
}

func (a *authServiceAdapter) HasAPIKeyPermissionAPI(ctx context.Context, apiKey, action, resource string) (userID, keyID string, allowed bool, err error) {
	userID, keyID, allowed, err = a.service.HasAPIKeyPermissionAPI(ctx, apiKey, action, resource)
	return
}

func (a *authServiceAdapter) HasAPIKeyPermissionForConstraintsAPI(ctx context.Context, keyID, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	return a.service.HasAPIKeyPermissionForConstraintsAPI(ctx, keyID, userID, action, resource, constraintSets)
}
