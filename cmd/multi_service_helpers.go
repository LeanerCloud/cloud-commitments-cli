package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	azureprovider "github.com/LeanerCloud/cloud-commitments-go/providers/azure"
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

// engineVersionData holds the results of engine version queries.
// failedRegions lists regions whose RDS inventory could not be read
// completely, mapped to the cause; instanceVersions then holds only what was
// read, so recommendations in those regions cannot be checked (issue #2147).
type engineVersionData struct {
	instanceVersions map[string][]InstanceEngineVersion
	versionInfo      map[string]MajorEngineVersionInfo
	failedRegions    map[string]error
}

// engineVersionFetcher is the seam tests replace so they never reach AWS.
var engineVersionFetcher = fetchEngineVersionData

// extendedSupportCheckNeeded reports whether extended-support exclusion will
// run for the given services: it is on by default and applies to RDS only.
func extendedSupportCheckNeeded(cfg Config, services []common.ServiceType) bool {
	if cfg.IncludeExtendedSupport {
		return false
	}
	return slices.Contains(services, common.ServiceRDS)
}

// fetchEngineVersionData queries running instances and major engine versions
// for extended-support exclusion. When needed is false nothing is queried.
// It fails when exclusion data is unavailable as a whole (region listing or
// any engine lifecycle query); per-region instance failures are returned in
// failedRegions and enforced per recommendation by requireRegionInventory.
func fetchEngineVersionData(ctx context.Context, cfg Config, needed bool) (engineVersionData, error) {
	data := engineVersionData{
		instanceVersions: make(map[string][]InstanceEngineVersion),
		versionInfo:      make(map[string]MajorEngineVersionInfo),
		failedRegions:    make(map[string]error),
	}
	if !needed {
		return data, nil
	}

	var err error
	data.instanceVersions, data.failedRegions, err = queryInstanceVersions(ctx, cfg)
	if err != nil {
		return engineVersionData{}, fmt.Errorf("extended-support exclusion data unavailable: %w (re-run, or pass --include-extended-support to skip the check)", err)
	}
	data.versionInfo, err = queryMajorVersions(ctx, cfg)
	if err != nil {
		return engineVersionData{}, fmt.Errorf("extended-support exclusion data unavailable: %w (re-run, or pass --include-extended-support to skip the check)", err)
	}
	return data, nil
}

// queryInstanceVersions queries running instances for engine version validation.
func queryInstanceVersions(ctx context.Context, cfg Config) (instances map[string][]InstanceEngineVersion, failedRegions map[string]error, err error) {
	AppLogger.Printf("🔍 Querying running RDS instances across all regions to validate engine versions...\n")
	instanceVersions, failedRegions, err := queryRunningInstanceEngineVersions(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	AppLogger.Printf("✅ Found %d instance types with version information across all regions\n", len(instanceVersions))
	if len(failedRegions) > 0 {
		AppLogger.Printf("⚠️  Warning: RDS inventory unavailable in %s; RDS recommendations in those regions will abort the run, other regions proceed\n", describeFailedRegions(failedRegions))
	}
	return instanceVersions, failedRegions, nil
}

// queryMajorVersions queries major engine versions for extended support detection.
func queryMajorVersions(ctx context.Context, cfg Config) (map[string]MajorEngineVersionInfo, error) {
	AppLogger.Printf("🔍 Querying AWS RDS major engine versions for extended support information...\n")
	versionInfo, err := queryMajorEngineVersions(ctx, cfg)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}

	AppLogger.Printf("✅ Found support information for %d major engine versions\n", len(versionInfo))
	return versionInfo, nil
}

// describeFailedRegions renders failed regions and causes in a stable order.
func describeFailedRegions(failed map[string]error) string {
	names := make([]string, 0, len(failed))
	for region := range failed {
		names = append(names, region)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, region := range names {
		parts = append(parts, fmt.Sprintf("%s (%v)", region, failed[region]))
	}
	return strings.Join(parts, "; ")
}

// requireRegionInventory fails when an RDS recommendation sits in a region
// (or has no region) whose instance inventory could not be read, because the
// extended-support exclusion cannot be applied to it. Recommendations in
// healthy regions are unaffected.
func requireRegionInventory(recs []common.Recommendation, cfg Config, data engineVersionData) error {
	if cfg.IncludeExtendedSupport || len(data.failedRegions) == 0 {
		return nil
	}
	affected := make(map[string]error)
	for i := range recs {
		if d, ok := recs[i].Details.(*common.DatabaseDetails); !ok || d == nil {
			continue
		}
		if recs[i].Region == "" {
			maps.Copy(affected, data.failedRegions)
		} else if cause, failed := data.failedRegions[recs[i].Region]; failed {
			affected[recs[i].Region] = cause
		}
	}
	if len(affected) == 0 {
		return nil
	}
	return fmt.Errorf("extended-support exclusion cannot be applied: RDS inventory unavailable in %s (re-run, or pass --include-extended-support to skip the check)", describeFailedRegions(affected))
}

// errRecommendationFetch marks a failure to obtain recommendations from the
// provider, as opposed to a successful but empty answer.
var errRecommendationFetch = errors.New("recommendation fetch failed")

// fetchRecommendationsForRegion fetches recommendations from AWS for a specific region.
// A provider failure is returned, not logged away: an empty slice with a nil
// error means the provider answered and had nothing to recommend. An
// incomplete-but-usable answer (partial sweep) is returned with a warning.
func fetchRecommendationsForRegion(
	ctx context.Context,
	recClient provider.RecommendationsClient,
	service common.ServiceType,
	region string,
	cfg Config,
) ([]common.Recommendation, error) {
	termStr := "1yr"
	if cfg.TermYears == 3 {
		termStr = "3yr"
	}

	lookback := cfg.RecLookbackPeriod
	if lookback == "" {
		lookback = recommendations.DefaultRecLookbackPeriod
	}
	params := common.RecommendationParams{
		Service:        service,
		PaymentOption:  cfg.PaymentOption,
		Term:           termStr,
		LookbackPeriod: lookback,
		// Savings Plans specific filters
		IncludeSPTypes: cfg.IncludeSPTypes,
		ExcludeSPTypes: cfg.ExcludeSPTypes,
		SPAccountScope: cfg.SPAccountScope,
	}
	// For Savings Plans, region is only the endpoint the account-level call is
	// signed in (spAPIRegion), not a filter. The provider folds params.Region
	// into its include-region set, which would drop every EC2 Instance plan
	// recommended outside that endpoint region.
	if !common.IsSavingsPlan(service) {
		params.Region = region
	}

	recs, err := recClient.GetRecommendations(ctx, &params)
	stampSavingsPlanRegions(recs)
	var incomplete *recommendations.IncompleteRecommendationsError
	if errors.As(err, &incomplete) {
		AppLogger.Printf("  ⚠️  %v\n", incomplete)
		return recs, nil
	}
	if partial := azureprovider.AsPartialSubscriptionFailure(err); partial != nil {
		// Keep the subscriptions that did answer, but say plainly that the
		// sweep was incomplete: without this the operator would read a short
		// list as "little to buy here" rather than "some subscriptions were
		// never queried".
		AppLogger.Printf("  ⚠️  Incomplete: %d of %d Azure subscriptions succeeded; %d could not be queried\n",
			partial.Succeeded, partial.Attempted, len(partial.Failed))
		for _, f := range partial.Failed {
			AppLogger.Printf("      subscription %s: %v\n", f.SubscriptionID, f.Err)
		}
		return recs, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Interrupted, not a provider failure: the caller stops on ctx.Err().
		return nil, ctxErr
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s in %s: %w", errRecommendationFetch, getServiceDisplayName(service), region, err)
	}

	return recs, nil
}

// stampSavingsPlanRegions copies the region Cost Explorer reports for an EC2
// Instance Savings Plan (kept in Details) into Recommendation.Region, which is
// what the table, CSV and audit log print. Account-level plans have no region
// and are left empty.
func stampSavingsPlanRegions(recs []common.Recommendation) {
	for i := range recs {
		if recs[i].CommitmentType == common.CommitmentSavingsPlan {
			recs[i].Region = awsprovider.EffectiveRegion(recs[i])
		}
	}
}

// populateRecommendationAccountNames populates account names from account IDs.
// An account whose name cannot be resolved gets its ID as the name (member
// accounts cannot call organizations:DescribeAccount). That is fine for
// ID-based account filters; requireNamesResolved fails the run when a
// name-based --include-accounts / --exclude-accounts value needs the name.
func populateRecommendationAccountNames(ctx context.Context, recs []common.Recommendation, accountCache *AccountAliasCache, cfg Config) error {
	for i := range recs {
		if recs[i].Account != "" {
			recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].Account)
		}
	}
	if err := requireIdentifiedForExclude(recs, &cfg); err != nil {
		return err
	}
	return requireNamesResolved(recs, &cfg)
}

// applyRegionFilters applies region and instance type filters to recommendations.
func applyRegionFilters(
	recs []common.Recommendation,
	engineData engineVersionData,
	region string,
	cfg Config,
	drops *common.DropSummary,
) []common.Recommendation {
	originalCount := len(recs)
	recs = applyFilters(recs, &cfg, engineData.instanceVersions, engineData.versionInfo, region, drops)

	if len(recs) < originalCount {
		AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
	}

	return recs
}

// applyCoverageAndOverrides applies sizing (coverage % or target-coverage)
// and count overrides. Sizing mode is selected by cfg.TargetCoverage: > 0
// routes to ApplyTargetCoverage; otherwise the legacy ApplyCoverage path.
// coverageMap (when non-nil) populates Recommendation.ExistingCoveragePct
// before sizing so the under-buy formula can subtract pool coverage already
// owned. Nil map = no-op; recs keep their default zero values and the
// sizing path falls back to the no-existing-commitments formula.
//
// expiringCommitments (when non-empty and cfg.RebuyWindowDays > 0) reduces
// ExistingCoveragePct further by the share of pool demand attributable to
// RIs expiring within the window, so --target-coverage recommends
// replacements before the cliff. Doesn't run if either input is empty.
// RDS recs sized by the family-NU pass (target-coverage mode) ignore that
// per-rec adjustment and instead get the same commitments and window through
// ApplyFamilyNUSizingRDSWithExpiryOptions, which deducts expiring normalized units
// from the family's coverage and credits replacement RIs bought inside the
// coverage lookback (so a re-run does not buy them again); this deliberately
// raises RDS counts and costs when RIs are about to expire (cli#2132). Callers passing nil commitments
// get the plain ApplyFamilyNUSizingRDS behavior.
//
// drops accumulates per-reason drop counts for the end-of-run summary.
// Pass nil to skip tracking.
func applyCoverageAndOverrides(recs []common.Recommendation, cfg Config, coverageMap recommendations.PoolCoverageMap, expiringCommitments []common.Commitment, drops *common.DropSummary) []common.Recommendation {
	recommendations.ApplyCoverageMapToRecommendations(recs, coverageMap)
	if cfg.RebuyWindowDays > 0 && len(expiringCommitments) > 0 {
		n, missingDemand := recommendations.AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, expiringCommitments, cfg.RebuyWindowDays, coverageMap)
		if n > 0 {
			AppLogger.Printf("  ⏰ Treating %d recs as partially uncovered (RIs expiring within %d days)\n", n, cfg.RebuyWindowDays)
		}
		if missingDemand > 0 {
			AppLogger.Printf("  ⚠️  Skipped expiry adjustment for %d recommendations because pool demand was unavailable; coverage was left unchanged\n", missingDemand)
		}
	}
	// Family-NU sizing for RDS recs: AWS rec API already bundles size-flex
	// demand within a family into one rec at one size, so per-pool sizing
	// under-buys. Run the family-NU pass first (RDS only, target-coverage
	// mode only); non-RDS recs flow through the per-pool path unchanged.
	// When TargetCoverage isn't set (legacy --coverage path) the per-pool
	// flow handles everything as before.
	var sizedRDS []common.Recommendation
	rest := recs
	if cfg.TargetCoverage > 0 {
		var familyDrops recommendations.FamilyDropCounts
		var expiry recommendations.FamilyExpiryReport
		lookbackDays := coverageLookbackDays(cfg)
		sizedRDS, rest, familyDrops, expiry = recommendations.ApplyFamilyNUSizingRDSWithExpiryOptions(
			recs, coverageMap, cfg.TargetCoverage, expiringCommitments,
			recommendations.FamilyExpiryOptions{WindowDays: cfg.RebuyWindowDays, CoverageLookbackDays: lookbackDays})
		logFamilyExpiry(expiry, cfg.RebuyWindowDays, lookbackDays)
		drops.Add(common.DropFamilyAlreadyAtTarget, familyDrops.AlreadyAtTarget)
		drops.Add(common.DropFamilyNoNUSignal, familyDrops.NoNUSignal)
		drops.Add(common.DropFamilySizedToZero, familyDrops.SizedToZero)
	}
	filteredRecs := applySizing(rest, cfg, cfg.Coverage, drops)
	filteredRecs = append(filteredRecs, sizedRDS...)
	if cfg.TargetCoverage > 0 {
		AppLogger.Printf("  🎯 Applying %.1f%% target-coverage: %d recommendations selected (%d via family-NU, %d via per-pool)\n",
			cfg.TargetCoverage, len(filteredRecs), len(sizedRDS), len(filteredRecs)-len(sizedRDS))
	} else {
		AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", cfg.Coverage, len(filteredRecs))
	}

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		filteredRecs = ApplyCountOverride(filteredRecs, cfg.OverrideCount)
	}

	return filteredRecs
}

// logFamilyExpiry reports how rebuy-window expiry was folded into the RDS
// family-NU sizing. Unusable RIs (unparsable instance class, or an engine
// spelled differently from the coverage report) were NOT deducted, so they are
// warned about rather than read as "no expiry".
func logFamilyExpiry(expiry recommendations.FamilyExpiryReport, windowDays, lookbackDays int) {
	if expiry.ExpiringNU > 0 {
		// The exact deduction is capped by the covered and active NU per family
		// (go#342); report the inputs rather than claim a net figure.
		AppLogger.Printf("  ⏰ RDS family sizing: %.0f normalized units of RIs expiring within %d days considered against family coverage; %.0f units of replacement RIs started in the last %d days credited back\n", expiry.ExpiringNU, windowDays, expiry.ReplacementNU, lookbackDays)
	}
	if expiry.Unusable > 0 {
		AppLogger.Printf("  ⚠️  %d RDS reserved instances expiring within %d days could not be applied to family sizing (unparsable instance class or no matching coverage family); their expiry was NOT deducted\n", expiry.Unusable, windowDays)
	}
}

// checkDuplicates adjusts recommendations against already-owned RIs so the run
// does not double-purchase existing capacity.
//
// It deliberately does NOT apply --max-instances. This function runs once per
// (service, region), and capping here caps each region independently, which
// multiplies the operator's cap by the number of service/region pairs. The cap
// is applied once run-wide instead, after every region has been fetched
// (applyGlobalInstanceLimit in multi_service.go).
//
// The duplicate check is the only guard between a re-run and a double
// purchase, so a failed check (throttling, a describe-call AccessDenied, a
// transient 5xx) must not fall back to the un-deduplicated counts on a
// purchase run: that would buy reserved capacity the account already owns.
// isDryRun selects the behavior on error: a dry run logs a loud warning and
// continues (nothing is bought, so reporting fidelity wins), while a purchase
// run refuses to spend and drops these recommendations entirely: an
// over-purchase of reserved capacity is not reversible.
//
// drops accumulates per-reason drop counts for the end-of-run summary; pass nil to skip.
//
// A recommendation dropped because the check itself failed is counted under
// dropDuplicateCheckFailed, not common.DropDuplicateDedup: the latter means
// "an actual duplicate was found and subtracted", and reusing it for a failed
// check would report the failure as a successful dedup in the summary.
func checkDuplicates(
	ctx context.Context,
	filteredRecs []common.Recommendation,
	serviceClient provider.ServiceClient,
	isDryRun bool,
	drops *common.DropSummary,
) []common.Recommendation {
	// Check for duplicate RIs to avoid double purchasing
	duplicateChecker := NewDuplicateChecker(toolCfg.IdempotencyWindowHours)
	adjustedRecs, dedupedOut, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, filteredRecs, serviceClient)
	if err != nil {
		if ctx.Err() != nil {
			// Interrupted: terminal in dry and real runs alike, never
			// "continue with un-deduplicated counts". The caller stops on ctx.Err().
			return nil
		}
		if !isDryRun {
			AppLogger.Printf("  ❌ Refusing to purchase %d instance(s): could not check for existing RIs (%v). Dropping these recommendations rather than risking a duplicate purchase.\n", CalculateTotalInstances(filteredRecs), err)
			drops.Add(dropDuplicateCheckFailed, len(filteredRecs))
			return nil
		}
		AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v (dry run; continuing with un-deduplicated counts)\n", err)
		// Continue with original filteredRecs on error; adjustedRecs is not used in this branch.
	} else {
		// Always use the adjusted recommendations (they might have different counts even if same length)
		originalInstances := CalculateTotalInstances(filteredRecs)
		adjustedInstances := CalculateTotalInstances(adjustedRecs)
		if originalInstances != adjustedInstances {
			AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
		}
		drops.Add(common.DropDuplicateDedup, len(dedupedOut))
		filteredRecs = mapRecs(adjustedRecs, refreshCountLinear)
	}

	return filteredRecs
}

// fetchAndFilterRegionRecs fetches, filters, applies coverage, and deduplicates
// recommendations for a single service+region. No purchases are made.
// coverageMap (when non-nil) feeds existing-pool coverage into the sizing
// step for --target-coverage. drops accumulates per-reason drop counts for
// the end-of-run summary; pass nil to skip tracking.
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
	coverageMap recommendations.PoolCoverageMap,
	drops *common.DropSummary,
) ([]common.Recommendation, error) {
	AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", regionIndex, totalRegions, region)

	recs, err := fetchRecommendationsForRegion(ctx, recClient, service, region, cfg)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations found\n")
		return nil, nil
	}
	AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

	if err := populateRecommendationAccountNames(ctx, recs, accountCache, cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", errRecommendationFetch, err)
	}
	if err := requireRegionInventory(recs, cfg, engineData); err != nil {
		return nil, err
	}
	recs = applyRegionFilters(recs, engineData, region, cfg, drops)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
		return nil, nil
	}

	// Build the regional service client once and reuse for both expiry-aware
	// coverage adjustment and the downstream duplicate check.
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = region
	serviceClient := createServiceClient(service, regionalCfg)

	// Fetch existing commitments up front when --rebuy-window-days is set so
	// the sizing step can deduct expiring RIs from ExistingCoveragePct.
	// Best-effort: a failure here logs and continues; expiry adjustment
	// becomes a no-op for this region.
	var expiringCommitments []common.Commitment
	if cfg.RebuyWindowDays > 0 && serviceClient != nil {
		commits, err := serviceClient.GetExistingCommitments(ctx)
		if err != nil {
			AppLogger.Printf("  ⚠️  Could not fetch existing commitments for expiry check: %v\n", err)
		} else {
			expiringCommitments = commits
		}
	}

	recs = applyCoverageAndOverrides(recs, cfg, coverageMap, expiringCommitments, drops)

	// Deduplication: skip recs matching recently-purchased commitments.
	// --max-instances is NOT applied here; it is enforced once run-wide by the
	// caller so the cap covers every service and region together.
	if serviceClient != nil {
		recs = checkDuplicates(ctx, recs, serviceClient, effectiveDryRun(cfg), drops)
	}

	return recs, nil
}

// fetchAllRecs collects recommendations from all services and regions without
// purchasing. coverageMap (when non-nil) populates Recommendation.ExistingCoveragePct
// on each rec before sizing, so --target-coverage can subtract what's already
// owned in the same pool. The returned DropSummary accumulates per-reason
// drop counts across every service and region for the end-of-run summary.
func fetchAllRecs(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	servicesToProcess []common.ServiceType,
	engineData engineVersionData,
	cfg Config,
	coverageMap recommendations.PoolCoverageMap,
) ([]common.Recommendation, *common.DropSummary, error) {
	all := make([]common.Recommendation, 0)
	drops := common.NewDropSummary()
	for _, service := range servicesToProcess {
		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🔍 Fetching %s recommendations\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		regions, err := determineRegionsForService(ctx, recClient, service, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return nil, drops, nil
			}
			return nil, nil, fmt.Errorf("%w: determining regions for %s: %w", errRecommendationFetch, getServiceDisplayName(service), err)
		}
		for i, region := range regions {
			if ctx.Err() != nil {
				return nil, drops, nil // partial data from an interrupted fetch is not a result
			}
			recs, err := fetchAndFilterRegionRecs(ctx, awsCfg, recClient, accountCache, service, region, i+1, len(regions), engineData, cfg, coverageMap, drops)
			if ctx.Err() != nil {
				return nil, drops, nil // interrupted mid-fetch: not a result, not a failure
			}
			if err != nil {
				return nil, nil, err
			}
			all = append(all, recs...)
		}
	}
	// Checked once over every account seen in any batch, including those the
	// filters dropped, so an exact account in another batch is not missed.
	if err := requireExactExcludes(accountCache.knownAccounts(), &cfg); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errRecommendationFetch, err)
	}
	return all, drops, nil
}
