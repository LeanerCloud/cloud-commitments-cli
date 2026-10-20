package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
)

// EC2ClientInterface defines the interface for EC2 operations.
type EC2ClientInterface interface {
	DescribeRegions(ctx context.Context, params *awsec2.DescribeRegionsInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeRegionsOutput, error)
}

// dropDuplicateCheckFailed is a cmd-local drop reason for recommendations
// dropped because the duplicate-purchase check itself failed (not because it
// found and subtracted an actual duplicate, which is common.DropDuplicateDedup).
// It is not a common.Drop* constant because adding one is a
// cloud-commitments-go change; this string is exclusive to this package.
const dropDuplicateCheckFailed = "duplicate-check-failed"

// formatServices formats a list of services for display.
func formatServices(services []common.ServiceType) string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = getServiceDisplayName(s)
	}
	return strings.Join(names, ", ")
}

// getServiceDisplayName returns the display name for a service type.
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
	}
	if name, ok := savingsPlanDisplayName(service); ok {
		return name
	}
	return string(service)
}

// savingsPlanDisplayName returns a friendly label for the four per-plan-type
// SP slugs and the legacy umbrella. Hoisted out of getServiceDisplayName to
// keep the main switch under the gocyclo limit; using a small lookup map
// instead of a five-arm switch also makes adding a fifth plan type a
// one-line change.
func savingsPlanDisplayName(service common.ServiceType) (string, bool) {
	labels := map[common.ServiceType]string{
		common.ServiceSavingsPlansAll:         "Savings Plans",
		common.ServiceSavingsPlansCompute:     "Compute Savings Plans",
		common.ServiceSavingsPlansEC2Instance: "EC2 Instance Savings Plans",
		common.ServiceSavingsPlansSageMaker:   "SageMaker Savings Plans",
		common.ServiceSavingsPlansDatabase:    "Database Savings Plans",
	}
	name, ok := labels[service]
	return name, ok
}

// getAllAWSRegions retrieves all available AWS regions.
func getAllAWSRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	// Create EC2 client to get regions
	ec2Client := awsec2.NewFromConfig(cfg)
	return getAllAWSRegionsWithClient(ctx, ec2Client)
}

// getAllAWSRegionsWithClient retrieves all available AWS regions using the provided client.
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

// discoverRegionsForService returns the regions Cost Explorer has
// recommendations in for the run's term, payment option and lookback. The
// regions come from the recommendations themselves, not from ec2:DescribeRegions:
// Cost Explorer returns linked-account recommendations in regions the caller
// has not opted into, and iterating only the caller's regions dropped them.
func discoverRegionsForService(ctx context.Context, client provider.RecommendationsClient, service common.ServiceType, cfg Config) ([]string, error) {
	recs, err := fetchRecommendationsForRegion(ctx, client, service, "", cfg)
	if err != nil {
		return nil, err
	}

	regionSet := make(map[string]bool)
	for _rvc := range recs {
		rec := recs[_rvc]
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

// determineServicesToProcess returns the list of services to process based on flags.
func determineServicesToProcess(cfg Config) ([]common.ServiceType, error) {
	var services []common.ServiceType
	switch {
	case cfg.AllServices:
		services = getAllServices()
	case len(cfg.Services) > 0:
		parsed, err := parseServices(cfg.Services)
		if err != nil {
			return nil, err
		}
		services = parsed
	default:
		// Default to RDS only for backward compatibility
		return []common.ServiceType{common.ServiceRDS}, nil
	}
	return filterSavingsPlanTypes(services, cfg.IncludeSPTypes, cfg.ExcludeSPTypes)
}

// spTypeServices maps the --include-sp-types / --exclude-sp-types names
// (matched case-insensitively) to the per-plan-type service slugs.
var spTypeServices = map[string]common.ServiceType{
	"compute":     common.ServiceSavingsPlansCompute,
	"ec2instance": common.ServiceSavingsPlansEC2Instance,
	"sagemaker":   common.ServiceSavingsPlansSageMaker,
	"database":    common.ServiceSavingsPlansDatabase,
}

// spTypeSet resolves --include-sp-types / --exclude-sp-types names to services.
func spTypeSet(flag string, names []string) (map[common.ServiceType]bool, error) {
	set := make(map[common.ServiceType]bool, len(names))
	for _, name := range names {
		service, ok := spTypeServices[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("unknown Savings Plan type %q in %s; valid types: Compute, EC2Instance, SageMaker, Database", name, flag)
		}
		set[service] = true
	}
	return set, nil
}

// filterSavingsPlanTypes drops the Savings Plan services that
// --include-sp-types / --exclude-sp-types rule out, so only the requested
// plan types are fetched. Each per-plan-type service queries exactly one plan
// type and the provider ignores the include/exclude lists for it, so the
// narrowing has to happen on the service list. Non-SP services pass through.
func filterSavingsPlanTypes(services []common.ServiceType, include, exclude []string) ([]common.ServiceType, error) {
	includeSet, err := spTypeSet("--include-sp-types", include)
	if err != nil {
		return nil, err
	}
	excludeSet, err := spTypeSet("--exclude-sp-types", exclude)
	if err != nil {
		return nil, err
	}
	kept := make([]common.ServiceType, 0, len(services))
	for _, service := range services {
		if common.IsSavingsPlan(service) && !spTypeWanted(service, includeSet, excludeSet) {
			continue
		}
		kept = append(kept, service)
	}
	return kept, nil
}

func spTypeWanted(service common.ServiceType, includeSet, excludeSet map[common.ServiceType]bool) bool {
	if len(includeSet) > 0 && !includeSet[service] {
		return false
	}
	return !excludeSet[service]
}

// printRunMode prints the current run mode (dry run or purchase).
func printRunMode(isDryRun bool) {
	if isDryRun {
		AppLogger.Println("🔍 DRY RUN MODE - No actual purchases will be made")
	} else {
		AppLogger.Println("💰 PURCHASE MODE - Reserved Instances will be purchased")
	}
}

// printPaymentAndTerm prints the payment option and term information.
func printPaymentAndTerm(cfg Config) {
	AppLogger.Printf("💳 Payment option: %s, Term: %d year(s)\n", cfg.PaymentOption, cfg.TermYears)
}

// generateCSVFilename generates a CSV filename based on the mode and timestamp.
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

// groupRecommendationsByServiceRegion groups recommendations by service and region.
func groupRecommendationsByServiceRegion(recs []common.Recommendation) map[common.ServiceType]map[string][]common.Recommendation {
	recsByServiceRegion := make(map[common.ServiceType]map[string][]common.Recommendation)
	for _rvc := range recs {
		rec := recs[_rvc]
		if _, ok := recsByServiceRegion[rec.Service]; !ok {
			recsByServiceRegion[rec.Service] = make(map[string][]common.Recommendation)
		}
		recsByServiceRegion[rec.Service][rec.Region] = append(recsByServiceRegion[rec.Service][rec.Region], rec)
	}
	return recsByServiceRegion
}

// populateAccountNames populates account names from account IDs using the cache.
func populateAccountNames(ctx context.Context, recs []common.Recommendation, accountCache *AccountAliasCache) {
	for i := range recs {
		if recs[i].Account != "" {
			recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].Account)
		}
	}
}

// adjustRecsForDuplicates checks for existing RIs and adjusts recommendations to avoid duplicates.
func adjustRecsForDuplicates(ctx context.Context, recs []common.Recommendation, serviceClient provider.ServiceClient) ([]common.Recommendation, error) {
	duplicateChecker := NewDuplicateChecker(toolCfg.IdempotencyWindowHours)
	adjustedRecs, _, err := duplicateChecker.AdjustRecommendationsForExisting(ctx, recs, serviceClient)
	if err != nil {
		return recs, err // Return original recommendations with error
	}
	adjustedRecs = mapRecs(adjustedRecs, refreshCountLinear)

	originalInstances := CalculateTotalInstances(recs)
	adjustedInstances := CalculateTotalInstances(adjustedRecs)
	if originalInstances != adjustedInstances {
		AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
	}

	return adjustedRecs, nil
}

// createDryRunResult creates a purchase result for dry run mode.
func createDryRunResult(rec common.Recommendation, region string, index int, cfg Config) common.PurchaseResult {
	return common.PurchaseResult{
		Recommendation: rec,
		Success:        true,
		CommitmentID:   generatePurchaseID(rec, region, index, true, effectiveSizingPct(cfg)),
		DryRun:         true,
		Timestamp:      time.Now(),
	}
}

// executePurchase executes an actual RI purchase.
func executePurchase(ctx context.Context, rec common.Recommendation, region string, index int, serviceClient provider.ServiceClient, cfg Config) common.PurchaseResult {
	AppLogger.Printf("    ⚠️  ACTUAL PURCHASE: About to buy %d instances of %s\n", rec.Count, rec.ResourceType)
	// Compute the descriptive commitment ID up front and hand it to the
	// provider so the purchased commitment is named descriptively at AWS
	// (e.g. RDS ReservedDBInstanceId), not just in our local report.
	commitmentID := generatePurchaseID(rec, region, index, false, effectiveSizingPct(cfg))
	opts := common.PurchaseOptions{Source: common.PurchaseSourceCLI, ReservationID: commitmentID}
	result, err := serviceClient.PurchaseCommitment(ctx, rec, opts)
	if err != nil {
		result.Success = false
		result.Error = err
	}
	if result.CommitmentID == "" {
		result.CommitmentID = commitmentID
	}
	return result
}

// determineRegionsForService determines which regions to process for a given service.
func determineRegionsForService(ctx context.Context, recClient provider.RecommendationsClient, service common.ServiceType, cfg Config) ([]string, error) {
	// Savings Plans are account-level, not regional - only query once, even
	// when --regions lists several. The recommendation call returns the same
	// account-wide set for every region, so honoring --regions here repeats
	// each Savings Plan once per listed region and, now that Savings Plan
	// purchases work, would buy it that many times. EC2Instance plans are
	// still narrowed to the requested regions by shouldIncludeRecommendationRegion
	// on Details.Region.
	if common.IsSavingsPlan(service) {
		AppLogger.Printf("🌍 Fetching account-level Savings Plans recommendations...\n")
		return []string{spAPIRegion}, nil // Single query for account-level data
	}

	// If regions are explicitly configured, use those
	if len(cfg.Regions) > 0 {
		return cfg.Regions, nil
	}

	AppLogger.Printf("🌍 Discovering regions with %s recommendations...\n", getServiceDisplayName(service))
	regions, err := discoverRegionsForService(ctx, recClient, service, cfg)
	if err != nil {
		return nil, err
	}

	AppLogger.Printf("📍 Processing %d region(s)\n", len(regions))
	return regions, nil
}
