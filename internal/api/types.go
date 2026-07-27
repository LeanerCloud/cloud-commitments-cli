// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"time"

	"github.com/LeanerCloud/CUDly/internal/analytics"
	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/commitmentopts"
	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/credentials"
	"github.com/LeanerCloud/CUDly/internal/email"
	"github.com/LeanerCloud/CUDly/internal/oidc"
	"github.com/LeanerCloud/CUDly/internal/scheduler"
)

// RateLimiterInterface defines the interface for rate limiting implementations
// This allows for both in-memory and database-backed rate limiters.
type RateLimiterInterface interface {
	// Allow checks if a request should be allowed based on rate limits
	// Returns (allowed bool, error)
	Allow(ctx context.Context, key string, endpoint string) (bool, error)

	// AllowWithIP is a convenience method for IP-based rate limiting
	AllowWithIP(ctx context.Context, ip string, endpoint string) (bool, error)

	// AllowWithEmail is a convenience method for email-based rate limiting
	AllowWithEmail(ctx context.Context, email string, endpoint string) (bool, error)

	// AllowWithUser is a convenience method for user-based rate limiting
	AllowWithUser(ctx context.Context, userID string, endpoint string) (bool, error)
}

// HandlerConfig holds configuration for the API handler.
type HandlerConfig struct {
	AnalyticsClient     AnalyticsClientInterface
	AnalyticsCollector  AnalyticsCollectorInterface
	PurchaseManager     PurchaseManagerInterface
	RateLimiter         RateLimiterInterface
	AuthService         AuthServiceInterface
	CommitmentOpts      CommitmentOptsInterface
	OIDCSigner          oidc.Signer
	AnalyticsSnapshots  AnalyticsSnapshotStoreInterface
	CredentialStore     credentials.CredentialStore
	EmailNotifier       email.SenderInterface
	Scheduler           SchedulerInterface
	ConfigStore         config.StoreInterface
	DashboardURL        string
	CORSAllowedOrigin   string
	DashboardBucket     string
	OIDCIssuerURL       string
	APIKeySecretARN     string
	EncryptionKeySource string
	EnableDashboard     bool
}

// CommitmentOptsInterface lets us swap the real *commitmentopts.Service for
// a stub in handler tests without pulling in the probe+store machinery.
type CommitmentOptsInterface interface {
	Get(ctx context.Context) (commitmentopts.Options, error)
	Validate(ctx context.Context, provider, service string, term int, payment string) (bool, error)
}

// AnalyticsClientInterface defines the interface for analytics queries.
//
// accountUUIDs / accountExternalIDsByProvider are the dual-column account
// filter: rows match when cloud_account_id = ANY(accountUUIDs) OR (provider = p
// AND account_id = ANY(accountExternalIDsByProvider[p])). Both nil/empty means
// "all accounts accessible to the caller" (scoping is enforced upstream in the
// handler). The handler resolves the requested account (a top-bar chip UUID)
// into both representations via resolveSingleAccountFilterIDs so rows that carry
// only the external account_id (cloud_account_id NULL) are still aggregated, and
// the external ids stay grouped by provider so a reused external number across
// providers cannot leak the wrong rows (issue #701/#498/#866).
type AnalyticsClientInterface interface {
	QueryHistory(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, start, end time.Time, interval string) ([]HistoryDataPoint, *HistorySummary, error)
	QueryBreakdown(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, start, end time.Time, dimension string) (map[string]BreakdownValue, error)
}

// AnalyticsCollectorInterface defines the interface for analytics collection.
type AnalyticsCollectorInterface interface {
	Collect(ctx context.Context) error
}

// AnalyticsSnapshotStoreInterface exposes the savings-snapshot time-series for
// the /api/analytics/trends endpoint. It is the read side of the now-wired
// internal/analytics collector. Scoping is the dual-column model: the handler
// resolves the requested account into accountUUIDs + accountExternalIDsByProvider
// (see resolveSingleAccountFilterIDs) and the store ORs both columns so rows
// carrying only one identifier are still matched. Both empty means "all" — the
// handler MUST enforce allowed_accounts scope before passing empty filters.
type AnalyticsSnapshotStoreInterface interface {
	QuerySavings(ctx context.Context, req analytics.QueryRequest) ([]analytics.SavingsSnapshot, error)
	QueryMonthlyTotals(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, months int) ([]analytics.MonthlySummary, error)
	QueryByProvider(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, startDate, endDate time.Time) ([]analytics.ProviderBreakdown, error)
	QueryByService(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, startDate, endDate time.Time) ([]analytics.ServiceBreakdown, error)
}

// HistoryDataPoint represents aggregated historical data.
type HistoryDataPoint struct {
	Timestamp         time.Time          `json:"timestamp"`
	ByService         map[string]float64 `json:"by_service,omitempty"`
	ByProvider        map[string]float64 `json:"by_provider,omitempty"`
	TotalSavings      float64            `json:"total_savings"`
	TotalUpfront      float64            `json:"total_upfront"`
	PurchaseCount     int                `json:"purchase_count"`
	CumulativeSavings float64            `json:"cumulative_savings"`
}

// HistorySummaryAnalytics contains aggregated statistics for analytics.
type HistorySummaryAnalytics struct {
	TotalPeriodSavings      float64 `json:"total_period_savings"`
	TotalUpfrontSpent       float64 `json:"total_upfront_spent"`
	PurchaseCount           int     `json:"purchase_count"`
	AverageSavingsPerPeriod float64 `json:"average_savings_per_period"`
	PeakSavings             float64 `json:"peak_savings"`
}

// BreakdownValue represents savings breakdown by dimension.
type BreakdownValue struct {
	TotalSavings  float64 `json:"total_savings"`
	TotalUpfront  float64 `json:"total_upfront"`
	PurchaseCount int     `json:"purchase_count"`
	Percentage    float64 `json:"percentage"`
}

// PurchaseManagerInterface defines purchase manager methods used by handler.
// `actor` is the session-authenticated user's email for per-user
// attribution via the auth-gated deep-link flow; pass "" for token-only
// paths (message workers, legacy callers) where attribution falls back to
// the notification email at render time.
//
// `transitionedBy` (ApproveAndExecute) is the session user's UUID stamped
// onto purchase_executions.transitioned_by for human-initiated approvals;
// pass nil for token/SQS/system flows so transitioned_by = NULL (issue #1009).
type PurchaseManagerInterface interface {
	ApproveExecution(ctx context.Context, execID, token, actor string) error
	ApproveAndExecute(ctx context.Context, execID, actor string, transitionedBy *string) error
	CancelExecution(ctx context.Context, execID, token, actor string) error
}

// SchedulerInterface defines scheduler methods used by handler.
type SchedulerInterface interface {
	CollectRecommendations(ctx context.Context) (*scheduler.CollectResult, error)
	ListRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error)
	// GetRecommendationByID fetches a single rec by its application-level id,
	// bypassing account-override filtering so deep-linked URLs to override-
	// hidden recs resolve. hiddenBy is non-nil when the rec would be dropped by
	// the override filter; callers render a "hidden" banner. Returns nil, nil,
	// nil when the rec is absent or fully suppressed.
	GetRecommendationByID(ctx context.Context, id string) (rec *config.RecommendationRecord, hiddenBy []string, err error)
}

// AuthServiceInterface defines auth service methods used by handler
// Note: This interface uses API-specific types that are converted from auth package types.
type AuthServiceInterface interface {
	Login(ctx context.Context, req LoginRequest) (*LoginResponse, error)
	Logout(ctx context.Context, token string) error
	ValidateSession(ctx context.Context, token string) (*Session, error)
	ValidateCSRFToken(ctx context.Context, sessionToken, csrfToken string) error
	SetupAdmin(ctx context.Context, req SetupAdminRequest) (*LoginResponse, error)
	CheckAdminExists(ctx context.Context) (bool, error)
	RequestPasswordReset(ctx context.Context, email string) error
	ConfirmPasswordReset(ctx context.Context, req PasswordResetConfirm) error
	// ResetTokenStatus returns the runtime state of a reset token
	// without consuming it. Used by the GET /api/auth/reset-password/
	// status endpoint so the frontend can branch on expired / used
	// tokens before rendering the reset-password form (issues #460,
	// #461). state is one of "valid" | "expired" | "used"; flow is
	// "reset" | "invite".
	ResetTokenStatus(ctx context.Context, token string) (state string, flow string, err error)
	GetUser(ctx context.Context, userID string) (*User, error)
	UpdateUserProfile(ctx context.Context, userID string, email string, currentPassword string, newPassword string) error
	// User management - uses auth.API* types
	CreateUserAPI(ctx context.Context, req any) (any, error)
	UpdateUserAPI(ctx context.Context, actorUserID, userID string, req any) (any, error)
	DeleteUser(ctx context.Context, userID string) error
	ListUsersAPI(ctx context.Context) (any, error)
	ChangePasswordAPI(ctx context.Context, userID, currentPassword, newPassword string) error
	// MFA lifecycle (issue #497). All four require the user to be
	// already authenticated; setup + disable additionally require a
	// fresh password re-verify carried in the request body.
	MFASetupAPI(ctx context.Context, userID, password string) (secret, provisioningURI string, err error)
	MFAEnableAPI(ctx context.Context, userID, code string) (recoveryCodes []string, err error)
	MFADisableAPI(ctx context.Context, userID, password, codeOrRecovery string) error
	MFARegenerateRecoveryCodesAPI(ctx context.Context, userID, code string) (recoveryCodes []string, err error)
	// Group management - uses auth.API* types
	CreateGroupAPI(ctx context.Context, req any) (any, error)
	UpdateGroupAPI(ctx context.Context, groupID string, req any) (any, error)
	DeleteGroup(ctx context.Context, groupID string) error
	GetGroupAPI(ctx context.Context, groupID string) (any, error)
	ListGroupsAPI(ctx context.Context) (any, error)
	// Permission checking
	HasPermissionAPI(ctx context.Context, userID, action, resource string) (bool, error)
	// HasPermissionForConstraintsAPI checks action on resource against
	// request-derived constraint sets so per-permission Constraints
	// (MaxPurchaseAmount, Providers, Services, Regions, AccountIDs) are
	// enforced at execution time. Every constraint set must be granted by
	// at least one of the user's permissions (SEC-01, issue #1141).
	HasPermissionForConstraintsAPI(ctx context.Context, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error)
	// GetUserPermissionsAPI returns the effective permission set for a user
	// (union of all group permissions). Used by GET /api/auth/me/permissions.
	// Returns []auth.APIPermission converted to []PermissionEntry by the handler.
	GetUserPermissionsAPI(ctx context.Context, userID string) (any, error)
	// Account access - returns the union of allowed_accounts from all user groups (empty = all access)
	GetAllowedAccountsAPI(ctx context.Context, userID string) ([]string, error)
	// API Key management
	CreateAPIKeyAPI(ctx context.Context, userID string, req any) (any, error)
	ListUserAPIKeysAPI(ctx context.Context, userID string) (any, error)
	// GetAPIKeysUsageStatsAPI returns the section-level usage summary
	// (active count, 24h/lifetime request totals, top-3 most active
	// keys) for the calling user's own API keys.
	GetAPIKeysUsageStatsAPI(ctx context.Context, userID string) (any, error)
	DeleteAPIKeyAPI(ctx context.Context, userID, keyID string) error
	RevokeAPIKeyAPI(ctx context.Context, userID, keyID string) error
	ValidateUserAPIKeyAPI(ctx context.Context, apiKey string) (any, any, error)
	// HasAPIKeyPermissionAPI validates a user API key and checks the
	// requested action/resource against the key's effective permissions
	// (the intersection of the key's scoped permissions with the owning
	// user's group-derived permissions). Returns the owning user's ID, the
	// key's database ID, and whether the permission is held; a non-nil error
	// means the key did not validate or the lookup failed, and callers must
	// deny (fail closed). Wired into requirePermission so per-key scoping is
	// enforced at request time (issue #1142).
	HasAPIKeyPermissionAPI(ctx context.Context, apiKey, action, resource string) (userID, keyID string, allowed bool, err error)
	// HasAPIKeyPermissionForConstraintsAPI checks request-derived permission
	// constraint sets against a user API key's effective permissions (the
	// intersection of the key's own constraints and the owning user's
	// group-derived permissions). Must be called after HasAPIKeyPermissionAPI
	// confirms action/resource access. Enforces per-key caps (MaxPurchaseAmount,
	// Providers, Services, Regions, AccountIDs) that the key's owner cannot
	// override via their broader group permissions (adversarial-review F2).
	HasAPIKeyPermissionForConstraintsAPI(ctx context.Context, keyID, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error)
}

// Auth request/response types (to avoid import cycle with auth package).
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	MFACode  string `json:"mfa_code,omitempty"`
}

// LoginResponse carries the JWT token and user summary returned after a successful login.
type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt string    `json:"expires_at"`
	User      *UserInfo `json:"user"`
	CSRFToken string    `json:"csrf_token,omitempty"`
}

// UserInfo is a lightweight projection of the authenticated user included in login
// and session responses; it carries only the fields needed for client-side routing
// and display, without exposing credential hashes or internal audit fields.
type UserInfo struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// SetupAdminRequest is the one-time bootstrap payload used to create the first
// administrator account when the system has no users yet.
type SetupAdminRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
}

// PasswordResetRequest initiates a password-reset flow by sending a reset link
// to the provided email address if it matches an existing account.
type PasswordResetRequest struct {
	Email string `json:"email"`
}

// PasswordResetConfirm completes a password-reset flow by pairing the one-time
// token (delivered by email) with the user's chosen new password.
type PasswordResetConfirm struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// Session holds the minimal identity claims extracted from a validated JWT and
// attached to the request context by the authentication middleware.
type Session struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	// UserAPIKeyID is the database key ID when the request was authenticated via
	// a user API key (not the admin infra key). Empty for bearer-token and
	// admin-API-key sessions. Used by requirePermissionConstraints to evaluate
	// constraints against the key's effective permissions rather than the owning
	// user's full group permissions (adversarial-review F2).
	UserAPIKeyID string `json:"-"`
}

// User is the full user record returned by the admin user-management endpoints.
// It mirrors UserInfo's identity fields and adds CreatedAt/UpdatedAt audit
// timestamps; it is never embedded in the public-facing login or session
// responses, which use the slimmer UserInfo / CurrentUserResponse types instead.
type User struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// CreateUserRequest represents a request to create a new user. Groups must be
// non-empty: authorization is group-membership-only (issue #907).
type CreateUserRequest struct {
	Email    string   `json:"email"`
	Password string   `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	Groups   []string `json:"groups,omitempty"`
}

// UpdateUserRequest represents a request to update a user.
type UpdateUserRequest struct {
	Email  string   `json:"email,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// Group represents a user group with permissions.
type Group struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Description     string       `json:"description,omitempty"`
	CreatedAt       string       `json:"created_at,omitempty"`
	UpdatedAt       string       `json:"updated_at,omitempty"`
	Permissions     []Permission `json:"permissions"`
	AllowedAccounts []string     `json:"allowed_accounts,omitempty"`
}

// Permission represents an action that can be performed on a resource.
type Permission struct {
	Constraints *PermissionConstraint `json:"constraints,omitempty"`
	Action      string                `json:"action"`
	Resource    string                `json:"resource"`
}

// PermissionConstraint limits where a permission applies.
type PermissionConstraint struct {
	Accounts  []string `json:"accounts,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Services  []string `json:"services,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	MaxAmount float64  `json:"max_amount,omitempty"`
}

// CreateGroupRequest represents a request to create a new group.
type CreateGroupRequest struct {
	Name            string       `json:"name"`
	Description     string       `json:"description,omitempty"`
	Permissions     []Permission `json:"permissions"`
	AllowedAccounts []string     `json:"allowed_accounts,omitempty"`
}

// UpdateGroupRequest represents a request to update a group.
type UpdateGroupRequest struct {
	Name            string       `json:"name,omitempty"`
	Description     string       `json:"description,omitempty"`
	Permissions     []Permission `json:"permissions,omitempty"`
	AllowedAccounts []string     `json:"allowed_accounts,omitempty"`
}

// ChangePasswordRequest represents a request to change password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ProfileUpdateRequest represents a profile update request.
type ProfileUpdateRequest struct {
	Email           string `json:"email"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password,omitempty"`
}

// API Response types for type safety

// ConfigResponse holds the configuration response.
type ConfigResponse struct {
	Global         *config.GlobalConfig   `json:"global"`
	SourceIdentity *sourceIdentity        `json:"source_identity,omitempty"`
	SourceCloud    string                 `json:"source_cloud,omitempty"`
	Services       []config.ServiceConfig `json:"services"`
}

// StatusResponse holds a simple status response.
type StatusResponse struct {
	Status string `json:"status"`
}

// RecommendationsSummary holds aggregate statistics for recommendations.
type RecommendationsSummary struct {
	TotalCount          int     `json:"total_count"`
	TotalMonthlySavings float64 `json:"total_monthly_savings"`
	TotalUpfrontCost    float64 `json:"total_upfront_cost"`
	AvgPaybackMonths    float64 `json:"avg_payback_months"`
}

// RecommendationsResponse holds the recommendations response.
type RecommendationsResponse struct {
	Recommendations []config.RecommendationRecord `json:"recommendations"`
	Regions         []string                      `json:"regions"`
	Summary         RecommendationsSummary        `json:"summary"`
}

// UsagePoint is a single sample in the per-recommendation usage time
// series surfaced by GET /api/recommendations/:id/detail. The series is
// always ordered by Timestamp ascending. CPUPct/MemPct are 0..100.
//
// Empty in the current implementation: the collector pipeline does not
// yet persist time-series utilization per recommendation. The endpoint
// returns the empty slice with a non-error status so the frontend can
// render a "Usage history not yet available" placeholder rather than a
// broken empty chart. See known_issues/28_recommendations_detail_endpoint.md
// for the full collector wiring follow-up.
type UsagePoint struct {
	Timestamp string  `json:"timestamp"`
	CPUPct    float64 `json:"cpu_pct"`
	MemPct    float64 `json:"mem_pct"`
}

// RecommendationDetailResponse is the per-id payload backing the
// Recommendations row-click drawer. Contract documented in issue #44.
//
// ConfidenceBucket is "low" | "medium" | "high" — server-side mirror of
// the client-side heuristic that previously lived in
// frontend/src/recommendations.ts:confidenceBucketFor. Centralizing it
// server-side lets future provider-specific tuning happen in one place.
//
// ProvenanceNote is a short human-readable string naming the collector
// + the freshness window. Rendered verbatim in the drawer.
type RecommendationDetailResponse struct {
	ID               string       `json:"id"`
	UsageHistory     []UsagePoint `json:"usage_history"`
	ConfidenceBucket string       `json:"confidence_bucket"`
	ProvenanceNote   string       `json:"provenance_note"`
	// HiddenBy is non-nil when the rec is filtered out by an account-service
	// override (issue #214). Each element names one failing dimension:
	// "enabled=false", "engine", "region", or "resource_type". The frontend
	// renders a "hidden by your override" banner when this field is present.
	// Absent (null) means the rec is fully visible.
	HiddenBy []string `json:"hidden_by,omitempty"`
}

// PlansResponse holds the purchase plans response.
type PlansResponse struct {
	Plans []config.PurchasePlan `json:"plans"`
}

// CurrentUserResponse holds the current user response.
type CurrentUserResponse struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// AdminExistsResponse holds the admin exists check response.
type AdminExistsResponse struct {
	AdminExists bool `json:"admin_exists"`
}

// PermissionEntry is a single {action, resource} pair in the permissions
// response. Constraints are omitted from the wire shape for now; the
// frontend uses the pair for UX gating only.
type PermissionEntry struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// UserPermissionsResponse is the response shape for GET /api/auth/me/permissions.
// Permissions is the effective set derived from the union of the user's groups.
// IsAdmin mirrors whether the effective set contains the {admin, *} wildcard.
type UserPermissionsResponse struct {
	Permissions []PermissionEntry `json:"permissions"`
	IsAdmin     bool              `json:"is_admin"`
}

// MFA enrollment + lifecycle DTOs (issue #497). Passwords carried by
// these requests are base64-encoded by the frontend (same convention
// as login / change-password / reset-password); the handler decodes
// before handing to the auth service.

// MFASetupRequest begins an MFA enrollment. Current password is
// required as defense-in-depth — a stolen session alone shouldn't
// be enough to swap a user's MFA secret.
type MFASetupRequest struct {
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
}

// MFASetupResponse returns the freshly-generated secret + the
// otpauth:// URI the frontend renders as a QR code. The secret is
// already persisted server-side as the pending secret; clients do
// not need to round-trip it back on enable.
type MFASetupResponse struct {
	Secret          string `json:"secret"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	ProvisioningURI string `json:"provisioning_uri"`
}

// MFAEnableRequest finalizes an enrollment by proving the user
// loaded the secret into their authenticator (the supplied code is
// validated against the pending secret).
type MFAEnableRequest struct {
	Code string `json:"code"`
}

// MFAEnableResponse returns the plaintext recovery codes exactly
// once. Backend stores only bcrypt hashes.
type MFAEnableResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// MFADisableRequest turns off MFA. Requires the current password AND
// a fresh proof-of-possession (TOTP code or unused recovery code).
type MFADisableRequest struct {
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	Code     string `json:"code"`
}

// MFARegenerateRequest replaces all stored recovery codes. Requires
// a fresh TOTP code (NOT a recovery code — see service for the
// rationale).
type MFARegenerateRequest struct {
	Code string `json:"code"`
}

// MFARegenerateResponse mirrors MFAEnableResponse — plaintext codes
// returned exactly once.
type MFARegenerateResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// EmptyServiceConfigResponse represents an empty service config.
type EmptyServiceConfigResponse struct{}

// PublicInfoResponse holds public information about the CUDly instance.
// Only fields safe for unauthenticated callers — sensitive identifiers
// (API key secret URL, deployment AWS account ID) live on the
// authenticated /api/info/deployment endpoint instead.
type PublicInfoResponse struct {
	Version     string `json:"version"`
	AdminExists bool   `json:"admin_exists"`
}

// DeploymentInfoResponse holds deployment-scoped identifiers that must
// not be exposed to unauthenticated callers. Served by
// GET /api/info/deployment (AuthUser).
type DeploymentInfoResponse struct {
	// APIKeySecretURL is the AWS Console deep-link to the Secrets Manager
	// secret holding the CUDly API key.
	APIKeySecretURL string `json:"api_key_secret_url,omitempty"`
	// DeploymentAWSAccountID is the AWS account ID of the Lambda host,
	// resolved via STS GetCallerIdentity. Empty on non-AWS deployments
	// or when STS is unreachable. Used by the frontend to distinguish
	// legitimate ambient-credential executions from orphan rows (#608).
	DeploymentAWSAccountID string `json:"deployment_aws_account_id,omitempty"`
}

// DashboardSummaryResponse holds the dashboard summary data.
type DashboardSummaryResponse struct {
	ByService               map[string]ServiceSavings `json:"by_service"`
	PotentialMonthlySavings float64                   `json:"potential_monthly_savings"`
	TotalRecommendations    int                       `json:"total_recommendations"`
	ActiveCommitments       int                       `json:"active_commitments"`
	CommittedMonthly        float64                   `json:"committed_monthly"`
	CurrentCoverage         float64                   `json:"current_coverage"`
	TargetCoverage          float64                   `json:"target_coverage"`
	YTDSavings              float64                   `json:"ytd_savings"`
}

// ServiceSavings holds savings data for a service.
type ServiceSavings struct {
	PotentialSavings float64 `json:"potential_savings"`
	CurrentSavings   float64 `json:"current_savings"`
}

// InventoryCommitment is one row in the per-commitment Inventory &
// Coverage view (issue #340 deferred sub-task — "Active commitments").
// Aggregated from PurchaseHistoryRecord rows that are still within
// their term; the inventory endpoint filters out expired commitments
// before responding.
//
// ID is `{account_id}:{purchase_id}` so the row is uniquely identifiable
// in the JSON payload without a DB schema change — purchase_id alone
// is unique within an account but not globally across the table.
//
// Status is always `"active"` today (the handler drops expired rows).
// The field stays in the response shape so a future "expiring soon"
// sub-state has a slot without a breaking API change.
type InventoryCommitment struct {
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	// MonthlyCost is nil when the source purchase_history row has a NULL
	// monthly_cost (provider did not return a monthly breakdown). The
	// frontend renders "--" for nil and "$X.XX" when non-nil.
	MonthlyCost      *float64 `json:"monthly_cost"`
	Provider         string   `json:"provider"`
	AccountID        string   `json:"account_id"`
	AccountName      string   `json:"account_name,omitempty"`
	Service          string   `json:"service"`
	ResourceType     string   `json:"resource_type,omitempty"`
	Region           string   `json:"region"`
	Status           string   `json:"status"`
	ID               string   `json:"id"`
	PaymentOption    string   `json:"payment_option,omitempty"`
	TermYears        int      `json:"term_years"`
	UpfrontCost      float64  `json:"upfront_cost"`
	EstimatedSavings float64  `json:"estimated_savings"`
	Count            int      `json:"count"`
}

// InventoryCommitmentsResponse is the envelope returned by
// GET /api/inventory/commitments. Commitments is always a slice — never
// nil — so the frontend can rely on `resp.commitments.length` without
// a null check.
type InventoryCommitmentsResponse struct {
	Commitments []InventoryCommitment `json:"commitments"`
}

// CoverageServiceRow is one service row within a provider's coverage
// section. CoveredMonthly is the sum of active-commitment MonthlyCost
// values for the (provider, service) pair. OnDemandMonthly is the sum
// of recommendation Savings values — i.e. the portion of on-demand
// spend that is NOT yet committed. CoveragePct is nil when both sums
// are zero (no usage detected), not 0, to preserve the "absent"
// semantic per feedback_nullable_not_zero.
type CoverageServiceRow struct {
	CoveragePct     *float64 `json:"coverage_pct"`
	Service         string   `json:"service"`
	CoveredMonthly  float64  `json:"covered_monthly"`
	OnDemandMonthly float64  `json:"on_demand_monthly"`
}

// ProviderCoverageSection is the per-provider block returned by
// GET /api/inventory/coverage. Services is nil (not []) when the
// provider has no usage data, which the frontend uses to distinguish
// "no usage detected" from "usage exists but all services are 0%".
// OverallCoveragePct follows the same null-vs-zero contract as
// CoverageServiceRow.CoveragePct.
type ProviderCoverageSection struct {
	OverallCoveragePct *float64             `json:"overall_coverage_pct"`
	Provider           string               `json:"provider"`
	Services           []CoverageServiceRow `json:"services"`
}

// CoverageBreakdownResponse is the envelope returned by
// GET /api/inventory/coverage.
type CoverageBreakdownResponse struct {
	Providers []ProviderCoverageSection `json:"providers"`
}

// UpcomingPurchaseResponse holds upcoming purchase data.
type UpcomingPurchaseResponse struct {
	Purchases []UpcomingPurchase `json:"purchases"`
}

// UpcomingPurchase represents one upcoming planned purchase — a pending
// purchase_executions row whose scheduled_date hasn't fired yet, joined
// to its parent PurchasePlan for display.
//
// The dashboard's Cancel button targets ExecutionID via
// DELETE /api/purchases/planned/{id} (api.deletePlannedPurchase) so the
// operator removes just THIS scheduled instance and leaves the plan
// template intact — the next scheduler tick re-creates the next instance
// for the plan. PlanID is exposed as context (e.g. for linking to the
// plan's settings) and is NOT what destructive action endpoints should
// target. PR #207 + #213 history: an earlier iteration routed Cancel to
// api.deletePlan(planID) which deleted the entire plan; that was too
// aggressive — operators usually want "skip this scheduled run", not
// "nuke the recurring template".
type UpcomingPurchase struct {
	// CreatedByUserID propagates the underlying execution's
	// created_by_user_id so the dashboard widget can apply the same
	// creator-scope ownership gate the Plans page uses (issue #950).
	// Without it the widget renders a "Cancel" button on every row
	// while the backend now 403s for non-owners -- a UX hole that
	// surfaces as a confusing toast on click. Mirrors the field on
	// PlannedPurchase / PurchaseHistoryEntry. omitempty so legacy
	// NULL-creator rows keep the JSON shape they had pre-fix.
	CreatedByUserID  *string `json:"created_by_user_id,omitempty"`
	ExecutionID      string  `json:"execution_id"`
	PlanID           string  `json:"plan_id"`
	PlanName         string  `json:"plan_name"`
	ScheduledDate    string  `json:"scheduled_date"`
	Provider         string  `json:"provider"`
	Service          string  `json:"service"`
	StepNumber       int     `json:"step_number"`
	TotalSteps       int     `json:"total_steps"`
	EstimatedSavings float64 `json:"estimated_savings"`
}

// PlannedPurchasesResponse holds the list of planned purchases.
type PlannedPurchasesResponse struct {
	Purchases []PlannedPurchase `json:"purchases"`
}

// PlannedPurchase represents a scheduled purchase from a plan.
type PlannedPurchase struct {
	// CreatedByUserID is the UUID of the user who created the scheduled
	// purchase, mirroring PurchaseHistoryRecord.CreatedByUserID. The
	// frontend gates the row action buttons on creator-scope ownership
	// (issue #950); omitted for legacy rows with a NULL creator.
	CreatedByUserID  *string `json:"created_by_user_id,omitempty"`
	Status           string  `json:"status"`
	PlanID           string  `json:"plan_id"`
	PlanName         string  `json:"plan_name"`
	ScheduledDate    string  `json:"scheduled_date"`
	Provider         string  `json:"provider"`
	Service          string  `json:"service"`
	ResourceType     string  `json:"resource_type"`
	Region           string  `json:"region"`
	ID               string  `json:"id"`
	Payment          string  `json:"payment"`
	Count            int     `json:"count"`
	UpfrontCost      float64 `json:"upfront_cost"`
	EstimatedSavings float64 `json:"estimated_savings"`
	StepNumber       int     `json:"step_number"`
	TotalSteps       int     `json:"total_steps"`
	Term             int     `json:"term"`
}

// PlanRequest represents the API request format for creating/updating plans
// The frontend sends ramp_schedule as a string, which we convert to the proper struct.
type PlanRequest struct {
	Payment                string   `json:"payment,omitempty"`
	Description            string   `json:"description,omitempty"`
	RampSchedule           string   `json:"ramp_schedule,omitempty"`
	Name                   string   `json:"name"`
	Provider               string   `json:"provider,omitempty"`
	Service                string   `json:"service,omitempty"`
	TargetAccounts         []string `json:"target_accounts,omitempty"`
	TargetCoverage         int      `json:"target_coverage,omitempty"`
	Term                   int      `json:"term,omitempty"`
	NotificationDaysBefore int      `json:"notification_days_before"`
	CustomStepPercent      int      `json:"custom_step_percent,omitempty"`
	CustomIntervalDays     int      `json:"custom_interval_days,omitempty"`
	AutoPurchase           bool     `json:"auto_purchase"`
	Enabled                bool     `json:"enabled"`
}

// toPurchasePlan converts a PlanRequest to a config.PurchasePlan.
func (r *PlanRequest) toPurchasePlan() *config.PurchasePlan {
	now := time.Now()
	plan := &config.PurchasePlan{
		Name:                   r.Name,
		Enabled:                r.Enabled,
		AutoPurchase:           r.AutoPurchase,
		NotificationDaysBefore: r.NotificationDaysBefore,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	plan.RampSchedule = r.buildRampSchedule(now)
	plan.Services = r.buildServiceConfig()
	plan.NextExecutionDate = r.calculateNextExecutionDate(now, plan.RampSchedule)

	return plan
}

// buildRampSchedule builds the ramp schedule from request parameters.
func (r *PlanRequest) buildRampSchedule(now time.Time) config.RampSchedule {
	if preset, ok := config.PresetRampSchedules[r.RampSchedule]; ok {
		preset.StartDate = now
		return preset
	}

	if r.RampSchedule == "custom" {
		return r.buildCustomRampSchedule(now)
	}

	// Default to immediate
	schedule := config.PresetRampSchedules["immediate"]
	schedule.StartDate = now
	return schedule
}

// buildCustomRampSchedule builds a custom ramp schedule with validated parameters.
func (r *PlanRequest) buildCustomRampSchedule(now time.Time) config.RampSchedule {
	stepPercent := float64(r.CustomStepPercent)
	if stepPercent <= 0 {
		stepPercent = 20
	}

	intervalDays := r.CustomIntervalDays
	if intervalDays <= 0 {
		intervalDays = 7
	}

	totalSteps := int(100 / stepPercent)
	if totalSteps < 1 {
		totalSteps = 1
	}

	return config.RampSchedule{
		Type:             "custom",
		PercentPerStep:   stepPercent,
		StepIntervalDays: intervalDays,
		TotalSteps:       totalSteps,
		StartDate:        now,
	}
}

// buildServiceConfig creates service configuration from request fields.
func (r *PlanRequest) buildServiceConfig() map[string]config.ServiceConfig {
	if r.Provider == "" || r.Service == "" {
		return nil
	}

	term := r.Term
	if term == 0 {
		term = 3
	}

	payment := r.Payment
	if payment == "" {
		payment = "no-upfront"
	}

	coverage := float64(r.TargetCoverage)
	if coverage == 0 {
		coverage = 80
	}

	return map[string]config.ServiceConfig{
		r.Provider + "/" + r.Service: {
			Provider: r.Provider,
			Service:  r.Service,
			Enabled:  true,
			Term:     term,
			Payment:  payment,
			Coverage: coverage,
		},
	}
}

// calculateNextExecutionDate determines the next execution date based on ramp schedule.
func (r *PlanRequest) calculateNextExecutionDate(now time.Time, schedule config.RampSchedule) *time.Time {
	var nextDate time.Time

	if schedule.Type == "immediate" {
		nextDate = now.AddDate(0, 0, 1) // Schedule for tomorrow
	} else if schedule.StepIntervalDays > 0 {
		nextDate = now.AddDate(0, 0, schedule.StepIntervalDays)
	} else {
		return nil
	}

	return &nextDate
}

// CreatePlannedPurchasesRequest represents a request to create planned purchases.
type CreatePlannedPurchasesRequest struct {
	StartDate string `json:"start_date"`
	Count     int    `json:"count"`
}

// CreatePlannedPurchasesResponse represents the response after creating planned purchases.
type CreatePlannedPurchasesResponse struct {
	Created int `json:"created"`
}

// HistoryResponse represents the response from the history API.
type HistoryResponse struct {
	Purchases []config.PurchaseHistoryRecord `json:"purchases"`
	Summary   HistorySummary                 `json:"summary"`
}

// HistorySummary provides aggregate statistics for purchase history.
// TotalPurchases is the total count of rows (completed + all non-completed
// states); the per-state counters break it down so the UI can render
// meaningful totals. Dollar totals count completed, non-revoked rows only:
// pending, in-progress, failed, expired, canceled, and revoked rows are all
// excluded because no money is being actively committed for those states.
type HistorySummary struct {
	TotalPurchases int `json:"total_purchases"`
	TotalCompleted int `json:"total_completed"`
	TotalPending   int `json:"total_pending"`
	// TotalInProgress counts executions that have been approved but whose
	// synchronous purchase has not finalized (status approved/running/paused).
	// Tracked separately from pending and excluded from the dollar totals so an
	// interrupted approval (issue #621) stays visible without inflating
	// committed spend/savings.
	TotalInProgress int `json:"total_in_progress"`
	TotalFailed     int `json:"total_failed"`
	TotalExpired    int `json:"total_expired"`
	// TotalRevoked counts commitments that have been revoked/refunded via
	// MarkPurchaseRevoked. They are excluded from TotalCompleted and all dollar
	// totals because the provider has canceled the commitment.
	TotalRevoked        int     `json:"total_revoked"`
	TotalUpfront        float64 `json:"total_upfront"`
	TotalMonthlySavings float64 `json:"total_monthly_savings"`
	TotalAnnualSavings  float64 `json:"total_annual_savings"`
}
