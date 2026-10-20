package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

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
