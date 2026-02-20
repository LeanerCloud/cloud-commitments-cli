package main

import (
	"github.com/LeanerCloud/CUDly/pkg/common"
)

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

// calculateServiceStats calculates statistics for a service based on recommendations and results
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

// printServiceSummary prints a summary for a single service
func printServiceSummary(service common.ServiceType, stats ServiceProcessingStats) {
	AppLogger.Printf("\n📊 %s Summary:\n", getServiceDisplayName(service))
	AppLogger.Printf("  Regions processed: %d\n", stats.RegionsProcessed)
	AppLogger.Printf("  Recommendations: %d\n", stats.RecommendationsSelected)
	AppLogger.Printf("  Instances: %d\n", stats.InstancesProcessed)
	AppLogger.Printf("  Successful: %d, Failed: %d\n", stats.SuccessfulPurchases, stats.FailedPurchases)
	if stats.TotalEstimatedSavings > 0 {
		AppLogger.Printf("  Estimated monthly savings: $%.2f\n", stats.TotalEstimatedSavings)
	}
}

// printMultiServiceSummary prints the final summary for all services
func printMultiServiceSummary(allRecommendations []common.Recommendation, allResults []common.PurchaseResult, serviceStats map[common.ServiceType]ServiceProcessingStats, isDryRun bool) {
	printSummaryHeader(isDryRun)

	spStats, riStats, riAggregates := separateAndAggregateStats(serviceStats)

	printReservedInstancesSection(riStats, riAggregates)

	if spStats.RecommendationsSelected > 0 {
		printSavingsPlansSection(allRecommendations, spStats)
	}

	if len(riStats) > 0 && spStats.RecommendationsSelected > 0 {
		printComparisonSection(allRecommendations, riStats, riAggregates.savings)
	}

	printSuccessRate(riAggregates.success, riAggregates.failed)
	printFinalMessage(isDryRun, riAggregates.success)
}

// riAggregateStats holds aggregated RI statistics
type riAggregateStats struct {
	recommendations int
	instances       int
	savings         float64
	success         int
	failed          int
}

// printSummaryHeader prints the summary header with mode indication
func printSummaryHeader(isDryRun bool) {
	AppLogger.Println("\n🎯 Final Summary:")
	AppLogger.Println("==========================================")
	if isDryRun {
		AppLogger.Println("Mode: DRY RUN")
	} else {
		AppLogger.Println("Mode: ACTUAL PURCHASE")
	}
}

// separateAndAggregateStats separates SP from RI stats and aggregates RI totals
func separateAndAggregateStats(serviceStats map[common.ServiceType]ServiceProcessingStats) (ServiceProcessingStats, map[common.ServiceType]ServiceProcessingStats, riAggregateStats) {
	spStats := ServiceProcessingStats{}
	riStats := make(map[common.ServiceType]ServiceProcessingStats)
	aggregates := riAggregateStats{}

	for service, stats := range serviceStats {
		if service == common.ServiceSavingsPlans {
			spStats = stats
		} else {
			riStats[service] = stats
			aggregates.recommendations += stats.RecommendationsSelected
			aggregates.instances += stats.InstancesProcessed
			aggregates.savings += stats.TotalEstimatedSavings
			aggregates.success += stats.SuccessfulPurchases
			aggregates.failed += stats.FailedPurchases
		}
	}

	return spStats, riStats, aggregates
}

// printReservedInstancesSection prints the RI section with per-service and total stats
func printReservedInstancesSection(riStats map[common.ServiceType]ServiceProcessingStats, aggregates riAggregateStats) {
	if len(riStats) == 0 {
		return
	}

	AppLogger.Println("\n💰 RESERVED INSTANCES:")
	AppLogger.Println("--------------------------------------------------")
	for service, stats := range riStats {
		AppLogger.Printf("%-15s | Recs: %3d | Instances: %3d | Savings: $%8.2f/mo\n",
			getServiceDisplayName(service),
			stats.RecommendationsSelected,
			stats.InstancesProcessed,
			stats.TotalEstimatedSavings)
	}
	AppLogger.Printf("%-15s | Recs: %3d | Instances: %3d | Savings: $%8.2f/mo\n",
		"TOTAL RIs",
		aggregates.recommendations,
		aggregates.instances,
		aggregates.savings)
}

// printSuccessRate prints the overall success rate if results exist
func printSuccessRate(success, failed int) {
	totalResults := success + failed
	if totalResults > 0 {
		successRate := (float64(success) / float64(totalResults)) * 100
		AppLogger.Printf("\nOverall success rate: %.1f%%\n", successRate)
	}
}

// printFinalMessage prints the final message based on mode and results
func printFinalMessage(isDryRun bool, riSuccess int) {
	if isDryRun {
		AppLogger.Println("\n💡 To actually purchase these RIs, run with --purchase flag")
		AppLogger.Println("   Note: Savings Plans purchasing not yet implemented")
	} else if riSuccess > 0 {
		AppLogger.Println("\n🎉 Purchase operations completed!")
		AppLogger.Println("⏰ Allow up to 15 minutes for RIs to appear in your account")
	}
}

// printSavingsPlansSection prints the Savings Plans summary section
func printSavingsPlansSection(allRecommendations []common.Recommendation, spStats ServiceProcessingStats) {
	AppLogger.Println("\n📊 SAVINGS PLANS:")
	AppLogger.Println("--------------------------------------------------")

	// Categorize recommendations by SP type
	breakdown := categorizeSPRecommendations(allRecommendations)

	// Print summary for each type
	printSPTypeSummaries(breakdown)

	// Show best options by category
	printBestSPOptions(breakdown)
}

// printComparisonSection prints the comparison between RIs and Savings Plans
func printComparisonSection(allRecommendations []common.Recommendation, riStats map[common.ServiceType]ServiceProcessingStats, riSavings float64) {
	AppLogger.Println("\n🔄 COMPARISON:")
	AppLogger.Println("--------------------------------------------------")

	// Collect SP savings by type
	spSavings := collectSPSavings(allRecommendations)

	// Collect RI savings by service
	risByService := collectRISavings(riStats)

	// Calculate comparison options
	opts := calculateComparisonOptions(riSavings, spSavings, risByService)

	// Print all options
	printComparisonOptions(opts)

	// Determine and print the best option
	determineBestOption(opts)
}
