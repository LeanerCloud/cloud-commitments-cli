package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/internal/common"
	"github.com/LeanerCloud/CUDly/internal/csv"
	"github.com/LeanerCloud/CUDly/internal/purchase"
	"github.com/LeanerCloud/CUDly/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// EC2ClientInterface defines the interface for EC2 operations
type EC2ClientInterface interface {
	DescribeRegions(ctx context.Context, params *ec2.DescribeRegionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error)
}

// ServiceProcessingStats holds statistics for each service
type ServiceProcessingStats struct {
	Service                 common.ServiceType
	RegionsProcessed        int
	RecommendationsFound    int
	RecommendationsSelected int
	InstancesProcessed      int32
	SuccessfulPurchases     int
	FailedPurchases         int
	TotalEstimatedSavings   float64
}

// determineServicesToProcess returns the list of services to process based on flags
func determineServicesToProcess(cfg Config) []common.ServiceType {
	if cfg.AllServices {
		return getAllServices()
	}
	if len(cfg.Services) > 0 {
		return parseServices(cfg.Services)
	}
	// Default to RDS only for backward compatibility
	return []common.ServiceType{common.ServiceRDS}
}

// printRunMode prints the current run mode (dry run or purchase)
func printRunMode(isDryRun bool) {
	if isDryRun {
		common.AppLogger.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		common.AppLogger.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}
}

// printPaymentAndTerm prints the payment option and term information
func printPaymentAndTerm(cfg Config) {
	common.AppLogger.Printf("💳 Payment option: %s, Term: %d year(s)\n", cfg.PaymentOption, cfg.TermYears)
}

// generateCSVFilename generates a CSV filename based on the mode and timestamp
func generateCSVFilename(isDryRun bool, cfg Config) string {
	if cfg.CSVOutput != "" {
		return cfg.CSVOutput
	}
	timestamp := time.Now().Format("20060102-150405")
	mode := "dryrun"
	if !isDryRun {
		mode = "purchase"
	}
	return fmt.Sprintf("ri-helper-%s-%s.csv", mode, timestamp)
}

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

	common.AppLogger.Printf("📊 Processing services: %s\n", formatServices(servicesToProcess))
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
	accountCache := common.NewAccountAliasCache(awsCfg)

	// Create recommendations client
	recClient := common.NewRecommendationsClient(awsCfg)

	// Process each service
	allRecommendations := make([]common.Recommendation, 0)
	allResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for _, service := range servicesToProcess {
		common.AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		common.AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		common.AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		// Process all services with common interface
		serviceRecs, serviceResults := processService(ctx, awsCfg, recClient, accountCache, service, isDryRun, cfg)
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
		common.AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(allRecommendations, allResults, serviceStats, isDryRun)
}

// determineCSVCoverage determines the coverage percentage to use for CSV mode
func determineCSVCoverage(cfg Config) float64 {
	// When using CSV input, default to 100% coverage (use exact numbers from CSV)
	// unless user explicitly provided a different coverage value
	if cfg.Coverage == 80.0 {
		// User didn't override the default, so use 100% for CSV mode
		return 100.0
	}
	return cfg.Coverage
}

// loadRecommendationsFromCSV reads and returns recommendations from a CSV file
func loadRecommendationsFromCSV(csvPath string) ([]common.Recommendation, error) {
	reader := csv.NewReader()
	return reader.ReadRecommendations(csvPath)
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

	// Apply filters
	originalCount := len(recommendations)
	recommendations = applyFilters(recommendations, cfg, instanceVersions, versionInfo)
	if len(recommendations) < originalCount {
		common.AppLogger.Printf("🔍 After filters: %d recommendations (filtered out %d)\n", len(recommendations), originalCount-len(recommendations))
	}

	// Apply coverage if not 100%
	if csvModeCoverage < 100 {
		beforeCoverage := len(recommendations)
		recommendations = applyCommonCoverage(recommendations, csvModeCoverage)
		common.AppLogger.Printf("📈 Applying %.1f%% coverage: %d recommendations selected (from %d)\n", csvModeCoverage, len(recommendations), beforeCoverage)
	}

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		recommendations = common.ApplyCountOverride(recommendations, cfg.OverrideCount)
	}

	// Apply instance limit if specified
	if cfg.MaxInstances > 0 {
		beforeLimit := len(recommendations)
		recommendations = common.ApplyInstanceLimit(recommendations, cfg.MaxInstances)
		if len(recommendations) < beforeLimit {
			common.AppLogger.Printf("🔒 Applied instance limit: %d recommendations after limiting to %d instances\n", len(recommendations), cfg.MaxInstances)
		}
	}

	return recommendations
}

// groupRecommendationsByServiceRegion groups recommendations by service and region
func groupRecommendationsByServiceRegion(recommendations []common.Recommendation) map[common.ServiceType]map[string][]common.Recommendation {
	recsByServiceRegion := make(map[common.ServiceType]map[string][]common.Recommendation)
	for _, rec := range recommendations {
		if _, ok := recsByServiceRegion[rec.Service]; !ok {
			recsByServiceRegion[rec.Service] = make(map[string][]common.Recommendation)
		}
		recsByServiceRegion[rec.Service][rec.Region] = append(recsByServiceRegion[rec.Service][rec.Region], rec)
	}
	return recsByServiceRegion
}

// populateAccountNames populates account names from account IDs using the cache
func populateAccountNames(ctx context.Context, recommendations []common.Recommendation, accountCache *common.AccountAliasCache) {
	for i := range recommendations {
		if recommendations[i].AccountID != "" {
			recommendations[i].AccountName = accountCache.GetAccountAlias(ctx, recommendations[i].AccountID)
		}
	}
}

// adjustRecsForDuplicates checks for existing RIs and adjusts recommendations to avoid duplicates
func adjustRecsForDuplicates(ctx context.Context, recs []common.Recommendation, purchaseClient common.PurchaseClient) ([]common.Recommendation, error) {
	duplicateChecker := common.NewDuplicateChecker()
	adjustedRecs, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, recs, purchaseClient)
	if err != nil {
		return recs, err // Return original recommendations with error
	}

	originalInstances := common.CalculateTotalInstances(recs)
	adjustedInstances := common.CalculateTotalInstances(adjustedRecs)
	if originalInstances != adjustedInstances {
		common.AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
	}

	return adjustedRecs, nil
}

// createDryRunResult creates a purchase result for dry run mode
func createDryRunResult(rec common.Recommendation, region string, index int, cfg Config) common.PurchaseResult {
	return common.PurchaseResult{
		Config:     rec,
		Success:    true,
		PurchaseID: generatePurchaseID(rec, region, index, true, cfg.Coverage),
		Message:    "Dry run - no actual purchase",
		Timestamp:  time.Now(),
	}
}

// createCancelledResults creates purchase results for cancelled purchases
func createCancelledResults(recs []common.Recommendation, region string, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, len(recs))
	for k := range recs {
		results[k] = common.PurchaseResult{
			Config:     recs[k],
			Success:    false,
			PurchaseID: generatePurchaseID(recs[k], region, k+1, false, cfg.Coverage),
			Message:    "Purchase cancelled by user",
			Timestamp:  time.Now(),
		}
	}
	return results
}

// executePurchase executes an actual RI purchase
func executePurchase(ctx context.Context, rec common.Recommendation, region string, index int, purchaseClient common.PurchaseClient, cfg Config) common.PurchaseResult {
	common.AppLogger.Printf("    ⚠️  ACTUAL PURCHASE: About to buy %d instances of %s\n", rec.Count, rec.InstanceType)
	result := purchaseClient.PurchaseRI(ctx, rec)
	if result.PurchaseID == "" {
		result.PurchaseID = generatePurchaseID(rec, region, index, false, cfg.Coverage)
	}
	return result
}

// processPurchaseLoop processes purchases for a single region
func processPurchaseLoop(ctx context.Context, recs []common.Recommendation, region string, isDryRun bool, purchaseClient common.PurchaseClient, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recs))

	for j, rec := range recs {
		common.AppLogger.Printf("    [%d/%d] Processing: %s\n", j+1, len(recs), rec.Description)
		common.AppLogger.Printf("    💳 Purchasing %d instances\n", rec.Count)

		var result common.PurchaseResult
		if isDryRun {
			result = createDryRunResult(rec, region, j+1, cfg)
		} else {
			// Ask for confirmation before proceeding with purchases (only on first item)
			if j == 0 {
				totalInstances := common.CalculateTotalInstances(recs)
				totalCost := 0.0
				for _, r := range recs {
					totalCost += r.EstimatedCost
				}

				if !common.ConfirmPurchase(totalInstances, totalCost, cfg.SkipConfirmation) {
					// User cancelled - return cancelled results for all
					return createCancelledResults(recs, region, cfg)
				}
			}

			// Execute actual purchase
			result = executePurchase(ctx, rec, region, j+1, purchaseClient, cfg)

			// Add delay between purchases to avoid rate limiting
			if j < len(recs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
				time.Sleep(2 * time.Second)
			}
		}

		results = append(results, result)

		if result.Success {
			common.AppLogger.Printf("    ✅ Success: %s\n", result.Message)
		} else {
			common.AppLogger.Printf("    ❌ Failed: %s\n", result.Message)
		}
	}

	return results
}

// runToolFromCSV processes recommendations from a CSV input file
func runToolFromCSV(ctx context.Context, cfg Config) {
	// Determine if this is a dry run
	isDryRun := !cfg.ActualPurchase
	printRunMode(isDryRun)

	csvModeCoverage := determineCSVCoverage(cfg)

	common.AppLogger.Printf("📄 Reading recommendations from CSV: %s\n", cfg.CSVInput)

	// Read recommendations from CSV
	recommendations, err := loadRecommendationsFromCSV(cfg.CSVInput)
	if err != nil {
		log.Fatalf("Failed to read CSV file: %v", err)
	}

	common.AppLogger.Printf("✅ Loaded %d recommendations from CSV\n", len(recommendations))

	// Filter and adjust recommendations
	recommendations = filterAndAdjustRecommendations(recommendations, csvModeCoverage, cfg)

	if len(recommendations) == 0 {
		common.AppLogger.Println("⚠️  No recommendations to process after filtering")
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
	accountCache := common.NewAccountAliasCache(awsCfg)

	// Populate account names from account IDs
	populateAccountNames(ctx, recommendations, accountCache)

	// Group recommendations by service and region
	recsByServiceRegion := groupRecommendationsByServiceRegion(recommendations)

	// Process purchases
	allResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for service, regionRecs := range recsByServiceRegion {
		common.AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		common.AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		common.AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		serviceRecs := make([]common.Recommendation, 0)
		for region, recs := range regionRecs {
			common.AppLogger.Printf("\n  📍 Region: %s (%d recommendations)\n", region, len(recs))

			// Get purchase client for this region
			regionalCfg := awsCfg.Copy()
			regionalCfg.Region = region
			purchaseClient := createPurchaseClient(service, regionalCfg)

			if purchaseClient == nil {
				common.AppLogger.Printf("  ⚠️  Purchase client not yet implemented for %s\n", getServiceDisplayName(service))
				common.AppLogger.Printf("     (Skipping purchase phase for this service)\n")
				continue
			}

			// Check for duplicate RIs to avoid double purchasing
			adjustedRecs, err := adjustRecsForDuplicates(ctx, recs, purchaseClient)
			if err != nil {
				common.AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v\n", err)
				adjustedRecs = recs // Continue with original recommendations if check fails
			}
			recs = adjustedRecs

			serviceRecs = append(serviceRecs, recs...)

			// Process purchases for this region
			regionResults := processPurchaseLoop(ctx, recs, region, isDryRun, purchaseClient, cfg)
			allResults = append(allResults, regionResults...)
		}

		// Calculate service statistics
		stats := calculateServiceStats(service, serviceRecs, allResults)
		serviceStats[service] = stats
		printServiceSummary(service, stats)
	}

	// Generate CSV filename and write report
	finalCSVOutput := generateCSVFilename(isDryRun, cfg)

	// Write CSV report
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		common.AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(recommendations, allResults, serviceStats, isDryRun)
}


func processService(ctx context.Context, awsCfg aws.Config, recClient common.RecommendationsClientInterface, accountCache *common.AccountAliasCache, service common.ServiceType, isDryRun bool, cfg Config) ([]common.Recommendation, []common.PurchaseResult) {
	// Determine regions to process
	regionsToProcess := cfg.Regions
	if len(regionsToProcess) == 0 {
		// Savings Plans are account-level, not regional - only query once
		if service == common.ServiceSavingsPlans {
			common.AppLogger.Printf("🌍 Fetching account-level Savings Plans recommendations...\n")
			regionsToProcess = []string{"us-east-1"} // Single query for account-level data
		} else {
			// Default to all AWS regions for other services
			common.AppLogger.Printf("🌍 Processing all AWS regions for %s...\n", getServiceDisplayName(service))
			allRegions, err := getAllAWSRegions(ctx, awsCfg)
			if err != nil {
				log.Printf("❌ Failed to get AWS regions: %v", err)
				// Fall back to auto-discovery
				common.AppLogger.Printf("🔍 Falling back to auto-discovery...\n")
				discoveredRegions, err := discoverRegionsForService(ctx, recClient, service)
				if err != nil {
					log.Printf("❌ Failed to discover regions: %v", err)
					return nil, nil
				}
				regionsToProcess = discoveredRegions
			} else {
				regionsToProcess = allRegions
			}
			common.AppLogger.Printf("📍 Processing %d region(s)\n", len(regionsToProcess))
		}
	}

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	// Query running instances for engine version validation (once for all regions)
	log.Printf("🔍 Querying running RDS instances across all regions to validate engine versions...")
	instanceVersions, err := queryRunningInstanceEngineVersions(ctx, cfg)
	if err != nil {
		log.Printf("⚠️  Warning: Failed to query running instances for engine version validation: %v", err)
		log.Printf("   Continuing without engine version filtering")
		instanceVersions = make(map[string][]InstanceEngineVersion)
	} else {
		log.Printf("✅ Found %d instance types with version information across all regions", len(instanceVersions))
	}

	// Query major engine versions for extended support detection (once for all regions)
	log.Printf("🔍 Querying AWS RDS major engine versions for extended support information...")
	versionInfo, err := queryMajorEngineVersions(ctx, cfg)
	if err != nil {
		log.Printf("⚠️  Warning: Failed to query major engine versions: %v", err)
		log.Printf("   Continuing without extended support detection")
		versionInfo = make(map[string]MajorEngineVersionInfo)
	} else {
		log.Printf("✅ Found support information for %d major engine versions", len(versionInfo))
	}

	for i, region := range regionsToProcess {
		common.AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", i+1, len(regionsToProcess), region)

		// Fetch recommendations
		params := common.RecommendationParams{
			Service:            service,
			Region:             region,
			PaymentOption:      cfg.PaymentOption,
			TermInYears:        cfg.TermYears,
			LookbackPeriodDays: 7,
		}

		recs, err := recClient.GetRecommendations(ctx, params)
		if err != nil {
			log.Printf("  ❌ Failed to fetch recommendations: %v", err)
			continue
		}

		if len(recs) == 0 {
			common.AppLogger.Printf("  ℹ️  No recommendations found\n")
			continue
		}

		common.AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

		// Populate account names from account IDs
		for i := range recs {
			if recs[i].AccountID != "" {
				recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].AccountID)
			}
		}

		// Apply region and instance type filters
		originalCount := len(recs)
		recs = applyFilters(recs, cfg, instanceVersions, versionInfo)
		if len(recs) == 0 {
			common.AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
			continue
		}
		if len(recs) < originalCount {
			common.AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
		}

		// Apply coverage
		filteredRecs := applyCommonCoverage(recs, cfg.Coverage)
		common.AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", cfg.Coverage, len(filteredRecs))

		// Apply count override if specified
		if cfg.OverrideCount > 0 {
			filteredRecs = common.ApplyCountOverride(filteredRecs, cfg.OverrideCount)
		}

		serviceRecs = append(serviceRecs, filteredRecs...)

		// Get purchase client
		regionalCfg := awsCfg.Copy()
		regionalCfg.Region = region
		purchaseClient := createPurchaseClient(service, regionalCfg)

		if purchaseClient == nil {
			common.AppLogger.Printf("  ⚠️  Purchase client not yet implemented for %s\n", getServiceDisplayName(service))
			common.AppLogger.Printf("     (Skipping purchase phase for this service)\n")
			continue
		}

		// Check for duplicate RIs to avoid double purchasing
		duplicateChecker := common.NewDuplicateChecker()
		adjustedRecs, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, filteredRecs, purchaseClient)
		if err != nil {
			common.AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v\n", err)
			adjustedRecs = filteredRecs // Continue with original recommendations if check fails
		} else {
			// Always use the adjusted recommendations (they might have different counts even if same length)
			originalInstances := common.CalculateTotalInstances(filteredRecs)
			adjustedInstances := common.CalculateTotalInstances(adjustedRecs)
			if originalInstances != adjustedInstances {
				common.AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
			}
			filteredRecs = adjustedRecs
		}

		// Apply instance limit if specified
		if cfg.MaxInstances > 0 {
			beforeLimit := len(filteredRecs)
			filteredRecs = common.ApplyInstanceLimit(filteredRecs, cfg.MaxInstances)
			if len(filteredRecs) < beforeLimit {
				common.AppLogger.Printf("  🔒 Applied instance limit: %d recommendations after limiting to %d instances\n", len(filteredRecs), cfg.MaxInstances)
			}
		}

		// Process purchases
		for j, rec := range filteredRecs {
			common.AppLogger.Printf("    [%d/%d] Processing: %s\n", j+1, len(filteredRecs), rec.Description)

			// Log the actual count being purchased
			common.AppLogger.Printf("    💳 Purchasing %d instances (coverage-adjusted)\n", rec.Count)

			var result common.PurchaseResult
			if isDryRun {
				result = common.PurchaseResult{
					Config:     rec,
					Success:    true,
					PurchaseID: generatePurchaseID(rec, region, j+1, true, cfg.Coverage),
					Message:    "Dry run - no actual purchase",
					Timestamp:  time.Now(),
				}
			} else {
				// Calculate total for this batch of purchases (only on first item)
				if j == 0 {
					totalInstances := common.CalculateTotalInstances(filteredRecs)
					totalCost := 0.0
					for _, r := range filteredRecs {
						totalCost += r.EstimatedCost
					}

					// Ask for confirmation before proceeding with purchases
					if !common.ConfirmPurchase(totalInstances, totalCost, cfg.SkipConfirmation) {
						// User cancelled - mark all as cancelled and exit
						for k := range filteredRecs {
							cancelResult := common.PurchaseResult{
								Config:     filteredRecs[k],
								Success:    false,
								PurchaseID: generatePurchaseID(filteredRecs[k], region, k+1, false, cfg.Coverage),
								Message:    "Purchase cancelled by user",
								Timestamp:  time.Now(),
							}
							serviceResults = append(serviceResults, cancelResult)
						}
						break // Exit the purchase loop for this region
					}
				}

				// Final confirmation log before actual purchase
				common.AppLogger.Printf("    ⚠️  ACTUAL PURCHASE: About to buy %d instances of %s\n", rec.Count, rec.InstanceType)
				result = purchaseClient.PurchaseRI(ctx, rec)
				if result.PurchaseID == "" {
					result.PurchaseID = generatePurchaseID(rec, region, j+1, false, cfg.Coverage)
				}
				// Add delay between purchases to avoid rate limiting
				// This delay can be disabled for testing by setting DISABLE_PURCHASE_DELAY env var
				if j < len(filteredRecs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
					time.Sleep(2 * time.Second)
				}
			}

			serviceResults = append(serviceResults, result)

			if result.Success {
				common.AppLogger.Printf("    ✅ Success: %s\n", result.Message)
			} else {
				common.AppLogger.Printf("    ❌ Failed: %s\n", result.Message)
			}
		}
	}

	return serviceRecs, serviceResults
}

// Helper functions

func formatServices(services []common.ServiceType) string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = getServiceDisplayName(s)
	}
	return strings.Join(names, ", ")
}

func getServiceDisplayName(service common.ServiceType) string {
	switch service {
	case common.ServiceRDS:
		return "RDS"
	case common.ServiceElastiCache:
		return "ElastiCache"
	case common.ServiceEC2:
		return "EC2"
	case common.ServiceOpenSearch, common.ServiceElasticsearch:
		return "OpenSearch"
	case common.ServiceRedshift:
		return "Redshift"
	case common.ServiceMemoryDB:
		return "MemoryDB"
	case common.ServiceSavingsPlans:
		return "Savings Plans"
	default:
		return string(service)
	}
}

// getAllAWSRegions retrieves all available AWS regions
func getAllAWSRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	// Create EC2 client to get regions
	ec2Client := ec2.NewFromConfig(cfg)
	return getAllAWSRegionsWithClient(ctx, ec2Client)
}

// getAllAWSRegionsWithClient retrieves all available AWS regions using the provided client
func getAllAWSRegionsWithClient(ctx context.Context, ec2Client EC2ClientInterface) ([]string, error) {
	// Describe all regions
	result, err := ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(false), // Only get opted-in regions
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe regions: %w", err)
	}

	regions := make([]string, 0, len(result.Regions))
	for _, region := range result.Regions {
		if region.RegionName != nil {
			regions = append(regions, *region.RegionName)
		}
	}

	sort.Strings(regions)
	return regions, nil
}

func discoverRegionsForService(ctx context.Context, client common.RecommendationsClientInterface, service common.ServiceType) ([]string, error) {
	recs, err := client.GetRecommendationsForDiscovery(ctx, service)
	if err != nil {
		return nil, err
	}

	regionSet := make(map[string]bool)
	for _, rec := range recs {
		if rec.Region != "" {
			regionSet[rec.Region] = true
		}
	}

	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}

	sort.Strings(regions)
	return regions, nil
}


func applyCommonCoverage(recs []common.Recommendation, coverage float64) []common.Recommendation {
	return common.ApplyCoverage(recs, coverage)
}


func calculateServiceStats(service common.ServiceType, recs []common.Recommendation, results []common.PurchaseResult) ServiceProcessingStats {
	stats := ServiceProcessingStats{
		Service:                 service,
		RecommendationsFound:    len(recs),
		RecommendationsSelected: len(recs),
	}

	regionSet := make(map[string]bool)
	for _, rec := range recs {
		regionSet[rec.Region] = true
		stats.InstancesProcessed += rec.Count
		stats.TotalEstimatedSavings += rec.EstimatedCost
	}
	stats.RegionsProcessed = len(regionSet)

	for _, result := range results {
		if result.Success {
			stats.SuccessfulPurchases++
		} else {
			stats.FailedPurchases++
		}
	}

	return stats
}

func printServiceSummary(service common.ServiceType, stats ServiceProcessingStats) {
	fmt.Printf("\n📊 %s Summary:\n", getServiceDisplayName(service))
	fmt.Printf("  Regions processed: %d\n", stats.RegionsProcessed)
	fmt.Printf("  Recommendations: %d\n", stats.RecommendationsSelected)
	fmt.Printf("  Instances: %d\n", stats.InstancesProcessed)
	fmt.Printf("  Successful: %d, Failed: %d\n", stats.SuccessfulPurchases, stats.FailedPurchases)
	if stats.TotalEstimatedSavings > 0 {
		fmt.Printf("  Estimated monthly savings: $%.2f\n", stats.TotalEstimatedSavings)
	}
}

func writeMultiServiceCSVReport(results []common.PurchaseResult, filepath string) error {
	// For backward compatibility, convert to old format for CSV writer
	// This is temporary until we update the CSV writer to handle multi-service
	oldResults := make([]purchase.Result, 0, len(results))

	for _, r := range results {
		// Create a generic old-style recommendation
		oldRec := recommendations.Recommendation{
			Region:                   r.Config.Region,
			InstanceType:             r.Config.InstanceType,
			PaymentOption:            r.Config.PaymentOption,
			Term:                     int32(r.Config.Term), // Fix type conversion
			Count:                    r.Config.Count,
			EstimatedCost:            r.Config.EstimatedCost,
			SavingsPercent:           r.Config.SavingsPercent,
			Timestamp:                r.Config.Timestamp,
			Description:              r.Config.Description,
			UpfrontCost:              r.Config.UpfrontCost,
			RecurringMonthlyCost:     r.Config.RecurringMonthlyCost,
			EstimatedMonthlyOnDemand: r.Config.EstimatedMonthlyOnDemand,
			AccountID:                r.Config.AccountID,
			AccountName:              r.Config.AccountName,
		}

		// Add service-specific details with nil checks
		switch r.Config.Service {
		case common.ServiceRDS:
			if rdsDetails, ok := r.Config.ServiceDetails.(*common.RDSDetails); ok && rdsDetails != nil {
				oldRec.Engine = rdsDetails.Engine
				oldRec.AZConfig = rdsDetails.AZConfig
			}
		case common.ServiceElastiCache:
			if ecDetails, ok := r.Config.ServiceDetails.(*common.ElastiCacheDetails); ok && ecDetails != nil {
				oldRec.Engine = ecDetails.Engine
				oldRec.AZConfig = "N/A"
			}
		case common.ServiceEC2:
			if ec2Details, ok := r.Config.ServiceDetails.(*common.EC2Details); ok && ec2Details != nil {
				oldRec.Engine = ec2Details.Platform
				oldRec.AZConfig = ec2Details.Tenancy
			}
		default:
			// For other services, use generic description
			oldRec.Engine = string(r.Config.Service)
			oldRec.AZConfig = "N/A"
		}

		oldResults = append(oldResults, purchase.Result{
			Config:        oldRec,
			Success:       r.Success,
			PurchaseID:    r.PurchaseID,
			ReservationID: r.ReservationID,
			Message:       r.Message,
			ActualCost:    r.ActualCost,
			Timestamp:     r.Timestamp,
		})
	}

	if len(oldResults) > 0 {
		writer := csv.NewWriter()
		return writer.WriteResults(oldResults, filepath)
	}

	return nil
}

func printMultiServiceSummary(allRecommendations []common.Recommendation, allResults []common.PurchaseResult, serviceStats map[common.ServiceType]ServiceProcessingStats, isDryRun bool) {
	fmt.Println("\n🎯 Final Summary:")
	fmt.Println("==========================================")

	if isDryRun {
		fmt.Println("Mode: DRY RUN")
	} else {
		fmt.Println("Mode: ACTUAL PURCHASE")
	}

	// Separate Savings Plans from RIs
	spStats := ServiceProcessingStats{}
	riStats := make(map[common.ServiceType]ServiceProcessingStats)

	for service, stats := range serviceStats {
		if service == common.ServiceSavingsPlans {
			spStats = stats
		} else {
			riStats[service] = stats
		}
	}

	// Calculate RI totals
	riRecommendations := 0
	riInstances := int32(0)
	riSavings := float64(0)
	riSuccess := 0
	riFailed := 0

	for _, stats := range riStats {
		riRecommendations += stats.RecommendationsSelected
		riInstances += stats.InstancesProcessed
		riSavings += stats.TotalEstimatedSavings
		riSuccess += stats.SuccessfulPurchases
		riFailed += stats.FailedPurchases
	}

	// Show Reserved Instances section
	if len(riStats) > 0 {
		fmt.Println("\n💰 RESERVED INSTANCES:")
		fmt.Println("--------------------------------------------------")
		for service, stats := range riStats {
			fmt.Printf("%-15s | Recs: %3d | Instances: %3d | Savings: $%8.2f/mo\n",
				getServiceDisplayName(service),
				stats.RecommendationsSelected,
				stats.InstancesProcessed,
				stats.TotalEstimatedSavings)
		}
		fmt.Printf("%-15s | Recs: %3d | Instances: %3d | Savings: $%8.2f/mo\n",
			"TOTAL RIs",
			riRecommendations,
			riInstances,
			riSavings)
	}

	// Show Savings Plans section
	if spStats.RecommendationsSelected > 0 {
		fmt.Println("\n📊 SAVINGS PLANS (Alternative to EC2 RIs):")
		fmt.Println("--------------------------------------------------")

		// Break down by SP type from recommendations
		computeSavings := 0.0
		ec2InstanceSavings := 0.0
		computeCount := 0
		ec2InstanceCount := 0

		for _, rec := range allRecommendations {
			if rec.Service == common.ServiceSavingsPlans {
				if details, ok := rec.ServiceDetails.(*common.SavingsPlanDetails); ok {
					if details.PlanType == "Compute" {
						computeSavings += rec.EstimatedCost
						computeCount++
					} else if details.PlanType == "EC2Instance" {
						ec2InstanceSavings += rec.EstimatedCost
						ec2InstanceCount++
					}
				}
			}
		}

		if computeCount > 0 {
			fmt.Printf("  Compute SP    | Recs: %3d | Covers: EC2, Fargate, Lambda | $%8.2f/mo\n", computeCount, computeSavings)
		}
		if ec2InstanceCount > 0 {
			fmt.Printf("  EC2 Inst SP   | Recs: %3d | Covers: EC2 only (better rate) | $%8.2f/mo\n", ec2InstanceCount, ec2InstanceSavings)
		}

		// Show best SP option
		bestSP := "None"
		bestSPSavings := 0.0
		if ec2InstanceSavings > computeSavings {
			bestSP = "EC2 Instance SP"
			bestSPSavings = ec2InstanceSavings
		} else if computeSavings > 0 {
			bestSP = "Compute SP"
			bestSPSavings = computeSavings
		}

		fmt.Printf("\n  ⭐ Recommended: %s ($%.2f/mo)\n", bestSP, bestSPSavings)
	}

	// Show comparison if we have both
	if len(riStats) > 0 && spStats.RecommendationsSelected > 0 {
		fmt.Println("\n🔄 COMPARISON:")
		fmt.Println("--------------------------------------------------")

		// Option 1: All RIs
		fmt.Printf("Option 1 (All RIs):\n")
		fmt.Printf("  Total monthly savings: $%.2f\n", riSavings)
		fmt.Printf("  Pros: Highest discount for specific instance types\n")
		fmt.Printf("  Cons: Less flexible, locked to instance family\n")

		// Option 2: SPs for EC2 + RIs for others
		ec2RISavings := 0.0
		if stats, ok := riStats[common.ServiceEC2]; ok {
			ec2RISavings = stats.TotalEstimatedSavings
		}

		bestSPSavings := 0.0
		for _, rec := range allRecommendations {
			if rec.Service == common.ServiceSavingsPlans {
				if details, ok := rec.ServiceDetails.(*common.SavingsPlanDetails); ok {
					if details.PlanType == "EC2Instance" {
						bestSPSavings += rec.EstimatedCost
					} else if bestSPSavings == 0 && details.PlanType == "Compute" {
						bestSPSavings += rec.EstimatedCost
					}
				}
			}
		}

		option2Savings := riSavings - ec2RISavings + bestSPSavings

		fmt.Printf("\nOption 2 (Savings Plans for EC2 + RIs for other services):\n")
		fmt.Printf("  Total monthly savings: $%.2f\n", option2Savings)
		fmt.Printf("  Pros: More flexible, can change instance families\n")
		fmt.Printf("  Cons: Slightly lower EC2 discount than dedicated RIs\n")

		if option2Savings > riSavings {
			fmt.Printf("\n  ⭐ RECOMMENDATION: Use Option 2 (saves $%.2f/mo more)\n", option2Savings-riSavings)
		} else {
			fmt.Printf("\n  ⭐ RECOMMENDATION: Use Option 1 (saves $%.2f/mo more)\n", riSavings-option2Savings)
		}
	}

	// Success rate
	totalResults := riSuccess + riFailed
	if totalResults > 0 {
		successRate := (float64(riSuccess) / float64(totalResults)) * 100
		fmt.Printf("\nOverall success rate: %.1f%%\n", successRate)
	}

	if isDryRun {
		fmt.Println("\n💡 To actually purchase these RIs, run with --purchase flag")
		fmt.Println("   Note: Savings Plans purchasing not yet implemented")
	} else if riSuccess > 0 {
		fmt.Println("\n🎉 Purchase operations completed!")
		fmt.Println("⏰ Allow up to 15 minutes for RIs to appear in your account")
	}
}

// applyFilters applies region, instance type, engine, and engine version filters to recommendations
func applyFilters(recs []common.Recommendation, cfg Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo) []common.Recommendation {
	var filtered []common.Recommendation

	for _, rec := range recs {
		// Apply region filters
		if !shouldIncludeRegion(rec.Region, cfg) {
			continue
		}

		// Apply instance type filters
		if !shouldIncludeInstanceType(rec.InstanceType, cfg) {
			continue
		}

		// Apply engine filters
		if !shouldIncludeEngine(rec, cfg) {
			continue
		}

		// Apply account filters
		if !shouldIncludeAccount(rec.AccountName, cfg) {
			continue
		}

		// Apply engine version filters - adjust instance count by subtracting extended support versions
		rec = adjustRecommendationForExcludedVersions(rec, instanceVersions, versionInfo)
		// Skip if all instances were excluded (count reduced to 0)
		if rec.Count <= 0 {
			continue
		}

		filtered = append(filtered, rec)
	}

	return filtered
}

// InstanceEngineVersion stores engine version information for an instance
type InstanceEngineVersion struct {
	Engine        string
	EngineVersion string
	InstanceClass string
	Region        string
}

// EngineLifecycleInfo stores lifecycle support information for a major engine version
type EngineLifecycleInfo struct {
	LifecycleSupportName      string
	LifecycleSupportStartDate time.Time
	LifecycleSupportEndDate   time.Time
}

// MajorEngineVersionInfo stores support information for a major engine version
type MajorEngineVersionInfo struct {
	Engine                    string
	MajorEngineVersion        string
	SupportedEngineLifecycles []EngineLifecycleInfo
}

// queryRunningInstanceEngineVersions queries all running RDS instances and returns their engine versions
func queryRunningInstanceEngineVersions(ctx context.Context, cfg Config) (map[string][]InstanceEngineVersion, error) {
	// Determine which profile to use for validation
	validationProfile := cfg.ValidationProfile
	if validationProfile == "" {
		validationProfile = cfg.Profile
	}

	// Load AWS configuration for validation
	var configOptions []func(*config.LoadOptions) error
	configOptions = append(configOptions, config.WithRegion("us-east-1"))
	if validationProfile != "" {
		configOptions = append(configOptions, config.WithSharedConfigProfile(validationProfile))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to load validation AWS config: %w", err)
	}

	// Get all regions
	ec2Client := ec2.NewFromConfig(awsCfg)
	regionsOutput, err := ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe regions: %w", err)
	}

	// Map of instanceType -> []InstanceEngineVersion
	instanceVersions := make(map[string][]InstanceEngineVersion)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Query all regions concurrently
	for _, region := range regionsOutput.Regions {
		wg.Add(1)
		go func(regionName string) {
			defer wg.Done()

			// Create RDS client for this region
			regionCfg := awsCfg.Copy()
			regionCfg.Region = regionName
			rdsClient := rds.NewFromConfig(regionCfg)

			// Describe all RDS instances in this region with pagination
			var marker *string
			for {
				input := &rds.DescribeDBInstancesInput{
					Marker: marker,
				}

				output, err := rdsClient.DescribeDBInstances(ctx, input)
				if err != nil {
					// Log error but continue with other regions
					log.Printf("⚠️  Warning: Failed to describe RDS instances in %s: %v", regionName, err)
					break
				}

				// Collect instances from this page
				localVersions := make(map[string][]InstanceEngineVersion)
				for _, dbInstance := range output.DBInstances {
					instanceClass := aws.ToString(dbInstance.DBInstanceClass)
					engine := aws.ToString(dbInstance.Engine)
					engineVersion := aws.ToString(dbInstance.EngineVersion)

					localVersions[instanceClass] = append(localVersions[instanceClass], InstanceEngineVersion{
						Engine:        engine,
						EngineVersion: engineVersion,
						InstanceClass: instanceClass,
						Region:        regionName,
					})
				}

				// Merge into shared map with mutex protection
				mu.Lock()
				for instanceType, versions := range localVersions {
					instanceVersions[instanceType] = append(instanceVersions[instanceType], versions...)
				}
				mu.Unlock()

				if output.Marker == nil || aws.ToString(output.Marker) == "" {
					break
				}
				marker = output.Marker
			}
		}(aws.ToString(region.RegionName))
	}

	// Wait for all goroutines to complete
	wg.Wait()

	return instanceVersions, nil
}

// queryMajorEngineVersions queries AWS for major engine version lifecycle support information
func queryMajorEngineVersions(ctx context.Context, cfg Config) (map[string]MajorEngineVersionInfo, error) {
	// Determine which profile to use
	profile := cfg.ValidationProfile
	if profile == "" {
		profile = cfg.Profile
	}

	// Load AWS configuration
	var configOptions []func(*config.LoadOptions) error
	configOptions = append(configOptions, config.WithRegion("us-east-1"))
	if profile != "" {
		configOptions = append(configOptions, config.WithSharedConfigProfile(profile))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	rdsClient := rds.NewFromConfig(awsCfg)

	// Map of "engine:majorVersion" -> MajorEngineVersionInfo
	versionInfo := make(map[string]MajorEngineVersionInfo)

	// Query all engine types we care about
	engines := []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"}

	for _, engine := range engines {
		output, err := rdsClient.DescribeDBMajorEngineVersions(ctx, &rds.DescribeDBMajorEngineVersionsInput{
			Engine: aws.String(engine),
		})
		if err != nil {
			log.Printf("⚠️  Warning: Failed to describe major engine versions for %s: %v", engine, err)
			continue
		}

		for _, version := range output.DBMajorEngineVersions {
			info := MajorEngineVersionInfo{
				Engine:             aws.ToString(version.Engine),
				MajorEngineVersion: aws.ToString(version.MajorEngineVersion),
			}

			// Parse lifecycle support dates
			for _, lifecycle := range version.SupportedEngineLifecycles {
				lifecycleInfo := EngineLifecycleInfo{
					LifecycleSupportName: string(lifecycle.LifecycleSupportName),
				}

				if lifecycle.LifecycleSupportStartDate != nil {
					lifecycleInfo.LifecycleSupportStartDate = *lifecycle.LifecycleSupportStartDate
				}
				if lifecycle.LifecycleSupportEndDate != nil {
					lifecycleInfo.LifecycleSupportEndDate = *lifecycle.LifecycleSupportEndDate
				}

				info.SupportedEngineLifecycles = append(info.SupportedEngineLifecycles, lifecycleInfo)
			}

			key := fmt.Sprintf("%s:%s", info.Engine, info.MajorEngineVersion)
			versionInfo[key] = info
		}
	}

	return versionInfo, nil
}

// extractMajorVersion extracts the major version from a full engine version string
// Handles special cases like Aurora MySQL version mapping
func extractMajorVersion(engine, fullVersion string) string {
	if fullVersion == "" {
		return ""
	}

	// Normalize engine name
	normalizedEngine := strings.ToLower(engine)
	normalizedEngine = strings.ReplaceAll(normalizedEngine, "-", "")
	normalizedEngine = strings.ReplaceAll(normalizedEngine, " ", "")

	// Handle Aurora MySQL special format
	if normalizedEngine == "auroramysql" {
		// Aurora MySQL 2.x is compatible with MySQL 5.7
		if strings.Contains(fullVersion, "mysql_aurora.2.") {
			return "5.7"
		}
		// Aurora MySQL 3.x is compatible with MySQL 8.0
		if strings.Contains(fullVersion, "mysql_aurora.3.") {
			return "8.0"
		}
		// Check if it starts with a version number
		if strings.HasPrefix(fullVersion, "5.7") {
			return "5.7"
		}
		if strings.HasPrefix(fullVersion, "8.0") {
			return "8.0"
		}
	}

	// For standard versions (MySQL, PostgreSQL, Aurora PostgreSQL), extract "X.Y" or "X"
	parts := strings.Split(fullVersion, ".")
	if len(parts) >= 2 {
		// Try to parse as major.minor
		major := parts[0]
		minor := parts[1]
		// Filter out non-numeric parts in minor version
		numericMinor := ""
		for _, ch := range minor {
			if ch >= '0' && ch <= '9' {
				numericMinor += string(ch)
			} else {
				break
			}
		}
		if numericMinor != "" {
			return major + "." + numericMinor
		}
		return major
	}
	if len(parts) >= 1 {
		return parts[0]
	}

	return ""
}

// isInExtendedSupport checks if a version is currently in extended support based on lifecycle dates
func isInExtendedSupport(engine, fullVersion string, versionInfo map[string]MajorEngineVersionInfo) bool {
	majorVersion := extractMajorVersion(engine, fullVersion)
	if majorVersion == "" {
		return false
	}

	// Normalize engine name for lookup
	normalizedEngine := strings.ToLower(engine)
	normalizedEngine = strings.ReplaceAll(normalizedEngine, " ", "")

	// Look up the version info
	key := fmt.Sprintf("%s:%s", normalizedEngine, majorVersion)
	info, exists := versionInfo[key]
	if !exists {
		// If we don't have info, assume not in extended support
		return false
	}

	// Check if current date falls within extended support period
	now := time.Now()
	for _, lifecycle := range info.SupportedEngineLifecycles {
		if lifecycle.LifecycleSupportName == "open-source-rds-extended-support" {
			// Check if we're past the start date of extended support
			if now.After(lifecycle.LifecycleSupportStartDate) || now.Equal(lifecycle.LifecycleSupportStartDate) {
				return true
			}
		}
	}

	return false
}

// adjustRecommendationForExcludedVersions reduces the instance count in a recommendation
// by the number of instances running versions in extended support
func adjustRecommendationForExcludedVersions(rec common.Recommendation, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo) common.Recommendation {
	// Check if this instance type has any running instances
	versions, exists := instanceVersions[rec.InstanceType]
	if !exists {
		// No running instances of this type, return unchanged
		return rec
	}

	// Get the engine name from the recommendation
	var recEngine string
	switch details := rec.ServiceDetails.(type) {
	case *common.RDSDetails:
		recEngine = details.Engine
	default:
		return rec // Not RDS, no engine version filtering
	}

	// Count how many instances in this region are running versions in extended support
	excludedCount := 0
	totalMatchingInstances := 0

	for _, version := range versions {
		// Only count instances in the same region
		if version.Region != rec.Region {
			continue
		}

		// Match engine (normalize by removing spaces/hyphens and comparing lowercase)
		normalizeEngine := func(engine string) string {
			normalized := strings.ToLower(engine)
			normalized = strings.ReplaceAll(normalized, "-", "")
			normalized = strings.ReplaceAll(normalized, " ", "")
			return normalized
		}

		versionEngineNorm := normalizeEngine(version.Engine)
		recEngineNorm := normalizeEngine(recEngine)

		if versionEngineNorm != recEngineNorm {
			continue
		}

		totalMatchingInstances++

		// Check if this version is in extended support
		if isInExtendedSupport(version.Engine, version.EngineVersion, versionInfo) {
			majorVersion := extractMajorVersion(version.Engine, version.EngineVersion)
			excludedCount++
			log.Printf("🚫 Found extended support instance: %s %s in %s running version %s (major version %s is in extended support)",
				recEngine, rec.InstanceType, rec.Region, version.EngineVersion, majorVersion)
		}
	}

	// If we found excluded instances, reduce the recommendation count
	if excludedCount > 0 {
		originalCount := rec.Count
		newCount := max(0, int32(int(rec.Count)-excludedCount))

		if newCount != originalCount {
			log.Printf("📉 Adjusting recommendation for %s %s in %s: %d instances → %d instances (excluded %d extended support instances)",
				recEngine, rec.InstanceType, rec.Region, originalCount, newCount, excludedCount)
			rec.Count = newCount
		}
	}

	return rec
}

// shouldIncludeRegion checks if a region should be included based on filters
func shouldIncludeRegion(region string, cfg Config) bool {
	// If include list is specified, region must be in it
	if len(cfg.IncludeRegions) > 0 && !slices.Contains(cfg.IncludeRegions, region) {
		return false
	}

	// If exclude list is specified, region must not be in it
	if slices.Contains(cfg.ExcludeRegions, region) {
		return false
	}

	return true
}

// shouldIncludeInstanceType checks if an instance type should be included based on filters
func shouldIncludeInstanceType(instanceType string, cfg Config) bool {
	// If include list is specified, instance type must be in it
	if len(cfg.IncludeInstanceTypes) > 0 && !slices.Contains(cfg.IncludeInstanceTypes, instanceType) {
		return false
	}

	// If exclude list is specified, instance type must not be in it
	if slices.Contains(cfg.ExcludeInstanceTypes, instanceType) {
		return false
	}

	return true
}

// shouldIncludeEngine checks if a recommendation should be included based on engine filters
func shouldIncludeEngine(rec common.Recommendation, cfg Config) bool {
	// Extract engine from recommendation
	engine := getEngineFromRecommendation(rec)
	if engine == "" {
		// If no engine info, include by default unless there's an include list
		return len(cfg.IncludeEngines) == 0
	}

	// Normalize engine name to lowercase for comparison
	engine = strings.ToLower(engine)

	// If include list is specified, engine must be in it
	if len(cfg.IncludeEngines) > 0 {
		found := false
		for _, e := range cfg.IncludeEngines {
			if strings.ToLower(e) == engine {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, engine must not be in it
	if len(cfg.ExcludeEngines) > 0 {
		for _, e := range cfg.ExcludeEngines {
			if strings.ToLower(e) == engine {
				return false
			}
		}
	}

	return true
}

// shouldIncludeAccount checks if an account should be included based on filters
func shouldIncludeAccount(accountName string, cfg Config) bool {
	// If account name is empty and there are filters, skip it (unless include list is empty)
	if accountName == "" {
		return len(cfg.IncludeAccounts) == 0 && len(cfg.ExcludeAccounts) == 0
	}

	// Normalize account name to lowercase for comparison
	accountLower := strings.ToLower(accountName)

	// If include list is specified, account must contain at least one of the patterns
	if len(cfg.IncludeAccounts) > 0 {
		found := false
		for _, a := range cfg.IncludeAccounts {
			// Support both exact match and substring match
			filterLower := strings.ToLower(a)
			if filterLower == accountLower || strings.Contains(accountLower, filterLower) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, account must not contain any of the patterns
	if len(cfg.ExcludeAccounts) > 0 {
		for _, a := range cfg.ExcludeAccounts {
			// Support both exact match and substring match
			filterLower := strings.ToLower(a)
			if filterLower == accountLower || strings.Contains(accountLower, filterLower) {
				return false
			}
		}
	}

	return true
}

// getEngineFromRecommendation extracts the engine from a recommendation based on service type
func getEngineFromRecommendation(rec common.Recommendation) string {
	// Check service-specific details for engine information
	if rec.ServiceDetails != nil {
		switch details := rec.ServiceDetails.(type) {
		case *common.RDSDetails:
			return details.Engine
		case *common.ElastiCacheDetails:
			return details.Engine
		}
	}

	// Fallback to description parsing for ElastiCache
	if rec.Service == common.ServiceElastiCache && rec.Description != "" {
		// Description format: "Redis cache.t4g.micro 3x" or "Valkey cache.t3.micro 18x"
		parts := strings.Fields(rec.Description)
		if len(parts) > 0 {
			return parts[0]
		}
	}

	return ""
}
