package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/reporter"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/google/uuid"
)

// fetchExistingCoverage retrieves the existing-RI coverage map from Cost
// Explorer so --target-coverage sizing can subtract what's already owned in
// each pool. Skipping the fetch entirely when --target-coverage is not in
// play avoids the per-region CE charges for users on the --coverage path.
//
// Coverage is fetched per-region per-account so CE's org-wide aggregate
// doesn't bleed one account's coverage into another in multi-account orgs.
// Regions come from cfg.Regions if set, otherwise from EC2 DescribeRegions.
//
// The lookback window is cfg.CoverageLookbackDays (default 30, matching the
// CE UI default). Operators reconciling against the AWS console coverage
// report should match this value to the report's own time window.
//
// A failed fetch is not survivable on a real purchase run: the sizing
// formula computes gapPct := target - ExistingCoveragePct, and a nil map
// leaves every recommendation at ExistingCoveragePct == 0, so a fetch failure
// silently sizes as if the account owns nothing. Against an account already
// at 75% coverage, --target-coverage 80 would then buy another 80% on top and
// land near 155%. So a real purchase run returns the error and the caller
// must abort rather than proceed with a nil map. A dry run logs a loud
// warning and returns a nil map with no error, matching the previous
// best-effort behavior, since nothing is bought and reporting fidelity wins.
func fetchExistingCoverage(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, cfg Config) (recommendations.PoolCoverageMap, error) {
	if cfg.TargetCoverage <= 0 {
		return nil, nil
	}
	adapter, ok := recClient.(*awsprovider.RecommendationsClientAdapter)
	if !ok {
		// Non-AWS provider: feature not wired up. Sizing degenerates to
		// the no-existing-commitments path. Not a fetch failure, so this is
		// not subject to the fail-closed behavior below.
		return nil, nil
	}
	lookbackDays := coverageLookbackDays(cfg)
	regions := cfg.Regions
	if len(regions) == 0 {
		allRegions, err := getAllAWSRegions(ctx, awsCfg)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, coverageFetchFailure(cfg, fmt.Errorf("could not list AWS regions for coverage fetch: %w", err))
		}
		regions = allRegions
	}
	AppLogger.Printf("\n🔎 Fetching existing-RI coverage from Cost Explorer per-account across %d regions (lookback %d days)...\n", len(regions), lookbackDays)
	cov, err := adapter.GetRICoverageMap(ctx, lookbackDays, regions)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, coverageFetchFailure(cfg, fmt.Errorf("could not fetch existing-RI coverage: %w", err))
	}
	AppLogger.Printf("  ✅ Fetched coverage for %d (region, instance-type, engine, account) entries\n", len(cov))
	return cov, nil
}

// coverageFetchFailure decides how fetchExistingCoverage reports a failed
// coverage fetch: nil (best-effort, matching the pre-#1942 behavior) on a dry
// run, or the error itself on a real purchase run so the caller aborts
// instead of sizing every recommendation as if nothing is owned.
func coverageFetchFailure(cfg Config, err error) error {
	if effectiveDryRun(cfg) {
		AppLogger.Printf("  ⚠️  %v; sizing will assume zero existing coverage (dry run only; a real --purchase run aborts instead, since --target-coverage would overbuy on top of what is already owned)\n", err)
		return nil
	}
	return err
}

// shutdownRequested is set to true when SIGINT is received during a purchase run.
var shutdownRequested atomic.Bool

// interruptNotice is printed once when the run is interrupted.
const interruptNotice = "interrupt received: stopping after the in-flight purchase, if any; Ctrl-\\ force-quits and may lose its audit record"

// registerShutdownSignalHandler arms shutdownRequested for the duration of a
// purchase run and returns the cleanup func the caller must defer (e.g.
// `defer registerShutdownSignalHandler(ctx)()`). ctx is the invocation context
// that SIGINT cancels (see runTool): when it is canceled the flag is set and
// the notice is printed once. Shared by both purchase entry points --
// runToolMultiService and runToolFromCSV -- so an in-flight run on either
// path can be stopped cleanly between purchases with Ctrl-C.
func registerShutdownSignalHandler(ctx context.Context) func() {
	shutdownRequested.Store(false)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			log.Print(interruptNotice)
			shutdownRequested.Store(true)
		case <-done:
		}
	}()
	return func() { close(done) }
}

// stopRequested reports whether the run was interrupted: SIGINT cancels the
// invocation context and sets shutdownRequested.
func stopRequested(ctx context.Context) bool {
	return shutdownRequested.Load() || ctx.Err() != nil
}

// effectiveDryRun reports whether the run must stay in dry-run mode. A run is
// dry-run unless the user opts into real purchases with --purchase; that single
// flag is the only control. It defaults to false, so a bare invocation is a
// dry run and moving money is always an explicit opt-in. Both the non-CSV and
// CSV code paths use this helper so the guard is consistent and defined in one
// place.
func effectiveDryRun(cfg Config) bool {
	return !cfg.ActualPurchase
}

// runToolMultiService is the main entry point for processing multiple services.
// It runs a two-phase pipeline: (1) fetch+filter all recommendations, then
// (2) score, display, confirm, and purchase.
func runToolMultiService(ctx context.Context, cfg Config) {
	if cfg.CSVInput != "" {
		runCSVPathOrFatal(ctx, cfg)
		return
	}

	servicesToProcess, serviceErr := determineServicesToProcess(cfg)
	if serviceErr != nil {
		log.Fatalf("Invalid services: %v", serviceErr)
	}
	if len(servicesToProcess) == 0 {
		log.Fatalf("No valid services specified")
	}

	isDryRun := effectiveDryRun(cfg)

	// Register SIGINT handler so a running purchase loop can be interrupted cleanly.
	defer registerShutdownSignalHandler(ctx)()

	// Verify the audit log and its immediate parents before making cloud API calls.
	if err := CheckAuditLogWritable(cfg.AuditLog); err != nil {
		log.Fatalf("Cannot prepare durable audit log: %v", err) //nolint:gocritic // exitAfterDefer: intentional startup fatal before cleanup matters
	}

	printRunMode(isDryRun)
	AppLogger.Printf("📊 Processing services: %s\n", formatServices(servicesToProcess))
	printPaymentAndTerm(cfg)

	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	accountCache := NewAccountAliasCache(awsCfg)
	recClient := awsprovider.NewRecommendationsClient(awsCfg)
	if adapter, ok := recClient.(*awsprovider.RecommendationsClientAdapter); ok && cfg.RecLookbackPeriod != "" {
		adapter.SetRecLookbackPeriod(cfg.RecLookbackPeriod)
	}
	engineData := mustFetchEngineVersionData(ctx, cfg, extendedSupportCheckNeeded(cfg, servicesToProcess))

	// Fetch existing-RI coverage so --target-coverage can subtract what
	// the user already owns. On a dry run, a failure logs a warning and
	// continues with an empty map (sizing degenerates to the
	// no-existing-commitments path). On a real purchase run, a failure
	// aborts instead: sizing against a nil map would treat every
	// recommendation as if nothing is owned and overbuy on top of the
	// account's existing coverage.
	coverageMap, err := fetchExistingCoverage(ctx, awsCfg, recClient, cfg)
	if err != nil {
		log.Fatalf("Cannot size --target-coverage: %v", err)
	}

	// Phase 1: collect all recommendations without purchasing.
	AppLogger.Printf("\n📥 Fetching recommendations from all services...\n")
	allRecs, drops, err := fetchAllRecs(ctx, awsCfg, recClient, accountCache, servicesToProcess, engineData, cfg, coverageMap)
	exitOnFetchError(err)
	exitIfInterrupted(ctx, "fetching recommendations")

	// Phase 2: score, enforce the run-wide instance cap, and display.
	scoredResult := scoreLimitAndDisplay(allRecs, cfg, drops, coverageMap)
	if len(scoredResult.Passed) == 0 {
		printDropSummary(drops)
		AppLogger.Printf("\nℹ️  No recommendations passed filters. Nothing to purchase.\n")
		return
	}

	// Phases 3-4: confirm, purchase, and produce summary outputs.
	fatalOnError(runPurchaseAndReport(ctx, awsCfg, scoredResult, isDryRun, cfg, drops))
}

// fatalOnError exits 1 on a non-nil error. The report and summary were already
// written, so this is the last step of the run (untested glue, like
// runCSVPathOrFatal).
func fatalOnError(err error) {
	if err != nil {
		log.Fatalf("%v", err)
	}
}

// mustFetchEngineVersionData is fetchEngineVersionData for the main pipeline:
// an interrupt or unavailable exclusion data during the lifecycle queries ends
// the run, since sizing and offering purchases on missing lifecycle data would
// be wrong.
func mustFetchEngineVersionData(ctx context.Context, cfg Config, needed bool) engineVersionData {
	data, err := engineVersionFetcher(ctx, cfg, needed)
	if ctx.Err() != nil {
		log.Fatalf("Interrupted while querying engine versions (%v); nothing was purchased", err)
	}
	exitOnExclusionError(err)
	return data
}

// exitIfInterrupted ends the run when the invocation was canceled during a
// pre-purchase phase. Nothing has been bought at that point.
func exitIfInterrupted(ctx context.Context, phase string) {
	if ctx.Err() != nil {
		log.Fatalf("Interrupted while %s; nothing was purchased", phase)
	}
}

// runPurchaseAndReport handles the confirm, execute, and report phases of
// the multi-service pipeline. It is a separate function to keep
// runToolMultiService within the cyclomatic-complexity limit.
func runPurchaseAndReport(ctx context.Context, awsCfg aws.Config, scoredResult scorer.ScoredResult, isDryRun bool, cfg Config, drops *common.DropSummary) error {
	runID := uuid.New().String()
	cfg, err := withCallerAccount(ctx, awsCfg, cfg)
	if err != nil {
		return err
	}
	if !confirmPurchaseRun(scoredResult.Passed, isDryRun) {
		printDropSummary(drops)
		AppLogger.Printf("\n❌ Purchase canceled.\n")
		return nil
	}

	return executeAndReport(ctx, awsCfg, scoredResult.Passed, isDryRun, runID, cfg, drops)
}

// executeAndReport runs the purchase pipeline and produces the report and
// summary for whatever ran, interrupted or not. Split from
// runPurchaseAndReport so the post-confirmation half is testable without a
// terminal.
func executeAndReport(ctx context.Context, awsCfg aws.Config, passed []common.Recommendation, isDryRun bool, runID string, cfg Config, drops *common.DropSummary) error {
	allResults, auditErr := executePurchasePipeline(ctx, awsCfg, passed, isDryRun, runID, cfg)
	writeReportAndSummary(passed, allResults, isDryRun, cfg, drops)
	if stopRequested(ctx) {
		printInterruptedSummary(len(allResults), len(passed), allResults, isDryRun)
	}
	if auditErr != nil {
		AppLogger.Print(auditStopMessage(len(allResults), len(passed), allResults, auditErr))
	}
	return errors.Join(auditErr, purchaseFailureError(allResults, isDryRun))
}

// confirmPurchaseRun asks for confirmation once against the full
// recommendation set on a real purchase run, and reports whether the run
// should proceed. Always true on a dry run (nothing is bought, so there is
// nothing to confirm). Shared by the non-CSV pipeline (runPurchaseAndReport)
// and the --input-csv path (runToolFromCSV) so both entry points show the
// operator the total they are actually authorizing and require exactly one
// confirmation per invocation.
func confirmPurchaseRun(recs []common.Recommendation, isDryRun bool) bool {
	if isDryRun {
		return true
	}
	totalInstances, totalSavings := sumPassedRecs(recs)
	return ConfirmPurchase(totalInstances, totalSavings)
}

// writeReportAndSummary writes the CSV report and prints the final summary.
func writeReportAndSummary(passed []common.Recommendation, allResults []common.PurchaseResult, isDryRun bool, cfg Config, drops *common.DropSummary) {
	serviceStats := buildServiceStats(passed, allResults)
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)
	writeCSVReport(allResults, finalCSVOutput)
	printDropSummary(drops)
	printMultiServiceSummary(passed, allResults, serviceStats, isDryRun)
}

// printDropSummary writes the accumulated drop reasons at the terminal summary
// boundary. A nil or empty summary produces no output.
func printDropSummary(drops *common.DropSummary) {
	if drops == nil {
		return
	}
	if line := drops.FormatOneLine(); line != "" {
		AppLogger.Printf("\n%s\n", line)
	}
}

// callerAccountFetcher is the seam tests replace so they never reach STS.
var callerAccountFetcher = fetchCallerAccount

func fetchCallerAccount(ctx context.Context, awsCfg aws.Config) (string, error) {
	out, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	if aws.ToString(out.Account) == "" {
		return "", errors.New("GetCallerIdentity returned no account")
	}
	return aws.ToString(out.Account), nil
}

// withCallerAccount returns cfg with CallerAccountID resolved, so the purchase
// preconditions can refuse recommendations that belong to another account. It
// runs on dry runs too, so a dry run predicts what a real run will refuse.
func withCallerAccount(ctx context.Context, awsCfg aws.Config, cfg Config) (Config, error) {
	account, err := callerAccountFetcher(ctx, awsCfg)
	if err != nil {
		return cfg, fmt.Errorf("cannot determine the AWS account of the credentials (needed to check purchases): %w", err)
	}
	cfg.CallerAccountID = account
	return cfg, nil
}

// loadAWSConfig builds an aws.Config from the tool config.
func loadAWSConfig(ctx context.Context, cfg Config) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	opts = append(opts, awsconfig.WithRegion("us-east-1"))
	if cfg.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// scoreLimitAndDisplay runs the scorer on recs, enforces the run-wide
// --max-instances cap on the survivors, and prints the scored table and
// summary.
//
// The cap runs between scoring and rendering so the table, the confirmation
// prompt and the purchase loop all describe the same post-cap set, and so the
// instances that survive are the highest-savings ones run-wide (scorer.Score
// sorts Passed by savings percentage descending).
//
// RDS family coverage is re-derived after the cap (see rederiveRDSFamilyCoverage),
// because the cap, --count-override and the duplicate check all change Count
// after family-NU sizing wrote ProjectedCoverage (#2214). coverageMap must be
// the map the sizing stage used.
func scoreLimitAndDisplay(recs []common.Recommendation, cfg Config, drops *common.DropSummary, coverageMap recommendations.PoolCoverageMap) scorer.ScoredResult {
	scorerCfg := scorer.Config{
		MinSavingsPct:      cfg.MinSavingsPct,
		MaxBreakEvenMonths: cfg.MaxBreakEvenMonths,
		MinCount:           cfg.MinCount,
	}
	result := scorer.Score(recs, scorerCfg)
	result.Passed = applyGlobalInstanceLimit(result.Passed, cfg, rankBySavingsPercentage, drops)
	result.Passed = rederiveRDSFamilyCoverage(result.Passed, cfg, coverageMap)
	fmt.Print(reporter.RenderTable(result))
	fmt.Print(reporter.RenderExcluded(result))
	fmt.Print(reporter.RenderSummary(result))
	return result
}

// rederiveRDSFamilyCoverage refreshes the family-wide RDS ProjectedCoverage
// from the final counts. It only runs for --target-coverage, the sole path
// where ApplyFamilyNUSizingRDS wrote that field; on the legacy --coverage path
// the field is a per-pool value that RederiveFamilyCoverage would overwrite.
// The CSV path has no coverage map and does not call it.
func rederiveRDSFamilyCoverage(recs []common.Recommendation, cfg Config, coverageMap recommendations.PoolCoverageMap) []common.Recommendation {
	if cfg.TargetCoverage <= 0 {
		return recs
	}
	return recommendations.RederiveFamilyCoverage(recs, coverageMap)
}

// exitOnExclusionError aborts the run before any purchase when the
// extended-support exclusion cannot be applied (issue #2147).
func exitOnFetchError(err error) {
	if errors.Is(err, errRecommendationFetch) {
		log.Fatalf("Cannot complete the recommendation fetch: %v", err)
	}
	exitOnExclusionError(err)
}

func exitOnExclusionError(err error) {
	if err != nil {
		log.Fatalf("Cannot apply extended-support exclusion: %v", err)
	}
}

// coverageLookbackDays is the Cost Explorer coverage lookback: the one value
// given to GetRICoverageMap and to the RDS family sizing, which must agree.
func coverageLookbackDays(cfg Config) int {
	if cfg.CoverageLookbackDays <= 0 {
		return 30
	}
	return cfg.CoverageLookbackDays
}
