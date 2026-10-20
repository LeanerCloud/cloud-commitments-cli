package main

import (
	"context"
	"fmt"
	"log"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
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
