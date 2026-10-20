package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
)

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
	recs, err = loadCSVRecommendations(cfg.CSVInput, csvThresholdNeedsFor(cfg))
	if err != nil {
		return nil, aws.Config{}, "", fmt.Errorf("failed to read CSV file: %w", err)
	}
	AppLogger.Printf("✅ Loaded %d recommendations from CSV\n", len(recs))

	err = rejectDuplicateSavingsPlanRows(recs)
	if err != nil {
		return nil, aws.Config{}, "", err
	}

	recs, err = filterAndAdjustRecommendations(ctx, recs, csvModeCoverage, cfg)
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
	defer registerShutdownSignalHandler(ctx)()

	csvModeCoverage := determineCSVCoverage(cfg)

	recs, awsCfg, runID, err := prepareCSVPurchaseRun(ctx, cfg, csvModeCoverage, isDryRun)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}

	cfg, err = withCallerAccount(ctx, awsCfg, cfg)
	if err != nil {
		return err
	}
	return runCSVPurchases(ctx, awsCfg, recs, isDryRun, cfg, runID)
}

// runCSVPurchases groups the confirmed CSV rows by service and region, buys
// them, and writes the report and summary for whatever ran. A non-nil error
// is errAuditTrailLost and/or errPurchasesFailed.
func runCSVPurchases(ctx context.Context, awsCfg aws.Config, recs []common.Recommendation, isDryRun bool, cfg Config, runID string) error {
	// Purchase IDs label the sizing via cfg.Coverage (effectiveSizingPct);
	// pin it to the coverage the CSV sizing used so the label cannot drift (#2248).
	cfg.Coverage = determineCSVCoverage(cfg)

	// Create account alias cache for lookup
	accountCache := NewAccountAliasCache(awsCfg)

	// Populate account names from account IDs
	populateAccountNames(ctx, recs, accountCache)

	// Group recommendations by service and region
	recsByServiceRegion := groupRecommendationsByServiceRegion(recs)

	// Process purchases
	allResults := make([]common.PurchaseResult, 0)
	var serviceResults []common.PurchaseResult
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)
	// allAdjustedRecs accumulates post-dedup recommendations so the final summary
	// reflects what was actually processed rather than the pre-dedup input slice.
	allAdjustedRecs := make([]common.Recommendation, 0)
	var auditErr error // errAuditTrailLost: set by a region, stops the service loop too

	for service, regionRecs := range recsByServiceRegion {
		if stopRequested(ctx) {
			log.Printf("Shutdown requested; stopping before %s", getServiceDisplayName(service))
			break
		}

		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		var serviceRecs []common.Recommendation
		serviceRecs, serviceResults, auditErr = processCSVService(ctx, awsCfg, service, regionRecs, isDryRun, cfg, runID)
		allAdjustedRecs = append(allAdjustedRecs, serviceRecs...)

		// Add service results to overall results
		allResults = append(allResults, serviceResults...)

		// Calculate service statistics (using only this service's results)
		stats := calculateServiceStats(service, serviceRecs, serviceResults)
		serviceStats[service] = stats
		printServiceSummary(service, stats)
		if auditErr != nil {
			break
		}
	}

	// Generate CSV filename and write report
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)

	// Write CSV report
	writeCSVReport(allResults, finalCSVOutput)

	// Print final summary using the post-dedup slice so counts match what was
	// actually processed, not the pre-dedup input passed into the outer loop.
	printMultiServiceSummary(allAdjustedRecs, allResults, serviceStats, isDryRun)
	if stopRequested(ctx) {
		printInterruptedSummary(len(allResults), len(recs), allResults, isDryRun)
	}
	if auditErr != nil {
		AppLogger.Print(auditStopMessage(len(allResults), len(recs), allResults, auditErr))
	}
	return errors.Join(auditErr, purchaseFailureError(allResults, isDryRun))
}

// processCSVRegionPurchases handles a single (service, region) pair within
// the --input-csv purchase loop: builds the regional service client, runs
// the duplicate check, applies the --min-count floor to whatever the
// deduction left, and executes the purchase loop. ok=false means there is
// nothing to add for this region (no service client yet, or the duplicate
// check refused it) and the caller should move on to the next region.
// Extracted out of runToolFromCSV to keep it under the project's gocyclo
// budget.
func processCSVRegionPurchases(ctx context.Context, awsCfg aws.Config, service common.ServiceType, region string, recs []common.Recommendation, isDryRun bool, cfg Config, runID string) (processedRecs []common.Recommendation, results []common.PurchaseResult, ok bool, auditErr error) {
	clientRegion, err := clientRegionFor(service, region)
	if err != nil {
		AppLogger.Printf("  ❌ Cannot purchase %d %s recommendation(s) from the CSV: %v\n", len(recs), getServiceDisplayName(service), err)
		return nil, nil, false, nil
	}
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = clientRegion
	serviceClient := createServiceClient(service, regionalCfg)

	if serviceClient == nil {
		AppLogger.Printf("  ⚠️  Service client not yet implemented for %s\n", getServiceDisplayName(service))
		AppLogger.Printf("     (Skipping purchase phase for this service)\n")
		return nil, nil, false, nil
	}

	// Check for duplicate RIs to avoid double purchasing.
	adjustedRecs, dedupOK := checkDuplicatesForCSVRegion(ctx, recs, serviceClient, service, region, isDryRun)
	if !dedupOK {
		return nil, nil, false, nil
	}
	// Deducting existing commitments shrinks Count, which can push a row
	// that cleared the floor in filterAndAdjustRecommendations back under it
	// (--min-count 5, a row of 6, and 5 matching recent commitments would
	// otherwise be purchased at 1). --min-count is a floor on what gets
	// bought, so it is re-applied to whatever the deduction left, not only
	// to the pre-deduction counts.
	processedRecs = applyMinCountFloor(adjustedRecs, cfg.MinCount)

	results, auditErr = processPurchaseLoop(ctx, processedRecs, region, isDryRun, serviceClient, cfg, runID)
	return processedRecs, results, true, auditErr
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
	if ctx.Err() != nil {
		// Interrupted: terminal in dry and real runs; runToolFromCSV stops on ctx.Err().
		return nil, false
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
func filterAndAdjustRecommendations(ctx context.Context, recs []common.Recommendation, csvModeCoverage float64, cfg Config) ([]common.Recommendation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engineData, err := engineVersionFetcher(ctx, cfg, !cfg.IncludeExtendedSupport && hasDatabaseRecs(recs))
	if err != nil {
		return nil, err
	}
	if err := requireFilterInputs(recs, cfg, engineData); err != nil {
		return nil, err
	}

	// Apply filters (empty currentRegion since we're processing from CSV, not iterating regions).
	// Drop tracking is skipped on the CSV path (nil drops).
	originalCount := len(recs)
	recs = applyFilters(recs, &cfg, engineData.instanceVersions, engineData.versionInfo, "", nil)
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

// requireFilterInputs fails when the data the filters need is missing: RDS
// inventory for the extended-support exclusion, and account names for
// name-based account filters.
func requireFilterInputs(recs []common.Recommendation, cfg Config, engineData engineVersionData) error {
	if err := requireRegionInventory(recs, cfg, engineData); err != nil {
		return err
	}
	return requireResolvedAccountNames(recs, &cfg)
}
