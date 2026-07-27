/**
 * Shared type definitions for CUDly frontend
 */

import { Chart } from 'chart.js';
import * as api from './api';

// App state interface
export interface AppState {
  currentUser: api.User | null;
  currentProvider: api.Provider | '';
  currentAccountIDs: string[]; // selected account UUIDs; empty = all accounts
  currentRecommendations: api.Recommendation[];
  // selectedRecommendations holds rec IDs (not indices). ID-keyed
  // storage survives filter changes that would reshuffle index
  // positions — selecting row 3 and then changing filter used to
  // mean "row 3 is selected" relative to the new list, i.e. some
  // other rec the user never clicked.
  selectedRecommendations: Set<string>;
  savingsChart: Chart | null;
}

// Dashboard types
export interface DashboardSummary {
  potential_monthly_savings?: number;
  total_recommendations?: number;
  active_commitments?: number;
  committed_monthly?: number;
  current_coverage?: number;
  target_coverage?: number;
  ytd_savings?: number;
  by_service?: Record<string, ServiceSavings>;
}

export interface ServiceSavings {
  potential_savings: number;
  current_savings: number;
}

export interface UpcomingPurchase {
  execution_id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: api.Provider;
  service: string;
  step_number: number;
  total_steps: number;
  estimated_savings: number;
  // created_by_user_id mirrors the field on api.UpcomingPurchase so the
  // dashboard widget can apply the issue-#950 creator-scope ownership
  // gate on the Cancel button. Optional: legacy / scheduler-tick rows
  // ship NULL here.
  created_by_user_id?: string;
}

// Recommendations types
export interface RecommendationsResponse {
  recommendations?: LocalRecommendation[];
  summary?: RecommendationsSummary;
  regions?: string[];
}

export interface LocalRecommendation {
  id: string;
  provider: api.Provider;
  service: string;
  resource_type: string;
  engine?: string;
  // Opaque ServiceDetails payload from the backend (json.RawMessage).
  // Stashed when the GET /api/recommendations response is parsed and
  // forwarded unchanged in the POST /api/purchases/execute body so the
  // backend can reconstruct the correct typed *Details pointer. Absent
  // on pre-#597 cached rows. See #597, #453.
  details?: unknown;
  region: string;
  count: number;
  // recommended_count is the pre-scaling count this rec carried before the
  // bulk-purchase Capacity % slider scaled it down. Stamped onto the scaled
  // copy at purchase-submit time so the backend can verify capacity_percent
  // against the scaled count instead of trusting a decorative audit field
  // (#647). Absent on un-scaled recs (the rendered list) and on legacy rows.
  recommended_count?: number;
  term: number;
  // The API stamps `payment` on every Recommendation row at collection
  // time, so runtime data carries it; surfacing it in the type lets the
  // issue #111 per-row purchase modal read/write it directly. Optional
  // because pre-#111 cached responses and some test fixtures omit it;
  // the modal handles undefined via its seed helper.
  payment?: string;
  savings: number;
  upfront_cost: number;
  // null = provider API did not return a monthly breakdown (renders as "—").
  monthly_cost?: number | null;
  // Canonical on-demand monthly baseline straight from the cloud provider
  // (Azure CostWithNoReservedInstances, AWS EstimatedMonthlyOnDemandCost).
  // Optional/null when the provider didn't return a baseline; in that case
  // effectiveSavingsPct reconstructs on-demand from
  // `monthly_cost + savings + amortized_upfront`. When populated it's
  // preferred — see #274.
  on_demand_cost?: number | null;
  // Provider-authoritative effective savings % reported directly by the cloud
  // provider (AWS EstimatedMonthlySavingsPercentage, Azure/GCP converter
  // SavingsPercentage), the same figure the CLI/reporter prints. When it is
  // a finite, non-null number the effective-% column and its sort prefer it
  // over the client-side reconstruction (effectiveSavingsPct), so the GUI
  // shows the identical value the CLI shows and AWS recs missing
  // on_demand_cost still render a real % instead of an em-dash (see #323).
  // Optional/null when the provider did not report a percentage; the column
  // then falls back to effectiveSavingsPct.
  savings_percentage?: number | null;
  cloud_account_id?: string;
  // Populated by the scheduler when any active purchase_suppression
  // matches this rec's 6-tuple. The three fields drive the "recently
  // purchased N/M — Xd remaining" badge. Absent / 0 means no active
  // suppression (badge hidden). Added in Commit 2 of the bulk-purchase-
  // with-grace feature.
  suppressed_count?: number;
  suppression_expires_at?: string;
  primary_suppression_execution_id?: string;
  // usage_history carries the last 7 daily RI-coverage percentages (0-100,
  // oldest-to-newest) returned by the AWS CE GetReservationCoverage API.
  // null or absent means the collector did not populate it (non-AWS providers
  // or pre-#239 cached rows); the cell renders "—" in that case.
  usage_history?: number[] | null;
  // ComputeDetails fields surfaced by PR #810/#816/#833.
  // null = provider catalogue did not return a value (renders as "—", not "0").
  // Absent on non-compute recs (RDS, savings plans, etc.).
  vcpu?: number | null;
  memory_gb?: number | null;
}

export interface RecommendationsSummary {
  total_count?: number;
  total_monthly_savings?: number;
  total_upfront_cost?: number;
  avg_payback_months?: number;
}

// Plans types
export interface PlansResponse {
  plans?: LocalPlan[];
}

export interface LocalPlan {
  id: string;
  name: string;
  description?: string;
  provider: api.Provider;
  service: string;
  term: number;
  payment: string;
  target_coverage: number;
  ramp_schedule: api.RampSchedule;
  auto_purchase: boolean;
  enabled: boolean;
  notification_days_before: number;
  current_step?: number;
  total_steps?: number;
  next_execution_date?: string;
  custom_step_percent?: number;
  custom_interval_days?: number;
}

export interface SavePlanData {
  name: string;
  description: string;
  provider: string;
  service: string;
  term: number;
  payment: string;
  target_coverage: number;
  ramp_schedule: string;
  auto_purchase: boolean;
  notification_days_before: number;
  enabled: boolean;
  custom_step_percent?: number;
  custom_interval_days?: number;
  recommendations?: api.Recommendation[];
  // UUIDs of cloud accounts the plan will purchase for. The server now
  // requires at least one (universal-plans fix). The modal renders selected
  // accounts as chips; savePlan reads them out of the hidden #plan-account-
  // ids field and stamps the array here before POST /plans.
  target_accounts?: string[];
}

// History types
export interface HistoryResponse {
  summary?: HistorySummary;
  purchases?: HistoryPurchase[];
}

export interface HistorySummary {
  total_purchases?: number;
  total_completed?: number;
  total_pending?: number;
  total_failed?: number;
  total_expired?: number;
  total_upfront?: number;
  total_monthly_savings?: number;
  total_annual_savings?: number;
}

export interface HistoryPurchase {
  purchase_id?: string;
  timestamp: string;
  provider: string;
  service: string;
  resource_type: string;
  region: string;
  count: number;
  term: number;
  payment?: string;
  upfront_cost: number;
  monthly_cost?: number | null;
  estimated_savings: number;
  account_id?: string;
  plan_name?: string;
  // Status is set by the API to "completed" or "pending". Legacy pre-schema
  // rows come back without it; the UI treats absent status as completed for
  // counting + badge rendering.
  status?: string;
  // Approver: the email address the approval request was sent to. Only set
  // on pending rows — the UI renders "awaiting approval from <approver>" so
  // the user knows exactly whose inbox to check.
  approver?: string;
  // StatusDescription: short reason rendered below the status badge for
  // non-ok rows. "failed" → backend's send-error message; "expired" → canned
  // 7-day-window reminder. Empty on completed / pending rows.
  status_description?: string;
  // CreatedByUserEmail: resolved email of the user who created the
  // underlying execution. Populated on synthesised execution rows when the
  // auth lookup succeeds. Empty for scheduler-driven rows, legacy NULL-
  // creator rows, and completed purchase_history rows. The Approval Queue
  // renders this instead of the raw UUID when present.
  created_by_user_email?: string;
  // CreatedByUserID: UUID of the user who created the underlying execution.
  // Populated on every synthesised purchase_executions row the History
  // endpoint returns (pending, notified, failed, expired, cancelled — see
  // historyExecutionStatuses in the backend). NOT populated on rows from
  // the purchase_history table (completed purchases) since attribution
  // there is via the older approver/cancelled_by fields.
  // Empty when:
  //   * the row is a completed-purchase row from purchase_history, OR
  //   * the underlying execution pre-dates migration 000041 (legacy
  //     NULL creator), OR
  //   * the execution was scheduler-driven (no human creator).
  // The History UI uses it to decide whether the inline Cancel button
  // (issue #46) is visible — non-admins only see it on rows they
  // themselves created.
  created_by_user_id?: string;
  // RetryExecutionID: pointer from a *failed* row to the successor
  // execution created when the user clicked Retry (issue #47). Set
  // only on the original failed row; absent on every other row
  // (including the retry row itself — its own retry_attempt_n > 0 is
  // the "this is a retry" marker). The History UI renders an inline
  // "↻ Retried as #abc" link to the successor when this is non-empty.
  retry_execution_id?: string;
  // RetryAttemptN: position of this execution in a retry chain. 0 on
  // every fresh execution; 1 on the first retry; n+1 on the n+1-th.
  // The History UI renders "↻ Retry of #xyz" on retry rows (n > 0)
  // pointing back to the predecessor, and gates the Retry button
  // against the soft-block threshold (n >= 5 → confirm-with-warning).
  retry_attempt_n?: number;
  // OpsHint: short operator-actionable message rendered inline in
  // place of the Retry button when the failure reason on the row
  // matches a known-persistent-misconfiguration pattern (e.g.
  // "FROM_EMAIL not configured" -> "Set FROM_EMAIL tfvar then retry").
  // Set only on `failed` rows whose Error matches the persistent map;
  // absent otherwise. Replaces the Retry button entirely -- there is
  // no actionable retry from a persistent misconfig.
  ops_hint?: string;

  // Revocation window fields (issue #290).
  //
  // revocation_window_closes_at: ISO-8601 timestamp after which in-app
  // revocation is no longer available. Set only for providers that support
  // a direct cancel API (Azure: 7 days). Absent for AWS and GCP.
  revocation_window_closes_at?: string;
  // revoked_at: ISO-8601 timestamp when the purchase was revoked via the
  // in-app flow. Set only on rows that have been successfully revoked.
  revoked_at?: string;
  // revoked_via: "direct-api" or "support-case". Absent unless revoked.
  revoked_via?: string;

  // OfferingClass is "standard" or "convertible" for EC2 RIs. Absent
  // on non-EC2 rows and on rows written before migration 000068.
  // The "Sell on Marketplace" button only renders when this equals
  // "standard" (issue #292).
  offering_class?: string;
  // ListingID is the AWS ReservedInstancesListingId. Non-empty when an
  // active or recently-closed marketplace listing exists (issue #292).
  listing_id?: string;
  // ListingState is the AWS marketplace listing state: "active",
  // "cancelled", or "closed". Empty when not listed.
  listing_state?: string;
}

// Savings Analytics types
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

// Settings types
export interface SourceIdentity {
  provider: string;
  account_id?: string;
  subscription_id?: string;
  tenant_id?: string;
  client_id?: string;
  project_id?: string;
  // AWS partition (`aws`, `aws-cn`, `aws-us-gov`) — populated only when
  // provider === "aws" and STS GetCallerIdentity returned an ARN we
  // could parse. Used to render partition-aware ARN prefixes in the
  // trust-policy snippet (issue #130c). Absent on non-AWS providers
  // and on best-effort STS failures (treated as "aws" downstream).
  partition?: string;
}

export interface ConfigResponse {
  global?: GlobalConfig;
  services?: api.ServiceConfig[];
  source_cloud?: string;
  source_identity?: SourceIdentity;
}

export interface GlobalConfig {
  enabled_providers?: string[];
  notification_email?: string;
  auto_collect?: boolean;
  collection_schedule?: string;
  default_term?: number;
  default_payment?: string;
  default_coverage?: number;
  notification_days_before?: number;
  // Per-provider grace-period window (days) for the recently-purchased
  // suppression feature. Keys: 'aws' / 'azure' / 'gcp'. Missing keys
  // fall back to the backend default (7). Explicit 0 = disabled.
  grace_period_days?: Record<string, number>;
  // EC2 Reserved Instance offering class. "convertible" (default) or "standard".
  offering_class?: 'convertible' | 'standard';
  // Age (hours) after which the recommendations cache triggers a background
  // stale-while-revalidate refresh. 0 disables automatic background refresh.
  // Valid range: 0–8760. Default: 24.
  recommendations_cache_stale_hours?: number;
  // AWS Cost Explorer lookback window (days). One of 7, 30, or 60. Default: 7.
  recommendations_lookback_days?: number;
  // Global kill-switch for the commitment-laddering feature (issue #1333 phase 3).
  // When false (the default) no laddering engine runs fire, regardless of
  // per-account LadderConfig settings. Set to true to allow per-account
  // configs to activate individually.
  laddering_enabled?: boolean;
  // When true, a purchase execution cannot be approved by the user who created
  // it -- a different user with approve-any:purchases must approve (4-eyes /
  // dual-control rule). Defaults to false (feature is opt-in).
  require_different_approver?: boolean;
}

// API Keys types
export interface APIKeyInfo {
  id: string;
  name: string;
  key_prefix: string;
  is_active: boolean;
  expires_at?: string;
  created_at: string;
  last_used_at?: string;
  permissions?: api.Permission[];
  // Usage counters (issue #340/#344 deferred sub-task). Optional so
  // older mock data / cached responses without the columns don't blow
  // up the renderer -- the backend always sends them as of migration
  // 000094.
  request_count_total?: number;
  request_count_24h?: number;
}

export interface CreateAPIKeyResponse {
  api_key: string;  // Full key shown only once
  key_id: string;
  // Backend wire field is `info` (see internal/auth/service_apikeys_api.go
  // APICreateAPIKeyResponse). Was previously typed as `key` which never
  // matched the response — same bug class as issue #9.
  info: APIKeyInfo;
}

// Window type declarations
declare global {
  interface Window {
    refreshRecommendations: () => Promise<void>;
    openCreatePlanModal: () => void;
    openNewPlanModal: () => void;
    closePlanModal: () => void;
    closePurchaseModal: () => void;
    resetSettings: () => void;
    loadHistory: () => Promise<void>;
    logout: () => Promise<void>;
    openCreateUserModal: () => void;
    closeUserModal: () => void;
    openCreateGroupModal: () => void;
    closeGroupModal: () => void;
    addPermission: () => void;
  }
}
