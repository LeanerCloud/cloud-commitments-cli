package main

import (
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
)

// filtersFromConfig maps the CLI Config's dimension-filter and min-pool-size
// fields onto a recfilter.Filters value. Account filtering stays cmd-only
// (see shouldIncludeAccount) so it is not part of recfilter.Filters.
func filtersFromConfig(cfg *Config) recfilter.Filters {
	return recfilter.Filters{
		IncludeRegions:       cfg.IncludeRegions,
		ExcludeRegions:       cfg.ExcludeRegions,
		IncludeInstanceTypes: cfg.IncludeInstanceTypes,
		ExcludeInstanceTypes: cfg.ExcludeInstanceTypes,
		IncludeEngines:       cfg.IncludeEngines,
		ExcludeEngines:       cfg.ExcludeEngines,
		MinPoolSize:          cfg.MinPoolSize,
	}
}

// applyFilters applies region, instance type, engine, and engine version filters to recommendations.
// currentRegion is the region being processed in the current loop iteration; if non-empty, only
// recommendations for that region are included.
// drops accumulates per-reason drop counts for the end-of-run summary; pass nil to skip tracking.
func applyFilters(recs []common.Recommendation, cfg *Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string, drops *common.DropSummary) []common.Recommendation {
	survivors := filtersFromConfig(cfg).ApplyMinPoolSize(recs, log.Printf, drops)

	var filtered []common.Recommendation
	for i := range survivors {
		adjusted, include, dropReason := processRecommendation(&survivors[i], cfg, instanceVersions, versionInfo, currentRegion)
		if include {
			filtered = append(filtered, adjusted)
		} else if dropReason != "" {
			drops.Add(dropReason, 1)
		}
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
//
// Region and account stay here rather than moving into recfilter: the
// region-agnostic handling (#1881) needs providers/aws, which the pkg module
// cannot import, and account filtering is name-substring matching backed by
// AccountAliasCache. Only the instance-type and engine checks are portable.
func passesDimensionFilters(rec *common.Recommendation, cfg *Config) bool {
	if !shouldIncludeRecommendationRegion(rec, cfg) {
		return false
	}
	if !shouldIncludeInstanceType(rec.ResourceType, cfg) {
		return false
	}
	if !shouldIncludeEngine(rec, cfg) {
		return false
	}
	return shouldIncludeAccountIDOrName(rec.Account, rec.AccountName, cfg)
}

// shouldIncludePoolSize checks if a recommendation's pool size meets cfg.MinPoolSize.
// Thin wrapper over recfilter.Filters.IncludesPoolSize; kept so cmd's existing call sites
// and tests are unchanged.
func shouldIncludePoolSize(rec *common.Recommendation, cfg *Config) bool {
	return filtersFromConfig(cfg).IncludesPoolSize(rec)
}

// shouldIncludeRecommendationRegion applies the region filters to a whole
// recommendation rather than to its bare Region field. Savings Plans
// recommendations leave the top-level Region empty and carry the
// EC2Instance-scoped region in Details instead, so matching on rec.Region
// alone dropped every SP recommendation under --include-regions and leaked
// region-scoped ones past --exclude-regions (#1582).
//
// Both predicates are the provider package's, which already filters AWS
// recommendations on these semantics, rather than a second implementation.
// Recommendations from other providers are unaffected: both predicates are
// gated on common.CommitmentSavingsPlan and fall through to the plain
// rec.Region comparison for everything else.
//
// A region-scoped Savings Plan (EC2 Instance, or one whose region Cost
// Explorer omitted) is also narrowed by --regions on its own region. Savings
// Plans are fetched once, not per region (determineRegionsForService), so
// --regions never reaches them through the fetch loop. An unknown region
// does not match, so it is dropped rather than bought outside the scope.
// In --input-csv mode, rows carry Provider (AWS) but no CommitmentType, so
// EffectiveRegion reads Details.Region for --include/--exclude-regions.
// --regions is not applied to CSV Savings Plans, which are used as supplied.
func shouldIncludeRecommendationRegion(rec *common.Recommendation, cfg *Config) bool {
	if awsprovider.IsRegionAgnostic(*rec) {
		return true
	}
	region := awsprovider.EffectiveRegion(*rec)
	if cfg.CSVInput == "" && common.IsSavingsPlan(rec.Service) && len(cfg.Regions) > 0 && !slices.Contains(cfg.Regions, region) {
		return false
	}
	return shouldIncludeRegion(region, cfg)
}

// shouldIncludeRegion checks if a region should be included based on filters.
// Thin wrapper over recfilter.Filters.IncludesRegion; kept so cmd's existing call sites
// and tests are unchanged.
func shouldIncludeRegion(region string, cfg *Config) bool {
	return filtersFromConfig(cfg).IncludesRegion(region)
}

// shouldIncludeInstanceType checks if an instance type should be included based on filters.
// Thin wrapper over recfilter.Filters.IncludesInstanceType; kept so cmd's existing call sites
// and tests are unchanged.
func shouldIncludeInstanceType(instanceType string, cfg *Config) bool {
	return filtersFromConfig(cfg).IncludesInstanceType(instanceType)
}

// shouldIncludeEngine checks if a recommendation should be included based on engine filters.
// Thin wrapper over recfilter.Filters.IncludesEngine; kept so cmd's existing call sites
// and tests are unchanged.
func shouldIncludeEngine(rec *common.Recommendation, cfg *Config) bool {
	return filtersFromConfig(cfg).IncludesEngine(rec)
}

// shouldIncludeAccount checks if an account should be included based on filters.
func shouldIncludeAccount(accountName string, cfg *Config) bool {
	return shouldIncludeAccountIDOrName("", accountName, cfg)
}

// shouldIncludeAccountIDOrName applies --include-accounts / --exclude-accounts
// to an account, matching each filter value against the account ID and the
// account name, both trimmed and case-insensitive, never by substring.
// Name-based values are only decidable once the name is known;
// requireNamesResolved fails the run before this point when it is not.
func shouldIncludeAccountIDOrName(accountID, accountName string, cfg *Config) bool {
	// If the account is unidentified and there are filters, skip it.
	if accountID == "" && accountName == "" {
		return len(cfg.IncludeAccounts) == 0 && len(cfg.ExcludeAccounts) == 0
	}
	matches := func(filter string) bool {
		return accountMatchesFilter(accountID, filter) || accountMatchesFilter(accountName, filter)
	}
	if len(cfg.IncludeAccounts) > 0 && !slices.ContainsFunc(cfg.IncludeAccounts, matches) {
		return false
	}
	return !slices.ContainsFunc(cfg.ExcludeAccounts, matches)
}

// accountIDPattern matches the 12-digit AWS account ID, the only filter value
// that can be decided without the account's name.
var accountIDPattern = regexp.MustCompile(`^\d{12}$`)

// requireResolvedAccountNames runs both account-filter pre-checks over a full
// set of recommendations: requireNamesResolved and requireExactExcludes.
func requireResolvedAccountNames(recs []common.Recommendation, cfg *Config) error {
	if err := requireNamesResolved(recs, cfg); err != nil {
		return err
	}
	return requireExactExcludes(recs, cfg)
}

// requireNamesResolved fails when an account filter holds a name and a
// recommendation's account name is unknown (its name lookup failed, so the ID
// stands in for it, or the CSV row carries no name). Such a recommendation
// cannot be matched against the name, and carrying on would treat an excluded
// account as "not excluded". Filters made only of account IDs never need the
// name.
func requireNamesResolved(recs []common.Recommendation, cfg *Config) error {
	nameFilter := ""
	for _, v := range slices.Concat(cfg.IncludeAccounts, cfg.ExcludeAccounts) {
		v = strings.TrimSpace(v)
		if v == "" || accountIDPattern.MatchString(v) {
			continue
		}
		nameFilter = v
		break
	}
	if nameFilter == "" {
		return nil
	}
	for i := range recs {
		if recs[i].Account != "" && (recs[i].AccountName == "" || recs[i].AccountName == recs[i].Account) {
			return fmt.Errorf("cannot apply the account filter %q: the name of account %s is unknown (organizations:DescribeAccount failed or the CSV row has no name); filter by account ID instead, or fix the access", nameFilter, recs[i].Account)
		}
	}
	return nil
}

// requireExactExcludes fails when an --exclude-accounts value matches no
// account exactly (name or ID) but is part of other account names: the
// operator likely meant a substring, which no longer excludes anything, so
// carrying on would buy in accounts they meant to skip. A value matching
// nothing at all is a harmless no-op. recs must hold every account of the run,
// not one batch, or an exact account in another batch is missed.
func requireExactExcludes(recs []common.Recommendation, cfg *Config) error {
	for _, raw := range cfg.ExcludeAccounts {
		if partial := partialOnlyMatches(recs, raw); len(partial) > 0 {
			return fmt.Errorf("--exclude-accounts %q matches no account exactly but is part of \"%s\"; list the full account names to exclude", strings.TrimSpace(raw), strings.Join(partial, `", "`))
		}
	}
	return nil
}

// partialOnlyMatches returns the sorted account names that contain the filter
// as a substring, or nil when the filter is blank or matches an account exactly.
func partialOnlyMatches(recs []common.Recommendation, filter string) []string {
	f := strings.ToLower(strings.TrimSpace(filter))
	if f == "" {
		return nil
	}
	var partial []string
	for i := range recs {
		if accountMatchesFilter(recs[i].Account, f) || accountMatchesFilter(recs[i].AccountName, f) {
			return nil
		}
		if n := recs[i].AccountName; strings.Contains(strings.ToLower(n), f) && !slices.Contains(partial, n) {
			partial = append(partial, n)
		}
	}
	slices.Sort(partial)
	return partial
}

// accountMatchesFilter reports whether value (an account name or ID) equals the
// filter, ignoring surrounding whitespace and case. Never a substring match, so
// "dev" does not select "devops-prod". An empty value or filter never matches.
func accountMatchesFilter(value, filter string) bool {
	value, filter = strings.TrimSpace(value), strings.TrimSpace(filter)
	return value != "" && filter != "" && strings.EqualFold(value, filter)
}
