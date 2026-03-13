package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	awsprovider "github.com/LeanerCloud/CUDly/providers/aws"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

// runToolMultiService is the main entry point for processing multiple services
func runToolMultiService(ctx context.Context, cfg Config) {
	// Validation is now handled in PreRunE

	// Check if we're using CSV input mode
	if cfg.CSVInput != "" {
		runToolFromCSV(ctx, cfg)
		return
	}

	// Determine services to process
	servicesToProcess := determineServicesToProcess(cfg)

	if len(servicesToProcess) == 0 {
		log.Fatalf("No valid services specified")
	}

	// Determine if this is a dry run
	isDryRun := !cfg.ActualPurchase
	printRunMode(isDryRun)

	AppLogger.Printf("📊 Processing services: %s\n", formatServices(servicesToProcess))
	printPaymentAndTerm(cfg)

	// Load AWS configuration
	var configOptions []func(*config.LoadOptions) error
	configOptions = append(configOptions, config.WithRegion("us-east-1"))
	if cfg.Profile != "" {
		configOptions = append(configOptions, config.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Create account alias cache for lookup
	accountCache := NewAccountAliasCache(awsCfg)

	// Create recommendations client
	recClient := awsprovider.NewRecommendationsClient(awsCfg)

	// Query engine version data once for all services
	engineData := fetchEngineVersionData(ctx, cfg)

	// Process each service
	allRecommendations := make([]common.Recommendation, 0)
	allResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for _, service := range servicesToProcess {
		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		// Process all services with common interface
		serviceRecs, serviceResults := processService(ctx, awsCfg, recClient, accountCache, service, isDryRun, cfg, engineData)
		allRecommendations = append(allRecommendations, serviceRecs...)
		allResults = append(allResults, serviceResults...)

		// Calculate service statistics
		stats := calculateServiceStats(service, serviceRecs, serviceResults)
		serviceStats[service] = stats
		printServiceSummary(service, stats)
	}

	// Generate CSV filename
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)

	// Write CSV report
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(allRecommendations, allResults, serviceStats, isDryRun)
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
	var configOptions []func(*config.LoadOptions) error
	configOptions = append(configOptions, config.WithRegion("us-east-1"))
	if cfg.Profile != "" {
		configOptions = append(configOptions, config.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, configOptions...)
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

// processService processes a single service and returns recommendations and results
func processService(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, accountCache *AccountAliasCache, service common.ServiceType, isDryRun bool, cfg Config, engineData engineVersionData) ([]common.Recommendation, []common.PurchaseResult) {
	// Determine regions to process
	regionsToProcess, err := determineRegionsForService(ctx, awsCfg, recClient, service, cfg.Regions)
	if err != nil {
		log.Printf("❌ Failed to determine regions: %v", err)
		return nil, nil
	}

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	// Process each region
	for i, region := range regionsToProcess {
		regionResult := processRegionRecommendations(
			ctx,
			awsCfg,
			recClient,
			accountCache,
			service,
			region,
			i+1,
			len(regionsToProcess),
			engineData,
			isDryRun,
			cfg,
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
				totalCost := 0.0
				for _, r := range recs {
					totalCost += r.EstimatedSavings
				}

				if !ConfirmPurchase(totalInstances, totalCost, cfg.SkipConfirmation) {
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
