package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
)

// EC2ClientInterface defines the interface for EC2 operations
type EC2ClientInterface interface {
	DescribeRegions(ctx context.Context, params *awsec2.DescribeRegionsInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeRegionsOutput, error)
}

// formatServices formats a list of services for display
func formatServices(services []common.ServiceType) string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = getServiceDisplayName(s)
	}
	return strings.Join(names, ", ")
}

// getServiceDisplayName returns the display name for a service type
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

// discoverRegionsForService discovers regions that have recommendations for a specific service
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

// applyCommonCoverage applies coverage percentage to recommendations
func applyCommonCoverage(recs []common.Recommendation, coverage float64) []common.Recommendation {
	return ApplyCoverage(recs, coverage)
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
	duplicateChecker := NewDuplicateChecker(0)
	adjustedRecs, _, err := duplicateChecker.AdjustRecommendationsForExisting(ctx, recs, serviceClient)
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
	result, err := serviceClient.PurchaseCommitment(ctx, rec)
	if err != nil {
		result.Success = false
		result.Error = err
	}
	if result.CommitmentID == "" {
		result.CommitmentID = generatePurchaseID(rec, region, index, false, cfg.Coverage)
	}
	return result
}

// determineRegionsForService determines which regions to process for a given service
func determineRegionsForService(ctx context.Context, awsCfg aws.Config, recClient provider.RecommendationsClient, service common.ServiceType, configuredRegions []string) ([]string, error) {
	// If regions are explicitly configured, use those
	if len(configuredRegions) > 0 {
		return configuredRegions, nil
	}

	// Savings Plans are account-level, not regional - only query once
	if service == common.ServiceSavingsPlans {
		AppLogger.Printf("🌍 Fetching account-level Savings Plans recommendations...\n")
		return []string{"us-east-1"}, nil // Single query for account-level data
	}

	// Default to all AWS regions for other services
	AppLogger.Printf("🌍 Processing all AWS regions for %s...\n", getServiceDisplayName(service))
	allRegions, err := getAllAWSRegions(ctx, awsCfg)
	if err != nil {
		return handleRegionDiscoveryError(ctx, recClient, service, err)
	}

	AppLogger.Printf("📍 Processing %d region(s)\n", len(allRegions))
	return allRegions, nil
}

// handleRegionDiscoveryError handles errors during region discovery by falling back to auto-discovery
func handleRegionDiscoveryError(ctx context.Context, recClient provider.RecommendationsClient, service common.ServiceType, originalErr error) ([]string, error) {
	AppLogger.Printf("❌ Failed to get AWS regions: %v\n", originalErr)
	AppLogger.Printf("🔍 Falling back to auto-discovery...\n")

	discoveredRegions, err := discoverRegionsForService(ctx, recClient, service)
	if err != nil {
		return nil, fmt.Errorf("failed to discover regions: %w", err)
	}

	return discoveredRegions, nil
}

// engineVersionData holds the results of engine version queries
type engineVersionData struct {
	instanceVersions map[string][]InstanceEngineVersion
	versionInfo      map[string]MajorEngineVersionInfo
}

// fetchEngineVersionData queries running instances and major engine versions for validation
func fetchEngineVersionData(ctx context.Context, cfg Config) engineVersionData {
	data := engineVersionData{
		instanceVersions: make(map[string][]InstanceEngineVersion),
		versionInfo:      make(map[string]MajorEngineVersionInfo),
	}

	// Query running instances for engine version validation
	data.instanceVersions = queryInstanceVersions(ctx, cfg)

	// Query major engine versions for extended support detection
	data.versionInfo = queryMajorVersions(ctx, cfg)

	return data
}

// queryInstanceVersions queries running instances for engine version validation
func queryInstanceVersions(ctx context.Context, cfg Config) map[string][]InstanceEngineVersion {
	AppLogger.Printf("🔍 Querying running RDS instances across all regions to validate engine versions...\n")
	instanceVersions, err := queryRunningInstanceEngineVersions(ctx, cfg)
	if err != nil {
		AppLogger.Printf("⚠️  Warning: Failed to query running instances for engine version validation: %v\n", err)
		AppLogger.Printf("   Continuing without engine version filtering\n")
		return make(map[string][]InstanceEngineVersion)
	}

	AppLogger.Printf("✅ Found %d instance types with version information across all regions\n", len(instanceVersions))
	return instanceVersions
}

// queryMajorVersions queries major engine versions for extended support detection
func queryMajorVersions(ctx context.Context, cfg Config) map[string]MajorEngineVersionInfo {
	AppLogger.Printf("🔍 Querying AWS RDS major engine versions for extended support information...\n")
	versionInfo, err := queryMajorEngineVersions(ctx, cfg)
	if err != nil {
		AppLogger.Printf("⚠️  Warning: Failed to query major engine versions: %v\n", err)
		AppLogger.Printf("   Continuing without extended support detection\n")
		return make(map[string]MajorEngineVersionInfo)
	}

	AppLogger.Printf("✅ Found support information for %d major engine versions\n", len(versionInfo))
	return versionInfo
}

// regionRecommendations holds the processed recommendations for a single region
type regionRecommendations struct {
	recommendations []common.Recommendation
	results         []common.PurchaseResult
}

// processRegionRecommendations fetches and processes recommendations for a single region
func processRegionRecommendations(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	service common.ServiceType,
	region string,
	regionIndex, totalRegions int,
	engineData engineVersionData,
	isDryRun bool,
	cfg Config,
) regionRecommendations {
	result := regionRecommendations{
		recommendations: make([]common.Recommendation, 0),
		results:         make([]common.PurchaseResult, 0),
	}

	AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", regionIndex, totalRegions, region)

	// Fetch recommendations
	recs := fetchRecommendationsForRegion(ctx, recClient, service, region, cfg)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations found\n")
		return result
	}

	AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

	// Populate account names
	populateRecommendationAccountNames(ctx, recs, accountCache)

	// Apply filters
	recs = applyRegionFilters(recs, engineData, region, cfg)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
		return result
	}

	// Apply coverage and overrides
	filteredRecs := applyCoverageAndOverrides(recs, cfg)

	result.recommendations = filteredRecs

	// Get service client and process purchases
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = region
	serviceClient := createServiceClient(service, regionalCfg)

	if serviceClient == nil {
		AppLogger.Printf("  ⚠️  Service client not yet implemented for %s\n", getServiceDisplayName(service))
		AppLogger.Printf("     (Skipping purchase phase for this service)\n")
		return result
	}

	// Check for duplicate RIs and apply instance limit
	adjustedRecs := checkDuplicatesAndApplyLimit(ctx, filteredRecs, serviceClient, cfg)

	// Process purchases
	regionResults := processPurchaseLoop(ctx, adjustedRecs, region, isDryRun, serviceClient, cfg)
	result.results = regionResults

	return result
}

// fetchRecommendationsForRegion fetches recommendations from AWS for a specific region
func fetchRecommendationsForRegion(
	ctx context.Context,
	recClient provider.RecommendationsClient,
	service common.ServiceType,
	region string,
	cfg Config,
) []common.Recommendation {
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
		AppLogger.Printf("  ❌ Failed to fetch recommendations: %v\n", err)
		return nil
	}

	return recs
}

// populateRecommendationAccountNames populates account names from account IDs
func populateRecommendationAccountNames(ctx context.Context, recs []common.Recommendation, accountCache *AccountAliasCache) {
	for i := range recs {
		if recs[i].Account != "" {
			recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].Account)
		}
	}
}

// applyRegionFilters applies region and instance type filters to recommendations
func applyRegionFilters(
	recs []common.Recommendation,
	engineData engineVersionData,
	region string,
	cfg Config,
) []common.Recommendation {
	originalCount := len(recs)
	recs = applyFilters(recs, cfg, engineData.instanceVersions, engineData.versionInfo, region)

	if len(recs) < originalCount {
		AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
	}

	return recs
}

// applyCoverageAndOverrides applies coverage percentage and count overrides
func applyCoverageAndOverrides(recs []common.Recommendation, cfg Config) []common.Recommendation {
	// Apply coverage
	filteredRecs := applyCommonCoverage(recs, cfg.Coverage)
	AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", cfg.Coverage, len(filteredRecs))

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		filteredRecs = ApplyCountOverride(filteredRecs, cfg.OverrideCount)
	}

	return filteredRecs
}

// checkDuplicatesAndApplyLimit checks for duplicate RIs and applies instance limits
func checkDuplicatesAndApplyLimit(
	ctx context.Context,
	filteredRecs []common.Recommendation,
	serviceClient provider.ServiceClient,
	cfg Config,
) []common.Recommendation {
	// Check for duplicate RIs to avoid double purchasing
	duplicateChecker := NewDuplicateChecker(0)
	adjustedRecs, _, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, filteredRecs, serviceClient)
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

	return filteredRecs
}

// fetchAndFilterRegionRecs fetches, filters, applies coverage, and deduplicates
// recommendations for a single service+region. No purchases are made.
func fetchAndFilterRegionRecs(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	service common.ServiceType,
	region string,
	regionIndex, totalRegions int,
	engineData engineVersionData,
	cfg Config,
) []common.Recommendation {
	AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", regionIndex, totalRegions, region)

	recs := fetchRecommendationsForRegion(ctx, recClient, service, region, cfg)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations found\n")
		return nil
	}
	AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

	populateRecommendationAccountNames(ctx, recs, accountCache)
	recs = applyRegionFilters(recs, engineData, region, cfg)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
		return nil
	}

	recs = applyCoverageAndOverrides(recs, cfg)

	// Deduplication: skip recs matching recently-purchased commitments
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = region
	serviceClient := createServiceClient(service, regionalCfg)
	if serviceClient != nil {
		recs = checkDuplicatesAndApplyLimit(ctx, recs, serviceClient, cfg)
	}

	return recs
}

// fetchAllRecs collects recommendations from all services and regions without purchasing.
func fetchAllRecs(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	servicesToProcess []common.ServiceType,
	engineData engineVersionData,
	cfg Config,
) []common.Recommendation {
	all := make([]common.Recommendation, 0)
	for _, service := range servicesToProcess {
		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🔍 Fetching %s recommendations\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		regions, err := determineRegionsForService(ctx, awsCfg, recClient, service, cfg.Regions)
		if err != nil {
			log.Printf("❌ Failed to determine regions for %s: %v", getServiceDisplayName(service), err)
			continue
		}
		for i, region := range regions {
			recs := fetchAndFilterRegionRecs(ctx, awsCfg, recClient, accountCache, service, region, i+1, len(regions), engineData, cfg)
			all = append(all, recs...)
		}
	}
	return all
}
