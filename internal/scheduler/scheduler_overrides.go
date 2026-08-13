package scheduler

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/CUDly/internal/config"
)

// applyAccountOverrides drops recs that the per-account override marks
// disabled, or whose engine / region / resource_type is excluded by the
// resolved (global ⊕ override) ServiceConfig.
//
// Architecturally this is the read-time half of issue #196 (Option B from
// issue #111). PR #193 already wired the override into the purchase forms;
// this helper applies the remaining override fields (enabled, include /
// exclude lists) to the recs list returned by Scheduler.ListRecommendations.
//
// Why post-DB rather than pushing into ListStoredRecommendations' WHERE clause:
//   - The override table is sparse (most accounts have no row).
//   - Filtering by include/exclude TEXT[] arrays per-(account, provider, service)
//     would force a correlated subquery per row — messier than the in-Go pass.
//   - The existing applySuppressions helper sets the precedent of post-DB
//     filtering inside ListRecommendations.
//
// Errors from the resolver are non-fatal: we log and pass the un-filtered
// list through, matching applySuppressions. Over-show is the safer default
// (an unconfigured-looking dashboard is recoverable; a blanked one is not).
func (s *Scheduler) applyAccountOverrides(ctx context.Context, recs []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	if len(recs) == 0 {
		return recs, nil
	}
	resolved, err := config.ResolveAccountConfigsForRecs(ctx, s.config, recs)
	if err != nil {
		return recs, fmt.Errorf("resolve account configs: %w", err)
	}
	if len(resolved) == 0 {
		return recs, nil
	}
	return filterRecsByResolvedConfigs(recs, resolved), nil
}

// filterRecsByResolvedConfigs drops recs whose resolved per-account
// ServiceConfig says enabled=false, or whose engine / region / resource_type
// is rejected by the resolved include/exclude lists.
//
// Recs without a CloudAccountID (e.g. AWS ambient-credentials path) are
// filtered by the global ServiceConfig alone, which the resolver stores under
// config.AmbientAccountKey: no per-account override can exist for them, but
// the operator's global enabled / min_count / include / exclude policy must
// still apply (issue #1604).
//
// Recs whose triple has no resolved entry (neither a global ServiceConfig row
// nor an override) pass through unfiltered — there is no policy to enforce.
func filterRecsByResolvedConfigs(
	recs []config.RecommendationRecord,
	resolved map[string]*config.ServiceConfig,
) []config.RecommendationRecord {
	// Allocate a fresh backing array so callers that hold a reference to
	// the original recs slice do not see mutations (05-M1).
	out := make([]config.RecommendationRecord, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		cfg := resolved[config.RecConfigKey(&rec)]
		if cfg == nil {
			out = append(out, rec)
			continue
		}
		if !cfg.Enabled {
			continue
		}
		if !engineMatches(rec.Engine, cfg) {
			continue
		}
		if !inListRule(rec.Region, cfg.IncludeRegions, cfg.ExcludeRegions) {
			continue
		}
		if !inListRule(rec.ResourceType, cfg.IncludeTypes, cfg.ExcludeTypes) {
			continue
		}
		if !meetsMinCount(rec.Count, cfg) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// meetsMinCount implements the read-time half of the GUI/CLI --min-count
// filter: a rec is dropped when its count is below cfg.MinCount. A MinCount
// of 0 (the default) disables the filter, matching the CLI flag's
// 0-no-floor semantics. count is the persisted RecommendationRecord.Count
// (already grace-suppression-adjusted by the time this runs, so the floor
// applies to the net count the user would actually see).
func meetsMinCount(count int, cfg *config.ServiceConfig) bool {
	if cfg.MinCount <= 0 {
		return true
	}
	return count >= cfg.MinCount
}

// engineMatches applies the engine include/exclude rule with a "lax for
// engine-less services" carve-out.
//
// Some recommendation services (Savings Plans, Compute) carry no engine
// field. Strictly applying an IncludeEngines list would silently drop every
// such rec the moment a user added an engine include for a different service
// on the same (account, provider). The carve-out: when rec.Engine == "" we
// skip the engine check entirely and let the region / type filters decide.
func engineMatches(engine string, cfg *config.ServiceConfig) bool {
	if engine == "" {
		return true
	}
	return inListRule(engine, cfg.IncludeEngines, cfg.ExcludeEngines)
}

// inListRule returns true when value is allowed by an include/exclude pair:
//   - empty include list = allow anything not on the exclude list
//   - non-empty include list = value must be on it AND not on exclude
//
// Order matters: exclude wins over include when the same value appears on both.
func inListRule(value string, include, exclude []string) bool {
	for _, e := range exclude {
		if e == value {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, i := range include {
		if i == value {
			return true
		}
	}
	return false
}
