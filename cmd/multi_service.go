package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
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
	lookbackDays := cfg.CoverageLookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 30
	}
	regions := cfg.Regions
	if len(regions) == 0 {
		allRegions, err := getAllAWSRegions(ctx, awsCfg)
		if err != nil {
			return nil, coverageFetchFailure(cfg, fmt.Errorf("could not list AWS regions for coverage fetch: %w", err))
		}
		regions = allRegions
	}
	AppLogger.Printf("\n🔎 Fetching existing-RI coverage from Cost Explorer per-account across %d regions (lookback %d days)...\n", len(regions), lookbackDays)
	cov, err := adapter.GetRICoverageMap(ctx, lookbackDays, regions)
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

// registerShutdownSignalHandler arms shutdownRequested for the duration of a
// purchase run and returns the cleanup func the caller must defer (e.g.
// `defer registerShutdownSignalHandler()()`). Shared by both purchase entry
// points -- runToolMultiService and runToolFromCSV -- so an in-flight run on
// either path can be stopped cleanly between purchases with Ctrl-C.
func registerShutdownSignalHandler() func() {
	shutdownRequested.Store(false)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() { <-sigCh; shutdownRequested.Store(true) }()
	return func() { signal.Stop(sigCh) }
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
	defer registerShutdownSignalHandler()()

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
	engineData := fetchEngineVersionData(ctx, cfg)

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
	allRecs, drops := fetchAllRecs(ctx, awsCfg, recClient, accountCache, servicesToProcess, engineData, cfg, coverageMap)

	// Phase 2: score, enforce the run-wide instance cap, and display.
	scoredResult := scoreLimitAndDisplay(allRecs, cfg, drops)
	if len(scoredResult.Passed) == 0 {
		printDropSummary(drops)
		AppLogger.Printf("\nℹ️  No recommendations passed filters. Nothing to purchase.\n")
		return
	}

	// Phases 3-4: confirm, purchase, and produce summary outputs.
	runPurchaseAndReport(ctx, awsCfg, scoredResult, isDryRun, cfg, drops)
}

// runPurchaseAndReport handles the confirm, execute, and report phases of
// the multi-service pipeline. It is a separate function to keep
// runToolMultiService within the cyclomatic-complexity limit.
func runPurchaseAndReport(ctx context.Context, awsCfg aws.Config, scoredResult scorer.ScoredResult, isDryRun bool, cfg Config, drops *common.DropSummary) {
	runID := uuid.New().String()
	if !confirmPurchaseRun(scoredResult.Passed, isDryRun) {
		printDropSummary(drops)
		AppLogger.Printf("\n❌ Purchase canceled.\n")
		return
	}

	allResults := executePurchasePipeline(ctx, awsCfg, scoredResult.Passed, isDryRun, runID, cfg)

	// Produce summary outputs.
	writeReportAndSummary(scoredResult.Passed, allResults, isDryRun, cfg, drops)
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
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}
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
func scoreLimitAndDisplay(recs []common.Recommendation, cfg Config, drops *common.DropSummary) scorer.ScoredResult {
	scorerCfg := scorer.Config{
		MinSavingsPct:      cfg.MinSavingsPct,
		MaxBreakEvenMonths: cfg.MaxBreakEvenMonths,
		MinCount:           cfg.MinCount,
	}
	result := scorer.Score(recs, scorerCfg)
	result.Passed = applyGlobalInstanceLimit(result.Passed, cfg, rankBySavingsPercentage, drops)
	fmt.Print(reporter.RenderTable(result))
	fmt.Print(reporter.RenderExcluded(result))
	fmt.Print(reporter.RenderSummary(result))
	return result
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
	// rankBySavingsPerInstance is the --input-csv path: a CSV row carries no
	// savings percentage, so sortBySavingsPerInstance derives the rate from
	// the EstimatedSavings and Count columns it does carry.
	rankBySavingsPerInstance rankingRule = "highest savings-per-instance rows first (a CSV row carries no savings percentage)"
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
	return cfg.MaxInstances > 0 && CalculateTotalInstances(recs) > int(cfg.MaxInstances)
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

	totalBefore := CalculateTotalInstances(passed)
	limited := ApplyInstanceLimit(passed, cfg.MaxInstances)
	limited, belowMin := dropTruncatedBelowMinCount(limited, cfg.MinCount)
	reportInstanceLimit(passed, limited, len(belowMin), totalBefore, cfg.MaxInstances, rule, drops)
	reportMinCountDrops(belowMin, cfg.MinCount, drops)
	return limited
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

	reduced, dropped := 0, 0
	for i := range before {
		rec := before[i]
		kept := 0
		if i < len(after) {
			kept = after[i].Count
		}
		switch {
		case kept == rec.Count:
			continue
		case kept > 0:
			reduced++
			AppLogger.Printf("   • reduced: %s %s %s %d → %d instances\n", rec.Service, rec.Region, rec.ResourceType, rec.Count, kept)
		default:
			dropped++
			AppLogger.Printf("   • dropped: %s %s %s (%d instances)\n", rec.Service, rec.Region, rec.ResourceType, rec.Count)
		}
	}

	drops.Add(common.DropMaxInstances, dropped-belowMinCount)
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
func executePurchasePipeline(ctx context.Context, awsCfg aws.Config, recs []common.Recommendation, isDryRun bool, runID string, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		if shutdownRequested.Load() {
			log.Printf("Shutdown requested — skipping %d remaining recommendations", len(recs)-i)
			break
		}
		result, status := purchaseSingleRec(ctx, awsCfg, rec, i+1, isDryRun, cfg)
		results = append(results, result)
		writePurchaseAuditRecord(runID, rec, result, status, isDryRun, cfg.AuditLog)
		if !isDryRun && i < len(recs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
			time.Sleep(PurchaseDelaySeconds * time.Second)
		}
	}
	return results
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
func writePurchaseAuditRecord(runID string, rec common.Recommendation, result common.PurchaseResult, status string, isDryRun bool, auditLogPath string) {
	auditRec := common.NewAuditRecord(runID, rec, result, status, isDryRun, common.PurchaseSourceCLI)
	if err := common.WriteAuditRecord(auditRec, auditLogPath); err != nil {
		log.Printf("Warning: failed to write audit record: %v", err)
	}
}

// purchaseAuditStatus derives the audit status for a completed (non-dry-run)
// purchase attempt. Callers handle the dry-run ("skipped") case separately,
// since that never reaches a PurchaseResult from an actual API call.
func purchaseAuditStatus(result common.PurchaseResult) string {
	if result.Success {
		return "success"
	}
	return "error"
}

// purchaseSingleRec executes or dry-runs a single purchase and returns the result + audit status.
func purchaseSingleRec(ctx context.Context, awsCfg aws.Config, rec common.Recommendation, index int, isDryRun bool, cfg Config) (purchaseResult common.PurchaseResult, auditStatus string) {
	label := purchaseRegionLabel(rec, rec.Region)
	AppLogger.Printf("  [%d] %s %s %s (count=%d)\n", index, rec.Service, label, rec.ResourceType, rec.Count)
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
	serviceClient := createServiceClient(rec.Service, regionalCfg)
	if serviceClient == nil {
		AppLogger.Printf("    ⚠️  No service client for %s\n", rec.Service)
		return common.PurchaseResult{Success: false}, "error"
	}

	result := executePurchase(ctx, rec, label, index, serviceClient, cfg)
	status := purchaseAuditStatus(result)
	if result.Success {
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

// runCSVPathOrFatal runs the CSV purchase path and exits fatally on error.
// It isolates the error-to-fatal glue from runToolMultiService so that path
// stays under the cyclomatic-complexity budget while runToolFromCSV remains
// unit-testable via its returned error.
func runCSVPathOrFatal(ctx context.Context, cfg Config) {
	if err := runToolFromCSV(ctx, cfg); err != nil {
		log.Fatalf("%v", err)
	}
}

// prepareCSVPurchaseRun validates and loads everything runToolFromCSV needs
// before the per-service purchase loop: the audit log writability, the CSV
// file, filtering/sizing, the single run-wide purchase confirmation, and the
// AWS config. Extracted to keep runToolFromCSV under the project's gocyclo
// budget.
//
// The audit-log check runs first and before any cloud API call, matching the
// non-CSV path (CheckAuditLogWritable in runToolMultiService). Before #1609
// this check ran only on the non-CSV path, so a CSV-mode purchase run could
// reach real purchase calls with no way to have written a durable,
// per-recommendation audit record even in principle.
//
// The confirmation runs once against the full post-filter set, as the
// non-CSV path does (confirmPurchaseRun, shared with runPurchaseAndReport).
// Before #1610 it was asked once per (service, region) with only that
// region's totals, and declining canceled only that region.
//
// A nil recs with a nil error means there is nothing to do (already logged):
// filtering left no recommendations, or the user declined the confirmation.
func prepareCSVPurchaseRun(ctx context.Context, cfg Config, csvModeCoverage float64, isDryRun bool) (recs []common.Recommendation, awsCfg aws.Config, runID string, err error) {
	if err = CheckAuditLogWritable(cfg.AuditLog); err != nil {
		return nil, aws.Config{}, "", fmt.Errorf("cannot write audit log: %w", err)
	}

	AppLogger.Printf("📄 Reading recommendations from CSV: %s\n", cfg.CSVInput)
	recs, err = loadRecommendationsFromCSV(cfg.CSVInput)
	if err != nil {
		return nil, aws.Config{}, "", fmt.Errorf("failed to read CSV file: %w", err)
	}
	AppLogger.Printf("✅ Loaded %d recommendations from CSV\n", len(recs))

	if err = rejectDuplicateSavingsPlanRows(recs); err != nil {
		return nil, aws.Config{}, "", err
	}

	recs, err = filterAndAdjustRecommendations(recs, csvModeCoverage, cfg)
	if err != nil {
		return nil, aws.Config{}, "", err
	}
	if len(recs) == 0 {
		AppLogger.Println("⚠️  No recommendations to process after filtering")
		return nil, aws.Config{}, "", nil
	}
	if !confirmPurchaseRun(recs, isDryRun) {
		AppLogger.Printf("\n❌ Purchase canceled.\n")
		return nil, aws.Config{}, "", nil
	}

	awsCfg, err = loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, aws.Config{}, "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	return recs, awsCfg, uuid.New().String(), nil
}

// runToolFromCSV processes recommendations from a CSV input file.
// It returns an error instead of exiting so the orchestration glue is
// unit-testable; the caller (runCSVPathOrFatal) turns errors fatal.
func runToolFromCSV(ctx context.Context, cfg Config) error {
	isDryRun := effectiveDryRun(cfg)
	printRunMode(isDryRun)

	// Register SIGINT handler so an in-flight purchase run can be stopped
	// cleanly between regions, matching the non-CSV path
	// (runToolMultiService). Before #1610 this path had no SIGINT handling
	// at all: runToolMultiService registers it only on the non-CSV branch,
	// in code unreachable from CSV mode (the CSV branch returns first).
	defer registerShutdownSignalHandler()()

	csvModeCoverage := determineCSVCoverage(cfg)

	recs, awsCfg, runID, err := prepareCSVPurchaseRun(ctx, cfg, csvModeCoverage, isDryRun)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}

	// Create account alias cache for lookup
	accountCache := NewAccountAliasCache(awsCfg)

	// Populate account names from account IDs
	populateAccountNames(ctx, recs, accountCache)

	// Group recommendations by service and region
	recsByServiceRegion := groupRecommendationsByServiceRegion(recs)

	// Process purchases
	allResults := make([]common.PurchaseResult, 0)
	serviceResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)
	// allAdjustedRecs accumulates post-dedup recommendations so the final summary
	// reflects what was actually processed rather than the pre-dedup input slice.
	allAdjustedRecs := make([]common.Recommendation, 0)

	for service, regionRecs := range recsByServiceRegion {
		if shutdownRequested.Load() {
			log.Printf("Shutdown requested; stopping before %s", getServiceDisplayName(service))
			break
		}

		// Reset service results for each service
		serviceResults = serviceResults[:0]

		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		serviceRecs := make([]common.Recommendation, 0)
		for region, recs := range regionRecs {
			if shutdownRequested.Load() {
				log.Printf("Shutdown requested; skipping remaining regions for %s", getServiceDisplayName(service))
				break
			}

			AppLogger.Printf("\n  📍 Region: %s (%d recommendations)\n", region, len(recs))

			processedRecs, regionResults, ok := processCSVRegionPurchases(ctx, awsCfg, service, region, recs, isDryRun, cfg, runID)
			if !ok {
				continue
			}

			serviceRecs = append(serviceRecs, processedRecs...)
			allAdjustedRecs = append(allAdjustedRecs, processedRecs...)
			serviceResults = append(serviceResults, regionResults...)
		}

		// Add service results to overall results
		allResults = append(allResults, serviceResults...)

		// Calculate service statistics (using only this service's results)
		stats := calculateServiceStats(service, serviceRecs, serviceResults)
		serviceStats[service] = stats
		printServiceSummary(service, stats)
	}

	// Generate CSV filename and write report
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)

	// Write CSV report
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary using the post-dedup slice so counts match what was
	// actually processed, not the pre-dedup input passed into the outer loop.
	printMultiServiceSummary(allAdjustedRecs, allResults, serviceStats, isDryRun)
	return nil
}

// processCSVRegionPurchases handles a single (service, region) pair within
// the --input-csv purchase loop: builds the regional service client, runs
// the duplicate check, applies the --min-count floor to whatever the
// deduction left, and executes the purchase loop. ok=false means there is
// nothing to add for this region (no service client yet, or the duplicate
// check refused it) and the caller should move on to the next region.
// Extracted out of runToolFromCSV to keep it under the project's gocyclo
// budget.
func processCSVRegionPurchases(ctx context.Context, awsCfg aws.Config, service common.ServiceType, region string, recs []common.Recommendation, isDryRun bool, cfg Config, runID string) (processedRecs []common.Recommendation, results []common.PurchaseResult, ok bool) {
	clientRegion, err := clientRegionFor(service, region)
	if err != nil {
		AppLogger.Printf("  ❌ Cannot purchase %d %s recommendation(s) from the CSV: %v\n", len(recs), getServiceDisplayName(service), err)
		return nil, nil, false
	}
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = clientRegion
	serviceClient := createServiceClient(service, regionalCfg)

	if serviceClient == nil {
		AppLogger.Printf("  ⚠️  Service client not yet implemented for %s\n", getServiceDisplayName(service))
		AppLogger.Printf("     (Skipping purchase phase for this service)\n")
		return nil, nil, false
	}

	// Check for duplicate RIs to avoid double purchasing.
	adjustedRecs, dedupOK := checkDuplicatesForCSVRegion(ctx, recs, serviceClient, service, region, isDryRun)
	if !dedupOK {
		return nil, nil, false
	}
	// Deducting existing commitments shrinks Count, which can push a row
	// that cleared the floor in filterAndAdjustRecommendations back under it
	// (--min-count 5, a row of 6, and 5 matching recent commitments would
	// otherwise be purchased at 1). --min-count is a floor on what gets
	// bought, so it is re-applied to whatever the deduction left, not only
	// to the pre-deduction counts.
	processedRecs = applyMinCountFloor(adjustedRecs, cfg.MinCount)

	results = processPurchaseLoop(ctx, processedRecs, region, isDryRun, serviceClient, cfg, runID)
	return processedRecs, results, true
}

// checkDuplicatesForCSVRegion runs the duplicate check for a single
// (service, region) pair in CSV mode and reports whether the caller should
// still process that region (ok). The duplicate check is the only guard
// between a re-run and a double purchase, so a failed check must not fall
// back to the un-deduplicated counts on a purchase run: that would buy
// reserved capacity the account already owns. A dry run logs a loud warning
// and continues with the un-deduplicated counts (nothing is bought, so
// reporting fidelity wins); a purchase run refuses to spend and returns
// ok=false, mirroring the fail-closed behavior of checkDuplicates on the
// non-CSV pipeline.
func checkDuplicatesForCSVRegion(ctx context.Context, recs []common.Recommendation, serviceClient provider.ServiceClient, service common.ServiceType, region string, isDryRun bool) (adjustedRecs []common.Recommendation, ok bool) {
	adjustedRecs, err := adjustRecsForDuplicates(ctx, recs, serviceClient)
	if err == nil {
		return adjustedRecs, true
	}
	if !isDryRun {
		AppLogger.Printf("  ❌ Refusing to purchase %s/%s: could not check for existing RIs (%v). Skipping this region rather than risking a duplicate purchase.\n", getServiceDisplayName(service), region, err)
		return nil, false
	}
	AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v (dry run; continuing with un-deduplicated counts)\n", err)
	return recs, true
}

// filterAndAdjustRecommendations applies filters, coverage, count override,
// the --min-count floor and the run-wide --max-instances cap to
// recommendations loaded from --input-csv.
//
// It returns an error when the cap cannot be enforced honestly; see
// requireRankingSignal.
func filterAndAdjustRecommendations(recs []common.Recommendation, csvModeCoverage float64, cfg Config) ([]common.Recommendation, error) {
	// Query running instances for engine version validation
	log.Printf("🔍 Querying running RDS instances across all regions to validate engine versions...")
	instanceVersions, err := queryRunningInstanceEngineVersions(context.Background(), cfg)
	if err != nil {
		log.Printf("⚠️  Warning: Failed to query running instances for engine version validation: %v", err)
		log.Printf("   Continuing without engine version filtering")
		instanceVersions = make(map[string][]InstanceEngineVersion)
	} else {
		log.Printf("✅ Found %d instance types with version information across all regions", len(instanceVersions))
	}

	// Query major engine versions for extended support detection
	log.Printf("🔍 Querying AWS RDS major engine versions for extended support information...")
	versionInfo, err := queryMajorEngineVersions(context.Background(), cfg)
	if err != nil {
		log.Printf("⚠️  Warning: Failed to query major engine versions: %v", err)
		log.Printf("   Continuing without extended support detection")
		versionInfo = make(map[string]MajorEngineVersionInfo)
	} else {
		log.Printf("✅ Found support information for %d major engine versions", len(versionInfo))
	}

	// Apply filters (empty currentRegion since we're processing from CSV, not iterating regions).
	// Drop tracking is skipped on the CSV path (nil drops).
	originalCount := len(recs)
	recs = applyFilters(recs, &cfg, instanceVersions, versionInfo, "", nil)
	if len(recs) < originalCount {
		AppLogger.Printf("🔍 After filters: %d recs (filtered out %d)\n", len(recs), originalCount-len(recs))
	}

	// Apply sizing — target-coverage if set, otherwise coverage.
	// Coverage 100% is a no-op (early-returned inside ApplyCoverage), but
	// --target-coverage always applies even at coverage 100%, so the
	// CSV-path short-circuit is conditional on TargetCoverage == 0.
	if cfg.TargetCoverage > 0 || csvModeCoverage < 100 {
		beforeSize := len(recs)
		recs = applySizing(recs, cfg, csvModeCoverage, nil)
		if cfg.TargetCoverage > 0 {
			AppLogger.Printf("🎯 Applying %.1f%% target-coverage: %d recs selected (from %d)\n", cfg.TargetCoverage, len(recs), beforeSize)
		} else {
			AppLogger.Printf("📈 Applying %.1f%% coverage: %d recs selected (from %d)\n", csvModeCoverage, len(recs), beforeSize)
		}
	}

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		recs = ApplyCountOverride(recs, cfg.OverrideCount)
	}

	// Enforce --min-count and the run-wide --max-instances cap.
	return scoreAndLimitCSVRecs(recs, cfg)
}

// processService processes a single service and returns recommendations and results.
// Used by legacy callers; new code should use fetchAllRecs + executePurchasePipeline.
func processService(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, accountCache *AccountAliasCache, service common.ServiceType, isDryRun bool, cfg Config, engineData engineVersionData) ([]common.Recommendation, []common.PurchaseResult) { //nolint:unparam // engineData always nil at current callsites but param is part of the API
	regionsToProcess, err := determineRegionsForService(ctx, awsCfg, recClient, service, cfg.Regions)
	if err != nil {
		log.Printf("❌ Failed to determine regions: %v", err)
		return nil, nil
	}

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	for i, region := range regionsToProcess {
		// Legacy single-service entry point — no coverage map is fetched here,
		// so sizing falls back to the no-existing-commitments formula. The new
		// path (runToolMultiService) fetches coverage once and threads it through.
		regionResult := processRegionRecommendations(
			ctx, awsCfg, recClient, accountCache,
			service, region, i+1, len(regionsToProcess),
			engineData, isDryRun, cfg, nil,
		)
		serviceRecs = append(serviceRecs, regionResult.recommendations...)
		serviceResults = append(serviceResults, regionResult.results...)
	}

	return serviceRecs, serviceResults
}

// processPurchaseLoop processes purchases for a single region (used by CSV
// mode). runID groups every recommendation processed across the whole CSV
// run into one audit trail, matching how executePurchasePipeline (the main
// pipeline) generates one runID per invocation. Confirmation is not asked
// here: prepareCSVPurchaseRun confirms once for the whole run before this
// loop is reached.
func processPurchaseLoop(ctx context.Context, recs []common.Recommendation, region string, isDryRun bool, serviceClient provider.ServiceClient, cfg Config, runID string) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recs))

	for j := range recs {
		if shutdownRequested.Load() {
			log.Printf("Shutdown requested; skipping %d remaining recommendation(s) in %s", len(recs)-j, region)
			break
		}

		rec := recs[j]
		label := purchaseRegionLabel(rec, region)
		AppLogger.Printf("    [%d/%d] Processing: %s %s\n", j+1, len(recs), rec.Service, rec.ResourceType)
		AppLogger.Printf("    💳 Purchasing %d instances\n", rec.Count)

		var result common.PurchaseResult
		var status string
		if isDryRun {
			result = createDryRunResult(rec, label, j+1, cfg)
			status = "skipped"
		} else {
			// Execute actual purchase
			result = executePurchase(ctx, rec, label, j+1, serviceClient, cfg)
			status = purchaseAuditStatus(result)

			// Add delay between purchases to avoid rate limiting
			if j < len(recs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
				time.Sleep(PurchaseDelaySeconds * time.Second)
			}
		}

		writePurchaseAuditRecord(runID, rec, result, status, isDryRun, cfg.AuditLog)
		results = append(results, result)

		if result.Success {
			AppLogger.Printf("    ✅ Success: %s\n", result.CommitmentID)
		} else {
			errMsg := "unknown error"
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			AppLogger.Printf("    ❌ Failed: %s\n", errMsg)
		}
	}

	return results
}
