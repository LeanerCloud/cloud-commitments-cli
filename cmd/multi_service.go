package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/csv"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

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
	// Validate coverage percentage
	if coverage < 0 || coverage > 100 {
		log.Fatalf("Coverage percentage must be between 0 and 100, got: %.2f", coverage)
	}

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
		fmt.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		fmt.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}

	fmt.Printf("📊 Processing services: %s\n", formatServices(servicesToProcess))

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
		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("🎯 Processing %s\n", getServiceDisplayName(service))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

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
		fmt.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print final summary
	printMultiServiceSummary(allRecommendations, allResults, serviceStats, isDryRun)
}


func processService(ctx context.Context, cfg aws.Config, recClient *common.RecommendationsClient, service common.ServiceType, isDryRun bool) ([]common.Recommendation, []common.PurchaseResult) {
	// Auto-discover regions if none specified
	regionsToProcess := regions
	if len(regionsToProcess) == 0 {
		fmt.Printf("🔍 Auto-discovering regions for %s...\n", getServiceDisplayName(service))
		discoveredRegions, err := discoverRegionsForService(ctx, recClient, service)
		if err != nil {
			log.Printf("❌ Failed to discover regions: %v", err)
			return nil, nil
		}

		if len(discoveredRegions) == 0 {
			fmt.Printf("ℹ️  No regions with %s RI recommendations found\n", getServiceDisplayName(service))
			return nil, nil
		}

		regionsToProcess = discoveredRegions
		fmt.Printf("✅ Found %d region(s) with recommendations: %s\n",
			len(regionsToProcess), strings.Join(regionsToProcess, ", "))
	}

	serviceRecs := make([]common.Recommendation, 0)
	serviceResults := make([]common.PurchaseResult, 0)

	for i, region := range regionsToProcess {
		fmt.Printf("\n  📍 [%d/%d] Region: %s\n", i+1, len(regionsToProcess), region)

		// Fetch recommendations
		params := common.RecommendationParams{
			Service:            service,
			Region:             region,
			PaymentOption:      "partial-upfront",
			TermInYears:        3,
			LookbackPeriodDays: 7,
		}

		recs, err := recClient.GetRecommendations(ctx, params)
		if err != nil {
			log.Printf("  ❌ Failed to fetch recommendations: %v", err)
			continue
		}

		if len(recs) == 0 {
			fmt.Printf("  ℹ️  No recommendations found\n")
			continue
		}

		fmt.Printf("  ✅ Found %d recommendations\n", len(recs))

		// Apply coverage
		filteredRecs := applyCommonCoverage(recs, coverage)
		fmt.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", coverage, len(filteredRecs))

		serviceRecs = append(serviceRecs, filteredRecs...)

		// Get purchase client
		regionalCfg := cfg.Copy()
		regionalCfg.Region = region
		purchaseClient := createPurchaseClient(service, regionalCfg)

		if purchaseClient == nil {
			fmt.Printf("  ⚠️  Purchase client not yet implemented for %s\n", getServiceDisplayName(service))
			continue
		}

		// Process purchases
		for j, rec := range filteredRecs {
			fmt.Printf("    [%d/%d] Processing: %s\n", j+1, len(filteredRecs), rec.Description)

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
				result = purchaseClient.PurchaseRI(ctx, rec)
				if result.PurchaseID == "" {
					result.PurchaseID = generatePurchaseID(rec, region, j+1, false)
				}
				if j < len(filteredRecs)-1 {
					time.Sleep(2 * time.Second)
				}
			}

			serviceResults = append(serviceResults, result)

			if result.Success {
				fmt.Printf("    ✅ Success: %s\n", result.Message)
			} else {
				fmt.Printf("    ❌ Failed: %s\n", result.Message)
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

func discoverRegionsForService(ctx context.Context, client *common.RecommendationsClient, service common.ServiceType) ([]string, error) {
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
	if coverage >= 100.0 {
		return recs
	}

	filtered := make([]common.Recommendation, 0, len(recs))
	for _, rec := range recs {
		adjustedCount := int32(float64(rec.Count) * (coverage / 100.0))
		if adjustedCount > 0 {
			rec.Count = adjustedCount
			filtered = append(filtered, rec)
		}
	}

	return filtered
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
			Region:         r.Config.Region,
			InstanceType:   r.Config.InstanceType,
			PaymentOption:  r.Config.PaymentOption,
			Term:           int32(r.Config.Term), // Fix type conversion
			Count:          r.Config.Count,
			EstimatedCost:  r.Config.EstimatedCost,
			SavingsPercent: r.Config.SavingsPercent,
			Timestamp:      r.Config.Timestamp,
			Description:    r.Config.Description,
		}

		// Add service-specific details if RDS
		if r.Config.Service == common.ServiceRDS {
			if rdsDetails, ok := r.Config.ServiceDetails.(*common.RDSDetails); ok {
				oldRec.Engine = rdsDetails.Engine
				oldRec.AZConfig = rdsDetails.AZConfig
			}
		} else {
			// For non-RDS services, use generic description
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