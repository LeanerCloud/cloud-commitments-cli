package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/csv"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/spf13/cobra"
)

var (
	regions        []string
	coverage       float64
	actualPurchase bool
	csvOutput      string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error executing command: %v", err)
	}
}

var rootCmd = &cobra.Command{
	Use:   "rds-ri-tool",
	Short: "AWS RDS Reserved Instance purchase tool based on Cost Management recommendations",
	Long: `A tool that fetches RDS Reserved Instance recommendations from AWS Cost Management
and purchases them based on specified coverage percentage. Supports multiple regions.`,
	Run: runTool,
}

func init() {
	rootCmd.Flags().StringSliceVarP(&regions, "regions", "r", []string{}, "AWS regions (comma-separated or multiple flags). If empty, auto-discovers regions from recommendations")
	rootCmd.Flags().Float64VarP(&coverage, "coverage", "c", 80.0, "Percentage of recommendations to purchase (0-100)")
	rootCmd.Flags().BoolVar(&actualPurchase, "purchase", false, "Actually purchase RIs instead of just printing the data")
	rootCmd.Flags().StringVarP(&csvOutput, "output", "o", "", "Output CSV file path (if not specified, auto-generates filename)")
}

// generatePurchaseID creates a descriptive purchase ID for dry runs
func generatePurchaseID(rec recommendations.Recommendation, region string, index int, isDryRun bool) string {
	timestamp := time.Now().Format("20060102-150405")

	// Clean up engine name (remove spaces and special characters)
	cleanEngine := strings.ReplaceAll(strings.ToLower(rec.Engine), " ", "-")
	cleanEngine = strings.ReplaceAll(cleanEngine, "_", "-")

	// Extract instance size from instance type (e.g., "db.t4g.medium" -> "t4g-medium")
	instanceParts := strings.Split(rec.InstanceType, ".")
	instanceSize := "unknown"
	if len(instanceParts) >= 3 {
		instanceSize = fmt.Sprintf("%s-%s", instanceParts[1], instanceParts[2])
	}

	// Determine deployment type
	deployment := "saz" // single-az
	if rec.GetMultiAZ() {
		deployment = "maz" // multi-az
	}

	if isDryRun {
		// Format: dryrun-aurora-mysql-t4g-medium-5x-saz-us-east-1-20250619-150405-001
		return fmt.Sprintf("dryrun-%s-%s-%dx-%s-%s-%s-%03d",
			cleanEngine,
			instanceSize,
			rec.Count,
			deployment,
			region,
			timestamp,
			index)
	} else {
		// For actual purchases, keep it shorter but still descriptive
		// Format: ri-aurora-mysql-t4g-medium-5x-us-east-1-001
		return fmt.Sprintf("ri-%s-%s-%dx-%s-%03d",
			cleanEngine,
			instanceSize,
			rec.Count,
			region,
			index)
	}
}

func runTool(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Validate coverage percentage
	if coverage < 0 || coverage > 100 {
		log.Fatalf("Coverage percentage must be between 0 and 100, got: %.2f", coverage)
	}

	// Determine if this is a dry run
	isDryRun := !actualPurchase
	if isDryRun {
		fmt.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		fmt.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}

	// Load AWS configuration with default region
	defaultRegion := "us-east-1"
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(defaultRegion))
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Auto-discover regions if none specified
	if len(regions) == 0 {
		fmt.Println("🔍 No regions specified - auto-discovering regions from recommendations...")
		discoveredRegions, err := discoverRegionsFromRecommendations(ctx, cfg)
		if err != nil {
			log.Fatalf("Failed to discover regions: %v", err)
		}

		if len(discoveredRegions) == 0 {
			fmt.Println("ℹ️  No regions with RDS RI recommendations found")
			return
		}

		regions = discoveredRegions
		fmt.Printf("✅ Auto-discovered %d region(s) with recommendations: %s\n",
			len(regions), strings.Join(regions, ", "))
	}

	// Validate regions
	if len(regions) == 0 {
		log.Fatalf("At least one region must be specified or discoverable")
	}

	// Process regions
	fmt.Printf("🌍 Processing %d region(s): %s\n", len(regions), strings.Join(regions, ", "))

	// Aggregate results across all regions
	allRecommendations := make([]recommendations.Recommendation, 0)
	allResults := make([]purchase.Result, 0)
	regionStats := make(map[string]RegionProcessingStats)

	// Process each region
	for i, region := range regions {
		fmt.Printf("\n[%d/%d] 📊 Processing region: %s\n", i+1, len(regions), region)

		// Update client region
		regionalCfg := cfg.Copy()
		regionalCfg.Region = region
		regionalRecClient := recommendations.NewClient(regionalCfg)
		regionalPurchaseClient := purchase.NewClient(regionalCfg)

		// Fetch RI recommendations for this region
		fmt.Printf("📊 Fetching RDS RI recommendations for region %s...\n", region)
		recs, err := regionalRecClient.GetRDSRecommendations(ctx, region)
		if err != nil {
			log.Printf("❌ Failed to fetch recommendations for region %s: %v", region, err)
			regionStats[region] = RegionProcessingStats{
				Region:       region,
				Success:      false,
				ErrorMessage: err.Error(),
			}
			continue
		}

		if len(recs) == 0 {
			fmt.Printf("ℹ️  No RDS RI recommendations found for region %s\n", region)
			regionStats[region] = RegionProcessingStats{
				Region:               region,
				Success:              true,
				RecommendationsFound: 0,
				InstancesProcessed:   0,
			}
			continue
		}

		fmt.Printf("✅ Found %d RDS RI recommendations for region %s\n", len(recs), region)

		// Apply coverage percentage
		filteredRecs := applyCoverage(recs, coverage)
		fmt.Printf("📈 Applying %.1f%% coverage for %s: %d recommendations selected\n", coverage, region, len(filteredRecs))

		// Add to global recommendations list
		allRecommendations = append(allRecommendations, filteredRecs...)

		// Print regional summary
		printRegionalSummary(region, filteredRecs)

		// Process purchases for this region
		regionalResults := make([]purchase.Result, 0, len(filteredRecs))

		for j, rec := range filteredRecs {
			fmt.Printf("  [%d/%d] Processing: %s %s (%d instances)\n",
				j+1, len(filteredRecs), rec.Engine, rec.InstanceType, rec.Count)

			var result purchase.Result
			if isDryRun {
				result = purchase.Result{
					Config:     rec,
					Success:    true,
					PurchaseID: generatePurchaseID(rec, region, j+1, true),
					Message:    "Dry run - no actual purchase",
					Timestamp:  time.Now(),
				}
			} else {
				result = regionalPurchaseClient.PurchaseRI(ctx, rec)
				// If the actual purchase doesn't provide a meaningful ID, use our generated one
				if result.PurchaseID == "" {
					result.PurchaseID = generatePurchaseID(rec, region, j+1, false)
				}
				// Add delay to avoid API rate limits
				if j < len(filteredRecs)-1 {
					time.Sleep(2 * time.Second)
				}
			}

			regionalResults = append(regionalResults, result)

			if result.Success {
				fmt.Printf("  ✅ Success: %s\n", result.Message)
			} else {
				fmt.Printf("  ❌ Failed: %s\n", result.Message)
			}
		}

		// Add regional results to global results
		allResults = append(allResults, regionalResults...)

		// Calculate regional statistics
		successCount := 0
		totalInstances := int32(0)
		for _, result := range regionalResults {
			if result.Success {
				successCount++
				totalInstances += result.Config.Count
			}
		}

		regionStats[region] = RegionProcessingStats{
			Region:                  region,
			Success:                 true,
			RecommendationsFound:    len(recs),
			RecommendationsSelected: len(filteredRecs),
			InstancesProcessed:      totalInstances,
			SuccessfulPurchases:     successCount,
			FailedPurchases:         len(regionalResults) - successCount,
		}

		fmt.Printf("📊 Region %s summary: %d successful, %d failed, %d instances\n",
			region, successCount, len(regionalResults)-successCount, totalInstances)
	}

	// Generate CSV filename if not provided
	finalCSVOutput := csvOutput
	if finalCSVOutput == "" {
		// Generate timestamp-based filename
		timestamp := time.Now().Format("20060102-150405")
		mode := "dryrun"
		if !isDryRun {
			mode = "purchase"
		}
		finalCSVOutput = fmt.Sprintf("rds-ri-%s-%s.csv", mode, timestamp)
	}

	// Create CSV writer and write results to file
	csvWriter := csv.NewWriter()
	if err := csvWriter.WriteResults(allResults, finalCSVOutput); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
	} else {
		fmt.Printf("\n📋 CSV report written to: %s\n", finalCSVOutput)
	}

	// Print comprehensive final summary
	printComprehensiveSummary(allRecommendations, allResults, regionStats, isDryRun)
}

// RegionProcessingStats holds statistics for each region processed
type RegionProcessingStats struct {
	Region                  string
	Success                 bool
	ErrorMessage            string
	RecommendationsFound    int
	RecommendationsSelected int
	InstancesProcessed      int32
	SuccessfulPurchases     int
	FailedPurchases         int
}

func applyCoverage(recs []recommendations.Recommendation, coverage float64) []recommendations.Recommendation {
	if coverage >= 100.0 {
		return recs
	}

	filtered := make([]recommendations.Recommendation, 0, len(recs))
	for _, rec := range recs {
		adjustedCount := int32(float64(rec.Count) * (coverage / 100.0))
		if adjustedCount > 0 {
			rec.Count = adjustedCount
			filtered = append(filtered, rec)
		}
	}

	return filtered
}

func printRegionalSummary(region string, recs []recommendations.Recommendation) {
	if len(recs) == 0 {
		return
	}

	fmt.Printf("\n📊 %s Purchase Summary:\n", region)
	fmt.Println("--------------------------------------------------")

	totalInstances := int32(0)
	engineCounts := make(map[string]int32)

	for _, rec := range recs {
		fmt.Printf("%-30s | %-15s | %3d instances\n",
			rec.Engine, rec.InstanceType, rec.Count)
		totalInstances += rec.Count
		engineCounts[rec.Engine] += rec.Count
	}

	fmt.Println("--------------------------------------------------")
	fmt.Printf("Region %s total instances: %d\n", region, totalInstances)
	fmt.Printf("By engine in %s:\n", region)
	for engine, count := range engineCounts {
		fmt.Printf("  %s: %d instances\n", engine, count)
	}
}

func printComprehensiveSummary(allRecommendations []recommendations.Recommendation, allResults []purchase.Result, regionStats map[string]RegionProcessingStats, isDryRun bool) {
	fmt.Println("\n🎯 Comprehensive Summary:")
	fmt.Println("==========================================")

	if isDryRun {
		fmt.Printf("Mode: DRY RUN\n")
	} else {
		fmt.Printf("Mode: ACTUAL PURCHASE\n")
	}

	// Overall statistics
	totalRecommendations := len(allRecommendations)
	totalSuccessful := 0
	totalFailed := 0
	totalInstances := int32(0)
	totalRegionsProcessed := 0
	totalRegionsWithErrors := 0

	for _, result := range allResults {
		if result.Success {
			totalSuccessful++
			totalInstances += result.Config.Count
		} else {
			totalFailed++
		}
	}

	for _, stats := range regionStats {
		if stats.Success {
			totalRegionsProcessed++
		} else {
			totalRegionsWithErrors++
		}
	}

	fmt.Printf("Total regions processed: %d\n", totalRegionsProcessed)
	fmt.Printf("Regions with errors: %d\n", totalRegionsWithErrors)
	fmt.Printf("Total recommendations: %d\n", totalRecommendations)
	fmt.Printf("Successful operations: %d\n", totalSuccessful)
	fmt.Printf("Failed operations: %d\n", totalFailed)
	fmt.Printf("Total instances processed: %d\n", totalInstances)

	// Regional breakdown
	fmt.Println("\n📊 By Region:")
	fmt.Println("--------------------------------------------------")
	for _, region := range regions {
		stats, exists := regionStats[region]
		if !exists {
			fmt.Printf("%-15s | ERROR: Not processed\n", region)
			continue
		}

		if !stats.Success {
			fmt.Printf("%-15s | ERROR: %s\n", region, stats.ErrorMessage)
			continue
		}

		fmt.Printf("%-15s | Found: %2d | Selected: %2d | Instances: %3d | Success: %2d | Failed: %2d\n",
			stats.Region,
			stats.RecommendationsFound,
			stats.RecommendationsSelected,
			stats.InstancesProcessed,
			stats.SuccessfulPurchases,
			stats.FailedPurchases)
	}

	// Engine breakdown across all regions
	if len(allRecommendations) > 0 {
		fmt.Println("\n🔧 By Engine (All Regions):")
		fmt.Println("--------------------------------------------------")
		engineTotals := make(map[string]int32)
		for _, rec := range allRecommendations {
			engineTotals[rec.Engine] += rec.Count
		}
		for engine, count := range engineTotals {
			fmt.Printf("%-25s | %3d instances\n", engine, count)
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

	if totalRegionsWithErrors > 0 {
		fmt.Printf("\n⚠️  %d region(s) had errors. Check the logs above for details.\n", totalRegionsWithErrors)
	}
}

// discoverRegionsFromRecommendations fetches recommendations without region filtering
// to discover which regions have RDS RI recommendations available
func discoverRegionsFromRecommendations(ctx context.Context, cfg aws.Config) ([]string, error) {
	// Ensure we're using us-east-1 for Cost Explorer (it's a global service accessed through us-east-1)
	ceConfig := cfg.Copy()
	ceConfig.Region = "us-east-1"

	// Create a recommendations client for discovery
	recClient := recommendations.NewClient(ceConfig)

	// Fetch recommendations without region filtering
	// Use a simple GetRDSRecommendations call, but don't filter by region yet
	fmt.Println("🔍 Fetching recommendations from Cost Explorer...")
	allRecs, err := recClient.GetRDSRecommendationsForDiscovery(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch recommendations for region discovery: %w", err)
	}

	// Extract unique regions from recommendations
	regionSet := make(map[string]bool)
	for _, rec := range allRecs {
		if rec.Region != "" {
			regionSet[rec.Region] = true
		}
	}

	// Convert map to sorted slice
	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}

	// Sort regions for consistent output
	sort.Strings(regions)

	fmt.Printf("🔍 Discovery scan found %d total recommendations across %d region(s)\n",
		len(allRecs), len(regions))

	return regions, nil
}
