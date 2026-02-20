package main

import (
	"slices"
	"strings"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// applyFilters applies region, instance type, engine, and engine version filters to recommendations
// currentRegion is the region being processed in the current loop iteration - if non-empty, only recommendations for that region are included
func applyFilters(recs []common.Recommendation, cfg Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string) []common.Recommendation {
	var filtered []common.Recommendation

	for _, rec := range recs {
		adjusted, include := processRecommendation(rec, cfg, instanceVersions, versionInfo, currentRegion)
		if include {
			filtered = append(filtered, adjusted)
		}
	}

	return filtered
}

// processRecommendation applies all filters to a recommendation and returns the adjusted recommendation and whether to include it
func processRecommendation(rec common.Recommendation, cfg Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string) (common.Recommendation, bool) {
	// Filter to only recommendations for the current region being processed
	// This prevents duplicating recommendations across all regions
	// Skip this filter for Savings Plans as they are account-level, not regional
	if currentRegion != "" && rec.Region != currentRegion && rec.Service != common.ServiceSavingsPlans {
		return rec, false
	}

	// Apply basic filters
	if !shouldIncludeRegion(rec.Region, cfg) {
		return rec, false
	}

	if !shouldIncludeInstanceType(rec.ResourceType, cfg) {
		return rec, false
	}

	if !shouldIncludeEngine(rec, cfg) {
		return rec, false
	}

	if !shouldIncludeAccount(rec.AccountName, cfg) {
		return rec, false
	}

	// Apply engine version filters - adjust instance count by subtracting extended support versions
	if !cfg.IncludeExtendedSupport {
		rec = adjustRecommendationForExcludedVersions(rec, instanceVersions, versionInfo)
		// Skip if all instances were excluded (count reduced to 0)
		if rec.Count <= 0 {
			return rec, false
		}
	}

	return rec, true
}

// shouldIncludeRegion checks if a region should be included based on filters
func shouldIncludeRegion(region string, cfg Config) bool {
	// If include list is specified, region must be in it
	if len(cfg.IncludeRegions) > 0 && !slices.Contains(cfg.IncludeRegions, region) {
		return false
	}

	// If exclude list is specified, region must not be in it
	if slices.Contains(cfg.ExcludeRegions, region) {
		return false
	}

	return true
}

// shouldIncludeInstanceType checks if an instance type should be included based on filters
func shouldIncludeInstanceType(instanceType string, cfg Config) bool {
	// If include list is specified, instance type must be in it
	if len(cfg.IncludeInstanceTypes) > 0 && !slices.Contains(cfg.IncludeInstanceTypes, instanceType) {
		return false
	}

	// If exclude list is specified, instance type must not be in it
	if slices.Contains(cfg.ExcludeInstanceTypes, instanceType) {
		return false
	}

	return true
}

// shouldIncludeEngine checks if a recommendation should be included based on engine filters
func shouldIncludeEngine(rec common.Recommendation, cfg Config) bool {
	// Extract engine from recommendation
	engine := getEngineFromRecommendation(rec)
	if engine == "" {
		// If no engine info, include by default unless there's an include list
		return len(cfg.IncludeEngines) == 0
	}

	// Normalize engine name to lowercase for comparison
	engine = strings.ToLower(engine)

	// If include list is specified, engine must be in it
	if len(cfg.IncludeEngines) > 0 {
		found := false
		for _, e := range cfg.IncludeEngines {
			if strings.ToLower(e) == engine {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// If exclude list is specified, engine must not be in it
	if len(cfg.ExcludeEngines) > 0 {
		for _, e := range cfg.ExcludeEngines {
			if strings.ToLower(e) == engine {
				return false
			}
		}
	}

	return true
}

// shouldIncludeAccount checks if an account should be included based on filters
func shouldIncludeAccount(accountName string, cfg Config) bool {
	// If account name is empty and there are filters, skip it (unless include list is empty)
	if accountName == "" {
		return len(cfg.IncludeAccounts) == 0 && len(cfg.ExcludeAccounts) == 0
	}

	accountLower := strings.ToLower(accountName)

	// Check include list
	if !checkIncludeList(accountLower, cfg.IncludeAccounts) {
		return false
	}

	// Check exclude list
	if checkExcludeList(accountLower, cfg.ExcludeAccounts) {
		return false
	}

	return true
}

// checkIncludeList checks if an account matches the include filters
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

// checkExcludeList checks if an account matches any exclude filters
func checkExcludeList(accountLower string, excludeAccounts []string) bool {
	for _, filter := range excludeAccounts {
		if accountMatchesFilter(accountLower, filter) {
			return true
		}
	}
	return false
}

// accountMatchesFilter checks if an account matches a filter pattern (exact or substring match)
func accountMatchesFilter(accountLower, filter string) bool {
	filterLower := strings.ToLower(filter)
	return filterLower == accountLower || strings.Contains(accountLower, filterLower)
}
