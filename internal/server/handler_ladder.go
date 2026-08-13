package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/google/uuid"

	"github.com/LeanerCloud/CUDly/internal/config"
	pkgcommon "github.com/LeanerCloud/CUDly/pkg/common"
	pkgladder "github.com/LeanerCloud/CUDly/pkg/ladder"
)

// Nominal planning period per cadence: how long a config waits between runs.
const (
	cadenceDailyPeriod  = 24 * time.Hour
	cadenceWeeklyPeriod = 7 * 24 * time.Hour
)

// Cadence thresholds for the per-config self-gate (Q6).
// Each constant is a minimum elapsed time before the same config may run again.
// The 4-hour slack on each bound prevents skipped firings from drifting into
// the next cadence window: a daily at 23:55 still qualifies at 19:55 the next
// day even if the cron fires a few minutes early.
const (
	cadenceRunSlack  = 4 * time.Hour
	cadenceDailyMin  = cadenceDailyPeriod - cadenceRunSlack  // 20h
	cadenceWeeklyMin = cadenceWeeklyPeriod - cadenceRunSlack // 6d 20h
)

// dataSourceAWSCostExplorer is the provenance tag stamped on every planned
// action in the PR-2 plan-only phase. The baseline is derived from AWS Cost
// Explorer once the CE adapter lands (PR-4); until then the tag documents the
// intended source. Named constant instead of a bare string literal so the
// provenance value has a single definition.
const dataSourceAWSCostExplorer = "aws-ce"

// ladderConfigOutcome is the result of processing a single ladder_config entry
// in the handleLadderRun loop. Using a typed constant avoids bare strings on
// the outcome path.
type ladderConfigOutcome int

const (
	outcomeSkippedDisabled     ladderConfigOutcome = iota
	outcomeSkippedMultiAccount                     // iota = 1
	outcomeSkippedCadence                          // iota = 2
	outcomeErrored                                 // iota = 3
	outcomePlanned                                 // iota = 4
)

// LadderRunResult is the aggregate outcome of one ladder_run task invocation.
// Each counter increments once per ladder_config entry processed.
type LadderRunResult struct {
	Planned             int `json:"planned"`
	SkippedCadence      int `json:"skipped_cadence"`
	SkippedDisabled     int `json:"skipped_disabled"`
	SkippedMultiAccount int `json:"skipped_multi_account"`
	Errored             int `json:"errored"`
}

// record increments the counter that corresponds to the given outcome.
func (r *LadderRunResult) record(o ladderConfigOutcome) {
	switch o {
	case outcomePlanned:
		r.Planned++
	case outcomeSkippedDisabled:
		r.SkippedDisabled++
	case outcomeSkippedMultiAccount:
		r.SkippedMultiAccount++
	case outcomeSkippedCadence:
		r.SkippedCadence++
	case outcomeErrored:
		r.Errored++
	}
}

// handleLadderRun is the top-level orchestrator for the ladder_run scheduled task.
// It iterates over all ladder_configs and runs the planning engine for each
// eligible config. The result struct carries per-outcome counts so the caller
// can surface them in the Lambda log and in future status pages.
//
// plan-only: no PurchaseLayer, ReshapeBuffer, email, or approval tokens.
func (app *Application) handleLadderRun(ctx context.Context) (*LadderRunResult, error) {
	// Global kill-switch check.
	globalCfg, err := app.Config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: failed to load global config: %w", err)
	}
	if !globalCfg.LadderingEnabled {
		log.Println("ladder_run: laddering_enabled=false in global config, skipping all configs")
		return &LadderRunResult{}, nil
	}

	// Parse global-default term and payment option. Fail loud: money-path
	// decisions must never proceed with an invalid/unknown option.
	term, err := ladderTermFromYears(globalCfg.DefaultTerm)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: invalid default_term in global config: %w", err)
	}
	paymentOpt, err := pkgladder.ParsePaymentOption(globalCfg.DefaultPayment)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: invalid default_payment in global config: %w", err)
	}

	// Load all ladder_config rows. GetLadderConfigs returns ALL rows; we
	// filter Enabled=false here so the counter is visible (SkippedDisabled).
	allConfigs, err := app.Config.GetLadderConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: failed to load ladder configs: %w", err)
	}
	if len(allConfigs) == 0 {
		log.Println("ladder_run: no ladder_config rows found, nothing to do")
		return &LadderRunResult{}, nil
	}

	// Q1: single-account only. Resolve the Lambda's own AWS account ID STRICTLY:
	// this ID scopes which configs run, so an unresolved account must abort the
	// whole task (fail loud) rather than fall through and skip every config as
	// multi-account, which would report a false success on a money path.
	ownAccountID, region, err := app.resolveLadderAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: could not resolve caller AWS account for the single-account gate: %w", err)
	}

	now := time.Now().UTC()
	result := app.runLadderConfigs(ctx, allConfigs, ownAccountID, region, term, paymentOpt, now, globalCfg.LadderExecutionEnabled)

	log.Printf("ladder_run done: planned=%d skipped_cadence=%d skipped_disabled=%d skipped_multi_account=%d errored=%d",
		result.Planned, result.SkippedCadence, result.SkippedDisabled, result.SkippedMultiAccount, result.Errored)
	return result, nil
}

// resolveLadderAccount resolves the caller AWS account ID and region for the
// single-account gate. It delegates to the injected LadderAccountResolver (used
// by tests) or the default STS-backed resolver. Any error is returned to the
// caller so handleLadderRun can fail loud.
func (app *Application) resolveLadderAccount(ctx context.Context) (accountID, region string, err error) {
	if app.LadderAccountResolver != nil {
		return app.LadderAccountResolver(ctx)
	}
	return defaultLadderAccountResolver(ctx)
}

// defaultLadderAccountResolver resolves the Lambda's own AWS account ID via STS
// STRICTLY: unlike resolveAccountID (which returns the "unknown" sentinel for
// audit-only use in the RI-exchange path), this returns an error when the
// account cannot be determined. The ladder path uses the account ID to SCOPE
// which configs run, so a fabricated/"unknown" value is unsafe: it would skip
// every config as multi-account and report a false success.
func defaultLadderAccountResolver(ctx context.Context) (accountID, region string, err error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", "", fmt.Errorf("load AWS config: %w", err)
	}
	return resolveLadderIdentity(ctx, awsCfg)
}

// resolveLadderIdentity validates the region and resolves the caller account ID
// from an already-loaded aws.Config. Split from defaultLadderAccountResolver so
// the region short-circuit is unit-testable without loading real credentials or
// calling STS.
//
// The region is validated FIRST: an empty region would make NewFromAWSConfig
// reject EVERY config later, turning the whole task into a silent all-Errored
// no-op that still returns success. Failing loud here aborts handleLadderRun
// before any config is processed (same silent-degradation class as the missing
// STS account ID below).
func resolveLadderIdentity(ctx context.Context, awsCfg aws.Config) (accountID, region string, err error) {
	if awsCfg.Region == "" {
		return "", "", fmt.Errorf("AWS region is empty; set AWS_REGION / AWS_DEFAULT_REGION or a region in the shared config")
	}
	stsClient := sts.NewFromConfig(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", "", fmt.Errorf("resolve caller account via STS: %w", err)
	}
	if identity.Account == nil || *identity.Account == "" {
		return "", "", fmt.Errorf("STS GetCallerIdentity returned no account ID")
	}
	return *identity.Account, awsCfg.Region, nil
}

// runLadderConfigs processes every ladder_config entry, isolating each one:
// an error on one config increments the Errored counter and continues to the
// next, so a single broken config never aborts the whole run. Extracted from
// handleLadderRun so the multi-config isolation behavior is unit-testable
// without the AWS SDK account-resolution path in handleLadderRun.
func (app *Application) runLadderConfigs(
	ctx context.Context,
	configs []config.LadderConfigDB,
	ownAccountID, region string,
	term pkgladder.Term,
	paymentOpt pkgladder.PaymentOption,
	now time.Time,
	executionEnabled bool,
) *LadderRunResult {
	result := &LadderRunResult{}
	for i := range configs {
		result.record(app.processOneLadderConfig(ctx, &configs[i], ownAccountID, region, term, paymentOpt, now, executionEnabled))
	}
	return result
}

// processOneLadderConfig applies all eligibility gates for a single
// ladder_config entry and runs the planning engine when eligible. It is
// extracted from handleLadderRun to keep both functions within the project's
// cyclomatic-complexity limit (10).
func (app *Application) processOneLadderConfig(
	ctx context.Context,
	dbCfg *config.LadderConfigDB,
	ownAccountID, region string,
	term pkgladder.Term,
	paymentOpt pkgladder.PaymentOption,
	now time.Time,
	executionEnabled bool,
) ladderConfigOutcome {
	if !dbCfg.Enabled {
		log.Printf("ladder_run: config %s: enabled=false, skipping", dbCfg.ID)
		return outcomeSkippedDisabled
	}

	// Resolve the cloud account to get the 12-digit AWS account number (Q2).
	cloudAcct, err := app.Config.GetCloudAccount(ctx, dbCfg.CloudAccountID)
	if err != nil {
		log.Printf("ladder_run: config %s: failed to get cloud account %s: %v", dbCfg.ID, dbCfg.CloudAccountID, err)
		return outcomeErrored
	}
	if cloudAcct == nil {
		log.Printf("ladder_run: config %s: cloud account %s not found in DB", dbCfg.ID, dbCfg.CloudAccountID)
		return outcomeErrored
	}

	// Q1: skip configs that belong to a different AWS account.
	if cloudAcct.ExternalID != ownAccountID {
		log.Printf("ladder_run: config %s: cloud account external_id=%q != lambda account=%q: skipped (multi_account_unsupported)", dbCfg.ID, cloudAcct.ExternalID, ownAccountID)
		return outcomeSkippedMultiAccount
	}

	// Cadence self-gate: skip if a run already started within the window. Fail
	// CLOSED on a lookup error: if we cannot tell whether a recent run exists,
	// running anyway risks a duplicate money action, so count the config Errored
	// (visible in LadderRunResult.Errored) instead of proceeding silently.
	within, reason, err := ladderWithinCadenceWindow(ctx, app.Config, dbCfg, now)
	if err != nil {
		log.Printf("ladder_run: config %s: cadence lookup failed, skipping to avoid a possible double-run: %v", dbCfg.ID, err)
		return outcomeErrored
	}
	if within {
		log.Printf("ladder_run: config %s: %s", dbCfg.ID, reason)
		return outcomeSkippedCadence
	}

	// Build and wire the LadderCapability for this account.
	capability, err := app.buildAndWireCapability(ctx, region, cloudAcct.ExternalID, executionEnabled)
	if err != nil {
		log.Printf("ladder_run: config %s: %v", dbCfg.ID, err)
		return outcomeErrored
	}

	// Run the plan engine and persist the result.
	if err := app.executeLadderRun(ctx, dbCfg, capability, cloudAcct.ExternalID, term, paymentOpt, now); err != nil {
		log.Printf("ladder_run: config %s: planning failed: %v", dbCfg.ID, err)
		return outcomeErrored
	}
	return outcomePlanned
}

// executeLadderRun runs the planning engine for a single ladder_config and
// persists the result as a ladder_runs row (+ ladder_tranches audit rows).
//
// It is split from handleLadderRun so tests can inject a hermetic
// LadderCapability (fake) and a mock store without touching AWS or PostgreSQL.
//
// L5 netting: before calling Allocate, the handler fetches the config's in-flight
// commitment (the sum of its status=scheduled, not-yet-fired ladder tranches
// ONLY). The Allocate engine subtracts this from the raw gap so each daily run
// plans only what is still needed, preventing pile-up when prior scheduled
// tranches have not yet fired. Fired/completed tranches are deliberately NOT
// counted here: they are executed purchases already reflected in the engine's
// ExistingUSDPerHour, so counting them again would double-subtract.
//
// Persistence is APPEND-ONLY (see persistLadderRun): every run inserts its new
// scheduled tranches and NEVER supersedes existing ones. Netting already
// subtracted the config's existing scheduled tranches from the gap, so each run
// appends exactly the delta needed to reach target-E: a Hold appends nothing
// and preserves the live ramp with its original fire dates; a drift-up run
// appends the top-up so total scheduled commitment converges to target.
func (app *Application) executeLadderRun(
	ctx context.Context,
	dbCfg *config.LadderConfigDB,
	capability pkgladder.LadderCapability,
	accountID string,
	term pkgladder.Term,
	paymentOpt pkgladder.PaymentOption,
	now time.Time,
) error {
	// Convert the DB config row to the engine's typed LadderConfig.
	engineCfg, err := ladderConfigToEngine(dbCfg, accountID)
	if err != nil {
		return fmt.Errorf("config conversion: %w", err)
	}
	scope := engineCfg.Scope

	// Collect read-side data. All three calls are independent; errors are
	// fail-loud (not silently defaulted to empty).
	baseline, err := capability.GetUsageBaseline(ctx, scope, engineCfg.LookbackDays, engineCfg.BaselinePercentile)
	if err != nil {
		return fmt.Errorf("GetUsageBaseline: %w", err)
	}

	layerStates, err := capability.GetLayerStates(ctx, scope)
	if err != nil {
		return fmt.Errorf("GetLayerStates: %w", err)
	}

	// Retire tranches that are scheduled but long past their fire date before
	// reading the in-flight sum: they never fired, so they are not in flight and
	// must not net against this run's gap (#1605).
	if err := app.expireElapsedLadderTranches(ctx, dbCfg.ID, engineCfg.Cadence, now); err != nil {
		return err
	}

	// L5: Fetch in-flight commitment BEFORE Allocate so the engine can subtract
	// it from the gap. In-flight = the config's status=scheduled (not-yet-fired)
	// ladder tranches ONLY. Fired/completed tranches are excluded because they
	// are executed purchases already counted in ExistingUSDPerHour; netting them
	// again would double-subtract. Including prior-run scheduled tranches lets
	// the engine account for en-route commitment and produce a ~zero gap (Hold)
	// when the scheduled ramp already covers the target.
	inFlight, err := app.Config.GetInFlightLadderCommitUSDHr(ctx, dbCfg.ID)
	if err != nil {
		return fmt.Errorf("GetInFlightLadderCommitUSDHr: %w", err)
	}
	if inFlight == nil {
		// The store must return a non-nil value; nil signals an impossible
		// query state (e.g. driver bug). Treat as fail-loud.
		return fmt.Errorf("GetInFlightLadderCommitUSDHr returned nil for config %s", dbCfg.ID)
	}

	supportedLayers := capability.SupportedLayers()

	// Run Allocate with the in-flight figure so the gap is net of the config's
	// prior scheduled (not-yet-fired) tranches.
	allocResult, err := pkgladder.Allocate(&pkgladder.AllocationInput{
		Now:                now,
		Baseline:           baseline,
		LayerStates:        layerStates,
		Layers:             supportedLayers,
		Config:             engineCfg,
		DataSources:        []string{dataSourceAWSCostExplorer},
		InFlightUSDPerHour: inFlight,
	})
	if err != nil {
		return fmt.Errorf("allocate: %w", err)
	}

	// Run BuildTranches to produce the ramp schedule.
	runID := uuid.New().String()
	trancheResult, err := pkgladder.BuildTranches(&pkgladder.TrancheInput{
		Config:        &engineCfg,
		RunID:         runID,
		Term:          term,
		PaymentOption: paymentOpt,
		NewID:         func() string { return uuid.New().String() },
		Now:           now,
		Allocations:   allocResult.Allocations,
	})
	if err != nil {
		return fmt.Errorf("BuildTranches: %w", err)
	}

	// Assemble LadderPlan (Q5: interleaves purchases AND reshapes in one JSON).
	plan := assembleLadderPlan(scope, now, allocResult, layerStates, baseline, engineCfg.TargetCoveragePct, term, paymentOpt)

	// Validate and persist the run row + tranche audit rows. L5 append-only:
	// SaveLadderRunWithTranches inserts the run and its NEW scheduled tranches
	// and leaves any existing scheduled tranches untouched (see persistLadderRun).
	return app.persistLadderRun(ctx, dbCfg, runID, now, plan, baseline, allocResult, trancheResult)
}

// ladderCadencePeriod returns the nominal planning period for a cadence. An
// unrecognized cadence is an error rather than a default: silently treating an
// unknown cadence as daily would shorten the window that protects a due tranche
// from premature expiry, which is a money-path decision.
func ladderCadencePeriod(cadence pkgladder.LadderCadence) (time.Duration, error) {
	switch cadence {
	case pkgladder.CadenceDaily:
		return cadenceDailyPeriod, nil
	case pkgladder.CadenceWeekly:
		return cadenceWeeklyPeriod, nil
	}
	return 0, fmt.Errorf("unknown cadence %q", cadence)
}

// expireElapsedLadderTranches cancels the config's scheduled tranches that are
// more than one full planning period past their fire date.
//
// A tranche leaves 'scheduled' only by firing, so one that is still scheduled a
// whole cadence after it was due never fired and never will. Until it is
// retired it keeps counting toward GetInFlightLadderCommitUSDHr, the netting
// keeps subtracting it from the gap, and every subsequent run Holds: the ladder
// stops planning with no error, looking exactly like "target coverage already
// met" (#1605).
//
// The one-period grace (strictly more than, so a tranche exactly one period
// past due survives) is deliberately generous: a tranche that is merely late is
// still genuinely in flight, and re-planning its amount while it can still fire
// would commit the money twice.
//
// A failed expiry is returned, not swallowed: proceeding would net stale
// tranches out of the gap and under-purchase silently.
func (app *Application) expireElapsedLadderTranches(ctx context.Context, configID string, cadence pkgladder.LadderCadence, now time.Time) error {
	period, err := ladderCadencePeriod(cadence)
	if err != nil {
		return fmt.Errorf("tranche expiry cutoff: %w", err)
	}
	cutoff := now.Add(-period)
	expired, err := app.Config.ExpireElapsedLadderTranches(ctx, configID, cutoff)
	if err != nil {
		return fmt.Errorf("ExpireElapsedLadderTranches: %w", err)
	}
	if expired > 0 {
		log.Printf("ladder_run: config %s: cancelled %d scheduled tranche(s) still un-fired before %s (cadence=%s); their commitment is no longer netted out of the gap",
			configID, expired, cutoff.Format(time.RFC3339), cadence)
	}
	return nil
}

// persistLadderRun validates the assembled plan and writes the ladder_runs row
// followed by its ladder_tranches audit rows. Split from executeLadderRun so
// each function stays within the project's cyclomatic-complexity limit (10).
// A failure at any step returns an error before the next write, so a config
// is never left with a half-persisted run.
func (app *Application) persistLadderRun(
	ctx context.Context,
	dbCfg *config.LadderConfigDB,
	runID string,
	now time.Time,
	plan *pkgladder.LadderPlan,
	baseline pkgladder.UsageBaseline,
	allocResult *pkgladder.AllocateResult,
	trancheResult *pkgladder.TrancheResult,
) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("LadderPlan.Validate: %w", err)
	}

	planJSON, err := marshalLadderPlan(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}

	// Map *big.Rat monetary snapshot to *float64 (nil stays nil).
	// These are stored in the run row for dashboards; the authoritative
	// detail is in plan_json.
	cfgID := dbCfg.ID
	modeStr := dbCfg.Mode
	cadenceStr := dbCfg.Cadence
	runRow := &config.LadderRunDB{
		ID:                runID,
		ConfigID:          &cfgID,
		StartedAt:         now,
		Status:            pkgladder.RunStatusPlanned,
		Mode:              &modeStr,
		Cadence:           &cadenceStr,
		Plan:              planJSON,
		BaselineUSDHr:     baseline.LowWaterUSDPerHour,
		TargetUSDHr:       ratToFloat64Ptr(plan.TargetUSDPerHour),
		ExistingUSDHr:     ratToFloat64Ptr(plan.ExistingUSDPerHour),
		GapUSDHr:          ratToFloat64Ptr(plan.GapUSDPerHour),
		TotalHourlyCommit: allocTotalHourlyCommit(allocResult.Allocations),
	}

	// Build the tranche audit rows (status=scheduled; no firing in PR-2). The
	// run ID is known up front (runRow.ID), so tranches can reference it before
	// the insert.
	trancheRows, err := buildTrancheDBRows(trancheResult.Tranches, runRow.ID, &cfgID)
	if err != nil {
		return fmt.Errorf("buildTrancheDBRows: %w", err)
	}

	// L5 APPEND-ONLY: always persist via the plain path, which INSERTS the new
	// scheduled tranches and NEVER cancels existing ones. In-flight netting
	// (GetInFlightLadderCommitUSDHr, subtracted from the gap in Allocate) has
	// already removed the config's existing scheduled tranches from the gap, so
	// trancheRows carries exactly the delta needed to reach target-E:
	//   - Hold (gap ~0): trancheRows is empty; only the run row is written and
	//     the existing scheduled ramp is preserved with its original fire dates.
	//   - Drift-up (gap re-opens): trancheRows is the top-up; appending it brings
	//     the total scheduled commitment to target-E while leaving (not re-dating
	//     or discarding) the prior generation intact.
	// Cancel-and-replace was removed because it fought the netting: on drift-up
	// it discarded the amount the netting had subtracted, capping coverage below
	// target and never converging.
	//
	// Drift-DOWN note: if usage drops after a ramp is planned, the too-large
	// scheduled ramp is preserved uncancelled (a Hold). That over-commitment is
	// an accepted, separately-tracked follow-up (drift-down shrink); it is
	// bounded by the low-water clamp in Allocate and not handled here.
	savedRun, err := app.Config.SaveLadderRunWithTranches(ctx, runRow, trancheRows)
	if err != nil {
		return fmt.Errorf("persist ladder run: %w", err)
	}

	log.Printf("ladder_run: config %s: persisted run %s (status=%s, allocations=%d, tranches=%d, holds=%d)",
		dbCfg.ID, savedRun.ID, savedRun.Status,
		len(allocResult.Allocations), len(trancheResult.Tranches), len(allocResult.Holds))
	return nil
}

// ladderWithinCadenceWindow reports whether a new run should be skipped because
// a recent run already covers the cadence window. It queries
// LatestLadderRunStartedAt for the config and returns:
//   - (true, reason, nil)  when a recent run is within the window (skip),
//   - (false, "", nil)     when the config is eligible to run,
//   - (false, "", err)     when the lookup itself failed.
//
// The lookup error is propagated (NOT swallowed) so the caller can fail CLOSED:
// if we cannot determine whether a recent run exists, proceeding risks a
// duplicate money action, so the caller counts the config Errored rather than
// running it.
func ladderWithinCadenceWindow(ctx context.Context, store config.StoreInterface, dbCfg *config.LadderConfigDB, now time.Time) (within bool, reason string, err error) {
	latest, err := store.LatestLadderRunStartedAt(ctx, dbCfg.ID)
	if err != nil {
		return false, "", fmt.Errorf("LatestLadderRunStartedAt: %w", err)
	}
	if latest == nil {
		return false, "", nil // no previous run, always eligible
	}
	elapsed := now.Sub(*latest)
	switch dbCfg.Cadence {
	case string(pkgladder.CadenceDaily):
		if elapsed < cadenceDailyMin {
			return true, fmt.Sprintf("cadence=daily: last run %v ago (threshold %v), skipping", elapsed.Round(time.Minute), cadenceDailyMin), nil
		}
	case string(pkgladder.CadenceWeekly):
		if elapsed < cadenceWeeklyMin {
			return true, fmt.Sprintf("cadence=weekly: last run %v ago (threshold %v), skipping", elapsed.Round(time.Minute), cadenceWeeklyMin), nil
		}
	default:
		// Unknown cadence: log but do not block the run. The LadderConfig
		// validator surfaces this as an error in ladderConfigToEngine (pre-persist,
		// fail-loud), so the run will fail before any money action is taken.
		log.Printf("ladder_run: config %s: unknown cadence=%q, proceeding without cadence gate", dbCfg.ID, dbCfg.Cadence)
	}
	return false, "", nil
}

// ladderConfigToEngine converts a LadderConfigDB row to a pkg/ladder LadderConfig.
// It parses typed enums at the boundary and fails loud on any unknown value.
func ladderConfigToEngine(dbCfg *config.LadderConfigDB, accountID string) (pkgladder.LadderConfig, error) {
	mode, err := pkgladder.ParseLadderMode(dbCfg.Mode)
	if err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("mode: %w", err)
	}
	cadence, err := pkgladder.ParseLadderCadence(dbCfg.Cadence)
	if err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("cadence: %w", err)
	}

	var ramp pkgladder.RampSchedule
	if err := json.Unmarshal(dbCfg.RampSchedule, &ramp); err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("ramp_schedule: %w", err)
	}

	return pkgladder.LadderConfig{
		Scope: pkgladder.Scope{
			Provider:  pkgcommon.ProviderAWS,
			AccountID: accountID,
		},
		Mode:                          mode,
		Cadence:                       cadence,
		Ramp:                          ramp,
		TargetCoveragePct:             dbCfg.TargetCoverage,
		BufferFraction:                dbCfg.BufferFraction,
		BaselinePercentile:            dbCfg.BaselinePercentile,
		LookbackDays:                  dbCfg.LookbackDays,
		MaxActionsPerRun:              dbCfg.MaxActionsPerRun,
		BufferUtilizationThresholdPct: dbCfg.BufferUtilizationThreshold,
		MaxHourlyCommitPerRun:         dbCfg.MaxHourlyCommitPerRun,
	}, nil
}

// assembleLadderPlan builds a LadderPlan from the Allocate result and the
// collected read-side data. Q5: Actions interleaves both purchases
// (Allocations) and Reshapes in one authoritative plan JSON. Term and
// PaymentOption are stamped on every ActionPurchase entry; they are required
// by PlannedAction.Validate for purchase actions.
//
// The monetary snapshot is derived to stay consistent with the authoritative
// allocation rather than recomputed independently:
//   - target = baseline low-water * targetCoveragePct/100 (the coverage goal),
//   - existing = sum of per-layer ExistingUSDPerHour,
//   - gap = sum of the planned allocation gaps (what is actually being placed
//     this run), NOT target - existing.
//
// When the baseline low-water is nil the run is a Hold-only no-op (Allocate
// returns no allocations), so target and gap stay nil (never 0-coerced);
// existing is still reported from the observed layer states.
func assembleLadderPlan(
	scope pkgladder.Scope,
	now time.Time,
	allocResult *pkgladder.AllocateResult,
	layerStates map[pkgladder.LayerType]pkgladder.LayerState,
	baseline pkgladder.UsageBaseline,
	targetCoveragePct float64,
	term pkgladder.Term,
	paymentOpt pkgladder.PaymentOption,
) *pkgladder.LadderPlan {
	actions := make([]pkgladder.PlannedAction, 0, len(allocResult.Allocations)+len(allocResult.Reshapes)+len(allocResult.Holds))
	// Purchase allocations come first so approval-email ordering is stable.
	for _, alloc := range allocResult.Allocations {
		actions = append(actions, pkgladder.PlannedAction{
			Action:           pkgladder.ActionPurchase,
			Layer:            alloc.Layer,
			AmountUSDPerHour: alloc.GapUSDPerHour,
			Term:             term,
			PaymentOption:    paymentOpt,
			Rationale:        alloc.Rationale,
			DataSources:      alloc.DataSources,
		})
	}
	// Reshapes follow; they carry no amount (AmountUSDPerHour must be nil).
	actions = append(actions, allocResult.Reshapes...)
	// Holds are informational; append last.
	actions = append(actions, allocResult.Holds...)

	// existing = sum of per-layer existing commitment (always reportable).
	var existingTotal float64
	for _, ls := range layerStates {
		if ls.ExistingUSDPerHour != nil {
			existingTotal += *ls.ExistingUSDPerHour
		}
	}
	existing := ratFromFloat(existingTotal)

	// target and gap are only meaningful when the baseline is available; nil
	// stays nil (a Hold-only no-op has no purchase target/gap to report).
	var target, gap *big.Rat
	if baseline.LowWaterUSDPerHour != nil {
		if low := ratFromFloat(*baseline.LowWaterUSDPerHour); low != nil {
			if pctFrac := ratFromFloat(targetCoveragePct / 100.0); pctFrac != nil {
				target = new(big.Rat).Mul(low, pctFrac)
			}
		}
		// gap is what is actually being placed this run (Σ allocation gaps),
		// consistent with total_hourly_commit, not target - existing.
		gap = sumAllocationGaps(allocResult.Allocations)
	}

	return &pkgladder.LadderPlan{
		Scope:              scope,
		GeneratedAt:        now,
		TargetUSDPerHour:   target,
		ExistingUSDPerHour: existing,
		GapUSDPerHour:      gap,
		Actions:            actions,
		Baseline:           baseline,
	}
}

// ratFromFloat converts a float64 to *big.Rat. It returns nil when
// big.Rat.SetFloat64 returns nil (non-finite inputs like NaN or Inf).
// This mirrors the project-wide convention for converting float64 to exact
// rational arithmetic at the boundary.
func ratFromFloat(f float64) *big.Rat {
	r := new(big.Rat)
	if r.SetFloat64(f) == nil {
		return nil
	}
	return r
}

// ratToFloat64Ptr converts a *big.Rat to a *float64 suitable for storage in
// a nullable DB column. nil stays nil (never 0-coerced).
func ratToFloat64Ptr(r *big.Rat) *float64 {
	if r == nil {
		return nil
	}
	f, _ := r.Float64()
	return &f
}

// sumAllocationGaps sums the GapUSDPerHour across all allocations as an exact
// *big.Rat. It returns a zero (non-nil) rat for an empty allocation set: a
// Hold-only run plans exactly $0 of new commitment, which is a meaningful
// value here (what is being placed), not "not computed".
func sumAllocationGaps(allocs []pkgladder.Allocation) *big.Rat {
	sum := new(big.Rat)
	for _, a := range allocs {
		if a.GapUSDPerHour != nil {
			sum.Add(sum, a.GapUSDPerHour)
		}
	}
	return sum
}

// allocTotalHourlyCommit sums the GapUSDPerHour across all allocations,
// converting to float64. This is the non-nullable total_hourly_commit stored
// in the run row; it starts at 0 for a no-op (Hold-only) plan, which is the
// correct semantic (zero new commitment, not "not computed").
func allocTotalHourlyCommit(allocs []pkgladder.Allocation) float64 {
	f, _ := sumAllocationGaps(allocs).Float64()
	return f
}

// buildTrancheDBRows converts pkg/ladder Tranche rows to config.LadderTrancheDB
// rows ready for SaveLadderTranches. It fails loud on a malformed
// AmountUSDPerHour (money path): rather than persist a silently 0-coerced
// $0 tranche, it returns an error so the whole run is counted Errored and
// nothing partial is written.
func buildTrancheDBRows(tranches []pkgladder.Tranche, runID string, configID *string) ([]config.LadderTrancheDB, error) {
	rows := make([]config.LadderTrancheDB, 0, len(tranches))
	for i := range tranches {
		tr := &tranches[i] // use pointer to avoid copying 152-byte Tranche struct per iteration
		// AmountUSDHr: parse the RatString back to float64. A missing or
		// malformed RatString means BuildTranches produced a broken tranche;
		// that should never happen after Validate() passes, so surface it as
		// an error instead of writing a $0 row.
		r, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
		if !ok || r == nil {
			return nil, fmt.Errorf("tranche %s: malformed amount_usd_per_hour %q (not a parseable rational)", tr.ID, tr.AmountUSDPerHour)
		}
		amountUSDHr, _ := r.Float64()
		runIDCopy := runID
		rows = append(rows, config.LadderTrancheDB{
			ID:            tr.ID,
			ConfigID:      configID,
			RunID:         &runIDCopy,
			LayerType:     tr.Layer,
			Term:          tr.Term,
			PaymentOption: tr.PaymentOption,
			Status:        pkgladder.TrancheStatusScheduled,
			AmountUSDHr:   amountUSDHr,
			ScheduledDate: tr.FireAfter,
		})
	}
	return rows, nil
}

// ladderPlanJSONAction is a JSON-serializable form of pkgladder.PlannedAction.
// PlannedAction.AmountUSDPerHour is *big.Rat (not JSON-friendly); this DTO
// converts it to *float64 at the boundary.
type ladderPlanJSONAction struct {
	Action        string   `json:"action"`
	Layer         string   `json:"layer"`
	AmountUSDHr   *float64 `json:"amount_usd_hr,omitempty"` // nil for Hold/Reshape
	Term          string   `json:"term,omitempty"`
	PaymentOption string   `json:"payment_option,omitempty"`
	Rationale     string   `json:"rationale"`
	DataSources   []string `json:"data_sources,omitempty"`
}

// ladderPlanJSONDTO is a JSON-serializable form of pkgladder.LadderPlan.
// The *big.Rat monetary fields are converted to *float64 so they round-trip
// cleanly through JSONB without binary encoding.
type ladderPlanJSONDTO struct {
	Scope         pkgladder.Scope         `json:"scope"`
	GeneratedAt   time.Time               `json:"generated_at"`
	TargetUSDHr   *float64                `json:"target_usd_hr,omitempty"`
	ExistingUSDHr *float64                `json:"existing_usd_hr,omitempty"`
	GapUSDHr      *float64                `json:"gap_usd_hr,omitempty"`
	Actions       []ladderPlanJSONAction  `json:"actions"`
	Baseline      pkgladder.UsageBaseline `json:"baseline"`
}

// marshalLadderPlan serializes a LadderPlan to JSON for storage in the
// plan JSONB column. *big.Rat fields are converted to *float64 at this
// boundary; nil stays nil.
func marshalLadderPlan(plan *pkgladder.LadderPlan) (json.RawMessage, error) {
	actions := make([]ladderPlanJSONAction, 0, len(plan.Actions))
	for _, a := range plan.Actions {
		action := ladderPlanJSONAction{
			Action:        string(a.Action),
			Layer:         string(a.Layer),
			AmountUSDHr:   ratToFloat64Ptr(a.AmountUSDPerHour),
			Term:          string(a.Term),
			PaymentOption: string(a.PaymentOption),
			Rationale:     a.Rationale,
			DataSources:   a.DataSources,
		}
		actions = append(actions, action)
	}
	dto := ladderPlanJSONDTO{
		Scope:         plan.Scope,
		GeneratedAt:   plan.GeneratedAt,
		TargetUSDHr:   ratToFloat64Ptr(plan.TargetUSDPerHour),
		ExistingUSDHr: ratToFloat64Ptr(plan.ExistingUSDPerHour),
		GapUSDHr:      ratToFloat64Ptr(plan.GapUSDPerHour),
		Actions:       actions,
		Baseline:      plan.Baseline,
	}
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// ladderTermFromYears converts a GlobalConfig.DefaultTerm (integer years) to
// a typed pkgladder.Term. Fails loud on any unrecognized value so money-path
// decisions never proceed with an unknown commitment term.
func ladderTermFromYears(years int) (pkgladder.Term, error) {
	switch years {
	case 1:
		return pkgladder.Term1Year, nil
	case 3:
		return pkgladder.Term3Year, nil
	default:
		return "", fmt.Errorf("unsupported default_term=%d years (allowed: 1, 3)", years)
	}
}
