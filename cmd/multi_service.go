package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/LeanerCloud/CUDly/internal/reporter"
	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/LeanerCloud/CUDly/pkg/scorer"
	awsprovider "github.com/LeanerCloud/CUDly/providers/aws"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/google/uuid"
)

// shutdownRequested is set to true when SIGINT is received during a purchase run.
var shutdownRequested atomic.Bool

// runToolMultiService is the main entry point for processing multiple services.
// It runs a two-phase pipeline: (1) fetch+filter all recommendations, then
// (2) score, display, confirm, and purchase.
func runToolMultiService(ctx context.Context, cfg Config) {
	if cfg.CSVInput != "" {
		runToolFromCSV(ctx, cfg)
		return
	}

	servicesToProcess := determineServicesToProcess(cfg)
	if len(servicesToProcess) == 0 {
		log.Fatalf("No valid services specified")
	}

	isDryRun := !cfg.ActualPurchase || cfg.DryRun

	// Register SIGINT handler so a running purchase loop can be interrupted cleanly.
	shutdownRequested.Store(false)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() { <-sigCh; shutdownRequested.Store(true) }()
	defer signal.Stop(sigCh)

	// Verify audit log is writable before making any cloud API calls.
	if err := CheckAuditLogWritable(cfg.AuditLog); err != nil {
		log.Fatalf("Cannot write audit log: %v", err)
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
	engineData := fetchEngineVersionData(ctx, cfg)

	// Phase 1: collect all recommendations without purchasing.
	AppLogger.Printf("\n📥 Fetching recommendations from all services...\n")
	allRecs := fetchAllRecs(ctx, awsCfg, recClient, accountCache, servicesToProcess, engineData, cfg)

	// Phase 2: score and display.
	scoredResult := scoreAndDisplay(allRecs, cfg)
	if len(scoredResult.Passed) == 0 {
		AppLogger.Printf("\nℹ️  No recommendations passed filters. Nothing to purchase.\n")
		return
	}

	// Phase 3: confirm (skipped in dry-run).
	runID := uuid.New().String()
	if !isDryRun {
		totalInstances, totalSavings := sumPassedRecs(scoredResult.Passed)
		if !ConfirmPurchase(totalInstances, totalSavings, cfg.SkipConfirmation) {
			AppLogger.Printf("\n❌ Purchase cancelled.\n")
			return
		}
	}

	// Phase 4: purchase each recommendation and write audit records.
	allResults := executePurchasePipeline(ctx, awsCfg, scoredResult.Passed, isDryRun, runID, cfg)

	// Produce summary outputs.
	serviceStats := buildServiceStats(scoredResult.Passed, allResults)
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}
	printMultiServiceSummary(scoredResult.Passed, allResults, serviceStats, isDryRun)
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

// scoreAndDisplay runs the scorer on recs and prints the scored table and summary.
func scoreAndDisplay(recs []common.Recommendation, cfg Config) scorer.ScoredResult {
	scorerCfg := scorer.Config{
		MinSavingsPct:      cfg.MinSavingsPct,
		MaxBreakEvenMonths: cfg.MaxBreakEvenMonths,
		MinCount:           cfg.MinCount,
	}
	result := scorer.Score(recs, scorerCfg)
	fmt.Print(reporter.RenderTable(result))
	fmt.Print(reporter.RenderExcluded(result))
	fmt.Print(reporter.RenderSummary(result))
	return result
}

// sumPassedRecs returns total instance count and total estimated savings for passed recs.
func sumPassedRecs(recs []common.Recommendation) (int, float64) {
	total := 0
	savings := 0.0
	for _, r := range recs {
		total += r.Count
		savings += r.EstimatedSavings
	}
	return total, savings
}

// executePurchasePipeline purchases each rec in the passed list (or dry-runs) and writes audit records.
func executePurchasePipeline(ctx context.Context, awsCfg aws.Config, recs []common.Recommendation, isDryRun bool, runID string, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recs))
	for i, rec := range recs {
		if shutdownRequested.Load() {
			log.Printf("Shutdown requested — skipping %d remaining recommendations", len(recs)-i)
			break
		}
		result, status := purchaseSingleRec(ctx, awsCfg, rec, i+1, isDryRun, cfg)
		results = append(results, result)
		auditRec := common.NewAuditRecord(runID, rec, result, status, isDryRun)
		if err := common.WriteAuditRecord(auditRec, cfg.AuditLog); err != nil {
			log.Printf("Warning: failed to write audit record: %v", err)
		}
		if !isDryRun && i < len(recs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
			time.Sleep(PurchaseDelaySeconds * time.Second)
		}
	}
	return results
}

// purchaseSingleRec executes or dry-runs a single purchase and returns the result + audit status.
func purchaseSingleRec(ctx context.Context, awsCfg aws.Config, rec common.Recommendation, index int, isDryRun bool, cfg Config) (common.PurchaseResult, string) {
	AppLogger.Printf("  [%d] %s %s %s (count=%d)\n", index, rec.Service, rec.Region, rec.ResourceType, rec.Count)
	if isDryRun {
		result := createDryRunResult(rec, rec.Region, index, cfg)
		AppLogger.Printf("    [dry-run] %s\n", result.CommitmentID)
		return result, "skipped"
	}

	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = rec.Region
	serviceClient := createServiceClient(rec.Service, regionalCfg)
	if serviceClient == nil {
		AppLogger.Printf("    ⚠️  No service client for %s\n", rec.Service)
		return common.PurchaseResult{Success: false}, "error"
	}

	result := executePurchase(ctx, rec, rec.Region, index, serviceClient, cfg)
	status := "success"
	if !result.Success {
		status = "error"
		AppLogger.Printf("    ❌ %v\n", result.Error)
	} else {
		AppLogger.Printf("    ✅ %s\n", result.CommitmentID)
	}
	return result, status
}

// buildServiceStats computes per-service statistics from a purchase run.
// Results are assumed to be in the same order as recs (1:1 correspondence).
func buildServiceStats(recs []common.Recommendation, results []common.PurchaseResult) map[common.ServiceType]ServiceProcessingStats {
	byService := make(map[common.ServiceType][]common.Recommendation)
	resultsByService := make(map[common.ServiceType][]common.PurchaseResult)
	for i, rec := range recs {
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

// runToolFromCSV processes recommendations from a CSV input file
func runToolFromCSV(ctx context.Context, cfg Config) {
	// Determine if this is a dry run
	isDryRun := !cfg.ActualPurchase
	printRunMode(isDryRun)

	csvModeCoverage := determineCSVCoverage(cfg)

	AppLogger.Printf("📄 Reading recommendations from CSV: %s\n", cfg.CSVInput)

	// Read recommendations from CSV
	recommendations, err := loadRecommendationsFromCSV(cfg.CSVInput)
	if err != nil {
		log.Fatalf("Failed to read CSV file: %v", err)
	}

	AppLogger.Printf("✅ Loaded %d recommendations from CSV\n", len(recommendations))

	// Filter and adjust recommendations
	recommendations = filterAndAdjustRecommendations(recommendations, csvModeCoverage, cfg)

	if len(recommendations) == 0 {
		AppLogger.Println("⚠️  No recommendations to process after filtering")
		return
	}

	// Load AWS configuration
	var configOptions []func(*awsconfig.LoadOptions) error
	configOptions = append(configOptions, awsconfig.WithRegion("us-east-1"))
	if cfg.Profile != "" {
		configOptions = append(configOptions, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Create account alias cache for lookup
	accountCache := NewAccountAliasCache(awsCfg)

	// Populate account names from account IDs
	populateAccountNames(ctx, recommendations, accountCache)

	// Group recommendations by service and region
	recsByServiceRegion := groupRecommendationsByServiceRegion(recommendations)

	// Process purchases
	allResults := make([]common.PurchaseResult, 0)
	serviceResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for service, regionRecs := range recsByServiceRegion {
		// Reset service results for each service
		serviceResults = serviceResults[:0]

		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		serviceRecs := make([]common.Recommendation, 0)
		for region, recs := range regionRecs {
			AppLogger.Printf("\n  📍 Region: %s (%d recommendations)\n", region, len(recs))

			// Get service client for this region
			regionalCfg := awsCfg.Copy()
			regionalCfg.Region = region
			serviceClient := createServiceClient(service, regionalCfg)

			if serviceClient == nil {
				AppLogger.Printf("  ⚠️  Service client not yet implemented for %s\n", getServiceDisplayName(service))
				AppLogger.Printf("     (Skipping purchase phase for this service)\n")
				continue
			}

			// Check for duplicate RIs to avoid double purchasing
			adjustedRecs, err := adjustRecsForDuplicates(ctx, recs, serviceClient)
			if err != nil {
				AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v\n", err)
				adjustedRecs = recs // Continue with original recommendations if check fails
			}
			recs = adjustedRecs

			serviceRecs = append(serviceRecs, recs...)

			// Process purchases for this region
			regionResults := processPurchaseLoop(ctx, recs, region, isDryRun, serviceClient, cfg)
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

	// Print final summary
	printMultiServiceSummary(recommendations, allResults, serviceStats, isDryRun)
}

// filterAndAdjustRecommendations applies filters, coverage, count override, and instance limits to recommendations
func filterAndAdjustRecommendations(recommendations []common.Recommendation, csvModeCoverage float64, cfg Config) []common.Recommendation {
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

	// Apply filters (empty currentRegion since we're processing from CSV, not iterating regions)
	originalCount := len(recommendations)
	recommendations = applyFilters(recommendations, cfg, instanceVersions, versionInfo, "")
	if len(recommendations) < originalCount {
		AppLogger.Printf("🔍 After filters: %d recommendations (filtered out %d)\n", len(recommendations), originalCount-len(recommendations))
	}

	// Apply coverage if not 100%
	if csvModeCoverage < 100 {
		beforeCoverage := len(recommendations)
		recommendations = applyCommonCoverage(recommendations, csvModeCoverage)
		AppLogger.Printf("📈 Applying %.1f%% coverage: %d recommendations selected (from %d)\n", csvModeCoverage, len(recommendations), beforeCoverage)
	}

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		recommendations = ApplyCountOverride(recommendations, cfg.OverrideCount)
	}

	// Apply instance limit if specified
	if cfg.MaxInstances > 0 {
		beforeLimit := len(recommendations)
		recommendations = ApplyInstanceLimit(recommendations, cfg.MaxInstances)
		if len(recommendations) < beforeLimit {
			AppLogger.Printf("🔒 Applied instance limit: %d recommendations after limiting to %d instances\n", len(recommendations), cfg.MaxInstances)
		}
	}

	return recommendations
}

// processService processes a single service and returns recommendations and results.
// Used by legacy callers; new code should use fetchAllRecs + executePurchasePipeline.
func processService(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, accountCache *AccountAliasCache, service common.ServiceType, isDryRun bool, cfg Config, engineData engineVersionData) ([]common.Recommendation, []common.PurchaseResult) {
	regionsToProcess, err := determineRegionsForService(ctx, awsCfg, recClient, service, cfg.Regions)
	if err != nil {
		log.Printf("❌ Failed to determine regions: %v", err)
		return nil, nil
	}

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	for i, region := range regionsToProcess {
		regionResult := processRegionRecommendations(
			ctx, awsCfg, recClient, accountCache,
			service, region, i+1, len(regionsToProcess),
			engineData, isDryRun, cfg,
		)
		serviceRecs = append(serviceRecs, regionResult.recommendations...)
		serviceResults = append(serviceResults, regionResult.results...)
	}

	return serviceRecs, serviceResults
}

// processPurchaseLoop processes purchases for a single region (used by CSV mode)
func processPurchaseLoop(ctx context.Context, recs []common.Recommendation, region string, isDryRun bool, serviceClient provider.ServiceClient, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recs))

	for j, rec := range recs {
		AppLogger.Printf("    [%d/%d] Processing: %s %s\n", j+1, len(recs), rec.Service, rec.ResourceType)
		AppLogger.Printf("    💳 Purchasing %d instances\n", rec.Count)

		var result common.PurchaseResult
		if isDryRun {
			result = createDryRunResult(rec, region, j+1, cfg)
		} else {
			// Ask for confirmation before proceeding with purchases (only on first item)
			if j == 0 {
				totalInstances := CalculateTotalInstances(recs)
				totalSavings := 0.0
				for _, r := range recs {
					totalSavings += r.EstimatedSavings
				}

				if !ConfirmPurchase(totalInstances, totalSavings, cfg.SkipConfirmation) {
					// User cancelled - return cancelled results for all
					return createCancelledResults(recs, region, cfg)
				}
			}

			// Execute actual purchase
			result = executePurchase(ctx, rec, region, j+1, serviceClient, cfg)

			// Add delay between purchases to avoid rate limiting
			if j < len(recs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
				time.Sleep(PurchaseDelaySeconds * time.Second)
			}
		}

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
