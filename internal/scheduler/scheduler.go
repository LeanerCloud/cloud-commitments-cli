// Package scheduler handles scheduled recommendation collection.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/credentials"
	"github.com/LeanerCloud/CUDly/internal/email"
	"github.com/LeanerCloud/CUDly/internal/execution"
	"github.com/LeanerCloud/CUDly/internal/oidc"
	"github.com/LeanerCloud/CUDly/internal/purchase"
	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/concurrency"
	"github.com/LeanerCloud/CUDly/pkg/logging"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	azureprovider "github.com/LeanerCloud/CUDly/providers/azure"
	gcpprovider "github.com/LeanerCloud/CUDly/providers/gcp"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// STSClient is the minimal AWS STS surface the scheduler uses to discover
// the Lambda's own AWS account ID for the ambient-path account-tagging fix
// (issue #604). Mirrors purchase.STSClient deliberately — both packages
// need the same single call, and cross-package imports would tangle the
// purchase ↔ scheduler dependency.
type STSClient interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// SchedulerConfig holds configuration for the scheduler.
type SchedulerConfig struct { //nolint:revive // exported: doc comment style intentional
	ConfigStore     config.StoreInterface
	PurchaseManager ManagerInterface
	EmailSender     email.SenderInterface
	ProviderFactory provider.FactoryInterface
	CredentialStore credentials.CredentialStore
	OIDCSigner      oidc.Signer
	AssumeRoleSTS   credentials.STSClient
	STSClient       STSClient
	DashboardURL    string
	OIDCIssuerURL   string
	IsLambda        bool
}

// CollectResult holds the result of collecting recommendations.
//
// SuccessfulProviders + FailedProviders track per-provider outcomes so the
// persistence layer (UpsertRecommendations) can scope the delete-stale
// clause to providers that actually ran, and the frontend banner can
// surface the specific failures.
type CollectResult struct {
	FailedProviders     map[string]string `json:"failed_providers,omitempty"`
	SuccessfulProviders []string          `json:"successful_providers,omitempty"`
	Recommendations     int               `json:"recommendations"`
	TotalSavings        float64           `json:"total_savings"`
}

// ManagerInterface defines the purchase manager methods used by scheduler.
type ManagerInterface interface {
	ProcessScheduledPurchases(ctx context.Context) (*purchase.ProcessResult, error)
	SendUpcomingPurchaseNotifications(ctx context.Context) (*purchase.NotificationResult, error)
	// FireScheduledDelayedPurchases fires purchase_executions in status=scheduled
	// whose scheduled_execution_at is in the past (Gmail-style pre-fire delay,
	// issue #291 wave-2). Called on the "fire_scheduled_purchases" scheduler tick.
	FireScheduledDelayedPurchases(ctx context.Context) (*purchase.FireResult, error)
}

// Scheduler handles scheduled tasks.
type Scheduler struct {
	stsClient       STSClient
	purchase        ManagerInterface
	email           email.SenderInterface
	providerFactory provider.FactoryInterface
	credStore       credentials.CredentialStore
	oidcSigner      oidc.Signer
	assumeRoleSTS   credentials.STSClient
	config          config.StoreInterface
	dashboardURL    string
	oidcIssuerURL   string
	cacheTTL        time.Duration
	collecting      atomic.Bool
	isLambda        bool
}

// defaultCacheTTL is the fallback when CUDLY_RECOMMENDATION_CACHE_TTL is
// unset or invalid. 6h is shorter than the default 1-day cron so
// opportunistic refresh closes the gap when users are active.
const defaultCacheTTL = 6 * time.Hour

// NewScheduler creates a new scheduler.
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	factory := cfg.ProviderFactory
	if factory == nil {
		factory = &provider.DefaultFactory{}
	}

	return &Scheduler{
		config:          cfg.ConfigStore,
		purchase:        cfg.PurchaseManager,
		email:           cfg.EmailSender,
		dashboardURL:    cfg.DashboardURL,
		providerFactory: factory,
		credStore:       cfg.CredentialStore,
		oidcSigner:      cfg.OIDCSigner,
		oidcIssuerURL:   cfg.OIDCIssuerURL,
		assumeRoleSTS:   cfg.AssumeRoleSTS,
		stsClient:       cfg.STSClient,
		isLambda:        cfg.IsLambda,
		cacheTTL:        cacheTTLFromEnv(),
	}
}

// cacheTTLFromEnv parses CUDLY_RECOMMENDATION_CACHE_TTL using
// time.ParseDuration. Falls back to defaultCacheTTL on parse error (with
// a warning) so a mis-set env doesn't take the feature offline.
func cacheTTLFromEnv() time.Duration {
	v := os.Getenv("CUDLY_RECOMMENDATION_CACHE_TTL")
	if v == "" {
		return defaultCacheTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logging.Warnf("CUDLY_RECOMMENDATION_CACHE_TTL invalid (%q); using default %s", v, defaultCacheTTL)
		return defaultCacheTTL
	}
	return d
}

// CollectRecommendations fetches recommendations from all configured cloud providers.
// Persists results to the recommendations cache so read handlers can serve
// from SQL instead of re-fetching live.
//
// Bookkeeping: clears last_collection_started_at on exit (success or
// failure) so the frontend polling loop can detect completion, but only if
// ownerToken is non-empty and still matches last_collection_owner_id
// (issue #261 compare-and-clear guard). The scheduler is invoked either by
// the cron EventBridge rule or by an async self-invoke from the POST
// /api/recommendations/refresh handler. In the async case,
// MarkCollectionStarted has already set last_collection_started_at and
// returned the token that ownerToken carries here; the cron case (and
// cold-start) call with an empty ownerToken since they never won a marker,
// and the clear is skipped entirely rather than clearing unconditionally,
// which previously let a cron run wipe a concurrent user-triggered run's
// marker. This deferred clear is the ONLY place that touches started_at:
// persistCollection's SetRecommendationsCollectionError call (below, on a
// provider failure) intentionally leaves started_at alone, so a tokenless
// run hitting a routine provider error cannot wipe another run's marker
// either.
func (s *Scheduler) CollectRecommendations(ctx context.Context, ownerToken string) (*CollectResult, error) {
	logging.Info("Collecting recommendations from cloud providers...")

	// Clear last_collection_started_at on exit so the frontend knows the
	// collection has finished. Use a fresh background context with a short
	// timeout: the request ctx may already be canceled by the time the
	// defer runs (e.g. caller deadline expired during a slow collect),
	// which would cause ClearCollectionStarted to fail and leave the
	// "in flight" marker until the 5-min auto-recovery window kicks in.
	defer func() {
		clearCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.clearCollectionStartedBestEffort(clearCtx, ownerToken)
	}()

	// Get global config
	globalCfg, err := s.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, err
	}

	// Attach a shared semaphore to ctx so every leaf goroutine in the
	// recommendations-collection fan-out tree (AWS service, Azure service,
	// GCP region×service) acquires one slot before issuing its cloud-API
	// call and releases it after. This bounds aggregate concurrent IO across
	// the whole tree at CUDLY_MAX_PARALLELISM (default 20) regardless of how
	// nested the dispatch is — without it, peak concurrency multiplies
	// through the nested fan-outs and can exhaust Lambda memory before work
	// completes (observed with a 512 MB function in dev). Intermediate
	// dispatchers (provider, account, GCP region) do NOT acquire — they only
	// launch sub-goroutines — so no goroutine can deadlock by holding a
	// permit while waiting for sub-permits.
	maxParallelism := concurrency.MaxParallelismFromEnv()
	sem := semaphore.NewWeighted(int64(maxParallelism))
	ctx = concurrency.WithSharedSemaphore(ctx, sem)
	logging.Infof("Recommendations collection: aggregate parallelism cap = %d", maxParallelism)

	// Collect recommendations from each enabled provider concurrently, tracking
	// per-provider outcomes so persistence can scope stale-row eviction to
	// (provider, account) pairs that actually ran. A partial collection (e.g.
	// one of three Azure subscriptions failed) preserves the failed pairs'
	// previous-cycle rows instead of wiping the whole provider's slice.
	//
	// Provider-level fan-out under errgroup. Each goroutine returns nil to the
	// group so a single provider's failure does not cancel siblings — matches
	// the previous loop's `continue`-on-error behavior. Per-provider results
	// are written into a map under a single mutex; the merge then walks
	// EnabledProviders in config order so successfulProviders ordering is
	// deterministic regardless of goroutine completion order. After Wait, ctx
	// cancellation is propagated. No concurrency cap — the universe is at
	// most 3 providers.
	allRecommendations, totalSavings, successfulProviders, successfulCollects, failedProviders, err := s.collectAllProviders(ctx, globalCfg)
	if err != nil {
		return nil, err
	}

	logging.Infof("Collected %d recommendations with $%.2f/month potential savings",
		len(allRecommendations), totalSavings)

	s.persistCollection(ctx, allRecommendations, successfulCollects, failedProviders)

	// Send notification if we have recommendations
	if len(allRecommendations) > 0 && totalSavings > 0 {
		// Sort by savings descending to show top recommendations in email
		sort.Slice(allRecommendations, func(i, j int) bool {
			return allRecommendations[i].Savings > allRecommendations[j].Savings
		})

		data := email.NotificationData{
			DashboardURL: s.dashboardURL,
			TotalSavings: totalSavings,
		}
		for _rvc := range allRecommendations {
			rec := allRecommendations[_rvc]
			if len(data.Recommendations) >= 10 { // Limit to top 10 in email
				break
			}
			data.Recommendations = append(data.Recommendations, email.RecommendationSummary{
				Service:        rec.Service,
				ResourceType:   rec.ResourceType,
				Engine:         rec.Engine,
				Region:         rec.Region,
				Count:          rec.Count,
				MonthlySavings: rec.Savings,
			})
		}

		if err := s.email.SendNewRecommendationsNotification(ctx, data); err != nil {
			logging.Errorf("Failed to send notification: %v", err)
		}
	}

	return &CollectResult{
		Recommendations:     len(allRecommendations),
		TotalSavings:        totalSavings,
		SuccessfulProviders: successfulProviders,
		FailedProviders:     failedProviders,
	}, nil
}

// persistCollection writes the collection results to the recommendations
// cache via UpsertRecommendations, which:
//
//   - Upserts each row by natural key so re-collected recommendations
//     reuse the same PK across runs (stable IDs for frontend).
//   - Evicts stale rows (collected_at < $now) but ONLY for the
//     (provider, account) pairs that successfully collected in this run.
//     Failed pairs' rows stay visible so the dashboard isn't blanked out
//     by transient cloud-API failures, even when one of several accounts
//     under the same provider succeeded.
//
// On any failure (including partial), the collection error is additionally
// recorded in recommendations_state so the frontend banner renders while
// the user still sees the valid rows we managed to upsert.
// providerOutcome bundles a single provider's collection outcome for the
// deterministic merge in collectAllProviders. Only one of recs/err is
// meaningful per outcome — err != nil means the provider failed entirely
// (mirrors the pre-fan-out `continue` branch); err == nil means the provider
// succeeded (possibly partially — partial-account-failure semantics live in
// fanOutPerAccount, not here).
type providerOutcome struct {
	err                 error
	recs                []config.RecommendationRecord
	succeededAccountIDs []string
}

// collectAllProviders fans out provider collection (AWS / Azure / GCP) under
// errgroup. Each goroutine returns nil so a single provider's error does not
// cancel siblings; per-provider results are written into a map under a single
// mutex. After Wait, ctx cancellation is propagated. The merge then walks
// EnabledProviders in config order so successfulProviders / successfulCollects
// / allRecommendations ordering is deterministic regardless of goroutine
// completion order — keeps existing tests stable. No concurrency cap; the
// universe is at most 3 providers.
//
// Extracted from CollectRecommendations to keep that function under the
// project's gocyclo gate (.golangci.yml min-complexity: 15) after the
// errgroup + post-Wait ctx.Err() block was added.
func (s *Scheduler) collectAllProviders(ctx context.Context, globalCfg *config.GlobalConfig) ( //nolint:gocritic // tooManyResultsChecker: multiple returns intentional
	allRecommendations []config.RecommendationRecord,
	totalSavings float64,
	successfulProviders []string,
	successfulCollects []config.SuccessfulCollect,
	failedProviders map[string]string,
	err error,
) {
	failedProviders = map[string]string{}

	var (
		mu       sync.Mutex
		outcomes = make(map[string]providerOutcome, len(globalCfg.EnabledProviders))
	)

	g, gctx := errgroup.WithContext(ctx)

	for _, providerName := range globalCfg.EnabledProviders {
		providerName := providerName // capture per-iteration
		g.Go(func() error {
			logging.Infof("Collecting recommendations from %s...", providerName)
			recs, succeededAccountIDs, perr := s.collectProviderRecommendations(gctx, providerName, globalCfg)
			mu.Lock()
			outcomes[providerName] = providerOutcome{
				recs:                recs,
				succeededAccountIDs: succeededAccountIDs,
				err:                 perr,
			}
			mu.Unlock()
			return nil // error isolation: per-provider failures don't cancel siblings
		})
	}

	// Wait for all goroutines. g.Wait() always returns nil because every
	// goroutine returns nil. After Wait, propagate ctx cancellation so
	// callers can distinguish "all providers completed" from "the parent
	// ctx was canceled mid-fan-out".
	if waitErr := g.Wait(); waitErr != nil {
		// Goroutines return nil to isolate per-provider failures; non-nil is unexpected.
		logging.Warnf("collectAllProviders: errgroup.Wait returned unexpected error: %v", waitErr)
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, 0, nil, nil, nil, cerr
	}

	// Deterministic merge: walk EnabledProviders in config order so
	// successfulProviders ordering is independent of goroutine completion
	// order — keeps existing tests stable.
	for _, providerName := range globalCfg.EnabledProviders {
		out, ok := outcomes[providerName]
		if !ok {
			continue
		}
		if out.err != nil {
			logging.Errorf("Failed to collect %s recommendations: %v", providerName, out.err)
			failedProviders[providerName] = out.err.Error()
			continue
		}
		successfulProviders = append(successfulProviders, providerName)
		successfulCollects = append(successfulCollects, expandSuccessfulCollects(providerName, out.succeededAccountIDs)...)
		for _rvc := range out.recs {
			rec := out.recs[_rvc]
			totalSavings += rec.Savings
		}
		allRecommendations = append(allRecommendations, out.recs...)
	}
	return allRecommendations, totalSavings, successfulProviders, successfulCollects, failedProviders, nil
}

// clearCollectionStartedBestEffort clears last_collection_started_at on the
// scheduler's exit path. Best-effort — a failure here is logged but does not
// prevent returning the collection result. Extracted so CollectRecommendations
// stays under the cyclomatic-complexity gate.
//
// A caller that never won MarkCollectionStarted (cron, cold-start) passes
// an empty ownerToken and by design owns no marker to clear; this is an
// explicit, logged skip, not a silent fallback.
func (s *Scheduler) clearCollectionStartedBestEffort(ctx context.Context, ownerToken string) {
	if ownerToken == "" {
		logging.Debugf("skipping collection-started clear: caller holds no owner token (cron/cold-start run)")
		return
	}
	if err := s.config.ClearCollectionStarted(ctx, ownerToken); err != nil {
		logging.Errorf("failed to clear collection started: %v", err)
	}
}

func (s *Scheduler) persistCollection(ctx context.Context, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect, failedProviders map[string]string) {
	if err := s.config.UpsertRecommendations(ctx, time.Now().UTC(), recs, successfulCollects); err != nil {
		logging.Errorf("Failed to persist recommendations: %v", err)
		if setErr := s.config.SetRecommendationsCollectionError(ctx, err.Error()); setErr != nil {
			logging.Errorf("Failed to record write error: %v", setErr)
		}
		return
	}

	if len(failedProviders) > 0 {
		if err := s.config.SetRecommendationsCollectionError(ctx, joinProviderErrors(failedProviders)); err != nil {
			logging.Errorf("Failed to record collection error: %v", err)
		}
	}
}

// joinProviderErrors formats a "provider: err; provider: err" string for
// the state table's last_collection_error column.
func joinProviderErrors(failed map[string]string) string {
	parts := make([]string, 0, len(failed))
	for p, msg := range failed {
		parts = append(parts, fmt.Sprintf("%s: %s", p, msg))
	}
	sort.Strings(parts) // deterministic ordering for test stability
	return strings.Join(parts, "; ")
}

// collectProviderRecommendations collects recommendations from a specific
// provider. The succeededAccountIDs return value is the per-account roster
// that completed in this run; the scheduler threads it into
// []config.SuccessfulCollect for account-scoped eviction. For the AWS
// ambient path (no registered accounts) the slice contains a single empty
// string sentinel that expandSuccessfulCollects translates into a nil
// CloudAccountID. Provider-level errors propagate unchanged.
func (s *Scheduler) collectProviderRecommendations(ctx context.Context, providerName string, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, []string, error) {
	switch providerName {
	case "aws":
		return s.collectAWSRecommendations(ctx, globalCfg)
	case "azure":
		return s.collectAzureRecommendations(ctx, globalCfg)
	case "gcp":
		return s.collectGCPRecommendations(ctx, globalCfg)
	default:
		logging.Warnf("Unknown provider: %s", providerName)
		return nil, nil, nil
	}
}

// ambientEvictionRoster builds the succeeded-account roster for an ambient
// (unregistered-credential) collection. An incomplete sweep yields an empty
// roster, so UpsertRecommendations evicts nothing for it and the
// subscriptions the sweep never queried keep their previous rows -- see
// tolerateIncompleteSweep and issue #1652.
//
// accountID is "" when the host identity did not resolve to a registered
// cloud_accounts row; that empty-string sentinel is what
// expandSuccessfulCollects maps to a nil CloudAccountID.
func ambientEvictionRoster(complete bool, accountID string) []string {
	if !complete {
		return nil
	}
	return []string{accountID}
}

// expandSuccessfulCollects converts the per-account ID slice returned by
// each provider collect into the typed []SuccessfulCollect the persist
// layer expects. The empty-string sentinel is translated into a nil
// CloudAccountID so the eviction's account_key join uses uuid.Nil
// (matching the AWS ambient-credential path).
func expandSuccessfulCollects(providerName string, accountIDs []string) []config.SuccessfulCollect {
	out := make([]config.SuccessfulCollect, 0, len(accountIDs))
	for _, id := range accountIDs {
		sc := config.SuccessfulCollect{Provider: providerName}
		if id != "" {
			id := id // pin
			sc.CloudAccountID = &id
		}
		out = append(out, sc)
	}
	return out
}

// collectAWSRecommendations fans out across all enabled AWS accounts.
// If no accounts are registered and CUDly runs on AWS, it falls back to
// ambient credentials (backward compatibility with single-account setups).
// Returns the merged recommendations + the IDs of accounts that succeeded
// (or [""] for the ambient path so the caller can synthesize a nil
// CloudAccountID for eviction).
func (s *Scheduler) collectAWSRecommendations(ctx context.Context, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, []string, error) {
	accounts := s.enabledAccounts(ctx, "aws")

	// Backward-compatible fallback: no registered accounts → ambient credentials
	if len(accounts) == 0 {
		recs, complete, err := s.collectAWSAmbient(ctx, globalCfg)
		if err != nil {
			return nil, nil, err
		}
		// Issue #604: if the Lambda's STS identity matches a registered
		// (possibly disabled) cloud_accounts row, tag every rec with that
		// account's UUID so the approve modal renders the account name
		// instead of `(ambient)`. Best-effort — STS or store errors must
		// NOT fail the collection; we just keep the pre-fix nil tagging
		// in those cases.
		if acctID := s.resolveAmbientHostAccountID(ctx); acctID != "" {
			recs = s.tagAccount(recs, acctID)
			return recs, ambientEvictionRoster(complete, acctID), nil
		}
		return recs, ambientEvictionRoster(complete, ""), nil
	}

	recs, outcome := fanOutPerAccount(ctx, "AWS", accounts, func(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, bool, error) {
		return s.collectAWSForAccount(ctx, globalCfg, acct)
	})
	if outcome.FailedCount == len(accounts) && len(accounts) > 0 {
		return nil, nil, errAllAccountsFailed("AWS", outcome)
	}
	return recs, outcome.SucceededAccountIDs, nil
}

// accountOutcome is the per-provider summary returned by
// fanOutPerAccount alongside the merged recommendations slice. The
// caller uses this to decide whether the provider's collection
// succeeded overall: if every registered account failed
// (FailedCount == len(accounts) > 0) the caller should surface an
// error so the provider lands in failedProviders + the freshness
// banner shows the failure. Partial success (some accounts succeeded)
// stays a success at the provider level — the per-account [ERROR]
// logs are the operator signal for the failed minority.
//
// SucceededAccountIDs lists the IDs of accounts whose collection
// completed IN FULL; the scheduler threads these into the
// []SuccessfulCollect slice that drives UpsertRecommendations'
// account-scoped eviction. Order is not preserved — the eviction
// query treats it as a set.
//
// IncompleteAccountIDs lists accounts that returned usable rows from a
// sweep that did not cover every subscription it was asked to. They are
// counted in SucceededCount (their rows are kept and upserted, and they
// must not trip the all-accounts-failed guard) but are deliberately
// absent from SucceededAccountIDs, because eviction is keyed on the
// account while a sweep fails per subscription; evicting on a partial
// sweep deletes the un-queried subscriptions' previous rows. See
// tolerateIncompleteSweep and issue #1652.
type accountOutcome struct {
	LastErr              string
	SucceededAccountIDs  []string
	IncompleteAccountIDs []string
	SucceededCount       int
	FailedCount          int
}

// fanOutPerAccount runs fn concurrently across accounts, bounded by the
// shared CUDLY_MAX_ACCOUNT_PARALLELISM env var. Per-account errors are
// logged and the run continues — the stale-row preservation in the
// UpsertRecommendations write path (see commit "parallelise collection")
// keeps those accounts' older rows visible. Order is not preserved; the
// SQL read in ListStoredRecommendations re-sorts anyway.
//
// Returns the merged recommendations + an accountOutcome the caller
// uses to detect "all accounts failed" → return error → provider
// flagged as failed in CollectRecommendations.
func fanOutPerAccount(
	ctx context.Context,
	providerLabel string,
	accounts []config.CloudAccount,
	fn func(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, bool, error),
) ([]config.RecommendationRecord, accountOutcome) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(execution.ConcurrencyFromEnv())

	// One mutex guards both the recs accumulator AND the outcome
	// counters — the same critical section, no second mutex needed.
	var mu sync.Mutex
	var all []config.RecommendationRecord
	var outcome accountOutcome

	for _rvc := range accounts {
		acct := accounts[_rvc]
		g.Go(func() error {
			recs, complete, err := fn(gctx, acct)
			if err != nil {
				if isAccountPermissionError(providerLabel, err) {
					// Operator-fixable misconfiguration (missing IAM role
					// on the deploy SA): log at WARN so a single
					// misconfigured account doesn't drown out other log
					// signals. The collection still counts as a per-account
					// failure — provider-success semantics are unchanged.
					logging.Warnf("%s account %s (%s) permission gap (operator action needed: grant required IAM role): %v",
						providerLabel, acct.Name, acct.ExternalID, err)
				} else {
					logging.Errorf("%s account %s (%s): %v", providerLabel, acct.Name, acct.ExternalID, err)
				}
				mu.Lock()
				outcome.FailedCount++
				outcome.LastErr = fmt.Sprintf("account %s (%s): %v", acct.Name, acct.ExternalID, err)
				mu.Unlock()
				return nil // never fail the whole group — partial is fine
			}
			mu.Lock()
			all = append(all, recs...)
			// A partially-swept account counts as succeeded -- its rows are
			// kept and upserted, and it must not trip the all-accounts-failed
			// guard -- but it is deliberately withheld from
			// SucceededAccountIDs, the roster that authorizes stale-row
			// eviction. See tolerateIncompleteSweep for why (issue #1652).
			outcome.SucceededCount++
			if complete {
				outcome.SucceededAccountIDs = append(outcome.SucceededAccountIDs, acct.ID)
			} else {
				outcome.IncompleteAccountIDs = append(outcome.IncompleteAccountIDs, acct.ID)
			}
			mu.Unlock()
			return nil
		})
	}
	if waitErr := g.Wait(); waitErr != nil {
		// Goroutines return nil to isolate per-account failures; non-nil is unexpected.
		logging.Warnf("fanOutPerAccount: errgroup.Wait returned unexpected error: %v", waitErr)
	}
	return all, outcome
}

// isAccountPermissionError reports whether err from a per-account
// collection call represents an operator-fixable IAM permission gap
// (rather than a genuine collector error). Used by fanOutPerAccount to
// downgrade the log severity from ERROR to WARN — see issue #57: a
// single misconfigured GCP account otherwise emits an [ERROR] line on
// every scheduler tick (~15 min) and drowns out other log signals.
//
// Currently dispatches per provider:
//   - "GCP": gcpprovider.IsPermissionError (HTTP 403 / gRPC PermissionDenied)
//   - other providers: false (existing ERROR behavior preserved until
//     analogous predicates are added for AWS/Azure)
func isAccountPermissionError(providerLabel string, err error) bool {
	if err == nil {
		return false
	}
	switch providerLabel {
	case "GCP":
		return gcpprovider.IsPermissionError(err)
	default:
		return false
	}
}

// errAllAccountsFailed wraps an accountOutcome's LastErr so callers
// (the per-provider collect funcs) can surface a single error to
// CollectRecommendations when every registered account failed.
func errAllAccountsFailed(providerLabel string, outcome accountOutcome) error {
	return fmt.Errorf("%s: all %d accounts failed; last error: %s",
		providerLabel, outcome.FailedCount, outcome.LastErr)
}

func (s *Scheduler) collectAWSAmbient(ctx context.Context, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, bool, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create AWS provider: %w", err)
	}
	return s.fetchAndConvert(ctx, prov, "aws", nil, globalCfg)
}

// resolveAmbientHostAccountID looks up the Lambda's own AWS account ID
// (via STS GetCallerIdentity) and, if it matches a registered
// cloud_accounts row by (provider="aws", external_id), returns that
// row's UUID. Returns "" when STS is unavailable, the lookup fails, or
// no registered account matches — callers fall back to the pre-fix
// nil tagging in that case, preserving the truly-orphan deployment.
//
// All errors are intentionally swallowed (logged at debug/warn) — this
// is a best-effort UX improvement on the ambient path and must not
// break the collection.
func (s *Scheduler) resolveAmbientHostAccountID(ctx context.Context) string {
	if s.stsClient == nil {
		return ""
	}
	stsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.stsClient.GetCallerIdentity(stsCtx, &sts.GetCallerIdentityInput{})
	if err != nil {
		logging.Warnf("ambient host-account lookup: STS GetCallerIdentity failed: %v", err)
		return ""
	}
	if out == nil || out.Account == nil || *out.Account == "" {
		return ""
	}
	hostAcctID := *out.Account

	acct, err := s.config.GetCloudAccountByExternalID(ctx, "aws", hostAcctID)
	if err != nil {
		logging.Warnf("ambient host-account lookup: GetCloudAccountByExternalID(aws,%s) failed: %v", hostAcctID, err)
		return ""
	}
	if acct == nil {
		// Truly orphan deployment — preserve pre-fix nil tagging.
		return ""
	}
	return acct.ID
}

// resolveAmbientAccountID is the provider-agnostic counterpart to
// resolveAmbientHostAccountID: given the host's external identifier (subscription
// ID for Azure, project ID for GCP) it checks whether a registered cloud_accounts
// row exists for (provider, externalID) and returns its UUID. Returns "" on any
// error or when no row matches, preserving the pre-fix nil-tagging behavior so
// truly-orphan deployments are unaffected. All errors are intentionally swallowed
// (logged at warn) — this is a best-effort UX improvement on the ambient path
// and must not break the collection.
func (s *Scheduler) resolveAmbientAccountID(ctx context.Context, providerName, externalID string) string {
	if externalID == "" {
		return ""
	}
	acct, err := s.config.GetCloudAccountByExternalID(ctx, providerName, externalID)
	if err != nil {
		logging.Warnf("ambient host-account lookup: GetCloudAccountByExternalID(%s,%s) failed: %v", providerName, externalID, err)
		return ""
	}
	if acct == nil {
		// Truly orphan deployment — preserve pre-fix nil tagging.
		return ""
	}
	return acct.ID
}

// collectAzureAmbient collects Azure recommendations using the host's managed
// identity (DefaultAzureCredential / ambient credentials). Used by the ambient
// fallback path in collectAzureRecommendations when no accounts are registered.
// subscriptionID is passed explicitly to avoid an unnecessary Azure API round-trip
// to auto-discover subscriptions — the caller already resolved it from env.
func (s *Scheduler) collectAzureAmbient(ctx context.Context, subscriptionID string) ([]config.RecommendationRecord, bool, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "azure", &provider.ProviderConfig{
		AzureSubscriptionID: subscriptionID,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create ambient Azure provider: %w", err)
	}
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get Azure recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	complete, err := tolerateIncompleteSweep("azure", err)
	if err != nil {
		return nil, false, fmt.Errorf("get Azure recommendations: %w", err)
	}
	return s.convertRecommendations(recs, "azure"), complete, nil
}

// collectGCPAmbient collects GCP recommendations using Application Default
// Credentials. Used by the ambient fallback path in collectGCPRecommendations
// when no accounts are registered.
func (s *Scheduler) collectGCPAmbient(ctx context.Context) ([]config.RecommendationRecord, bool, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "gcp", nil)
	if err != nil {
		return nil, false, fmt.Errorf("create ambient GCP provider: %w", err)
	}
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get GCP recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	complete, err := tolerateIncompleteSweep("gcp", err)
	if err != nil {
		return nil, false, fmt.Errorf("get GCP recommendations: %w", err)
	}
	return s.convertRecommendations(recs, "gcp"), complete, nil
}

func (s *Scheduler) collectAWSForAccount(ctx context.Context, globalCfg *config.GlobalConfig, acct config.CloudAccount) ([]config.RecommendationRecord, bool, error) {
	// Self-account (role_arn with no role ARN) or ambient modes use ambient credentials
	if acct.AWSRoleARN == "" {
		prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", nil)
		if err != nil {
			return nil, false, fmt.Errorf("create ambient provider: %w", err)
		}
		return s.fetchAndConvert(ctx, prov, "aws", &acct.ID, globalCfg)
	}
	awsCreds, err := credentials.ResolveAWSCredentialProvider(ctx, &acct, s.credStore, s.assumeRoleSTS)
	if err != nil {
		return nil, false, fmt.Errorf("resolve credentials: %w", err)
	}
	cfg := &provider.ProviderConfig{Name: "aws", AWSCredentialsProvider: awsCreds}
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", cfg)
	if err != nil {
		return nil, false, fmt.Errorf("create provider: %w", err)
	}
	return s.fetchAndConvert(ctx, prov, "aws", &acct.ID, globalCfg)
}

// collectAzureRecommendations fans out across all enabled Azure accounts,
// resolving per-account federated credentials via the KMS signer.
// When no accounts are registered but AZURE_SUBSCRIPTION_ID is set (CUDly
// running natively on Azure with managed identity), falls back to ambient
// credentials and tags recommendations with the registered subscription's
// UUID if found in cloud_accounts.
func (s *Scheduler) collectAzureRecommendations(ctx context.Context, _ *config.GlobalConfig) ([]config.RecommendationRecord, []string, error) {
	accounts := s.enabledAccounts(ctx, "azure")
	if len(accounts) == 0 {
		// Issue #662: mirror the AWS ambient-path tagging fix for Azure.
		// When the host subscription matches a registered cloud_accounts row,
		// tag recs with that account's UUID instead of returning nil.
		// Best-effort: env lookup or store errors must not fail the collection.
		subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
		if subscriptionID == "" {
			logging.Info("No enabled Azure accounts — skipping Azure recommendations")
			return nil, nil, nil
		}
		recs, complete, err := s.collectAzureAmbient(ctx, subscriptionID)
		if err != nil {
			return nil, nil, err
		}
		if acctID := s.resolveAmbientAccountID(ctx, "azure", subscriptionID); acctID != "" {
			recs = s.tagAccount(recs, acctID)
			return recs, ambientEvictionRoster(complete, acctID), nil
		}
		return recs, ambientEvictionRoster(complete, ""), nil
	}

	recs, outcome := fanOutPerAccount(ctx, "Azure", accounts, s.collectAzureForAccount)
	if outcome.FailedCount == len(accounts) && len(accounts) > 0 {
		return nil, nil, errAllAccountsFailed("Azure", outcome)
	}
	return recs, outcome.SucceededAccountIDs, nil
}

func (s *Scheduler) collectAzureForAccount(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, bool, error) {
	// Every recommendation returned below is tagged with THIS account's UUID
	// (see tagAccount at the end of this function), so the provider must be
	// pinned to this account's subscription. An empty AzureSubscriptionID
	// leaves the provider unpinned, and an unpinned provider now fans out
	// across every subscription the credential can see -- which would file
	// other subscriptions' recommendations under this account and expose them
	// to anyone authorized for it. Fail loud instead; the row is misconfigured.
	if acct.AzureSubscriptionID == "" {
		return nil, false, fmt.Errorf("cloud account %s has no azure_subscription_id configured", acct.ID)
	}

	azCred, err := credentials.ResolveAzureTokenCredentialWithOpts(ctx, &acct, s.credStore, credentials.AzureResolveOptions{
		Signer:    s.oidcSigner,
		IssuerURL: s.oidcIssuerURL,
	})
	if err != nil {
		return nil, false, fmt.Errorf("resolve credentials: %w", err)
	}
	azProv, err := azureprovider.NewAzureProvider(&provider.ProviderConfig{Profile: acct.AzureSubscriptionID})
	if err != nil {
		return nil, false, fmt.Errorf("create provider: %w", err)
	}
	azProv.SetCredential(azCred)

	recClient, err := azProv.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	complete, err := tolerateIncompleteSweep("azure", err)
	if err != nil {
		return nil, false, fmt.Errorf("get recommendations: %w", err)
	}
	return s.tagAccount(s.convertRecommendations(recs, "azure"), acct.ID), complete, nil
}

// collectGCPRecommendations fans out across all enabled GCP accounts,
// resolving per-account federated credentials via the KMS signer.
// When no accounts are registered but GCP_PROJECT_ID is set (CUDly
// running natively on GCP with ADC), falls back to ambient credentials
// and tags recommendations with the registered project's UUID if found
// in cloud_accounts.
func (s *Scheduler) collectGCPRecommendations(ctx context.Context, _ *config.GlobalConfig) ([]config.RecommendationRecord, []string, error) {
	accounts := s.enabledAccounts(ctx, "gcp")
	if len(accounts) == 0 {
		// Issue #662: mirror the AWS ambient-path tagging fix for GCP.
		// When the host project matches a registered cloud_accounts row,
		// tag recs with that account's UUID instead of returning nil.
		// Best-effort: env lookup or store errors must not fail the collection.
		projectID := os.Getenv("GCP_PROJECT_ID")
		if projectID == "" {
			logging.Info("No enabled GCP accounts — skipping GCP recommendations")
			return nil, nil, nil
		}
		recs, complete, err := s.collectGCPAmbient(ctx)
		if err != nil {
			return nil, nil, err
		}
		if acctID := s.resolveAmbientAccountID(ctx, "gcp", projectID); acctID != "" {
			recs = s.tagAccount(recs, acctID)
			return recs, ambientEvictionRoster(complete, acctID), nil
		}
		return recs, ambientEvictionRoster(complete, ""), nil
	}

	recs, outcome := fanOutPerAccount(ctx, "GCP", accounts, s.collectGCPForAccount)
	if outcome.FailedCount == len(accounts) && len(accounts) > 0 {
		return nil, nil, errAllAccountsFailed("GCP", outcome)
	}
	return recs, outcome.SucceededAccountIDs, nil
}

func (s *Scheduler) collectGCPForAccount(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, bool, error) {
	gcpTS, err := credentials.ResolveGCPTokenSourceWithOpts(ctx, &acct, s.credStore, credentials.GCPResolveOptions{
		Signer:    s.oidcSigner,
		IssuerURL: s.oidcIssuerURL,
	})
	if err != nil {
		return nil, false, fmt.Errorf("resolve credentials: %w", err)
	}

	var prov provider.Provider
	if gcpTS != nil {
		prov = gcpprovider.NewProviderWithCredentials(ctx, acct.GCPProjectID, gcpTS)
	} else {
		// ADC mode (application_default): use ambient credentials
		created, createErr := s.providerFactory.CreateAndValidateProvider(ctx, "gcp", nil)
		if createErr != nil {
			return nil, false, fmt.Errorf("create ambient GCP provider: %w", createErr)
		}
		prov = created
	}

	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	complete, err := tolerateIncompleteSweep("gcp", err)
	if err != nil {
		return nil, false, fmt.Errorf("get recommendations: %w", err)
	}
	return s.tagAccount(s.convertRecommendations(recs, "gcp"), acct.ID), complete, nil
}

// enabledAccounts returns all enabled cloud accounts for the given provider.
func (s *Scheduler) enabledAccounts(ctx context.Context, providerName string) []config.CloudAccount {
	enabled := true
	accounts, err := s.config.ListCloudAccounts(ctx, config.CloudAccountFilter{
		Provider: &providerName,
		Enabled:  &enabled,
	})
	if err != nil {
		logging.Errorf("Failed to list %s accounts: %v", providerName, err)
		return nil
	}
	return accounts
}

// tolerateIncompleteSweep converts an org-wide partial-subscription failure
// into a warning and a nil error, so the caller keeps the recommendations that
// WERE collected instead of discarding them.
//
// Every recommendation-fetch site in this file has the shape
// `recs, err := ...; if err != nil { return nil, ... }`, which throws the
// results away. That is correct for a real error, but for a partial sweep it
// would turn one flaky subscription out of fifty into a total collection
// outage -- strictly worse than the silent under-collection that
// PartialSubscriptionFailureError exists to prevent. Routing every site
// through this helper keeps the policy in one place rather than relying on
// each call site to remember it.
//
// The failed subscription IDs are named in the log so an operator can tell an
// under-collected sweep from a genuinely shrinking savings opportunity. Any
// other error is returned unchanged, preserving the existing fail-loud
// behavior.
//
// The complete return value is what stops a tolerated sweep from being
// mistaken for a whole one. It is false only for a partial sweep, and callers
// must keep such an account OUT of the succeeded-account roster: that roster
// drives UpsertRecommendations' stale-row eviction, which deletes with
//
//	DELETE FROM recommendations
//	 WHERE collected_at < $1 AND (provider, account_key) IN (…)
//
// keyed on the CUDly account UUID, while a sweep fails one SUBSCRIPTION at a
// time. Reporting a partial sweep as succeeded therefore deletes the
// previous-cycle rows of every subscription the sweep never queried, and
// their savings silently read as "nothing to buy" rather than "collection
// incomplete" (issue #1652). That is data loss, not a display gap.
//
// Withholding eviction is deliberately all this does. The rows that WERE
// collected are still upserted by natural key, and the account is not marked
// failed -- failing it would blank the whole account's dashboard over one
// flaky subscription, the opposite failure and the one the succeeded-account
// roster exists to prevent.
//
// NOTE: the incompleteness still reaches the state table only as a log line;
// UpsertRecommendations continues to clear last_collection_error. That column
// is a single global row (recommendations_state WHERE id = 1), not per
// account, so representing one account's partial sweep in it is a separate
// design decision and is left as follow-up work. With eviction withheld the
// residue is observability, not lost rows.
func tolerateIncompleteSweep(providerName string, err error) (complete bool, _ error) {
	partial := azureprovider.AsPartialSubscriptionFailure(err)
	if partial == nil {
		return err == nil, err
	}
	logging.Warnf(
		"%s recommendations incomplete: %d of %d subscriptions succeeded; not queried: %s (keeping the %d that did, "+
			"and holding back stale-row eviction so the un-queried subscriptions' previous rows survive)",
		providerName, partial.Succeeded, partial.Attempted,
		strings.Join(partial.FailedSubscriptionIDs(), ", "), partial.Succeeded)
	return false, nil
}

// fetchAndConvert is a convenience for the AWS ambient path.
//
// The bool reports whether the sweep covered everything it was asked to; see
// tolerateIncompleteSweep for why an incomplete one must not authorize
// stale-row eviction.
func (s *Scheduler) fetchAndConvert(ctx context.Context, prov provider.Provider, providerName string, accountID *string, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, bool, error) {
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get %s recommendations client: %w", providerName, err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	complete, err := tolerateIncompleteSweep(providerName, err)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get %s recommendations: %w", providerName, err)
	}
	if len(recs) == 0 && globalCfg != nil {
		lookbackDays := globalCfg.RecommendationsLookbackDays
		if lookbackDays == 0 {
			lookbackDays = config.DefaultRecommendationsLookbackDays
		}
		params := common.RecommendationParams{
			Term:           fmt.Sprintf("%dyr", globalCfg.DefaultTerm),
			PaymentOption:  globalCfg.DefaultPayment,
			LookbackPeriod: fmt.Sprintf("%dd", lookbackDays),
		}
		var recErr error
		recs, recErr = recClient.GetRecommendations(ctx, &params)
		fallbackComplete, recErr := tolerateIncompleteSweep(providerName, recErr)
		if recErr != nil {
			// Fail loud: a misconfigured DefaultPayment/DefaultTerm or a CE
			// failure on this fallback must surface to the operator instead
			// of silently presenting as "zero recommendations".
			return nil, false, fmt.Errorf("failed to get %s recommendations with default term/payment fallback (term=%s, payment=%s, lookback=%s): %w",
				providerName, params.Term, params.PaymentOption, params.LookbackPeriod, recErr)
		}
		// AND, not assignment: the fallback re-queries the same scope, so an
		// incomplete first sweep stays incomplete even if the retry happens to
		// come back whole. Overwriting here would let the retry launder away
		// the first sweep's missing subscriptions and re-authorize eviction.
		complete = complete && fallbackComplete
	}
	result := s.convertRecommendations(recs, providerName)
	if accountID != nil {
		result = s.tagAccount(result, *accountID)
	}
	return result, complete, nil
}

// tagAccount sets CloudAccountID on each recommendation record.
func (s *Scheduler) tagAccount(recs []config.RecommendationRecord, accountID string) []config.RecommendationRecord {
	for i := range recs {
		recs[i].CloudAccountID = &accountID
	}
	return recs
}

// ListRecommendations reads cached recommendations from the store, applying
// the given filter as SQL WHERE clauses. Previously this did live cloud
// API calls on every request (2–10s per provider); now it's a pure DB
// read, typically under 100ms.
//
// Order of operations:
//  1. Read freshness.
//  2. Cold-start (LastCollectedAt==nil): synchronous CollectRecommendations
//     so the first caller sees real data rather than an empty table. Safe
//     on all runtimes since the call is sync.
//  3. Read from the cache.
//  4. Stale-while-revalidate: if the cache is older than cacheTTL AND we
//     aren't on Lambda, kick off a background CollectRecommendations so
//     the NEXT read sees fresh data. Lambda skips this (goroutines freeze
//     between invocations); the scheduled cron is Lambda's refresh path.
func (s *Scheduler) ListRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	logging.Info("Reading recommendations from cache...")

	freshness, err := s.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read recommendations freshness: %w", err)
	}

	if freshness.LastCollectedAt == nil {
		logging.Info("Recommendations cache is empty; performing synchronous cold-start collect")
		_, collectErr := s.CollectRecommendations(ctx, "")
		if collectErr != nil {
			return nil, fmt.Errorf("cold-start collect failed: %w", collectErr)
		}
	}

	recs, err := s.config.ListStoredRecommendations(ctx, filter)
	if err != nil {
		return nil, err
	}

	recs, err = s.applySuppressions(ctx, recs)
	if err != nil {
		// A suppression read failure shouldn't block the recs list —
		// the user-facing cost of that is an empty page; we'd rather
		// over-show than under-show. Log and continue.
		logging.Errorf("failed to apply suppressions; returning un-suppressed recs: %v", err)
	}

	recs, err = s.applyAccountOverrides(ctx, recs)
	if err != nil {
		// Same over-show-vs-under-show trade-off as applySuppressions.
		// A missing global ServiceConfig or override-table read failure
		// should leave the user looking at un-overridden recs, not a
		// blank page. Issue #196.
		logging.Errorf("failed to apply account overrides; returning un-filtered recs: %v", err)
	}

	// Background refresh is only attempted on non-Lambda runtimes; skip
	// the DB config read entirely on Lambda so GetGlobalConfig is not called
	// on the hot read path (Lambda has no persistent goroutines).
	if !s.isLambda {
		ttl, disabled := s.resolveEffectiveCacheTTL(ctx)
		if disabled {
			return recs, nil
		}
		s.maybeKickBackgroundRefresh(freshness, ttl)
	}
	return recs, nil
}

// GetRecommendationByID fetches a single recommendation by its application-
// level ID (the id field in the stored payload), bypassing the account-override
// filter so deep-linked URLs to override-hidden recs still resolve.
//
// Suppressions are still applied: a rec whose remaining count has been
// suppressed to zero is treated as gone (returns nil, nil, nil) so the caller
// can render a 404 rather than a "hidden" banner for actively-dismissed recs.
//
// hiddenBy is non-nil (one or more reason strings) when the rec exists and
// passes the suppression check, but would be dropped by the account-override
// filter. Possible reasons: "enabled=false", "engine", "region",
// "resource_type".  The caller renders a "hidden by your override" banner.
//
// Returns (nil, nil, nil) when the rec is genuinely absent or fully suppressed.
func (s *Scheduler) GetRecommendationByID(ctx context.Context, id string) (rec *config.RecommendationRecord, hiddenBy []string, err error) {
	recs, err := s.config.ListStoredRecommendations(ctx, config.RecommendationFilter{ID: id})
	if err != nil {
		return nil, nil, fmt.Errorf("GetRecommendationByID: store lookup: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}

	// Apply suppressions. A fully-suppressed rec becomes genuinely absent from
	// the caller's perspective (the suppression is intentional user action).
	recs, err = s.applySuppressions(ctx, recs)
	if err != nil {
		// Over-show: on suppression read failure treat the rec as un-suppressed.
		logging.Errorf("GetRecommendationByID: suppression check failed; treating rec as un-suppressed: %v", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}
	found := &recs[0]

	// Check whether the account-override filter would drop this rec. This is
	// a read-only call — we never drop it here, only report the reasons.
	// Recs with no cloud account are checked too: the global ServiceConfig
	// applies to them (issue #1604), so the list filter can hide them and the
	// detail page must say why.
	resolved, resolveErr := config.ResolveAccountConfigsForRecs(ctx, s.config, recs)
	if resolveErr != nil {
		// Non-fatal: if the override check fails we surface the rec without
		// a hidden_by marker (over-show is the safer default).
		logging.Errorf("GetRecommendationByID: override resolution failed; returning rec without hidden_by: %v", resolveErr)
		return found, nil, nil
	}
	if cfg := resolved[config.RecConfigKey(found)]; cfg != nil {
		hiddenBy = overrideHiddenReasons(found, cfg)
	}

	return found, hiddenBy, nil
}

// overrideHiddenReasons returns a non-empty slice when rec would be dropped by
// the given resolved ServiceConfig, naming each failing dimension.
func overrideHiddenReasons(rec *config.RecommendationRecord, cfg *config.ServiceConfig) []string {
	var reasons []string
	if !cfg.Enabled {
		reasons = append(reasons, "enabled=false")
	}
	if !engineMatches(rec.Engine, cfg) {
		reasons = append(reasons, "engine")
	}
	if !inListRule(rec.Region, cfg.IncludeRegions, cfg.ExcludeRegions) {
		reasons = append(reasons, "region")
	}
	if !inListRule(rec.ResourceType, cfg.IncludeTypes, cfg.ExcludeTypes) {
		reasons = append(reasons, "resource_type")
	}
	return reasons
}

// resolveEffectiveCacheTTL returns the effective stale-while-revalidate TTL
// and whether background auto-refresh has been explicitly disabled (value 0).
// It prefers the DB-configured RecommendationsCacheStaleHours; falls back to
// s.cacheTTL (env-var / compile-time default) when the DB read fails.
func (s *Scheduler) resolveEffectiveCacheTTL(ctx context.Context) (ttl time.Duration, disabled bool) {
	ttl = s.cacheTTL
	globalCfg, gcErr := s.config.GetGlobalConfig(ctx)
	if gcErr != nil || globalCfg == nil {
		// DB read failed OR returned (nil, nil) — fall back to the env-var /
		// default TTL with refresh enabled rather than panicking on the
		// nil deref below. (nil, nil) is a defensive case; the Postgres
		// store currently never returns it, but a future store impl or a
		// test mock might.
		return ttl, false
	}
	if globalCfg.RecommendationsCacheStaleHours == 0 {
		// Explicit 0 = operator opted out of automatic background refresh.
		return 0, true
	}
	return time.Duration(globalCfg.RecommendationsCacheStaleHours) * time.Hour, false
}

// suppressionKey is the 6-tuple used to match suppressions to recs.
type suppressionKey struct {
	accountID, provider, service, region, resourceType, engine string
}

// suppressionAgg aggregates all active suppression rows for a single
// 6-tuple: cumulative count, earliest expiry (drives the badge's
// "Xd remaining"), and the execution whose suppression contributed
// the most (drives the badge deep-link).
type suppressionAgg struct {
	earliestExpiresAt       time.Time
	primaryExecutionCreated time.Time
	primaryExecutionID      string
	suppressedCount         int
	primaryExecutionContrib int
}

// applySuppressions subtracts active purchase_suppressions from each
// rec's count and annotates the rec with the SuppressedCount /
// SuppressionExpiresAt / PrimarySuppressionExecutionID fields that
// drive the "recently purchased" badge on the frontend. Recs whose
// remaining count hits 0 are dropped entirely.
func (s *Scheduler) applySuppressions(ctx context.Context, recs []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	sups, err := s.config.ListActiveSuppressions(ctx)
	if err != nil {
		return recs, err
	}
	if len(sups) == 0 {
		return recs, nil
	}
	index := aggregateSuppressions(sups)
	return applySuppressionIndex(recs, index), nil
}

// aggregateSuppressions groups a raw suppression list by 6-tuple.
func aggregateSuppressions(sups []config.PurchaseSuppression) map[suppressionKey]*suppressionAgg {
	index := make(map[suppressionKey]*suppressionAgg, len(sups))
	for i := range sups {
		sup := sups[i]
		k := suppressionKey{
			accountID: sup.AccountID, provider: sup.Provider, service: sup.Service,
			region: sup.Region, resourceType: sup.ResourceType, engine: sup.Engine,
		}
		a, ok := index[k]
		if !ok {
			a = &suppressionAgg{earliestExpiresAt: sup.ExpiresAt}
			index[k] = a
		}
		a.suppressedCount += sup.SuppressedCount
		if sup.ExpiresAt.Before(a.earliestExpiresAt) {
			a.earliestExpiresAt = sup.ExpiresAt
		}
		// "Most contribution" selects the biggest single suppression
		// row; ties go to the most-recently-created row so a fresh
		// cancel of an older peer doesn't leave the badge pointing at
		// a stale execution.
		if sup.SuppressedCount > a.primaryExecutionContrib ||
			(sup.SuppressedCount == a.primaryExecutionContrib && sup.CreatedAt.After(a.primaryExecutionCreated)) {
			a.primaryExecutionID = sup.ExecutionID
			a.primaryExecutionCreated = sup.CreatedAt
			a.primaryExecutionContrib = sup.SuppressedCount
		}
	}
	return index
}

// applySuppressionIndex walks recs, subtracts the matching aggregate
// count from each rec, drops recs that go to 0 or below, and
// annotates the survivors with the badge fields.
func applySuppressionIndex(recs []config.RecommendationRecord, index map[suppressionKey]*suppressionAgg) []config.RecommendationRecord {
	// Allocate a fresh backing array so callers that hold a reference to
	// the original recs slice do not see mutations (05-M1).
	out := make([]config.RecommendationRecord, 0, len(recs))
	for _rvc := range recs {
		rec := recs[_rvc]
		accountID := ""
		if rec.CloudAccountID != nil {
			accountID = *rec.CloudAccountID
		}
		k := suppressionKey{
			accountID: accountID, provider: rec.Provider, service: rec.Service,
			region: rec.Region, resourceType: rec.ResourceType, engine: rec.Engine,
		}
		a, ok := index[k]
		if !ok {
			out = append(out, rec)
			continue
		}
		rec.Count -= a.suppressedCount
		if rec.Count <= 0 {
			continue // fully covered by recent purchases; hide
		}
		rec.SuppressedCount = a.suppressedCount
		expiry := a.earliestExpiresAt
		rec.SuppressionExpiresAt = &expiry
		primary := a.primaryExecutionID
		rec.PrimarySuppressionExecutionID = &primary
		out = append(out, rec)
	}
	return out
}

// maybeKickBackgroundRefresh spawns a detached CollectRecommendations
// goroutine when the cache is stale AND the runtime can safely run
// background goroutines (i.e. not Lambda). The atomic.Bool guard
// single-flights concurrent callers so only one refresh fires per TTL
// window. Recovers from panics so one bad collect can't crash the
// long-lived process.
//
// effectiveTTL is the caller-resolved stale threshold (from DB config
// RecommendationsCacheStaleHours, or the env-var default if unconfigured).
func (s *Scheduler) maybeKickBackgroundRefresh(freshness *config.RecommendationsFreshness, effectiveTTL time.Duration) {
	if s.isLambda {
		return
	}
	if freshness.LastCollectedAt == nil {
		// Just handled synchronously; nothing to backfill.
		return
	}
	if time.Since(*freshness.LastCollectedAt) < effectiveTTL {
		return
	}
	if !s.collecting.CompareAndSwap(false, true) {
		// Another refresh is already in flight.
		return
	}

	logging.Infof("Recommendations cache is stale (age > %s); triggering background refresh", effectiveTTL)
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	go func() {
		defer cancel()
		defer s.collecting.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("background recommendations refresh panic: %v", r)
			}
		}()
		if _, err := s.CollectRecommendations(bgCtx, ""); err != nil {
			// CollectRecommendations already surfaces errors via
			// recommendations_state.last_collection_error, so just log
			// locally here for operator visibility.
			logging.Errorf("background refresh: %v", err)
		}
	}()
}

// nonZeroPtr returns &v when v > 0, else nil. Used by convertRecommendations
// to pass the provider's on-demand baseline (rec.OnDemandCost) through as a
// nullable pointer — a literal 0 from the provider means "not populated"
// because every real on-demand baseline is non-zero. Always returning a
// pointer would poison the frontend's "is this populated?" branch
// (recommendations.ts::effectiveSavingsPct, see #274). Extracted to keep
// convertRecommendations under the gocyclo gate (.golangci.yml
// min-complexity: 15; pre-commit hook flags > 10).
func nonZeroPtr(v float64) *float64 {
	if v > 0 {
		return &v
	}
	return nil
}

// extractEngine pulls the engine string out of a polymorphic
// common.ServiceDetails value when one is present. DatabaseDetails/
// CacheDetails are always pointers (every producer constructs them that
// way; see pkg/common/service_details_codec.go's package doc for the
// invariant). Returns "" for any other Details type (or nil). The
// nil-pointer guards are not dead code: the `details == nil` check above
// only catches an untyped nil, so a typed nil reaches the switch and would
// panic on the field read. Extracted from convertRecommendations to keep
// that function under the gocyclo budget (min-complexity: 10 in
// .pre-commit-config.yaml).
func extractEngine(details common.ServiceDetails) string {
	if details == nil {
		return ""
	}
	switch d := details.(type) {
	case *common.DatabaseDetails:
		if d == nil {
			return ""
		}
		return d.Engine
	case *common.CacheDetails:
		if d == nil {
			return ""
		}
		return d.Engine
	}
	return ""
}

// marshalRecDetails encodes the full ServiceDetails payload into a raw
// JSON blob so the purchase manager can reconstruct the correct typed
// *Details pointer later (issue #453). Without this, every persisted
// rec round-trips through the DB as "service + engine" only — losing
// EC2 platform / tenancy / scope, RDS AZ-config, SP plan-type / hourly
// commitment, etc. — and findOfferingID either picks the wrong offering
// (Windows recs purchased as Linux/UNIX) or fails the type-assertion
// outright. Marshal errors are non-fatal: the row still gets persisted
// without Details and falls through to the graceful-degradation path in
// common.DecodeServiceDetailsFor. Extracted from convertRecommendations
// to keep that function under the gocyclo budget.
func marshalRecDetails(rec common.Recommendation, providerName string) []byte {
	blob, err := common.MarshalServiceDetails(rec.Details)
	if err != nil {
		logging.Warnf("Failed to marshal service details for %s/%s rec (%s): %v — persisting without Details",
			providerName, rec.Service, rec.ResourceType, err)
		return nil
	}
	return blob
}

// convertRecommendations converts common.Recommendation slice to config.RecommendationRecord slice.
func (s *Scheduler) convertRecommendations(recs []common.Recommendation, providerName string) []config.RecommendationRecord {
	records := make([]config.RecommendationRecord, 0, len(recs))

	for _rvc := range recs {
		rec := recs[_rvc]
		engine := extractEngine(rec.Details)
		detailsBlob := marshalRecDetails(rec, providerName)

		// Canonicalize PaymentOption at the emission boundary so a downstream
		// plan-validator round-trip never sees a cross-provider/AWS-style
		// token on a non-AWS rec (issue #698). The provider service clients
		// historically aliased "all-upfront"/"no-upfront" to the canonical
		// "upfront"/"monthly" inside their pricing switches; the validator
		// no longer accepts those aliases. Normalize once here so persisted
		// recs always carry the provider-canonical token.
		if canon, ok := config.NormalizePaymentOption(providerName, rec.PaymentOption); ok {
			if canon != rec.PaymentOption {
				logging.Warnf("convertRecommendations: coerced %s payment_option %q to canonical %q (account=%s service=%s sku=%s)",
					providerName, rec.PaymentOption, canon, rec.Account, rec.Service, rec.ResourceType)
				rec.PaymentOption = canon
			}
		} else if rec.PaymentOption != "" {
			// Unknown provider or unmapped token: leave as-is. The next
			// validator boundary will surface the issue with a clear error.
			logging.Warnf("convertRecommendations: cannot canonicalize %s payment_option %q (account=%s service=%s sku=%s) — leaving as-is",
				providerName, rec.PaymentOption, rec.Account, rec.Service, rec.ResourceType)
		}

		// Parse term to integer (e.g., "3yr" -> 3). An empty or unparseable
		// term must NOT be coerced to a valid default: rec.Term round-trips
		// through this int on the purchase path (internal/purchase/execution.go
		// formats it back as "%dyr"), so silently defaulting here would let a
		// malformed term launder into a valid, purchasable commitment length
		// downstream, bypassing the ARCH-04 whitelist checks in the provider
		// clients entirely, since "3yr" is itself a whitelisted value (issue
		// #1207 follow-up). Drop the recommendation instead of persisting a
		// fabricated term.
		termStr := strings.TrimSuffix(rec.Term, "yr")
		term, err := strconv.Atoi(termStr)
		if err != nil || term <= 0 {
			logging.Errorf("convertRecommendations: dropping %s rec (account=%s service=%s sku=%s region=%s): invalid term %q",
				providerName, rec.Account, rec.Service, rec.ResourceType, rec.Region, rec.Term)
			continue
		}

		// The ID must be unique per logically-distinct rec — otherwise
		// the frontend collapses two rows into one selection (the
		// data-rec-id collision in recommendations.ts:1067-1069 / the
		// selection-set toggle in :1639-1660 — see issue #187), and
		// any downstream stage that dedupes by ID drops the second rec
		// entirely (the AWS-1yr-missing symptom in issue #188).
		//
		// We use the natural composite key directly rather than a hash:
		// no truncation collision risk, self-documenting in DevTools
		// (an id like "aws|123456789012|ec2|us-east-1|m5.large||1|all-upfront"
		// makes any future regression visibly identical), and the
		// downstream consumers — frontend selection Set, plan-target
		// matching, suppression keying — all treat the id opaquely as
		// a string. The fields are alphanumeric/hyphen by upstream
		// contract (provider slugs, AWS/Azure/GCP account IDs and
		// subscription UUIDs, AWS region names, instance-type SKUs,
		// payment-option enums), so `|` cannot appear inside any
		// component — no escaping needed.
		//
		// Fields:
		//   - providerName: separates AWS/Azure/GCP recs
		//   - rec.Account:  separates per-account/per-subscription recs
		//                   sharing the same provider+SKU+region+payment
		//   - rec.Service / rec.Region / rec.ResourceType: the cell
		//   - engine:       MySQL vs Postgres RDS at same SKU collide otherwise
		//   - term:         1yr vs 3yr at same SKU collide otherwise
		//   - rec.PaymentOption: all-upfront vs no-upfront collide otherwise
		//
		// The parsed integer `term` is used (not rec.Term) so a rec
		// with Term="" or "3yr" both reduce to the same canonical
		// value and don't drift out of agreement with the persisted
		// Term column.
		recordID := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%s",
			providerName, rec.Account, rec.Service, rec.Region,
			rec.ResourceType, engine, term, rec.PaymentOption)

		records = append(records, config.RecommendationRecord{
			ID:                recordID,
			Provider:          providerName,
			Service:           string(rec.Service),
			Region:            rec.Region,
			ResourceType:      rec.ResourceType,
			Engine:            engine,
			Details:           detailsBlob, // full ServiceDetails payload (issue #453)
			Count:             rec.Count,
			Term:              term,
			Payment:           rec.PaymentOption,
			UpfrontCost:       rec.CommitmentCost,
			MonthlyCost:       rec.RecurringMonthlyCost, // nil when provider API didn't return a monthly breakdown
			Savings:           rec.EstimatedSavings,
			OnDemandCost:      nonZeroPtr(rec.OnDemandCost),      // nil when provider API didn't return a baseline; frontend falls back to reconstruction (#274)
			SavingsPercentage: nonZeroPtr(rec.SavingsPercentage), // provider-authoritative %; nil when not reported, frontend falls back to effectiveSavingsPct
			UsageHistory:      rec.UsageHistory,                  // daily coverage pcts (nil when provider not yet wired; see #239)
			Selected:          true,                              // Default to selected
			Purchased:         false,
		})
	}

	return records
}
