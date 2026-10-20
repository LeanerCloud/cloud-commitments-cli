package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

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

// rankingRule names the ordering --max-instances consumes, so the cap can say
// which rule decided what survived. The two paths rank on different keys
// because they carry different data, and an operator reading the drop list has
// to know which one applied. Typed rather than a bare string so the call sites
// cannot invent a third wording that does not match any implemented ordering.
type rankingRule string

const (
	// rankBySavingsPercentage is the recommendation-driven path: scorer.Score
	// sorts on SavingsPercentage, an intensive rate, before the cap runs.
	rankBySavingsPercentage rankingRule = "highest savings-percentage recommendations first"
	// rankBySavingsPerInstance is the --input-csv path: the cap is a budget in
	// instances, and a CSV may lack the savings percentage (older reports), so
	// sortBySavingsPerInstance derives the rate from the EstimatedSavings and
	// Count columns every CSV carries.
	rankBySavingsPerInstance rankingRule = "highest savings-per-instance rows first (EstimatedSavings / Count, not savings percentage)"
)

// capBinds reports whether --max-instances will actually remove instances from
// recs, rather than being unset or already satisfied.
//
// applyGlobalInstanceLimit and requireRankingSignal have to agree on this
// exactly: the second refuses precisely the runs whose outcome the first would
// otherwise decide on an ordering that carries no information. If the two
// conditions drift apart, either a run is refused for a cap that changes
// nothing, or an unrankable run reaches the cap after all.
func capBinds(recs []common.Recommendation, cfg Config) bool {
	others := withoutSavingsPlans(recs)
	return cfg.MaxInstances > 0 && CalculateTotalInstances(others) > int(cfg.MaxInstances)
}

// applyGlobalInstanceLimit enforces --max-instances once across the entire run.
//
// The flag is documented as a hard cap on the total number of instances
// purchased across all recommendations, so it has to see every service and
// every region together. Applying it inside the per-region fetch instead caps
// each (service, region) pair independently and multiplies the operator's cap
// by the number of pairs.
//
// passed must already be ordered best-first by rule: ApplyInstanceLimit
// consumes the slice in order and drops the tail, so the ordering decides
// which commitments survive. scorer.Score guarantees the
// rankBySavingsPercentage ordering; scoreAndLimitCSVRecs establishes the
// rankBySavingsPerInstance one.
//
// Truncation can push a recommendation under --min-count, which is a hard
// floor rather than advice, so dropTruncatedBelowMinCount removes any such
// recommendation instead of purchasing it short.
//
// Nothing is truncated silently. Every reduced or dropped recommendation is
// named on stdout, and the drops are counted into the end-of-run summary.
func applyGlobalInstanceLimit(passed []common.Recommendation, cfg Config, rule rankingRule, drops *common.DropSummary) []common.Recommendation {
	if !capBinds(passed, cfg) {
		return passed
	}

	// Savings Plans are exempt from the cap and the post-cap floor: their Count
	// is a placeholder 1, not an instance count. The report and the floor see
	// only the instance-based rows, whose capped result is a prefix of its input.
	others := withoutSavingsPlans(passed)
	totalBefore := CalculateTotalInstances(others)
	limited := ApplyInstanceLimit(others, cfg.MaxInstances)
	limited, belowMin := dropTruncatedBelowMinCount(limited, cfg.MinCount)
	reportInstanceLimit(others, limited, len(belowMin), totalBefore, cfg.MaxInstances, rule, drops)
	reportMinCountDrops(belowMin, cfg.MinCount, drops)
	return mergeKeepingOrder(passed, limited)
}

// dropTruncatedBelowMinCount removes recommendations that the cap truncated to
// fewer instances than --min-count allows, returning the survivors and the
// removed recommendations at their truncated counts.
//
// --min-count is a hard floor everywhere else in the codebase, never advice:
// the scorer rejects recommendations under it outright
// (scorer.filterReason, "count %d below minimum %d"), the scheduler's
// meetsMinCount drops them, and both `docs/cli/filtering.md` and
// `docs/cli/README.md` describe it as dropping recommendations below the
// number. filtering.md applies it to "the adjusted instance count (after
// coverage scaling)", so the floor is meant to gate the *sized* count, and
// truncation by --max-instances is another form of sizing.
//
// Buying a commitment smaller than the operator's stated minimum can be worse
// than buying nothing, which is the whole reason the floor exists, so a
// truncated recommendation is dropped rather than purchased short. The freed
// budget is deliberately not redistributed: the next recommendation would have
// to fit in an even smaller remainder and would fail the same floor.
//
// Removal is always from the tail. ApplyInstanceLimit reduces at most one
// recommendation (the one where the budget runs out, which is the last it
// keeps), and every earlier one still carries the full count that already
// cleared the scorer's floor. Taking only from the tail keeps the result a
// prefix of the input, which reportInstanceLimit relies on.
func dropTruncatedBelowMinCount(limited []common.Recommendation, minCount int) (kept, removed []common.Recommendation) {
	if minCount <= 0 {
		return limited, nil
	}
	kept = limited
	for len(kept) > 0 && kept[len(kept)-1].Count < minCount {
		removed = append(removed, kept[len(kept)-1])
		kept = kept[:len(kept)-1]
	}
	return kept, removed
}

// reportMinCountDrops names each recommendation the --min-count floor rejected
// after --max-instances truncated it. It continues the reportInstanceLimit
// listing, where these already appear as dropped, and explains why they were
// not simply purchased at the reduced count.
func reportMinCountDrops(removed []common.Recommendation, minCount int, drops *common.DropSummary) {
	for i := range removed {
		rec := removed[i]
		AppLogger.Printf("     ↳ %s %s %s: the cap left room for only %d instances, below --min-count %d, so it is dropped rather than purchased short\n",
			rec.Service, rec.Region, rec.ResourceType, rec.Count, minCount)
	}
	drops.Add(common.DropMinCountAfterCap, len(removed))
}

// reportInstanceLimit prints what --max-instances removed from the run.
// after must be the prefix of before produced by ApplyInstanceLimit, so
// after[i] and before[i] describe the same recommendation.
//
// rule is the ordering the caller sorted before by, and is printed verbatim:
// the drop list is unreadable without knowing which key decided it, and the
// two paths do not rank on the same key.
//
// belowMinCount is how many of the missing entries were removed by the
// --min-count floor rather than by the budget. They are still listed here as
// dropped (they were), but they are attributed to --min-count-after-cap by
// reportMinCountDrops, so excluding them from this tally keeps each dropped
// recommendation counted exactly once in the end-of-run summary.
func reportInstanceLimit(before, after []common.Recommendation, belowMinCount, totalBefore int, maxInstances int32, rule rankingRule, drops *common.DropSummary) {
	AppLogger.Printf("\n🔒 --max-instances=%d caps the whole run: the %d recommendations that passed scoring total %d instances.\n",
		maxInstances, len(before), totalBefore)
	AppLogger.Printf("   Keeping the %s. The following are reduced or dropped:\n", rule)

	// after is a prefix of before, so membership is positional: every entry past
	// len(after) was removed, whatever its Count (a zero-Count entry equals its
	// own "kept" count of 0 and must still be counted as removed).
	reduced, dropped := 0, 0
	for i := range before {
		rec := before[i]
		if i >= len(after) {
			dropped++
			AppLogger.Printf("   • dropped: %s %s %s (%d instances)\n", rec.Service, rec.Region, rec.ResourceType, rec.Count)
			continue
		}
		if kept := after[i].Count; kept != rec.Count {
			reduced++
			AppLogger.Printf("   • reduced: %s %s %s %d → %d instances\n", rec.Service, rec.Region, rec.ResourceType, rec.Count, kept)
		}
	}

	// The floor only removes entries from the tail of what the cap kept, so
	// they are a subset of the removed entries. DropSummary.Add panics on a
	// negative count; surface the broken invariant with context instead.
	budgetDropped := dropped - belowMinCount
	if budgetDropped < 0 {
		panic(fmt.Sprintf("reportInstanceLimit: %d recommendations attributed to --min-count-after-cap but only %d were removed (before=%d, after=%d)",
			belowMinCount, dropped, len(before), len(after)))
	}
	drops.Add(common.DropMaxInstances, budgetDropped)
	AppLogger.Printf("   Proceeding with %d instances across %d recommendations (%d reduced, %d dropped).\n",
		CalculateTotalInstances(after), len(after), reduced, dropped)
}

// sumPassedRecs returns total instance count and total estimated savings for passed recs.
func sumPassedRecs(recs []common.Recommendation) (total int, totalSavings float64) {
	for _rvc := range recs {
		r := recs[_rvc]
		total += r.Count
		totalSavings += r.EstimatedSavings
	}
	return
}

// executePurchasePipeline purchases each rec in the passed list (or dry-runs) and writes audit records.
// It stops at the first audit failure on a real run and returns errAuditTrailLost.
func executePurchasePipeline(ctx context.Context, awsCfg aws.Config, recs []common.Recommendation, isDryRun bool, runID string, cfg Config) ([]common.PurchaseResult, error) {
	results := make([]common.PurchaseResult, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		if stopRequested(ctx) {
			log.Printf("Shutdown requested — skipping %d remaining recommendations", len(recs)-i)
			break
		}
		if err := probeAuditLog(isDryRun, cfg.AuditLog); err != nil {
			return results, err
		}
		// The purchase runs on a context that survives Ctrl-C: canceling the
		// request mid-flight can leave AWS having created the commitment
		// while the CLI records a failure, and a re-run then buys it again.
		// The per-call SDK timeouts still bound it.
		result, status := purchaseSingleRec(context.WithoutCancel(ctx), awsCfg, rec, i+1, isDryRun, cfg)
		results = append(results, result)
		if err := writePurchaseAuditRecord(runID, rec, result, status, isDryRun, cfg.AuditLog); err != nil {
			return results, err
		}
		if !isDryRun && i < len(recs)-1 {
			waitBetweenPurchases(ctx)
		}
	}
	return results, nil
}

// writePurchaseAuditRecord writes a single purchase's audit record. Shared by
// both purchase entry points -- executePurchasePipeline (the main pipeline)
// and processPurchaseLoop (the --input-csv path) -- so every recommendation
// that reaches a purchase attempt, dry-run or real, is recorded to
// cfg.AuditLog regardless of which one produced it. Before #1609,
// processPurchaseLoop never wrote a record at all, so CSV-mode purchases left
// no audit trail: on a partial failure there was no durable, per-recommendation
// record of which rows succeeded, so an operator could only re-run the whole
// file, which is a double purchase for the rows that already succeeded.
//
// A failed write on a real run returns errAuditTrailLost (#2103): the caller
// must stop buying. The record may be missing or written but not durable
// (sync failed); both count as lost. A dry run only warns.
func writePurchaseAuditRecord(runID string, rec common.Recommendation, result common.PurchaseResult, status string, isDryRun bool, auditLogPath string) error {
	auditRec := common.NewAuditRecord(runID, rec, result, status, isDryRun, common.PurchaseSourceCLI)
	if err := writeAuditRecordFn(auditRec, auditLogPath); err != nil {
		if isDryRun {
			log.Printf("Warning: failed to write audit record: %v", err)
			return nil
		}
		return fmt.Errorf("%w: write: %w", errAuditTrailLost, err)
	}
	return nil
}

// probeAuditLog checks, before a real purchase, that the audit log can still
// be appended to, so a log that broke after the start-of-run check stops the
// run before money moves. Dry runs skip it.
func probeAuditLog(isDryRun bool, auditLogPath string) error {
	if isDryRun {
		return nil
	}
	if err := common.CheckAuditLogWritable(auditLogPath); err != nil {
		return fmt.Errorf("%w: probe before purchase: %w", errAuditTrailLost, err)
	}
	return nil
}

// newServiceClient is the service-client factory used by purchaseSingleRec.
// It is a seam so tests can drive the real purchase path with a fake provider.
var newServiceClient = createServiceClient

// purchaseSucceeded reports whether a purchase result is a success: the
// provider set Success AND reported no error. A provider may return
// Success=true with a non-nil Error; that is a failure (issue #1900).
func purchaseSucceeded(result common.PurchaseResult) bool {
	return result.Success && result.Error == nil
}

// purchaseAuditStatus derives the audit status for a completed (non-dry-run)
// purchase attempt. Callers handle the dry-run ("skipped") case separately,
// since that never reaches a PurchaseResult from an actual API call.
func purchaseAuditStatus(result common.PurchaseResult) string {
	if purchaseSucceeded(result) {
		return "success"
	}
	return "error"
}

// purchaseSingleRec executes or dry-runs a single purchase and returns the result + audit status.
func purchaseSingleRec(ctx context.Context, awsCfg aws.Config, rec common.Recommendation, index int, isDryRun bool, cfg Config) (purchaseResult common.PurchaseResult, auditStatus string) {
	label := purchaseRegionLabel(rec, rec.Region)
	AppLogger.Printf("  [%d] %s %s %s (count=%d)\n", index, rec.Service, label, rec.ResourceType, rec.Count)
	if err := validatePurchasePreconditions(rec, rec.Region, cfg.CallerAccountID); err != nil {
		AppLogger.Printf("    ❌ cannot purchase: %v\n", err)
		return preconditionFailure(rec, err, isDryRun), "error"
	}
	if isDryRun {
		result := createDryRunResult(rec, label, index, cfg)
		AppLogger.Printf("    [dry-run] %s\n", result.CommitmentID)
		return result, "skipped"
	}

	clientRegion, err := clientRegionFor(rec.Service, rec.Region)
	if err != nil {
		AppLogger.Printf("    ❌ %v\n", err)
		return common.PurchaseResult{Recommendation: rec, Success: false, Error: err, Timestamp: time.Now()}, "error"
	}
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = clientRegion
	serviceClient := newServiceClient(rec.Service, regionalCfg)
	if serviceClient == nil {
		AppLogger.Printf("    ⚠️  No service client for %s\n", rec.Service)
		return common.PurchaseResult{Success: false}, "error"
	}

	result := executePurchase(ctx, rec, label, index, serviceClient, cfg)
	status := purchaseAuditStatus(result)
	if purchaseSucceeded(result) {
		AppLogger.Printf("    ✅ %s\n", result.CommitmentID)
	} else {
		AppLogger.Printf("    ❌ %v\n", result.Error)
	}
	return result, status
}

// buildServiceStats computes per-service statistics from a purchase run.
// Results are assumed to be in the same order as recs (1:1 correspondence).
func buildServiceStats(recs []common.Recommendation, results []common.PurchaseResult) map[common.ServiceType]ServiceProcessingStats {
	byService := make(map[common.ServiceType][]common.Recommendation)
	resultsByService := make(map[common.ServiceType][]common.PurchaseResult)
	for i := range recs {
		rec := recs[i]
		byService[rec.Service] = append(byService[rec.Service], rec)
		if i < len(results) {
			resultsByService[rec.Service] = append(resultsByService[rec.Service], results[i])
		}
	}
	stats := make(map[common.ServiceType]ServiceProcessingStats)
	for service, serviceRecs := range byService {
		stats[service] = calculateServiceStats(service, serviceRecs, resultsByService[service])
	}
	return stats
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
