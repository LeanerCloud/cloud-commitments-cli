/**
 * Type definitions for CUDly API
 */

// Core types
export type Provider = 'aws' | 'azure' | 'gcp';
export type PaymentOption = 'no-upfront' | 'partial-upfront' | 'all-upfront';
export type RampSchedule = 'immediate' | 'weekly-25pct' | 'monthly-10pct' | 'custom';

// User types

/**
 * PermissionEntry matches the JSON shape returned by GET /api/auth/me/permissions.
 * Both action and resource are string to stay forward-compatible with new
 * constants added to internal/auth/types.go without a frontend change.
 */
export interface PermissionEntry {
  action: string;
  resource: string;
}

/**
 * UserPermissionsResponse is the shape of GET /api/auth/me/permissions.
 */
export interface UserPermissionsResponse {
  permissions: PermissionEntry[];
  is_admin: boolean;
}

export interface User {
  id: string;
  email: string;
  // groups holds the UUIDs of the groups this user belongs to.
  // Authorization is now derived entirely from group membership
  // (PR #912 drops the role column). Admin status = member of
  // Administrators group (00000000-0000-5000-8000-000000000001).
  groups: string[];
  // Whether two-factor authentication is enabled on this user
  // account. Optional for backward compatibility -- older login
  // responses may not include it. The profile/MFA section in
  // auth.ts treats `mfa_enabled === true` (strict) as "enabled".
  mfa_enabled?: boolean;
  // Effective permissions fetched from GET /api/auth/me/permissions
  // on login/bootstrap (issue #917). Undefined until the fetch
  // completes; canAccess() falls back to group-membership-only
  // gating (admins pass, others blocked) while this is loading.
  effectivePermissions?: PermissionEntry[];
}

export interface LoginResponse {
  token: string;
  user?: User;
}

// MFA enrollment / lifecycle response shapes (issue #497). All four
// endpoints live under /api/auth/mfa/ and require an authenticated
// session.

/**
 * Response from POST /api/auth/mfa/setup. The secret is also the
 * payload inside the otpauth:// provisioning URI; we expose both
 * separately so the UI can show the QR code AND a manual-entry
 * fallback (some authenticator apps don't have a camera).
 */
export interface MFASetupResponse {
  secret: string;
  provisioning_uri: string;
}

/**
 * Response from POST /api/auth/mfa/enable and POST
 * /api/auth/mfa/regenerate-recovery-codes. Plaintext codes are
 * returned exactly once; the backend stores only bcrypt hashes.
 */
export interface MFARecoveryCodesResponse {
  recovery_codes: string[];
}

/**
 * Discriminator returned by the login endpoint when MFA is required
 * or when the supplied MFA code didn't match. The backend encodes
 * these as the `error` field on the 401 response body so the
 * frontend can branch on a machine-readable code rather than
 * substring-matching a human message. See issue #497.
 */
export type MFALoginErrorCode = 'mfa_required' | 'invalid_mfa_code';

// Dashboard types
export interface DashboardSummary {
  potential_monthly_savings: number;
  total_recommendations: number;
  active_commitments: number;
  committed_monthly: number;
  current_coverage: number;
  target_coverage: number;
  ytd_savings: number;
  by_service: Record<string, { potential_savings: number; current_savings: number }>;
}

export interface UpcomingPurchase {
  execution_id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: string;
  service: string;
  step_number: number;
  total_steps: number;
  estimated_savings: number;
  // created_by_user_id is the UUID of the user who scheduled the
  // execution, propagated by the backend so the dashboard's "Cancel"
  // button can apply the same creator-scope ownership gate the Plans
  // page uses (issue #950). Optional because legacy / scheduler-tick
  // rows ship NULL here; the gate treats undefined as "not the current
  // user" and hides the button for non-update-any callers.
  created_by_user_id?: string;
}

// Recommendation types
export interface Recommendation {
  id: string;
  provider: string;
  service: string;
  region: string;
  resource_type: string;
  engine?: string;
  // Opaque ServiceDetails payload from the backend (json.RawMessage).
  // The frontend treats this as a pass-through blob: it is stashed when
  // the GET /api/recommendations response is parsed and forwarded unchanged
  // in the POST /api/purchases/execute body so the backend can reconstruct
  // the correct typed *Details pointer (Platform, Tenancy, Scope, Engine
  // etc.) for each cloud service client. Absent on pre-#597 cached rows;
  // those fall back to zero-valued defaults on the backend. See #597, #453.
  details?: unknown;
  count: number;
  // recommended_count is the pre-scaling count this rec carried before the
  // bulk-purchase Capacity % slider scaled it down. Stamped onto the scaled
  // copy at submit time so the backend can verify capacity_percent against the
  // scaled count rather than trusting a decorative audit field (#647). Absent
  // on un-scaled / full-capacity / legacy recs, in which case the backend
  // skips the consistency check for that rec.
  recommended_count?: number;
  term: number;
  payment: string;
  upfront_cost: number;
  // null when the provider API did not return a monthly recurring breakdown.
  monthly_cost: number | null;
  savings: number;
  // Canonical on-demand monthly baseline straight from the cloud provider
  // (Azure CostWithNoReservedInstances, AWS EstimatedMonthlyOnDemandCost).
  // Optional/null when the provider didn't return a baseline; in that case
  // the frontend reconstructs on-demand from monthly_cost + savings +
  // amortized_upfront. When populated it's preferred — see #274.
  on_demand_cost?: number | null;
  selected: boolean;
  purchased: boolean;
  purchase_id?: string;
  error?: string;
  cloud_account_id?: string;
  // usage_history carries the last 7 daily RI-coverage percentages (0-100,
  // oldest-to-newest). Absent/null when the provider did not populate it
  // (non-AWS providers or pre-#239 cached rows).
  usage_history?: number[] | null;
  // vcpu / memory_gb surface the compute size of the recommended instance
  // type so the Capacity column can render "<vcpu> vCPU / <memory_gb> GB"
  // (#219). Emitted top-level by the backend (buildRecommendationsResponse
  // decodes the nested ComputeDetails and stamps them) only for compute recs
  // with a known size; absent for non-compute services, legacy rows, or
  // unknown sizes. formatCapacity renders absent/0 as "—".
  vcpu?: number | null;
  memory_gb?: number | null;
}

export interface RecommendationFilters {
  provider?: Provider | '';
  service?: string;
  region?: string;
  /**
   * Minimum absolute dollar savings per month. Sent to the API as
   * `min_savings_usd`. 0 or absent means no floor.
   *
   * IMPORTANT: this is a DOLLAR amount, not a percentage. The CLI equivalent
   * is `--min-savings-pct` which filters by percentage -- these are two
   * distinct concepts. Use `minSavingsPct` to filter by percentage.
   */
  minSavingsUsd?: number;
  /**
   * Minimum effective savings percentage (0-100 scale). Sent to the API as
   * `min_savings_pct`. 0 or absent means no floor.
   *
   * IMPORTANT: this is a PERCENTAGE (0-100), not a dollar amount. The CLI
   * `--min-savings-pct` flag uses the same semantics. Use `minSavingsUsd`
   * to filter by an absolute dollar floor.
   */
  minSavingsPct?: number;
  account_ids?: string[];
}

// PlanFilters are the query parameters accepted by the GET /api/plans endpoint.
export interface PlanFilters {
  account_ids?: string[];
}

// Plan types
export interface PlanRampSchedule {
  type: string;
  percent_per_step: number;
  step_interval_days: number;
  current_step: number;
  total_steps: number;
  start_date?: string;
}

export interface Plan {
  id: string;
  name: string;
  enabled: boolean;
  auto_purchase: boolean;
  notification_days_before: number;
  services: Record<string, ServiceConfig>;
  ramp_schedule: PlanRampSchedule;
  created_at: string;
  updated_at: string;
  next_execution_date?: string;
  last_execution_date?: string;
  // unassigned is true for legacy plans that have zero plan_accounts rows.
  // Such plans are surfaced under an "Unassigned" section in the Plans UI
  // so operators can find and re-scope them (issue #973).
  unassigned?: boolean;
}

export interface CreatePlanRequest {
  name: string;
  enabled: boolean;
  auto_purchase: boolean;
  notification_days_before: number;
  services: Record<string, ServiceConfig>;
  ramp_schedule: PlanRampSchedule;
  // Required server-side (universal-plans fix): a plan must be tied to at
  // least one cloud account. The frontend bundles the selected account IDs
  // here so the backend receives plan creation + account assignment in a
  // single request and can validate atomically.
  target_accounts: string[];
}

// History types
export interface PurchaseHistory {
  account_id: string;
  purchase_id: string;
  timestamp: string;
  provider: string;
  service: string;
  region: string;
  resource_type: string;
  count: number;
  term: number;
  payment: string;
  upfront_cost: number;
  monthly_cost: number | null;
  estimated_savings: number;
  plan_id?: string;
  plan_name?: string;
  ramp_step?: number;
  cloud_account_id?: string;
}

export interface HistoryFilters {
  start?: string;
  end?: string;
  provider?: Provider;
  planId?: string;
  account_ids?: string[];
}

// Config types
export interface Config {
  enabled_providers: Provider[];
  notification_email?: string;
  auto_collect: boolean;
  collection_schedule: string;
  notification_days_before: number;
  approval_required?: boolean;
  default_term: number;
  default_payment: PaymentOption;
  default_coverage: number;
  default_ramp_schedule?: string;
  // Per-provider grace-period window (days) during which recently-
  // purchased capacity is suppressed from the Recommendations view.
  // Keys: 'aws' / 'azure' / 'gcp'. A missing key defaults to 7 on
  // the backend. Explicit 0 disables the feature for that provider.
  grace_period_days?: Record<string, number>;
  // EC2 Reserved Instance offering class. "convertible" (default) allows
  // future exchanges for different families/sizes/OS; "standard" is
  // ~5% cheaper but locked to the exact instance type for the full term.
  offering_class?: 'convertible' | 'standard';
  ri_exchange_enabled?: boolean;
  ri_exchange_mode?: string;
  ri_exchange_utilization_threshold?: number;
  ri_exchange_max_per_exchange_usd?: number;
  ri_exchange_max_daily_usd?: number;
  ri_exchange_lookback_days?: number;
  // Age (hours) after which the recommendations cache triggers a background
  // stale-while-revalidate refresh. 0 disables automatic background refresh;
  // the cron scheduler and the manual Refresh button still work regardless.
  // Valid range: 0–8760 (up to one year). Default: 24.
  recommendations_cache_stale_hours?: number;
  // AWS Cost Explorer lookback window (days) for fresh recommendations.
  // Must be 7, 30, or 60 (AWS LookbackPeriodInDays enum). Default: 7.
  // GCP CUD Recommender has no equivalent parameter; applies to AWS only.
  recommendations_lookback_days?: number;
  // Global kill-switch for the commitment-laddering feature (issue #1336).
  // Default false. When true, per-account LadderConfig.enabled settings
  // determine whether the engine runs for that account.
  laddering_enabled?: boolean;
  // When true, a purchase execution cannot be approved by the same user who
  // created it -- a different person with approval rights must do so.
  // SOX / SOC2 segregation-of-duties control. Default: false.
  require_different_approver?: boolean;
}

export interface ServiceConfig {
  provider: string;
  service: string;
  enabled: boolean;
  term: number;
  payment: string;
  coverage: number;
  ramp_schedule?: string;
  include_engines?: string[];
  exclude_engines?: string[];
  include_regions?: string[];
  exclude_regions?: string[];
  include_types?: string[];
  exclude_types?: string[];
  /** Minimum instance/node count a recommendation must carry to be surfaced
   *  (GUI equivalent of the CLI --min-count flag). 0 / undefined disables the
   *  filter. */
  min_count?: number;
}

/** Response from GET /api/info (unauthenticated). Only safe-to-expose fields. */
export interface PublicInfo {
  version: string;
  admin_exists: boolean;
}

/** Response from GET /api/info/deployment (AuthUser). Sensitive identifiers
 *  that must not be visible to unauthenticated callers (#633). */
export interface DeploymentInfo {
  /** AWS Console deep-link to the Secrets Manager secret holding the API key. */
  api_key_secret_url?: string;
  /** AWS account ID of the CUDly Lambda (from STS GetCallerIdentity). Present
   *  only on AWS-hosted deployments; omitted on Azure/GCP or when STS is
   *  unreachable. Used by formatAccountLabel to distinguish genuine
   *  ambient-credential executions from orphan rows (issue #608). */
  deployment_aws_account_id?: string;
}

// Purchase types
export interface PurchaseResult {
  execution_id: string;
  status: string;
  recommendation_count?: number;
  total_upfront_cost?: number;
  estimated_savings?: number;
  message?: string;
  // Whether the approval email was actually sent. When false, email_reason
  // carries a human-readable explanation (e.g. "no notification email set in
  // Settings → General") so the UI can tell the user to approve/cancel from
  // History instead of waiting for an inbox.
  email_sent?: boolean;
  email_reason?: string;
  // Resolved To address that received the approval email. Surfaced so the
  // post-submit toast can name the approver ("Approval request sent to
  // alice@acme.com") per the CR pass on PR #294 / issue #288. Absent when
  // recipient resolution itself failed (no approvers configured).
  approval_recipient?: string;
  // True when the request was handled via the direct-execute path (issue
  // #289). Absent (undefined) on the standard approval-required flow.
  direct_execute?: boolean;
  results?: Array<{
    recommendation_id: string;
    status: string;
    error?: string;
  }>;
}

// PurchaseDetails matches the shape produced by backend
// buildPurchaseDetailsResponse (internal/api/handler_purchases.go).
// `recommendations` carries the per-rec snapshot that was captured at
// approval-request time so callers can render the deal without
// re-querying provider APIs. plan_id / plan_name / scheduled_date are
// only present when the execution belongs to a plan (direct-execute
// purchases from the recommendations page have plan_id="" and
// plan_name="").
export interface PurchaseDetails {
  execution_id: string;
  status: string;
  plan_id?: string;
  plan_name?: string;
  step_number?: number;
  scheduled_date?: string;
  total_upfront_cost: number;
  estimated_savings: number;
  recommendations: Recommendation[];
  notification_sent?: string;
  completed_at?: string;
  error?: string;
}

export interface PlannedPurchasesResponse {
  purchases: PlannedPurchase[];
}

export interface PlannedPurchase {
  id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: Provider;
  service: string;
  resource_type: string;
  region: string;
  count: number;
  term: number;
  payment: string;
  estimated_savings: number;
  upfront_cost: number;
  status: 'pending' | 'paused' | 'running' | 'completed' | 'failed';
  step_number: number;
  total_steps: number;
  // created_by_user_id is the UUID of the user who scheduled the purchase.
  // The row action buttons are gated on creator-scope ownership (issue #950):
  // a non-creator without update-any:purchases sees no actionable buttons.
  // Omitted (undefined) for legacy rows with a NULL creator.
  created_by_user_id?: string;
}

// User Management Types
export interface APIUser {
  id: string;
  email: string;
  // role is intentionally absent: authorization is now purely
  // group-membership based (PR #912). The groups array is the
  // single source of truth for what a user can do.
  groups: string[];
  mfa_enabled: boolean;
  created_at?: string;
  updated_at?: string;
  last_login?: string;
}

export interface CreateUserRequest {
  email: string;
  password: string;
  // groups is required (>= 1) by the backend (PR #912). Sending an
  // empty array is rejected with 400.
  groups: string[];
}

// CreateUserResponse extends APIUser with optional invite-delivery
// status fields that POST /api/users returns when the request created
// an invited (passwordless) user. invite_email_sent is undefined for
// password-up-front creates, true when the invite email was handed to
// the configured sender, and false when the user row exists but the
// recipient has not been told how to activate it (admin should re-mail
// the setup link via Forgot Password).
export interface CreateUserResponse extends APIUser {
  invite_email_sent?: boolean;
  invite_email_error?: string;
}

export interface UpdateUserRequest {
  email?: string;
  // role is removed; update group membership to change authorization.
  groups?: string[];
}

// Group Management Types
export interface APIGroup {
  id: string;
  name: string;
  description: string;
  permissions: Permission[];
  allowed_accounts?: string[];
  // system_managed groups are seeded by migrations; they cannot be
  // renamed or deleted via the UI (only membership can change).
  system_managed?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface Permission {
  action: string;
  resource: string;
  constraints?: {
    accounts?: string[];
    providers?: string[];
    services?: string[];
    regions?: string[];
    max_amount?: number;
  };
}

export interface CreateGroupRequest {
  name: string;
  description?: string;
  permissions: Permission[];
  allowed_accounts?: string[];
}

export interface UpdateGroupRequest {
  name?: string;
  description?: string;
  permissions?: Permission[];
  allowed_accounts?: string[];
}

// API Keys Management Types
export interface APIKeyInfo {
  id: string;
  name: string;
  key_prefix: string;
  is_active: boolean;
  expires_at?: string;
  created_at: string;
  last_used_at?: string;
  permissions?: Permission[];
  // Usage counters (issue #340/#344 deferred sub-task).
  // Optional in the type so older mock data / cached responses without
  // the columns don't blow up the renderer -- but the backend always
  // sends them as of migration 000094.
  request_count_total?: number;
  request_count_24h?: number;
}

export interface APIKeysUsageStatsTopKey {
  id: string;
  name: string;
  key_prefix: string;
  request_count_24h: number;
}

export interface APIKeysUsageStats {
  total_active: number;
  total_requests_24h: number;
  total_requests_lifetime: number;
  top_keys: APIKeysUsageStatsTopKey[];
}

export interface CreateAPIKeyRequest {
  name: string;
  permissions?: Permission[];
  expires_at?: string;
}

export interface CreateAPIKeyResponse {
  api_key: string;  // Full key shown only once
  key_id: string;
  // Backend wire field is `info` (see internal/auth/service_apikeys_api.go
  // APICreateAPIKeyResponse). Was previously typed as `key` which never
  // matched the response — same bug class as issue #9 above.
  info: APIKeyInfo;
}

export interface GetAPIKeysResponse {
  // Backend wire field is `api_keys` (see internal/auth/service_apikeys_api.go
  // APIListAPIKeysResponse). The previous `keys` declaration didn't match
  // any backend field, so `response.keys` was always undefined and
  // crashed renderApiKeysList() with "Cannot read properties of undefined
  // (reading 'length')" — see GitHub issue #9.
  api_keys: APIKeyInfo[];
}

// Savings Analytics Types
export interface SavingsAnalyticsResponse {
  start: string;
  end: string;
  interval: string;
  summary: SavingsAnalyticsSummary;
  data_points: SavingsDataPoint[];
}

export interface SavingsAnalyticsSummary {
  total_period_savings: number;
  total_upfront_spent: number;
  purchase_count: number;
  average_savings_per_period: number;
  peak_savings: number;
}

export interface SavingsDataPoint {
  timestamp: string;
  total_savings: number;
  total_upfront: number;
  purchase_count: number;
  cumulative_savings: number;
  by_service?: Record<string, number>;
  by_provider?: Record<string, number>;
}

export interface SavingsBreakdownResponse {
  dimension: string;
  start: string;
  end: string;
  data: Record<string, SavingsBreakdownValue>;
}

export interface SavingsBreakdownValue {
  total_savings: number;
  total_upfront: number;
  purchase_count: number;
  percentage: number;
}

export interface SavingsAnalyticsFilters {
  start?: string;
  end?: string;
  interval?: 'hourly' | 'daily' | 'weekly' | 'monthly';
  provider?: Provider;
  service?: string;
  account_ids?: string[];
}

// RI Exchange Types
export interface ConvertibleRI {
  reserved_instance_id: string;
  instance_type: string;
  availability_zone: string;
  instance_count: number;
  start: string;
  end: string;
  offering_type: string;
  fixed_price: number;
  usage_price: number;
  state: string;
  normalization_factor: number;
}

// ExchangeableAzureRI is one Azure VM reservation eligible for the
// cross-SKU/cross-region exchange flow, returned by
// GET /api/ri-exchange/azure-instances. Mirrors the Go
// azurecompute.ExchangeableReservation struct (issue #871).
export interface ExchangeableAzureRI {
  reservation_order_id: string;
  reservation_id: string;
  sku: string;
  quantity: number;
  region?: string;
  term?: string;
  expiry_date?: string;
  instance_flexibility: string;
  display_name?: string;
}

export interface RIUtilization {
  reserved_instance_id: string;
  utilization_percent: number;
  purchased_hours: number;
  total_actual_hours: number;
  unused_hours: number;
}

export interface OfferingOption {
  instance_type: string;
  offering_id: string;
  effective_monthly_cost: number;
}

// TargetOffering is one valid exchange target returned by
// GET /api/ri-exchange/target-offerings. The offering_id is the AWS
// ReservedInstancesOfferingId UUID that must be submitted in the
// targets[] exchange request.
export interface TargetOffering {
  offering_id: string;
  instance_type: string;
  offering_type: string;
  product_description: string;
  duration: number;
  fixed_price: number;
  usage_price: number;
  currency_code: string;
  scope: string;
  normalization_factor: number;
}

export interface ReshapeRecommendation {
  source_ri_id: string;
  source_instance_type: string;
  source_count: number;
  target_instance_type: string;
  target_count: number;
  alternative_targets?: OfferingOption[];
  utilization_percent: number;
  normalized_used: number;
  normalized_purchased: number;
  reason: string;
}

// ExchangeTarget is one entry in a multi-target exchange request.
// Matches the Go struct pkg/exchange.TargetConfig.
export interface ExchangeTarget {
  offering_id: string;
  count: number;
}

// ExchangeQuoteRequest shape: callers may supply either the new
// `targets[]` array (preferred) or the legacy singleton fields
// (`target_offering_id` + `target_count`). When `targets[]` is
// present, the singleton fields are ignored server-side. The
// backend's validateExecuteExchangeBody accepts either shape since
// commit 5eb274690.
export interface ExchangeQuoteRequest {
  ri_ids: string[];
  targets?: ExchangeTarget[];
  target_offering_id?: string;
  target_count?: number;
  region?: string;
}

export interface ExchangeQuoteSummary {
  IsValidExchange: boolean;
  ValidationFailureReason: string;
  CurrencyCode: string;
  PaymentDueRaw: string;
  SourceHourlyPriceRaw: string;
  SourceRemainingUpfrontRaw: string;
  SourceRemainingTotalRaw: string;
  TargetHourlyPriceRaw: string;
  TargetRemainingUpfrontRaw: string;
  TargetRemainingTotalRaw: string;
  OutputReservedInstancesExp?: string;
}

// ExchangeExecuteRequest: same `targets[]` vs legacy singleton
// semantics as ExchangeQuoteRequest. `max_payment_due_usd` is a
// TOTAL cap across all targets — AWS returns a single aggregated
// PaymentDue so spend-cap checking becomes a total when `targets[]`
// has multiple entries.
export interface ExchangeExecuteRequest {
  ri_ids: string[];
  targets?: ExchangeTarget[];
  target_offering_id?: string;
  target_count?: number;
  max_payment_due_usd: string;
  region?: string;
}

export interface ExchangeResult {
  exchange_id: string;
  quote: ExchangeQuoteSummary;
}

// RI Exchange Config Types
export interface RIExchangeConfig {
  auto_exchange_enabled: boolean;
  mode: "manual" | "auto";
  utilization_threshold: number;
  max_payment_per_exchange_usd: number;
  max_payment_daily_usd: number;
  lookback_days: number;
}

export interface RIExchangeHistoryRecord {
  id: string;
  account_id: string;
  exchange_id: string;
  region: string;
  source_ri_ids: string[];
  source_instance_type: string;
  source_count: number;
  target_offering_id: string;
  target_instance_type: string;
  target_count: number;
  payment_due: string;
  status: string;
  mode: string;
  error?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  expires_at?: string;
  /** UUID of the session user who submitted this exchange (issue #300). */
  created_by_user_id?: string;
  /** Email of the session user who approved via the dashboard Approve button (issue #300). */
  approved_by?: string;
}

// Inventory & Coverage types (issue #340 deferred sub-task — Active commitments)
export interface InventoryCommitment {
  id: string;
  provider: string;
  account_id: string;
  account_name?: string;
  service: string;
  resource_type?: string;
  region: string;
  count: number;
  term_years: number;
  payment_option?: string;
  start_date: string;
  end_date: string;
  upfront_cost: number;
  /** null when the provider API did not return a monthly recurring breakdown; "$X.XX" when populated, "—" when null. */
  monthly_cost: number | null;
  estimated_savings: number;
  /**
   * Always "active" today — the backend filters expired rows before
   * responding. The field stays in the shape so a future "expiring
   * soon" sub-state can land without a breaking API change.
   */
  status: string;
}

// Coverage breakdown types (issue #754)

/**
 * One service row within a provider's coverage section.
 * coverage_pct is null when both covered_monthly and on_demand_monthly
 * are zero (no usage detected) -- do not coerce null to 0.
 */
export interface CoverageServiceRow {
  service: string;
  covered_monthly: number;
  on_demand_monthly: number;
  coverage_pct: number | null;
}

/**
 * Per-provider coverage section returned by GET /api/inventory/coverage.
 * services is null when the provider has no usage data. Render "No usage
 * detected" rather than an empty table in that case.
 */
export interface ProviderCoverageSection {
  provider: string;
  services: CoverageServiceRow[] | null;
  overall_coverage_pct: number | null;
}

/** Envelope returned by GET /api/inventory/coverage. */
export interface CoverageBreakdownResponse {
  providers: ProviderCoverageSection[];
}

// Internal types
export interface ApiError extends Error {
  status?: number;
  // Structured detail fields the backend attaches to a 4xx response
  // alongside the human `error` message (e.g. `ops_hint`,
  // `retry_attempt_n`, `threshold`, `retry_execution_id`). Callers
  // can branch on these without substring-matching the message.
  // See internal/api/handler.go for the flattening — keys are
  // promoted to the top level of the JSON body.
  details?: Record<string, unknown>;
}

export interface RequestOptions extends RequestInit {
  headers?: Record<string, string>;
  /**
   * Hard timeout for the request in milliseconds. Defaults to
   * DEFAULT_API_TIMEOUT_MS (90 000) in client.ts; pass `0` to disable
   * the timeout (e.g. for long-running admin flows that genuinely
   * need to block) or a smaller value for cheap endpoints.
   */
  timeoutMs?: number;
}
