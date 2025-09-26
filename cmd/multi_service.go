package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/csv"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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

func runToolMultiService(ctx context.Context) {
	// Validation is now handled in PreRunE

	// Determine services to process
	var servicesToProcess []common.ServiceType
	if allServices {
		servicesToProcess = getAllServices()
	} else if len(services) > 0 {
		servicesToProcess = parseServices(services)
	} else {
		// Default to RDS only for backward compatibility
		servicesToProcess = []common.ServiceType{common.ServiceRDS}
	}

	if len(servicesToProcess) == 0 {
		log.Fatalf("No valid services specified")
	}

	// Determine if this is a dry run
	isDryRun := !actualPurchase
	if isDryRun {
		common.AppLogger.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		common.AppLogger.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}

	common.AppLogger.Printf("📊 Processing services: %s\n", formatServices(servicesToProcess))
	common.AppLogger.Printf("💳 Payment option: %s, Term: %d year(s)\n", paymentOption, termYears)

	// Load AWS configuration
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Create recommendations client
	recClient := common.NewRecommendationsClient(cfg)

	// Process each service
	allRecommendations := make([]common.Recommendation, 0)
	allResults := make([]common.PurchaseResult, 0)
	serviceStats := make(map[common.ServiceType]ServiceProcessingStats)

	for _, service := range servicesToProcess {
		common.AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		common.AppLogger.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		common.AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		// Process all services with common interface
		serviceRecs, serviceResults := processService(ctx, cfg, recClient, service, isDryRun)
		allRecommendations = append(allRecommendations, serviceRecs...)
		allResults = append(allResults, serviceResults...)

		// Calculate service statistics
		stats := calculateServiceStats(service, serviceRecs, serviceResults)
		serviceStats[service] = stats
		printServiceSummary(service, stats)
	}

	// Generate CSV filename
	finalCSVOutput := csvOutput
	if finalCSVOutput == "" {
		timestamp := time.Now().Format("20060102-150405")
		mode := "dryrun"
		if !isDryRun {
			mode = "purchase"
		}
		finalCSVOutput = fmt.Sprintf("ri-helper-%s-%s.csv", mode, timestamp)
	}

	// Write CSV report
	if err := writeMultiServiceCSVReport(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		common.AppLogger.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(allRecommendations, allResults, serviceStats, isDryRun)
}


func processService(ctx context.Context, cfg aws.Config, recClient common.RecommendationsClientInterface, service common.ServiceType, isDryRun bool) ([]common.Recommendation, []common.PurchaseResult) {
	// Determine regions to process
	regionsToProcess := regions
	if len(regionsToProcess) == 0 {
		// Default to all AWS regions
		common.AppLogger.Printf("🌍 Processing all AWS regions for %s...\n", getServiceDisplayName(service))
		allRegions, err := getAllAWSRegions(ctx, cfg)
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

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	for i, region := range regionsToProcess {
		common.AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", i+1, len(regionsToProcess), region)

		// Fetch recommendations
		params := common.RecommendationParams{
			Service:            service,
			Region:             region,
			PaymentOption:      paymentOption,
			TermInYears:        termYears,
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

		// Apply region and instance type filters
		originalCount := len(recs)
		recs = applyFilters(recs)
		if len(recs) == 0 {
			common.AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
			continue
		}
		if len(recs) < originalCount {
			common.AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
		}

		// Apply coverage
		filteredRecs := applyCommonCoverage(recs, coverage)
		common.AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", coverage, len(filteredRecs))

		serviceRecs = append(serviceRecs, filteredRecs...)

		// Get purchase client
		regionalCfg := cfg.Copy()
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
		if maxInstances > 0 {
			beforeLimit := len(filteredRecs)
			filteredRecs = common.ApplyInstanceLimit(filteredRecs, maxInstances)
			if len(filteredRecs) < beforeLimit {
				common.AppLogger.Printf("  🔒 Applied instance limit: %d recommendations after limiting to %d instances\n", len(filteredRecs), maxInstances)
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
					PurchaseID: generatePurchaseID(rec, region, j+1, true),
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
					if !common.ConfirmPurchase(totalInstances, totalCost, skipConfirmation) {
						// User cancelled - mark all as cancelled and exit
						for k := range filteredRecs {
							cancelResult := common.PurchaseResult{
								Config:     filteredRecs[k],
								Success:    false,
								PurchaseID: generatePurchaseID(filteredRecs[k], region, k+1, false),
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
					result.PurchaseID = generatePurchaseID(rec, region, j+1, false)
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
		}

		// Add service-specific details
		switch r.Config.Service {
		case common.ServiceRDS:
			if rdsDetails, ok := r.Config.ServiceDetails.(*common.RDSDetails); ok {
				oldRec.Engine = rdsDetails.Engine
				oldRec.AZConfig = rdsDetails.AZConfig
			}
		case common.ServiceElastiCache:
			if ecDetails, ok := r.Config.ServiceDetails.(*common.ElastiCacheDetails); ok {
				oldRec.Engine = ecDetails.Engine
				oldRec.AZConfig = "N/A"
			}
		case common.ServiceEC2:
			if ec2Details, ok := r.Config.ServiceDetails.(*common.EC2Details); ok {
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

	// Overall statistics
	totalRecommendations := len(allRecommendations)
	totalSuccessful := 0
	totalFailed := 0
	totalInstances := int32(0)
	totalSavings := float64(0)

	for _, result := range allResults {
		if result.Success {
			totalSuccessful++
			totalInstances += result.Config.Count
		} else {
			totalFailed++
		}
	}

	for _, stats := range serviceStats {
		totalSavings += stats.TotalEstimatedSavings
	}

	fmt.Printf("Total services processed: %d\n", len(serviceStats))
	fmt.Printf("Total recommendations: %d\n", totalRecommendations)
	fmt.Printf("Successful operations: %d\n", totalSuccessful)
	fmt.Printf("Failed operations: %d\n", totalFailed)
	fmt.Printf("Total instances: %d\n", totalInstances)
	if totalSavings > 0 {
		fmt.Printf("Total estimated monthly savings: $%.2f\n", totalSavings)
	}

	// Service breakdown
	if len(serviceStats) > 0 {
		fmt.Println("\n📊 By Service:")
		fmt.Println("--------------------------------------------------")
		for service, stats := range serviceStats {
			fmt.Printf("%-15s | Recs: %3d | Instances: %3d | Success: %3d | Failed: %3d\n",
				getServiceDisplayName(service),
				stats.RecommendationsSelected,
				stats.InstancesProcessed,
				stats.SuccessfulPurchases,
				stats.FailedPurchases)
		}
	}

	// Success rate
	if len(allResults) > 0 {
		successRate := (float64(totalSuccessful) / float64(len(allResults))) * 100
		fmt.Printf("\nOverall success rate: %.1f%%\n", successRate)
	}

	if isDryRun {
		fmt.Println("\n💡 To actually purchase these RIs, run with --purchase flag")
	} else if totalSuccessful > 0 {
		fmt.Println("\n🎉 Purchase operations completed!")
		fmt.Println("⏰ Allow up to 15 minutes for RIs to appear in your account")
	}
}

// applyFilters applies region, instance type, and engine filters to recommendations
func applyFilters(recs []common.Recommendation) []common.Recommendation {
	var filtered []common.Recommendation

	for _, rec := range recs {
		// Apply region filters
		if !shouldIncludeRegion(rec.Region) {
			continue
		}

		// Apply instance type filters
		if !shouldIncludeInstanceType(rec.InstanceType) {
			continue
		}

		// Apply engine filters
		if !shouldIncludeEngine(rec) {
			continue
		}

		filtered = append(filtered, rec)
	}

	return filtered
}

// shouldIncludeRegion checks if a region should be included based on filters
func shouldIncludeRegion(region string) bool {
	// If include list is specified, region must be in it
	if len(includeRegions) > 0 {
		found := false
		for _, r := range includeRegions {
			if r == region {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, region must not be in it
	if len(excludeRegions) > 0 {
		for _, r := range excludeRegions {
			if r == region {
				return false
			}
		}
	}

	return true
}

// shouldIncludeInstanceType checks if an instance type should be included based on filters
func shouldIncludeInstanceType(instanceType string) bool {
	// If include list is specified, instance type must be in it
	if len(includeInstanceTypes) > 0 {
		found := false
		for _, t := range includeInstanceTypes {
			if t == instanceType {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, instance type must not be in it
	if len(excludeInstanceTypes) > 0 {
		for _, t := range excludeInstanceTypes {
			if t == instanceType {
				return false
			}
		}
	}

	return true
}

// shouldIncludeEngine checks if a recommendation should be included based on engine filters
func shouldIncludeEngine(rec common.Recommendation) bool {
	// Extract engine from recommendation
	engine := getEngineFromRecommendation(rec)
	if engine == "" {
		// If no engine info, include by default unless there's an include list
		return len(includeEngines) == 0
	}

	// Normalize engine name to lowercase for comparison
	engine = strings.ToLower(engine)

	// If include list is specified, engine must be in it
	if len(includeEngines) > 0 {
		found := false
		for _, e := range includeEngines {
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
	if len(excludeEngines) > 0 {
		for _, e := range excludeEngines {
			if strings.ToLower(e) == engine {
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