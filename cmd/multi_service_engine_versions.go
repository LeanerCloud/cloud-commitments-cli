package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

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
//
// The returned failedRegions maps each region whose inventory could not be read
// completely to the cause; the instance map still holds whatever was read. A
// non-nil error means the inventory is unusable as a whole (config, region
// listing or canceled context).
func queryRunningInstanceEngineVersions(ctx context.Context, cfg Config) (instances map[string][]InstanceEngineVersion, failedRegions map[string]error, err error) {
	awsCfg, err := loadValidationAWSConfig(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	regions, err := getAWSRegions(ctx, awsCfg)
	if err != nil {
		return nil, nil, err
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

// maxInstancePages caps DescribeDBInstances pagination per region. A page holds
// 100 instances by default, so 200 pages covers 20k instances per region, far
// above the RDS per-account instance quota, and bounds a non-terminating marker.
const maxInstancePages = 200

// RDSInstancesClient is the subset of the RDS API needed by
// queryRDSInstancesPaged, extracted so tests can inject a fake.
type RDSInstancesClient interface {
	DescribeDBInstances(ctx context.Context, params *awsrds.DescribeDBInstancesInput, optFns ...func(*awsrds.Options)) (*awsrds.DescribeDBInstancesOutput, error)
}

// RDSMajorVersionsClient is the subset of the RDS API needed by
// queryMajorEngineVersionsWithClient, extracted so tests can inject a mock.
type RDSMajorVersionsClient interface {
	DescribeDBMajorEngineVersions(ctx context.Context, params *awsrds.DescribeDBMajorEngineVersionsInput, optFns ...func(*awsrds.Options)) (*awsrds.DescribeDBMajorEngineVersionsOutput, error)
}

// queryRDSInstancesInRegions queries RDS instances in all regions concurrently.
// A region that fails (API error on any page, or a worker panic) is reported in
// failedRegions rather than silently treated as an empty inventory. A canceled
// ctx is returned as the error.
func queryRDSInstancesInRegions(ctx context.Context, awsCfg aws.Config, regions []ec2types.Region) (instanceVersions map[string][]InstanceEngineVersion, failedRegions map[string]error, err error) {
	instanceVersions = make(map[string][]InstanceEngineVersion)
	failedRegions = make(map[string]error)
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
					mu.Lock()
					failedRegions[regionName] = fmt.Errorf("panic in region worker: %v", r)
					mu.Unlock()
				}
			}()
			if regionErr := queryRDSInstancesInRegion(ctx, awsCfg, regionName, instanceVersions, &mu); regionErr != nil {
				mu.Lock()
				failedRegions[regionName] = regionErr
				mu.Unlock()
			}
		}(aws.ToString(region.RegionName))
	}

	wg.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	return instanceVersions, failedRegions, nil
}

// queryRDSInstancesInRegion queries RDS instances in a single region and
// returns the first page error, leaving whatever earlier pages merged in place.
func queryRDSInstancesInRegion(ctx context.Context, awsCfg aws.Config, regionName string, instanceVersions map[string][]InstanceEngineVersion, mu *sync.Mutex) error {
	regionCfg := awsCfg.Copy()
	regionCfg.Region = regionName
	return queryRDSInstancesPaged(ctx, awsrds.NewFromConfig(regionCfg), regionName, instanceVersions, mu)
}

// queryRDSInstancesPaged pages through DescribeDBInstances, failing loudly on a
// canceled ctx or when maxInstancePages is reached rather than truncating.
func queryRDSInstancesPaged(ctx context.Context, rdsClient RDSInstancesClient, regionName string, instanceVersions map[string][]InstanceEngineVersion, mu *sync.Mutex) error {
	var marker *string
	for pageIdx := 0; ; pageIdx++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pageIdx >= maxInstancePages {
			return fmt.Errorf("RDS instance pagination cap reached after %d pages in %s", maxInstancePages, regionName)
		}
		localVersions, nextMarker, err := queryRDSInstancesPage(ctx, rdsClient, marker, regionName)
		if err != nil {
			return fmt.Errorf("failed to describe RDS instances in %s: %w", regionName, err)
		}

		// Merge into shared map with mutex protection
		mu.Lock()
		for instanceType, versions := range localVersions {
			instanceVersions[instanceType] = append(instanceVersions[instanceType], versions...)
		}
		mu.Unlock()

		if nextMarker == nil {
			return nil
		}
		marker = nextMarker
	}
}

// queryRDSInstancesPage queries a single page of RDS instances.
func queryRDSInstancesPage(ctx context.Context, rdsClient RDSInstancesClient, marker *string, regionName string) (versions map[string][]InstanceEngineVersion, nextMarker *string, err error) {
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
	var engineErrs []error

	for _, engine := range engines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := fetchMajorEngineVersionsForEngine(ctx, rdsClient, engine, versionInfo); err != nil {
			// Canceled caller context is terminal (issue #1325); check ctx.Err(),
			// not the wrapped API error, so SDK-internal timeouts are reported as engine failures.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			// Lifecycle data for an engine is required to tell whether its
			// instances are on extended support; keep trying the remaining
			// engines so the error reports every unavailable one (issue #2147).
			engineErrs = append(engineErrs, fmt.Errorf("major engine versions for %s: %w", engine, err))
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(engineErrs) > 0 {
		return nil, errors.Join(engineErrs...)
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
