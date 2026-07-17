package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// InstanceEngineVersion stores engine version information for an instance.
type InstanceEngineVersion struct {
	Engine        string
	EngineVersion string
	InstanceClass string
	Region        string
}

// EngineLifecycleInfo stores lifecycle support information for a major engine version.
type EngineLifecycleInfo struct {
	LifecycleSupportStartDate time.Time
	LifecycleSupportEndDate   time.Time
	LifecycleSupportName      string
}

// MajorEngineVersionInfo stores support information for a major engine version.
type MajorEngineVersionInfo struct {
	Engine                    string
	MajorEngineVersion        string
	SupportedEngineLifecycles []EngineLifecycleInfo
}

// queryRunningInstanceEngineVersions queries all running RDS instances and returns their engine versions.
func queryRunningInstanceEngineVersions(ctx context.Context, cfg Config) (map[string][]InstanceEngineVersion, error) {
	awsCfg, err := loadValidationAWSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	regions, err := getAWSRegions(ctx, awsCfg)
	if err != nil {
		return nil, err
	}

	return queryRDSInstancesInRegions(ctx, awsCfg, regions)
}

// loadValidationAWSConfig loads AWS configuration for validation.
func loadValidationAWSConfig(ctx context.Context, cfg Config) (aws.Config, error) {
	validationProfile := cfg.ValidationProfile
	if validationProfile == "" {
		validationProfile = cfg.Profile
	}

	var configOptions []func(*config.LoadOptions) error
	configOptions = append(configOptions, config.WithRegion("us-east-1"))
	if validationProfile != "" {
		configOptions = append(configOptions, config.WithSharedConfigProfile(validationProfile))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load validation AWS config: %w", err)
	}

	return awsCfg, nil
}

// getAWSRegions retrieves all AWS regions.
func getAWSRegions(ctx context.Context, awsCfg aws.Config) ([]ec2types.Region, error) {
	ec2Client := awsec2.NewFromConfig(awsCfg)
	regionsOutput, err := ec2Client.DescribeRegions(ctx, &awsec2.DescribeRegionsInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe regions: %w", err)
	}
	return regionsOutput.Regions, nil
}

// maxConcurrentRegionQueries limits the number of concurrent AWS API calls across regions.
const maxConcurrentRegionQueries = 10

// maxEngineVersionPages caps DescribeDBMajorEngineVersions pagination per engine.
// 20 pages x ~100 records/page = ~2000 records, enough for any engine list (issue #692).
const maxEngineVersionPages = 20

// RDSMajorVersionsClient is the subset of the RDS API needed by
// queryMajorEngineVersionsWithClient, extracted so tests can inject a mock.
type RDSMajorVersionsClient interface {
	DescribeDBMajorEngineVersions(ctx context.Context, params *awsrds.DescribeDBMajorEngineVersionsInput, optFns ...func(*awsrds.Options)) (*awsrds.DescribeDBMajorEngineVersionsOutput, error)
}

// queryRDSInstancesInRegions queries RDS instances in all regions concurrently.
func queryRDSInstancesInRegions(ctx context.Context, awsCfg aws.Config, regions []ec2types.Region) (map[string][]InstanceEngineVersion, error) {
	instanceVersions := make(map[string][]InstanceEngineVersion)
	var mu sync.Mutex
	var wg sync.WaitGroup

	sem := make(chan struct{}, maxConcurrentRegionQueries)

	for _, region := range regions {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore
		go func(regionName string) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Printf("ERROR: panic in region worker (region=%s): %v\n%s", regionName, r, buf[:n])
				}
			}()
			queryRDSInstancesInRegion(ctx, awsCfg, regionName, instanceVersions, &mu)
		}(aws.ToString(region.RegionName))
	}

	wg.Wait()
	return instanceVersions, nil
}

// queryRDSInstancesInRegion queries RDS instances in a single region.
func queryRDSInstancesInRegion(ctx context.Context, awsCfg aws.Config, regionName string, instanceVersions map[string][]InstanceEngineVersion, mu *sync.Mutex) {
	regionCfg := awsCfg.Copy()
	regionCfg.Region = regionName
	rdsClient := awsrds.NewFromConfig(regionCfg)

	var marker *string
	for {
		localVersions, nextMarker, err := queryRDSInstancesPage(ctx, rdsClient, marker, regionName)
		if err != nil {
			log.Printf("⚠️  Warning: Failed to describe RDS instances in %s: %v", regionName, err)
			break
		}

		// Merge into shared map with mutex protection
		mu.Lock()
		for instanceType, versions := range localVersions {
			instanceVersions[instanceType] = append(instanceVersions[instanceType], versions...)
		}
		mu.Unlock()

		if nextMarker == nil {
			break
		}
		marker = nextMarker
	}
}

// queryRDSInstancesPage queries a single page of RDS instances.
func queryRDSInstancesPage(ctx context.Context, rdsClient *awsrds.Client, marker *string, regionName string) (versions map[string][]InstanceEngineVersion, nextMarker *string, err error) {
	input := &awsrds.DescribeDBInstancesInput{Marker: marker}
	output, err := rdsClient.DescribeDBInstances(ctx, input)
	if err != nil {
		return nil, nil, err
	}

	versions = make(map[string][]InstanceEngineVersion)
	for _rvc := range output.DBInstances {
		dbInstance := output.DBInstances[_rvc]
		instanceClass := aws.ToString(dbInstance.DBInstanceClass)
		engine := aws.ToString(dbInstance.Engine)
		engineVersion := aws.ToString(dbInstance.EngineVersion)

		versions[instanceClass] = append(versions[instanceClass], InstanceEngineVersion{
			Engine:        engine,
			EngineVersion: engineVersion,
			InstanceClass: instanceClass,
			Region:        regionName,
		})
	}

	if output.Marker != nil && aws.ToString(output.Marker) != "" {
		nextMarker = output.Marker
	}

	return
}

// queryMajorEngineVersions queries AWS for major engine version lifecycle support information.
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

	return queryMajorEngineVersionsWithClient(ctx, awsrds.NewFromConfig(awsCfg))
}

// queryMajorEngineVersionsWithClient is the testable core of queryMajorEngineVersions.
// It accepts a RDSMajorVersionsClient so tests can inject a mock without real AWS creds.
func queryMajorEngineVersionsWithClient(ctx context.Context, rdsClient RDSMajorVersionsClient) (map[string]MajorEngineVersionInfo, error) {
	// Map of "engine:majorVersion" -> MajorEngineVersionInfo
	versionInfo := make(map[string]MajorEngineVersionInfo)

	// Query all engine types we care about
	engines := []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"}

	for _, engine := range engines {
		if err := fetchMajorEngineVersionsForEngine(ctx, rdsClient, engine, versionInfo); err != nil {
			log.Printf("Warning: Failed to describe major engine versions for %s: %v", engine, err)
		}
	}

	return versionInfo, nil
}

// fetchMajorEngineVersionsForEngine fetches all pages of major engine version
// info for a single engine and merges results into versionInfo. Returns an error
// only on API failure or pagination cap exceeded (issue #692).
func fetchMajorEngineVersionsForEngine(ctx context.Context, rdsClient RDSMajorVersionsClient, engine string, versionInfo map[string]MajorEngineVersionInfo) error {
	var marker *string

	for pageIdx := 0; ; pageIdx++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pageIdx >= maxEngineVersionPages {
			return fmt.Errorf(
				"pagination cap reached after %d pages for engine %s (issue #692)",
				maxEngineVersionPages, engine,
			)
		}

		output, err := rdsClient.DescribeDBMajorEngineVersions(ctx, &awsrds.DescribeDBMajorEngineVersionsInput{
			Engine: aws.String(engine),
			Marker: marker,
		})
		if err != nil {
			return err
		}

		for _, version := range output.DBMajorEngineVersions {
			info := parseDBMajorEngineVersion(version)
			key := fmt.Sprintf("%s:%s", info.Engine, info.MajorEngineVersion)
			versionInfo[key] = info
		}

		if output.Marker == nil || aws.ToString(output.Marker) == "" {
			break
		}
		marker = output.Marker
	}

	return nil
}

// parseDBMajorEngineVersion converts an RDS DBMajorEngineVersion into a
// MajorEngineVersionInfo, extracting lifecycle support dates. Extracted from
// fetchMajorEngineVersionsForEngine to keep its cyclomatic complexity below
// the gocyclo cap.
func parseDBMajorEngineVersion(version rdstypes.DBMajorEngineVersion) MajorEngineVersionInfo {
	info := MajorEngineVersionInfo{
		Engine:             aws.ToString(version.Engine),
		MajorEngineVersion: aws.ToString(version.MajorEngineVersion),
	}

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

	return info
}

// extractMajorVersion extracts the major version from a full engine version string
// Handles special cases like Aurora MySQL version mapping.
func extractMajorVersion(engine, fullVersion string) string {
	if fullVersion == "" {
		return ""
	}

	normalizedEngine := normalizeEngineNameForVersion(engine)

	// Handle Aurora MySQL special format
	if normalizedEngine == "auroramysql" {
		if auroraVersion := extractAuroraMySQLVersion(fullVersion); auroraVersion != "" {
			return auroraVersion
		}
	}

	// For standard versions, extract "X.Y" or "X"
	return extractStandardVersion(fullVersion)
}

// normalizeEngineNameForVersion normalizes an engine name by removing spaces and hyphens.
func normalizeEngineNameForVersion(engine string) string {
	normalized := strings.ToLower(engine)
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return normalized
}

// extractAuroraMySQLVersion extracts the MySQL-compatible version from Aurora MySQL.
func extractAuroraMySQLVersion(fullVersion string) string {
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
	return ""
}

// extractStandardVersion extracts major.minor version from a standard version string.
func extractStandardVersion(fullVersion string) string {
	parts := strings.Split(fullVersion, ".")
	if len(parts) >= 2 {
		return extractMajorMinorVersion(parts[0], parts[1])
	}
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

// extractMajorMinorVersion combines major and minor version parts.
func extractMajorMinorVersion(major, minor string) string {
	// Filter out non-numeric parts in minor version
	numericMinor := extractNumericPrefix(minor)
	if numericMinor != "" {
		return major + "." + numericMinor
	}
	return major
}

// extractNumericPrefix extracts the numeric prefix from a string.
func extractNumericPrefix(s string) string {
	numericPrefix := ""
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			numericPrefix += string(ch)
		} else {
			break
		}
	}
	return numericPrefix
}

// isInExtendedSupport checks if a version is currently in extended support based on lifecycle dates.
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
		if lifecycle.LifecycleSupportName != string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport) {
			continue
		}
		// Not in extended support before its start date
		if now.Before(lifecycle.LifecycleSupportStartDate) {
			continue
		}
		// Past the end date means extended support is over; a zero end date
		// is open-ended (issue #1182)
		if lifecycle.LifecycleSupportEndDate.IsZero() || now.Before(lifecycle.LifecycleSupportEndDate) {
			return true
		}
	}

	return false
}

// adjustRecommendationForExcludedVersions reduces the instance count in a recommendation
// by the number of instances running versions in extended support.
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
