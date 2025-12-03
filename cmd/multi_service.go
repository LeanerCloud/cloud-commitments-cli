package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	awsprovider "github.com/LeanerCloud/CUDly/providers/aws"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
)

// EC2ClientInterface defines the interface for EC2 operations
type EC2ClientInterface interface {
	DescribeRegions(ctx context.Context, params *awsec2.DescribeRegionsInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeRegionsOutput, error)
}

// ServiceProcessingStats holds statistics for each service
type ServiceProcessingStats struct {
	Service                 common.ServiceType
	RegionsProcessed        int
	RecommendationsFound    int
	RecommendationsSelected int
	InstancesProcessed      int
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
		AppLogger.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		AppLogger.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}
}

// printPaymentAndTerm prints the payment option and term information
func printPaymentAndTerm(cfg Config) {
	AppLogger.Printf("💳 Payment option: %s, Term: %d year(s)\n", cfg.PaymentOption, cfg.TermYears)
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

	// Process each service
	allRecommendations := make([]common.Recommendation, 0)
	allResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for _, service := range servicesToProcess {
		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

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
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
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
	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// Read header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}

	// Build column index map
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[col] = i
	}

	var recommendations []common.Recommendation
	for {
		record, err := reader.Read()
		if err != nil {
			break // End of file
		}

		rec := common.Recommendation{}

		// Parse fields from CSV
		if idx, ok := colIdx["Service"]; ok && idx < len(record) {
			rec.Service = common.ServiceType(record[idx])
		}
		if idx, ok := colIdx["Region"]; ok && idx < len(record) {
			rec.Region = record[idx]
		}
		if idx, ok := colIdx["ResourceType"]; ok && idx < len(record) {
			rec.ResourceType = record[idx]
		}
		if idx, ok := colIdx["Count"]; ok && idx < len(record) {
			fmt.Sscanf(record[idx], "%d", &rec.Count)
		}
		if idx, ok := colIdx["Account"]; ok && idx < len(record) {
			rec.Account = record[idx]
		}
		if idx, ok := colIdx["AccountName"]; ok && idx < len(record) {
			rec.AccountName = record[idx]
		}
		if idx, ok := colIdx["Term"]; ok && idx < len(record) {
			rec.Term = record[idx]
		}
		if idx, ok := colIdx["PaymentOption"]; ok && idx < len(record) {
			rec.PaymentOption = record[idx]
		}
		if idx, ok := colIdx["EstimatedSavings"]; ok && idx < len(record) {
			fmt.Sscanf(record[idx], "%f", &rec.EstimatedSavings)
		}

		recommendations = append(recommendations, rec)
	}

	return recommendations, nil
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
func populateAccountNames(ctx context.Context, recommendations []common.Recommendation, accountCache *AccountAliasCache) {
	for i := range recommendations {
		if recommendations[i].Account != "" {
			recommendations[i].AccountName = accountCache.GetAccountAlias(ctx, recommendations[i].Account)
		}
	}
}

// adjustRecsForDuplicates checks for existing RIs and adjusts recommendations to avoid duplicates
func adjustRecsForDuplicates(ctx context.Context, recs []common.Recommendation, serviceClient provider.ServiceClient) ([]common.Recommendation, error) {
	duplicateChecker := NewDuplicateChecker()
	adjustedRecs, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, recs, serviceClient)
	if err != nil {
		return recs, err // Return original recommendations with error
	}

	originalInstances := CalculateTotalInstances(recs)
	adjustedInstances := CalculateTotalInstances(adjustedRecs)
	if originalInstances != adjustedInstances {
		AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
	}

	return adjustedRecs, nil
}

// createDryRunResult creates a purchase result for dry run mode
func createDryRunResult(rec common.Recommendation, region string, index int, cfg Config) common.PurchaseResult {
	return common.PurchaseResult{
		Recommendation: rec,
		Success:        true,
		CommitmentID:   generatePurchaseID(rec, region, index, true, cfg.Coverage),
		DryRun:         true,
		Timestamp:      time.Now(),
	}
}

// createCancelledResults creates purchase results for cancelled purchases
func createCancelledResults(recs []common.Recommendation, region string, cfg Config) []common.PurchaseResult {
	results := make([]common.PurchaseResult, len(recs))
	for k := range recs {
		results[k] = common.PurchaseResult{
			Recommendation: recs[k],
			Success:        false,
			CommitmentID:   generatePurchaseID(recs[k], region, k+1, false, cfg.Coverage),
			Error:          fmt.Errorf("purchase cancelled by user"),
			Timestamp:      time.Now(),
		}
	}
	return results
}

// executePurchase executes an actual RI purchase
func executePurchase(ctx context.Context, rec common.Recommendation, region string, index int, serviceClient provider.ServiceClient, cfg Config) common.PurchaseResult {
	AppLogger.Printf("    ⚠️  ACTUAL PURCHASE: About to buy %d instances of %s\n", rec.Count, rec.ResourceType)
	result, _ := serviceClient.PurchaseCommitment(ctx, rec)
	if result.CommitmentID == "" {
		result.CommitmentID = generatePurchaseID(rec, region, index, false, cfg.Coverage)
	}
	return result
}

// processPurchaseLoop processes purchases for a single region
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
				time.Sleep(2 * time.Second)
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
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for service, regionRecs := range recsByServiceRegion {
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
		AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(recommendations, allResults, serviceStats, isDryRun)
}


func processService(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, accountCache *AccountAliasCache, service common.ServiceType, isDryRun bool, cfg Config) ([]common.Recommendation, []common.PurchaseResult) {
	// Determine regions to process
	regionsToProcess := cfg.Regions
	if len(regionsToProcess) == 0 {
		// Savings Plans are account-level, not regional - only query once
		if service == common.ServiceSavingsPlans {
			AppLogger.Printf("🌍 Fetching account-level Savings Plans recommendations...\n")
			regionsToProcess = []string{"us-east-1"} // Single query for account-level data
		} else {
			// Default to all AWS regions for other services
			AppLogger.Printf("🌍 Processing all AWS regions for %s...\n", getServiceDisplayName(service))
			allRegions, err := getAllAWSRegions(ctx, awsCfg)
			if err != nil {
				log.Printf("❌ Failed to get AWS regions: %v", err)
				// Fall back to auto-discovery
				AppLogger.Printf("🔍 Falling back to auto-discovery...\n")
				discoveredRegions, err := discoverRegionsForService(ctx, recClient, service)
				if err != nil {
					log.Printf("❌ Failed to discover regions: %v", err)
					return nil, nil
				}
				regionsToProcess = discoveredRegions
			} else {
				regionsToProcess = allRegions
			}
			AppLogger.Printf("📍 Processing %d region(s)\n", len(regionsToProcess))
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
		AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", i+1, len(regionsToProcess), region)

		// Fetch recommendations
		termStr := "1yr"
		if cfg.TermYears == 3 {
			termStr = "3yr"
		}
		params := common.RecommendationParams{
			Service:        service,
			Region:         region,
			PaymentOption:  cfg.PaymentOption,
			Term:           termStr,
			LookbackPeriod: "7d",
			// Savings Plans specific filters
			IncludeSPTypes: cfg.IncludeSPTypes,
			ExcludeSPTypes: cfg.ExcludeSPTypes,
		}

		recs, err := recClient.GetRecommendations(ctx, params)
		if err != nil {
			log.Printf("  ❌ Failed to fetch recommendations: %v", err)
			continue
		}

		if len(recs) == 0 {
			AppLogger.Printf("  ℹ️  No recommendations found\n")
			continue
		}

		AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

		// Populate account names from account IDs
		for i := range recs {
			if recs[i].Account != "" {
				recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].Account)
			}
		}

		// Apply region and instance type filters
		// Pass current region to filter recommendations to only those for this region
		originalCount := len(recs)
		recs = applyFilters(recs, cfg, instanceVersions, versionInfo, region)
		if len(recs) == 0 {
			AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
			continue
		}
		if len(recs) < originalCount {
			AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
		}

		// Apply coverage
		filteredRecs := applyCommonCoverage(recs, cfg.Coverage)
		AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", cfg.Coverage, len(filteredRecs))

		// Apply count override if specified
		if cfg.OverrideCount > 0 {
			filteredRecs = ApplyCountOverride(filteredRecs, cfg.OverrideCount)
		}

		serviceRecs = append(serviceRecs, filteredRecs...)

		// Get service client
		regionalCfg := awsCfg.Copy()
		regionalCfg.Region = region
		serviceClient := createServiceClient(service, regionalCfg)

		if serviceClient == nil {
			AppLogger.Printf("  ⚠️  Service client not yet implemented for %s\n", getServiceDisplayName(service))
			AppLogger.Printf("     (Skipping purchase phase for this service)\n")
			continue
		}

		// Check for duplicate RIs to avoid double purchasing
		duplicateChecker := NewDuplicateChecker()
		adjustedRecs, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, filteredRecs, serviceClient)
		if err != nil {
			AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v\n", err)
			adjustedRecs = filteredRecs // Continue with original recommendations if check fails
		} else {
			// Always use the adjusted recommendations (they might have different counts even if same length)
			originalInstances := CalculateTotalInstances(filteredRecs)
			adjustedInstances := CalculateTotalInstances(adjustedRecs)
			if originalInstances != adjustedInstances {
				AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
			}
			filteredRecs = adjustedRecs
		}

		// Apply instance limit if specified
		if cfg.MaxInstances > 0 {
			beforeLimit := len(filteredRecs)
			filteredRecs = ApplyInstanceLimit(filteredRecs, cfg.MaxInstances)
			if len(filteredRecs) < beforeLimit {
				AppLogger.Printf("  🔒 Applied instance limit: %d recommendations after limiting to %d instances\n", len(filteredRecs), cfg.MaxInstances)
			}
		}

		// Process purchases
		for j, rec := range filteredRecs {
			AppLogger.Printf("    [%d/%d] Processing: %s %s\n", j+1, len(filteredRecs), rec.Service, rec.ResourceType)

			// Log the actual count being purchased
			AppLogger.Printf("    💳 Purchasing %d instances (coverage-adjusted)\n", rec.Count)

			var result common.PurchaseResult
			if isDryRun {
				result = common.PurchaseResult{
					Recommendation: rec,
					Success:        true,
					CommitmentID:   generatePurchaseID(rec, region, j+1, true, cfg.Coverage),
					DryRun:         true,
					Timestamp:      time.Now(),
				}
			} else {
				// Calculate total for this batch of purchases (only on first item)
				if j == 0 {
					totalInstances := CalculateTotalInstances(filteredRecs)
					totalCost := 0.0
					for _, r := range filteredRecs {
						totalCost += r.EstimatedSavings
					}

					// Ask for confirmation before proceeding with purchases
					if !ConfirmPurchase(totalInstances, totalCost, cfg.SkipConfirmation) {
						// User cancelled - mark all as cancelled and exit
						for k := range filteredRecs {
							cancelResult := common.PurchaseResult{
								Recommendation: filteredRecs[k],
								Success:        false,
								CommitmentID:   generatePurchaseID(filteredRecs[k], region, k+1, false, cfg.Coverage),
								Error:          fmt.Errorf("purchase cancelled by user"),
								Timestamp:      time.Now(),
							}
							serviceResults = append(serviceResults, cancelResult)
						}
						break // Exit the purchase loop for this region
					}
				}

				// Final confirmation log before actual purchase
				AppLogger.Printf("    ⚠️  ACTUAL PURCHASE: About to buy %d instances of %s\n", rec.Count, rec.ResourceType)
				result, _ = serviceClient.PurchaseCommitment(ctx, rec)
				if result.CommitmentID == "" {
					result.CommitmentID = generatePurchaseID(rec, region, j+1, false, cfg.Coverage)
				}
				// Add delay between purchases to avoid rate limiting
				// This delay can be disabled for testing by setting DISABLE_PURCHASE_DELAY env var
				if j < len(filteredRecs)-1 && os.Getenv("DISABLE_PURCHASE_DELAY") != "true" {
					time.Sleep(2 * time.Second)
				}
			}

			serviceResults = append(serviceResults, result)

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
	case common.ServiceOpenSearch:
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
	ec2Client := awsec2.NewFromConfig(cfg)
	return getAllAWSRegionsWithClient(ctx, ec2Client)
}

// getAllAWSRegionsWithClient retrieves all available AWS regions using the provided client
func getAllAWSRegionsWithClient(ctx context.Context, ec2Client EC2ClientInterface) ([]string, error) {
	// Describe all regions
	result, err := ec2Client.DescribeRegions(ctx, &awsec2.DescribeRegionsInput{
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

func discoverRegionsForService(ctx context.Context, client provider.RecommendationsClient, service common.ServiceType) ([]string, error) {
	recs, err := client.GetRecommendationsForService(ctx, service)
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
	return ApplyCoverage(recs, coverage)
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
		stats.TotalEstimatedSavings += rec.EstimatedSavings
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
	if len(results) == 0 {
		return nil
	}

	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{
		"Service", "Region", "ResourceType", "Count", "Account", "AccountName",
		"Term", "PaymentOption", "EstimatedSavings", "CommitmentID",
		"Success", "Error", "Timestamp",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write data rows
	for _, r := range results {
		rec := r.Recommendation
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}

		row := []string{
			string(rec.Service),
			rec.Region,
			rec.ResourceType,
			fmt.Sprintf("%d", rec.Count),
			rec.Account,
			rec.AccountName,
			rec.Term,
			rec.PaymentOption,
			fmt.Sprintf("%.2f", rec.EstimatedSavings),
			r.CommitmentID,
			fmt.Sprintf("%t", r.Success),
			errStr,
			r.Timestamp.Format(time.RFC3339),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}
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
	riInstances := 0
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
		fmt.Println("\n📊 SAVINGS PLANS:")
		fmt.Println("--------------------------------------------------")

		// Break down by SP type from recommendations
		computeSavings := 0.0
		ec2InstanceSavings := 0.0
		sagemakerSavings := 0.0
		databaseSavings := 0.0
		computeCount := 0
		ec2InstanceCount := 0
		sagemakerCount := 0
		databaseCount := 0

		for _, rec := range allRecommendations {
			if rec.Service == common.ServiceSavingsPlans {
				if details, ok := rec.Details.(common.SavingsPlanDetails); ok {
					switch details.PlanType {
					case "Compute":
						computeSavings += rec.EstimatedSavings
						computeCount++
					case "EC2Instance":
						ec2InstanceSavings += rec.EstimatedSavings
						ec2InstanceCount++
					case "SageMaker":
						sagemakerSavings += rec.EstimatedSavings
						sagemakerCount++
					case "Database":
						databaseSavings += rec.EstimatedSavings
						databaseCount++
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
		if sagemakerCount > 0 {
			fmt.Printf("  SageMaker SP  | Recs: %3d | Covers: SageMaker instances    | $%8.2f/mo\n", sagemakerCount, sagemakerSavings)
		}
		if databaseCount > 0 {
			fmt.Printf("  Database SP   | Recs: %3d | Covers: RDS, Aurora, ElastiCache, etc. | $%8.2f/mo\n", databaseCount, databaseSavings)
		}

		// Show best SP options by category
		fmt.Println()
		if ec2InstanceSavings > 0 || computeSavings > 0 {
			if ec2InstanceSavings > computeSavings {
				fmt.Printf("  ⭐ Best for EC2: EC2 Instance SP ($%.2f/mo)\n", ec2InstanceSavings)
			} else if computeSavings > 0 {
				fmt.Printf("  ⭐ Best for Compute: Compute SP ($%.2f/mo) - more flexible\n", computeSavings)
			}
		}
		if databaseSavings > 0 {
			fmt.Printf("  ⭐ Best for Databases: Database SP ($%.2f/mo)\n", databaseSavings)
		}
		if sagemakerSavings > 0 {
			fmt.Printf("  ⭐ Best for ML: SageMaker SP ($%.2f/mo)\n", sagemakerSavings)
		}
	}

	// Show comparison if we have both RIs and Savings Plans
	if len(riStats) > 0 && spStats.RecommendationsSelected > 0 {
		fmt.Println("\n🔄 COMPARISON:")
		fmt.Println("--------------------------------------------------")

		// Collect SP savings by type
		ec2SPSavings := 0.0
		computeSPSavings := 0.0
		databaseSPSavings := 0.0
		for _, rec := range allRecommendations {
			if rec.Service == common.ServiceSavingsPlans {
				if details, ok := rec.Details.(common.SavingsPlanDetails); ok {
					switch details.PlanType {
					case "EC2Instance":
						ec2SPSavings += rec.EstimatedSavings
					case "Compute":
						computeSPSavings += rec.EstimatedSavings
					case "Database":
						databaseSPSavings += rec.EstimatedSavings
					}
				}
			}
		}

		// Collect RI savings by service
		ec2RISavings := 0.0
		dbRISavings := 0.0 // RDS, ElastiCache, etc.
		if stats, ok := riStats[common.ServiceEC2]; ok {
			ec2RISavings = stats.TotalEstimatedSavings
		}
		for service, stats := range riStats {
			if service == common.ServiceRDS || service == common.ServiceElastiCache ||
				service == common.ServiceMemoryDB || service == common.ServiceRedshift {
				dbRISavings += stats.TotalEstimatedSavings
			}
		}

		// Option 1: All RIs
		fmt.Printf("Option 1 (All RIs):\n")
		fmt.Printf("  Total monthly savings: $%.2f\n", riSavings)
		fmt.Printf("  Pros: Highest discount for specific instance types\n")
		fmt.Printf("  Cons: Less flexible, locked to instance family/engine\n")

		// Option 2: Best compute SP + non-EC2 RIs
		bestComputeSP := ec2SPSavings
		bestComputeSPName := "EC2 Instance SP"
		if computeSPSavings > ec2SPSavings {
			bestComputeSP = computeSPSavings
			bestComputeSPName = "Compute SP"
		}
		option2Savings := riSavings - ec2RISavings + bestComputeSP

		fmt.Printf("\nOption 2 (%s for compute + RIs for databases):\n", bestComputeSPName)
		fmt.Printf("  Total monthly savings: $%.2f\n", option2Savings)
		fmt.Printf("  Pros: Flexible compute (can change EC2 families)\n")
		fmt.Printf("  Cons: DB RIs still locked to engine/instance type\n")

		// Option 3: If we have Database SP recommendations
		if databaseSPSavings > 0 {
			option3Savings := riSavings - ec2RISavings - dbRISavings + bestComputeSP + databaseSPSavings
			fmt.Printf("\nOption 3 (%s + Database SP):\n", bestComputeSPName)
			fmt.Printf("  Total monthly savings: $%.2f\n", option3Savings)
			fmt.Printf("  Pros: Maximum flexibility for both compute and databases\n")
			fmt.Printf("  Cons: May have slightly lower discount than targeted RIs\n")

			// Find best option
			best := "Option 1 (All RIs)"
			bestSavings := riSavings
			if option2Savings > bestSavings {
				best = "Option 2 (Compute SP + DB RIs)"
				bestSavings = option2Savings
			}
			if option3Savings > bestSavings {
				best = "Option 3 (Compute SP + Database SP)"
				bestSavings = option3Savings
			}
			fmt.Printf("\n  ⭐ RECOMMENDATION: %s ($%.2f/mo)\n", best, bestSavings)
		} else {
			if option2Savings > riSavings {
				fmt.Printf("\n  ⭐ RECOMMENDATION: Use Option 2 (saves $%.2f/mo more)\n", option2Savings-riSavings)
			} else {
				fmt.Printf("\n  ⭐ RECOMMENDATION: Use Option 1 (saves $%.2f/mo more)\n", riSavings-option2Savings)
			}
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
// currentRegion is the region being processed in the current loop iteration - if non-empty, only recommendations for that region are included
func applyFilters(recs []common.Recommendation, cfg Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string) []common.Recommendation {
	var filtered []common.Recommendation

	for _, rec := range recs {
		// Filter to only recommendations for the current region being processed
		// This prevents duplicating recommendations across all regions
		// Skip this filter for Savings Plans as they are account-level, not regional
		if currentRegion != "" && rec.Region != currentRegion && rec.Service != common.ServiceSavingsPlans {
			continue
		}

		// Apply region filters
		if !shouldIncludeRegion(rec.Region, cfg) {
			continue
		}

		// Apply instance type filters
		if !shouldIncludeInstanceType(rec.ResourceType, cfg) {
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
		// Skip this filter if --include-extended-support is set
		if !cfg.IncludeExtendedSupport {
			rec = adjustRecommendationForExcludedVersions(rec, instanceVersions, versionInfo)
			// Skip if all instances were excluded (count reduced to 0)
			if rec.Count <= 0 {
				continue
			}
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
	ec2Client := awsec2.NewFromConfig(awsCfg)
	regionsOutput, err := ec2Client.DescribeRegions(ctx, &awsec2.DescribeRegionsInput{})
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
			rdsClient := awsrds.NewFromConfig(regionCfg)

			// Describe all RDS instances in this region with pagination
			var marker *string
			for {
				input := &awsrds.DescribeDBInstancesInput{
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

	rdsClient := awsrds.NewFromConfig(awsCfg)

	// Map of "engine:majorVersion" -> MajorEngineVersionInfo
	versionInfo := make(map[string]MajorEngineVersionInfo)

	// Query all engine types we care about
	engines := []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"}

	for _, engine := range engines {
		output, err := rdsClient.DescribeDBMajorEngineVersions(ctx, &awsrds.DescribeDBMajorEngineVersionsInput{
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
	versions, exists := instanceVersions[rec.ResourceType]
	if !exists {
		// No running instances of this type, return unchanged
		return rec
	}

	// Get the engine name from the recommendation
	var recEngine string
	switch details := rec.Details.(type) {
	case common.DatabaseDetails:
		recEngine = details.Engine
	case *common.DatabaseDetails:
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
				recEngine, rec.ResourceType, rec.Region, version.EngineVersion, majorVersion)
		}
	}

	// If we found excluded instances, reduce the recommendation count
	if excludedCount > 0 {
		originalCount := rec.Count
		newCount := max(0, rec.Count-excludedCount)

		if newCount != originalCount {
			log.Printf("📉 Adjusting recommendation for %s %s in %s: %d instances → %d instances (excluded %d extended support instances)",
				recEngine, rec.ResourceType, rec.Region, originalCount, newCount, excludedCount)
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

// getEngineFromRecommendationRaw extracts the raw engine from a recommendation (not normalized)
// Use getEngineFromRecommendation from helpers.go for normalized engine names
func getEngineFromRecommendationRaw(rec common.Recommendation) string {
	// Check service-specific details for engine information
	if rec.Details != nil {
		switch details := rec.Details.(type) {
		case common.DatabaseDetails:
			return details.Engine
		case *common.DatabaseDetails:
			return details.Engine
		case common.CacheDetails:
			return details.Engine
		case *common.CacheDetails:
			return details.Engine
		}
	}

	return ""
}
