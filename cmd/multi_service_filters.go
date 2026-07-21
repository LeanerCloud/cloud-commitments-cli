package main

import (
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// applyFilters applies region, instance type, engine, and engine version filters to recommendations.
// currentRegion is the region being processed in the current loop iteration; if non-empty, only
// recommendations for that region are included.
// drops accumulates per-reason drop counts for the end-of-run summary; pass nil to skip tracking.
func applyFilters(recs []common.Recommendation, cfg *Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string, drops *common.DropSummary) []common.Recommendation {
	var filtered []common.Recommendation
	var poolDropCount int
	var poolDropInstances float64

	for i := range recs {
		if cfg.MinPoolSize > 0 && !shouldIncludePoolSize(&recs[i], cfg) {
			poolDropInstances += recs[i].AverageInstancesUsedPerHour
			label := fmt.Sprintf("%s/%s/%s", recs[i].Service, recs[i].Region, recs[i].ResourceType)
			log.Printf("INFO: --min-pool-size=%.1f dropped %s (avg=%.2f < threshold)", cfg.MinPoolSize, label, recs[i].AverageInstancesUsedPerHour)
			poolDropCount++
			drops.Add(common.DropMinPoolSize, 1)
			continue
		}
		adjusted, include, dropReason := processRecommendation(&recs[i], cfg, instanceVersions, versionInfo, currentRegion)
		if include {
			filtered = append(filtered, adjusted)
		} else if dropReason != "" {
			drops.Add(dropReason, 1)
		}
	}

	if poolDropCount > 0 {
		log.Printf("INFO: --min-pool-size dropped %d recommendation(s) (%.2f avg instances/hr total)", poolDropCount, poolDropInstances)
	}

	return filtered
}

// processRecommendation applies all filters to a recommendation and returns
// (adjusted, include, dropReason). dropReason is non-empty only when
// include is false and the drop is worth surfacing in the end-of-run
// summary (dimension-filter mismatches such as region/account/engine are
// expected exclusions and are not counted). The flat boolean-filter checks
// are delegated to passesDimensionFilters to keep this function under
// gocyclo's complexity threshold.
func processRecommendation(rec *common.Recommendation, cfg *Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string) (result common.Recommendation, include bool, dropReason string) {
	// Filter to only recommendations for the current region being processed.
	// This prevents duplicating recommendations across all regions.
	// Skip for Savings Plans (account-level, not regional). No drop reason:
	// same rec will be returned by its own region's pass.
	if currentRegion != "" && rec.Region != currentRegion && !common.IsSavingsPlan(rec.Service) {
		return *rec, false, ""
	}

	if !passesDimensionFilters(rec, cfg) {
		// Dimension mismatches (region/account/engine/instance-type) are expected
		// operator-scoping choices, not drops worth surfacing in the summary.
		// --min-pool-size drops are counted separately in applyFilters before
		// this function runs, so no drop reason is surfaced here.
		return *rec, false, ""
	}

	// Apply engine version filters - adjust instance count by subtracting extended support versions.
	if !cfg.IncludeExtendedSupport {
		adjusted := adjustRecommendationForExcludedVersions(*rec, instanceVersions, versionInfo)
		// Skip if all instances were excluded (count reduced to 0).
		if adjusted.Count <= 0 {
			return adjusted, false, common.DropExtendedSupport
		}
		return adjusted, true, ""
	}

	return *rec, true, ""
}

// passesDimensionFilters runs the stateless include/exclude checks on
// region, instance type, engine, and account. Returns false on
// the first failing filter. Split out of processRecommendation to keep
// each function's cyclomatic complexity under the gocyclo limit; the
// dimension filters here are pure functions of rec + cfg with no side
// effects. Pool-size filtering is handled with logging in applyFilters.
func passesDimensionFilters(rec *common.Recommendation, cfg *Config) bool {
	if !shouldIncludeRegion(rec.Region, cfg) {
		return false
	}
	if !shouldIncludeInstanceType(rec.ResourceType, cfg) {
		return false
	}
	if !shouldIncludeEngine(rec, cfg) {
		return false
	}
	if !shouldIncludeAccount(rec.AccountName, cfg) {
		return false
	}
	return true
}

// shouldIncludePoolSize filters out RI recommendations for pools whose
// AverageInstancesUsedPerHour is below cfg.MinPoolSize. The purpose is to
// drop tiny pools where integer-arithmetic sizing forces 100% coverage
// regardless of --target-coverage (e.g. avg=1 with target=80% -> floor(0.8)=0
// drops, ceil(0.8)=1 over-covers). Setting --min-pool-size=2 keeps pools
// where target can be meaningfully approximated.
//
// Pass-through cases: filter disabled (MinPoolSize<=0), or rec has no
// per-hour signal (avg<=0 -- SPs and recs CE didn't return usage for).
// Those pools aren't sized via the per-hour formula so the filter doesn't
// apply to them.
func shouldIncludePoolSize(rec *common.Recommendation, cfg *Config) bool {
	if cfg.MinPoolSize <= 0 {
		return true
	}
	if rec.AverageInstancesUsedPerHour <= 0 {
		return true
	}
	return rec.AverageInstancesUsedPerHour >= cfg.MinPoolSize
}

// shouldIncludeRegion checks if a region should be included based on filters.
func shouldIncludeRegion(region string, cfg *Config) bool {
	// If include list is specified, region must be in it.
	if len(cfg.IncludeRegions) > 0 && !slices.Contains(cfg.IncludeRegions, region) {
		return false
	}

	// If exclude list is specified, region must not be in it.
	if slices.Contains(cfg.ExcludeRegions, region) {
		return false
	}

	return true
}

// shouldIncludeInstanceType checks if an instance type should be included based on filters.
func shouldIncludeInstanceType(instanceType string, cfg *Config) bool {
	// If include list is specified, instance type must be in it.
	if len(cfg.IncludeInstanceTypes) > 0 && !slices.Contains(cfg.IncludeInstanceTypes, instanceType) {
		return false
	}

	// If exclude list is specified, instance type must not be in it.
	if slices.Contains(cfg.ExcludeInstanceTypes, instanceType) {
		return false
	}

	return true
}

// shouldIncludeEngine checks if a recommendation should be included based on engine filters.
func shouldIncludeEngine(rec *common.Recommendation, cfg *Config) bool {
	// Extract engine from recommendation.
	engine := getEngineFromRecommendation(*rec)
	if engine == "" {
		// If no engine info, include by default unless there's an include list.
		return len(cfg.IncludeEngines) == 0
	}

	// Normalize engine name to lowercase for comparison.
	engine = strings.ToLower(engine)

	// If include list is specified, engine must be in it.
	if len(cfg.IncludeEngines) > 0 {
		found := false
		for _, e := range cfg.IncludeEngines {
			if strings.EqualFold(e, engine) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, engine must not be in it.
	if len(cfg.ExcludeEngines) > 0 {
		for _, e := range cfg.ExcludeEngines {
			if strings.EqualFold(e, engine) {
				return false
			}
		}
	}

	return true
}

// shouldIncludeAccount checks if an account should be included based on filters.
func shouldIncludeAccount(accountName string, cfg *Config) bool {
	// If account name is empty and there are filters, skip it (unless include list is empty).
	if accountName == "" {
		return len(cfg.IncludeAccounts) == 0 && len(cfg.ExcludeAccounts) == 0
	}

	accountLower := strings.ToLower(accountName)

	// Check include list.
	if !checkIncludeList(accountLower, cfg.IncludeAccounts) {
		return false
	}

	// Check exclude list.
	if checkExcludeList(accountLower, cfg.ExcludeAccounts) {
		return false
	}

	return true
}

// checkIncludeList checks if an account matches the include filters.
func checkIncludeList(accountLower string, includeAccounts []string) bool {
	if len(includeAccounts) == 0 {
		return true
	}

	for _, filter := range includeAccounts {
		if accountMatchesFilter(accountLower, filter) {
			return true
		}
	}

	return false
}

// checkExcludeList checks if an account matches any exclude filters.
func checkExcludeList(accountLower string, excludeAccounts []string) bool {
	for _, filter := range excludeAccounts {
		if accountMatchesFilter(accountLower, filter) {
			return true
		}
	}
	return false
}

// accountMatchesFilter checks if an account matches a filter pattern (exact or substring match).
func accountMatchesFilter(accountLower, filter string) bool {
	filterLower := strings.ToLower(filter)
	return filterLower == accountLower || strings.Contains(accountLower, filterLower)
}
