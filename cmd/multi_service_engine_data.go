package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

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
