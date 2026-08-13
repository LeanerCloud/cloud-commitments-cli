package config

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/ladder"
)

// StoreInterface defines the methods required for configuration storage.
type StoreInterface interface {
	// Global configuration
	GetGlobalConfig(ctx context.Context) (*GlobalConfig, error)
	SaveGlobalConfig(ctx context.Context, config *GlobalConfig) error
	// UpdateGlobalConfigAtomic serializes a read-modify-write of the
	// global_config singleton under an advisory-locked transaction so
	// concurrent partial PUTs cannot lose each other's updates. apply mutates
	// the loaded config in place; its error aborts the write and is propagated.
	UpdateGlobalConfigAtomic(ctx context.Context, apply func(*GlobalConfig) error) (*GlobalConfig, error)

	// Service configuration
	GetServiceConfig(ctx context.Context, provider, service string) (*ServiceConfig, error)
	SaveServiceConfig(ctx context.Context, config *ServiceConfig) error
	ListServiceConfigs(ctx context.Context) ([]ServiceConfig, error)

	// Purchase plans
	CreatePurchasePlan(ctx context.Context, plan *PurchasePlan) error
	GetPurchasePlan(ctx context.Context, planID string) (*PurchasePlan, error)
	UpdatePurchasePlan(ctx context.Context, plan *PurchasePlan) error
	// IncrementPlanCurrentStep atomically advances the ramp schedule for planID
	// inside a SELECT FOR UPDATE transaction, preventing the concurrent-write
	// lost-update race described in issue #1071.
	IncrementPlanCurrentStep(ctx context.Context, planID string) error
	// UpdatePurchasePlanTx is the tx-accepting variant of UpdatePurchasePlan.
	// Used from createPlannedPurchases' WithTx block so the per-row
	// SavePurchaseExecutionTx writes and the plan's next_execution_date
	// bump commit atomically — a partial failure leaves no orphaned
	// rows and no stale plan pointer.
	UpdatePurchasePlanTx(ctx context.Context, tx pgx.Tx, plan *PurchasePlan) error
	DeletePurchasePlan(ctx context.Context, planID string) error
	ListPurchasePlans(ctx context.Context, filter PurchasePlanFilter) ([]PurchasePlan, error)

	// Purchase executions
	SavePurchaseExecution(ctx context.Context, execution *PurchaseExecution) error
	GetPendingExecutions(ctx context.Context) ([]PurchaseExecution, error)
	// GetExecutionsByStatuses returns executions in any of the given states,
	// newest first, capped at `limit`. Used by the History handler to render
	// pending + failed + expired alongside completed purchases; the scheduler
	// keeps using GetPendingExecutions (which is narrower and doesn't share
	// this method's status filter) to avoid accidental double-processing of
	// failed / expired rows.
	GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error)
	// CountExecutionsByPlanAndStatus returns, keyed by plan ID, the exact
	// number of executions in each of the supplied statuses that last
	// changed state (updated_at) at or after `since` -- not those merely
	// scheduled since then, which for a canceled row is a future date. Rows
	// with a NULL plan_id (direct-execute purchases, and executions orphaned
	// by a deleted plan) belong to no plan and are excluded.
	//
	// Deliberately NOT derived from a GetExecutionsByStatuses page: that
	// method is capped by `limit` across ALL plans, so any caller counting
	// per-plan rows out of it silently understates plans whose executions
	// fall outside the newest `limit` rows. The plan health score
	// (internal/api/plan_health.go) needs exact counts -- an understated
	// count renders a confidently-healthy badge for an unhealthy plan --
	// so the aggregation happens in SQL and the result is bounded by
	// plans x statuses rather than by execution volume.
	CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]ExecutionStatusCounts, error)
	// GetPlannedExecutions returns executions in any of the given states
	// ordered by scheduled_date ASC (soonest first), the order the Planned
	// Purchases UI lists rows so the user acts on imminent purchases first.
	// Distinct from GetExecutionsByStatuses (which is DESC for History's
	// "newest first" semantics): when the result set exceeds `limit`, an
	// ORDER-BY-DESC + LIMIT in SQL truncates away the soonest rows, exactly
	// the rows this list must surface. Secondary sort by id ASC stabilizes
	// ordering when multiple rows share a scheduled_date.
	GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error)
	// GetStaleApprovedExecutions returns executions stuck in the "approved"
	// status with updated_at older than olderThan — strands left behind when a
	// synchronous purchase run was interrupted before finalizing (issue #632).
	// The recovery sweep in the purchase manager re-drives these into a
	// terminal "failed" state so they can never sit permanently approved.
	GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]PurchaseExecution, error)
	// GetExecutionByID retrieves a purchase execution by execution ID.
	// Returns an error wrapping ErrNotFound when no execution exists;
	// never returns (nil, nil). A nil error guarantees a non-nil execution.
	GetExecutionByID(ctx context.Context, executionID string) (*PurchaseExecution, error)
	GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*PurchaseExecution, error)
	// GetUserEmailByID resolves the email address of the auth user identified
	// by userID (the `users` table, owned by internal/auth). Read-only helper
	// used by the 4-eyes approval policy (issue #1005) to compare the acting
	// approver's identity against a PurchaseExecution.CreatedByUserID without
	// internal/purchase importing internal/auth (which would create an import
	// cycle: internal/auth already imports internal/config). Returns ("", nil)
	// when no user matches userID — the caller treats an empty result as
	// "identity unresolved" and fails closed, never as "email intentionally
	// blank".
	GetUserEmailByID(ctx context.Context, userID string) (string, error)
	// CountPendingExecutionsForAccount returns the number of purchase_executions
	// in status 'pending' or 'notified' that reference the given cloud account.
	// Used by the deleteAccount handler to preflight DB-level FK violations
	// (migration 000053 tightened the FK to ON DELETE RESTRICT) and emit a
	// 409 with a count so the frontend can offer Cancel-All-Then-Delete UX
	// instead of surfacing a raw constraint error. See issue #606.
	CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error)
	// ListPendingExecutionIDsForAccount returns the execution IDs of all
	// pending / notified executions referencing this account, used by the
	// frontend's Cancel-All-Then-Delete flow when the operator opts to
	// cancel everything in one go after a 409. Capped at 1000 rows; if a
	// single account has more pending executions than that, the cleanup
	// is a one-off operator task rather than a button click anyway.
	ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error)
	CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error)
	// TransitionExecutionStatus atomically transitions an execution status.
	// actor is the UUID of the user performing the transition (nil for system-initiated paths).
	// When non-nil the actor is stamped onto transitioned_by + transitioned_at; when nil,
	// transitioned_by is set to NULL and transitioned_at is still set to NOW() for ordering.
	TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*PurchaseExecution, error)
	// SetCancelledBy stamps canceled_by and the legacy cancelled_by column on
	// an execution without overwriting any other columns. Used after
	// TransitionExecutionStatus to fold the actor attribution into the same
	// logical write without the full-row SavePurchaseExecution clobber risk
	// (Finding #5 / PR #889).
	SetCancelledBy(ctx context.Context, executionID string, cancelledBy string) error
	// CancelExecutionAtomic atomically flips status from pending / notified
	// to 'canceled' (canonical US spelling), setting canceled_by. The
	// 'scheduled' status is NOT accepted here; scheduled rows are revoked via
	// CancelScheduledExecutionAtomic (Gmail-style pre-fire delay revoke
	// path, issue #291 wave-2) so the two flows surface distinct CAS race
	// outcomes. Returns (true, "canceled", nil) on success and (false,
	// currentStatus, nil) when zero rows were affected (the execution had
	// already been approved or otherwise transitioned). Must be called
	// inside a WithTx block so the suppression cleanup and the status flip
	// commit atomically.
	CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error)
	// CancelScheduledExecutionAtomic atomically flips status from 'scheduled' to
	// 'canceled' (canonical US spelling), setting canceled_by. Used by the
	// Gmail-style pre-fire delay revoke path (issue #291 wave-2) to cancel a
	// scheduled execution at $0 before the scheduler fires the SDK call. The
	// 'pending'/'notified' set accepted by CancelExecutionAtomic is
	// intentionally not extended here so the two revoke flows surface distinct
	// CAS race outcomes -- a scheduled row that the scheduler has already
	// transitioned to 'approved' / 'running' must surface as a 410 ("window
	// closed") rather than a 409 ("not pending"). Returns (true, "canceled",
	// nil) on success and (false, currentStatus, nil) when zero rows were
	// affected. Must be called inside a WithTx block.
	CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error)
	// ListStuckExecutions returns executions in any of the given statuses
	// whose updated_at is older than the given duration. Used by the
	// reaper sweep (issue #678) to find rows stuck in approved/running
	// after the synchronous executor failed mid-flight without flipping
	// them to a terminal state. Oldest-stuck-first (ORDER BY updated_at
	// ASC), capped at MaxListLimit per sweep.
	ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]PurchaseExecution, error)

	// GetScheduledExecutionsDue returns purchase_executions with
	// status='scheduled' whose scheduled_execution_at is in the past
	// (scheduled_execution_at <= NOW()). Used by the Gmail-style pre-fire
	// delay scheduler tick (issue #291 wave-2) to find rows ready to fire.
	// Oldest-due-first (ORDER BY scheduled_execution_at ASC), capped at
	// MaxListLimit per sweep.
	GetScheduledExecutionsDue(ctx context.Context) ([]PurchaseExecution, error)

	// Purchase history
	SavePurchaseHistory(ctx context.Context, record *PurchaseHistoryRecord) error
	GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]PurchaseHistoryRecord, error)
	GetAllPurchaseHistory(ctx context.Context, limit int) ([]PurchaseHistoryRecord, error)
	// GetActivePurchaseHistory returns every purchase_history row whose commitment
	// is still within its term at asOf (term > 0 AND timestamp + term years >= asOf;
	// the expiry boundary is inclusive, matching the API layer's isActiveCommitment),
	// newest-first, optionally scoped to a set of accounts. The account scope uses
	// the same dual-column predicate as GetPurchaseHistoryFiltered: accountIDs
	// match cloud_account_id (cloud_accounts UUIDs) and externalIDsByProvider
	// matches account_id per provider, OR'd together so rows carrying only one
	// identifier are still returned (issues #701/#498/#866). Both empty means all
	// accounts. Unlike GetAllPurchaseHistory it is not row-capped: the analytics
	// collector, dashboard KPIs, and inventory endpoints need the complete active
	// set, and filtering expired commitments in SQL keeps the result bounded by
	// the number of live commitments rather than by all history ever recorded (so
	// it cannot silently truncate older-but-still-active 1y/3y commitments the
	// way a newest-first capped page does, issue #1140).
	GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]PurchaseHistoryRecord, error)
	// GetPurchaseHistoryFiltered reads purchase_history rows matching the
	// PurchaseHistoryFilter, newest-first, capped at filter.Limit. Each field is
	// applied independently and only when populated (see PurchaseHistoryFilter).
	// The account predicate matches BOTH identifier columns:
	//   (cloud_account_id = ANY(AccountIDs)
	//      OR (provider = $p AND account_id = ANY(ExternalIDsByProvider[p])) OR ...)
	// because purchase_history rows carry the cloud_accounts UUID FK
	// (cloud_account_id, NULL on direct-execute/ambient/pre-000011 rows) and the
	// cloud-provider external number (account_id, always populated) independently.
	// The top-bar Account chip emits the UUID; matching only one column silently
	// dropped rows that carried only the other (issues #701/#498/#866). The caller
	// resolves AccountIDs to their external account numbers grouped by provider
	// and populates ExternalIDsByProvider so rows that carry only account_id are
	// also matched, while the per-provider grouping keeps a reused external number
	// across providers (aws/123 vs azure/123) from leaking the wrong rows.
	GetPurchaseHistoryFiltered(ctx context.Context, filter PurchaseHistoryFilter) ([]PurchaseHistoryRecord, error)
	// GetPurchaseHistoryByPurchaseID returns the single purchase_history row
	// whose purchase_id matches (AWS ReservedInstancesId / Azure reservation
	// ID). Returns (nil, nil) when no row is found. Used by the revoke
	// endpoint to load the record before calling the provider cancel API
	// (issue #290) and by the marketplace-list handler to validate
	// offering_class and look up the cloud account (issue #292).
	GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*PurchaseHistoryRecord, error)
	// MarkPurchaseRevoked stamps revoked_at, revoked_via, and optionally
	// support_case_id on a purchase_history row identified by purchase_id.
	// calcRefundAmount and calcRefundCurrency capture the Azure CalculateRefund
	// quote for audit (migration 000071, Finding #4); both nil/empty for
	// non-Azure paths or legacy rows written before the migration.
	// Returns a not-found error when no row matches. Idempotent: a second
	// call for the same row is a no-op (revoked_at is not overwritten when
	// it is already non-null). Used by the revoke endpoint (issue #290).
	MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia string, supportCaseID string, calcRefundAmount *float64, calcRefundCurrency string) error

	// FlipPurchaseRevocationInFlight atomically sets revocation_in_flight=true
	// on a purchase_history row. Called immediately before the Azure Return API
	// call so that the row can be identified by the finalize sweep if the
	// subsequent MarkPurchaseRevoked DB write fails (partial-success reconciliation,
	// issue #290 Finding #6, migration 000072). No-op when the flag is already
	// true (idempotent). Returns a not-found error when no row matches.
	FlipPurchaseRevocationInFlight(ctx context.Context, purchaseID string) error

	// ClearRevocationInFlight resets revocation_in_flight=false on a
	// purchase_history row. Called when the Azure Return call fails with a
	// transient or client error (not "already returned"), so the row is not
	// left in a permanently-sticky in-flight state that would prevent future
	// retries or mislead the finalize_revocations sweep (issue #290, second-wave
	// CR Finding D). No-op when the row is already false. Best-effort: callers
	// should log on error but not surface it to the user.
	ClearRevocationInFlight(ctx context.Context, purchaseID string) error

	// GetPurchaseHistoryInFlight returns all purchase_history rows with
	// revocation_in_flight=true and revoked_at IS NULL.  These are rows where
	// the Azure Return call succeeded but MarkPurchaseRevoked failed; the
	// finalize_revocations scheduled sweep calls this to retry the DB write.
	GetPurchaseHistoryInFlight(ctx context.Context) ([]*PurchaseHistoryRecord, error)

	// UpdatePurchaseHistoryListing stamps the AWS marketplace listing_id and
	// listing_state onto a purchase_history row. Called after
	// CreateReservedInstancesListing succeeds (listing_state="active") and
	// on subsequent poll/cancel transitions (issue #292).
	UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error

	// StampOfferingClass writes the offering_class value to a purchase_history
	// row identified by purchase_id. Called by the marketplace-list handler
	// when offering_class is absent in the DB (pre-migration 000087 rows and
	// externally-created Standard RIs): after fetching the class from AWS
	// DescribeReservedInstances it is persisted so subsequent requests do not
	// incur an extra AWS API call.
	StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error

	// ClaimMarketplaceListingSlot atomically reserves the marketplace-listing
	// slot for a purchase_history row so two concurrent marketplace-list
	// requests cannot both proceed to create a duplicate AWS listing (issue
	// #292). It transitions listing_state to ListingStatePending only when the
	// row is not already listed or mid-listing, and reports whether this call
	// won the claim: (true, nil) means the caller reserved the slot and must
	// then persist the real listing on success or release the slot back to its
	// prior state on failure; (false, nil) means another request already holds
	// an active or pending listing (the caller maps this to a 409). Modeled on
	// FlipPurchaseRevocationInFlight.
	ClaimMarketplaceListingSlot(ctx context.Context, purchaseID string) (bool, error)

	// RI Exchange history
	SaveRIExchangeRecord(ctx context.Context, record *RIExchangeRecord) error
	GetRIExchangeRecord(ctx context.Context, id string) (*RIExchangeRecord, error)
	GetRIExchangeRecordByToken(ctx context.Context, token string) (*RIExchangeRecord, error)
	GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]RIExchangeRecord, error)
	// TransitionRIExchangeStatus atomically transitions an RI exchange record status.
	// actor is the UUID of the user performing the transition (nil for system-initiated paths).
	TransitionRIExchangeStatus(ctx context.Context, id string, fromStatus string, toStatus string, actor *string) (*RIExchangeRecord, error)
	CompleteRIExchange(ctx context.Context, id string, exchangeID string) error
	// CompleteRIExchangeWithPayment marks an RI exchange as completed and
	// updates payment_due to the amount AWS actually accepted. Use this instead
	// of CompleteRIExchange on the manual-approval path so the daily-spend
	// ledger (GetRIExchangeDailySpend) reflects the real accepted amount rather
	// than the stale pre-execution quote (H3 fix).
	CompleteRIExchangeWithPayment(ctx context.Context, id string, exchangeID string, acceptedPaymentDue string) error
	// StampRIExchangeApprovedBy sets the approved_by column on a completed
	// exchange row (issue #300). Called after CompleteRIExchangeWithPayment when
	// the approval came from a session-authed user rather than an email token.
	StampRIExchangeApprovedBy(ctx context.Context, id string, approverEmail string) error
	FailRIExchange(ctx context.Context, id string, errorMsg string) error
	GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error)
	CancelAllPendingExchanges(ctx context.Context) (int64, error)
	// CancelPendingExchangesByOrigin cancels only pending records whose origin
	// matches:
	//   - common.ExchangeOriginStandalone: cancels WHERE ladder_run_id IS NULL
	//   - common.ExchangeOriginLadder:     cancels WHERE ladder_run_id IS NOT NULL
	// The origin is validated at the boundary and an unknown value fails loud.
	// This prevents the standalone ri_exchange_reshape task from wiping out
	// ladder-linked pending reshapes and vice versa (gap G10 / issue #1348).
	CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error)
	GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]RIExchangeRecord, error)

	// Cloud accounts
	CreateCloudAccount(ctx context.Context, account *CloudAccount) error
	GetCloudAccount(ctx context.Context, id string) (*CloudAccount, error)
	// GetCloudAccountByExternalID looks up a cloud account by its
	// (provider, external_id) pair. Used by the scheduler's ambient
	// collector path to tag rec rows with the registered host-account's
	// UUID when the Lambda's STS identity matches a registered (but
	// possibly disabled) account — so the approve-modal Account column
	// shows the account name instead of `(ambient)`. Returns
	// (nil, nil) when no row matches (the caller treats this as the
	// genuine orphan case). The underlying
	// `UNIQUE(provider, external_id)` constraint on cloud_accounts
	// guarantees the lookup hits an index.
	GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*CloudAccount, error)
	UpdateCloudAccount(ctx context.Context, account *CloudAccount) error
	DeleteCloudAccount(ctx context.Context, id string) error
	ListCloudAccounts(ctx context.Context, filter CloudAccountFilter) ([]CloudAccount, error)

	// Account credentials (encrypted blobs; never returned via API)
	SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error
	GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error)
	DeleteAccountCredentials(ctx context.Context, accountID string) error
	HasAccountCredentials(ctx context.Context, accountID string) (bool, error)

	// Account service overrides
	GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*AccountServiceOverride, error)
	SaveAccountServiceOverride(ctx context.Context, override *AccountServiceOverride) error
	DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error
	ListAccountServiceOverrides(ctx context.Context, accountID string) ([]AccountServiceOverride, error)

	// Plan ↔ account association
	SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error
	GetPlanAccounts(ctx context.Context, planID string) ([]CloudAccount, error)

	// Recommendations cache (ADR: store recommendations in Postgres so the
	// dashboard serves provider-switch clicks from SQL instead of live cloud
	// API calls). ReplaceRecommendations is the "force full resync" path;
	// UpsertRecommendations is the steady-state write path and takes a list
	// of (provider, account) pairs that successfully collected this cycle.
	// Stale-row eviction is scoped to that union so a partially-failed
	// provider preserves the failed accounts' previous-cycle rows. See
	// SuccessfulCollect for the per-row semantics.
	ReplaceRecommendations(ctx context.Context, collectedAt time.Time, recs []RecommendationRecord) error
	UpsertRecommendations(ctx context.Context, collectedAt time.Time, recs []RecommendationRecord, successfulCollects []SuccessfulCollect) error
	ListStoredRecommendations(ctx context.Context, filter RecommendationFilter) ([]RecommendationRecord, error)
	GetRecommendationsFreshness(ctx context.Context) (*RecommendationsFreshness, error)
	SetRecommendationsCollectionError(ctx context.Context, errMsg string) error
	// MarkCollectionStarted atomically sets last_collection_started_at = now
	// only when no in-flight collection is running (last_collection_started_at IS NULL
	// OR older than 5 minutes), and stamps a freshly generated owner token
	// alongside it. Returns the token and true when this caller won the race
	// and should proceed with the async invoke, passing the token through so
	// only this caller can later clear the marker; ("", false, nil) when
	// another collection is already in flight and the caller should return
	// 409.
	MarkCollectionStarted(ctx context.Context) (token string, ok bool, err error)
	// ClearCollectionStarted clears last_collection_started_at, but only if
	// last_collection_owner_id still matches token: a compare-and-clear
	// guard (issue #261) so a caller that never won MarkCollectionStarted
	// (cron, cold-start) cannot wipe another caller's in-flight marker. A
	// mismatched token is a documented silent no-op (the marker belongs to
	// someone else); an empty token is a boundary error, since only a
	// caller that actually owns a marker should ever call Clear.
	ClearCollectionStarted(ctx context.Context, token string) error

	// RI utilization cache. Postgres-backed TTL cache for Cost Explorer
	// GetReservationUtilization; shared across Lambda containers so
	// dashboard loads don't each fan out to a paid CE API call. A
	// per-process in-memory cache effectively never hits because each
	// cold container starts empty. GetRIUtilizationCache returns nil
	// when the (region, lookback_days) key is absent — callers treat
	// that as a miss and re-fetch.
	GetRIUtilizationCache(ctx context.Context, region string, lookbackDays int) (*RIUtilizationCacheEntry, error)
	UpsertRIUtilizationCache(ctx context.Context, region string, lookbackDays int, payload []byte, fetchedAt time.Time) error

	// Account registrations (self-service enrollment via federation IaC)
	CreateAccountRegistration(ctx context.Context, reg *AccountRegistration) error
	GetAccountRegistration(ctx context.Context, id string) (*AccountRegistration, error)
	GetAccountRegistrationByToken(ctx context.Context, token string) (*AccountRegistration, error)
	ListAccountRegistrations(ctx context.Context, filter AccountRegistrationFilter) ([]AccountRegistration, error)
	UpdateAccountRegistration(ctx context.Context, reg *AccountRegistration) error
	// TransitionRegistrationStatus atomically updates a registration's workflow fields.
	// actor is the UUID of the reviewer (nil for system-initiated transitions).
	TransitionRegistrationStatus(ctx context.Context, reg *AccountRegistration, fromStatus string, actor *string) error
	DeleteAccountRegistration(ctx context.Context, id string) error

	// Purchase suppressions. Written inside a WithTx block during bulk
	// purchase submit so the execution insert + the suppression rows
	// commit atomically. Deleted on cancel/expire of the execution,
	// also inside a WithTx block paired with the status update.
	//
	// The plain variants (no Tx) open their own single-call transaction
	// — useful for tests and one-off admin operations. The Tx variants
	// reuse a caller-provided transaction so multi-write operations
	// can roll back atomically.
	CreateSuppression(ctx context.Context, sup *PurchaseSuppression) error
	CreateSuppressionTx(ctx context.Context, tx pgx.Tx, sup *PurchaseSuppression) error
	DeleteSuppressionsByExecution(ctx context.Context, executionID string) error
	DeleteSuppressionsByExecutionTx(ctx context.Context, tx pgx.Tx, executionID string) error
	ListActiveSuppressions(ctx context.Context) ([]PurchaseSuppression, error)

	// SavePurchaseExecutionTx is the tx-accepting variant of
	// SavePurchaseExecution. Used from executePurchase's WithTx block
	// so the execution insert + suppression writes commit atomically.
	SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *PurchaseExecution) error

	// GetPendingExecutionsTx is the tx-accepting variant of
	// GetPendingExecutions. Used inside the executePurchase WithTx block
	// so the duplicate-detection read and the new-execution insert are
	// atomic under the same transaction, eliminating the TOCTOU race (#643).
	GetPendingExecutionsTx(ctx context.Context, tx pgx.Tx) ([]PurchaseExecution, error)

	// WithTx opens a pgx transaction, runs fn, and commits on success or
	// rolls back on error. fn can call any *Tx method on the store to
	// participate in the transaction. Nested transactions are not
	// supported — fn must not call WithTx recursively.
	WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	// Ladder configuration (per-account, per-provider).
	// GetLadderConfigs returns all rows, newest first.
	// GetLadderConfig returns the single row for (cloudAccountID, provider),
	// or (nil, nil) when no row exists.
	// UpsertLadderConfig inserts or updates via the UNIQUE(cloud_account_id, provider)
	// constraint and returns the persisted row with all DB-stamped fields populated.
	GetLadderConfigs(ctx context.Context) ([]LadderConfigDB, error)
	GetLadderConfig(ctx context.Context, cloudAccountID, provider string) (*LadderConfigDB, error)
	UpsertLadderConfig(ctx context.Context, cfg *LadderConfigDB) (*LadderConfigDB, error)

	// Ladder run/tranche persistence (migration 000080/000081, PR-2).
	//
	// SaveLadderRun inserts a new ladder_runs row, returning the persisted row
	// with all DB-stamped fields (id, created_at, updated_at) populated.
	// If run.ID is empty, a new UUID is generated before the insert.
	//
	// GetLadderRun returns the row for the given id, or (nil, nil) when no
	// row exists (mirrors GetLadderConfig semantics).
	//
	// SaveLadderTranches inserts a batch of ladder_tranches rows inside a
	// single transaction. Each tranche must carry a non-empty ID; duplicate
	// IDs within the batch are rejected at the DB UNIQUE constraint level.
	//
	// LatestLadderRunStartedAt returns the maximum started_at for the given
	// config_id, or nil when no run has been recorded yet. Powers the per-cadence
	// self-gate in the scheduler (Q6).
	//
	// TransitionLadderRunStatus atomically updates the status of a ladder_runs
	// row from one of the fromStatuses to toStatus, returning the updated row.
	// Returns (nil, nil) when zero rows are affected (CAS race lost or wrong
	// current status), so callers can distinguish a race from a hard error.
	// Statuses are typed ladder.RunStatus so callers cannot pass an arbitrary
	// string that would never match a stored status.
	// SaveLadderRunWithTranches inserts the run row and its tranches in ONE
	// transaction: a tranche-insert failure rolls back the run row too, so a
	// status=planned run never persists without its tranches (which would let
	// the cadence gate suppress the retry for a full window).
	//
	// L5 append-only: every ladder run persists its new scheduled tranches via
	// this method and leaves any existing scheduled tranches untouched. In-flight
	// netting (GetInFlightLadderCommitUSDHr) already subtracts existing
	// scheduled tranches from the gap, so each run appends exactly the delta
	// needed to reach target-E. Appending (never superseding) keeps prior
	// tranches' original fire dates, converges to target on drift-up (run N
	// tops up the gap the netting leaves), and cannot oscillate.
	//
	// GetInFlightLadderCommitUSDHr returns the total hourly USD commitment in
	// flight for the given config: the sum of amount_usd_hr for tranches with
	// status = 'scheduled' ONLY. Fired/completed tranches are executed
	// purchases already reflected in the engine's ExistingUSDPerHour (the
	// provider adapters fold payment-pending and active commitments into E),
	// so summing them here too would double-count and under-purchase. Returns
	// a non-nil pointer (zero when no scheduled tranches exist) so callers can
	// pass it directly to AllocationInput.InFlightUSDPerHour. Never returns
	// nil without an error.
	//
	// ExpireElapsedLadderTranches moves the config's scheduled tranches whose
	// scheduled_date is strictly before cutoff to status='cancelled' and returns
	// how many rows moved. A tranche only leaves 'scheduled' by firing, so one
	// still scheduled well past its own fire date never fired: without this
	// transition it stays in the in-flight sum forever, the netting keeps
	// zeroing the gap, and the ladder silently stops planning (#1605). Callers
	// must run it before GetInFlightLadderCommitUSDHr and must pick a cutoff
	// that leaves a just-due tranche time to fire, so expiry can never race a
	// firing tranche into a duplicate purchase.
	SaveLadderRun(ctx context.Context, run *LadderRunDB) (*LadderRunDB, error)
	SaveLadderRunWithTranches(ctx context.Context, run *LadderRunDB, tranches []LadderTrancheDB) (*LadderRunDB, error)
	GetInFlightLadderCommitUSDHr(ctx context.Context, configID string) (*float64, error)
	ExpireElapsedLadderTranches(ctx context.Context, configID string, cutoff time.Time) (int64, error)
	GetLadderRun(ctx context.Context, id string) (*LadderRunDB, error)
	SaveLadderTranches(ctx context.Context, tranches []LadderTrancheDB) error
	LatestLadderRunStartedAt(ctx context.Context, configID string) (*time.Time, error)
	TransitionLadderRunStatus(ctx context.Context, id string, fromStatuses []ladder.RunStatus, toStatus ladder.RunStatus) (*LadderRunDB, error)

	// Notification mutes (issue #297 / migration 000091).
	// UpsertNotificationMute inserts or updates a mute row for (email, scope).
	// Idempotent for row existence: calling it again for an already-muted
	// address refreshes muted_at and replaces unmute_token if the token changes.
	UpsertNotificationMute(ctx context.Context, recipientEmail, scope, unmuteToken string) error
	// IsNotificationMuted returns true when (email, scope) has a row in
	// muted_recipients. The email comparison is case-insensitive.
	IsNotificationMuted(ctx context.Context, recipientEmail, scope string) (bool, error)
}
