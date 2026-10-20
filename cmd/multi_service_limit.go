package main

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// rankingRule names the ordering --max-instances consumes, so the cap can say
// which rule decided what survived. The two paths rank on different keys
// because they carry different data, and an operator reading the drop list has
// to know which one applied. Typed rather than a bare string so the call sites
// cannot invent a third wording that does not match any implemented ordering.
type rankingRule string

const (
	// rankBySavingsPercentage is the recommendation-driven path: scorer.Score
	// sorts on SavingsPercentage, an intensive rate, before the cap runs.
	rankBySavingsPercentage rankingRule = "highest savings-percentage recommendations first"
	// rankBySavingsPerInstance is the --input-csv path: the cap is a budget in
	// instances, and a CSV may lack the savings percentage (older reports), so
	// sortBySavingsPerInstance derives the rate from the EstimatedSavings and
	// Count columns every CSV carries.
	rankBySavingsPerInstance rankingRule = "highest savings-per-instance rows first (EstimatedSavings / Count, not savings percentage)"
)

// capBinds reports whether --max-instances will actually remove instances from
// recs, rather than being unset or already satisfied.
//
// applyGlobalInstanceLimit and requireRankingSignal have to agree on this
// exactly: the second refuses precisely the runs whose outcome the first would
// otherwise decide on an ordering that carries no information. If the two
// conditions drift apart, either a run is refused for a cap that changes
// nothing, or an unrankable run reaches the cap after all.
func capBinds(recs []common.Recommendation, cfg Config) bool {
	others := withoutSavingsPlans(recs)
	return cfg.MaxInstances > 0 && CalculateTotalInstances(others) > int(cfg.MaxInstances)
}

// applyGlobalInstanceLimit enforces --max-instances once across the entire run.
//
// The flag is documented as a hard cap on the total number of instances
// purchased across all recommendations, so it has to see every service and
// every region together. Applying it inside the per-region fetch instead caps
// each (service, region) pair independently and multiplies the operator's cap
// by the number of pairs.
//
// passed must already be ordered best-first by rule: ApplyInstanceLimit
// consumes the slice in order and drops the tail, so the ordering decides
// which commitments survive. scorer.Score guarantees the
// rankBySavingsPercentage ordering; scoreAndLimitCSVRecs establishes the
// rankBySavingsPerInstance one.
//
// Truncation can push a recommendation under --min-count, which is a hard
// floor rather than advice, so dropTruncatedBelowMinCount removes any such
// recommendation instead of purchasing it short.
//
// Nothing is truncated silently. Every reduced or dropped recommendation is
// named on stdout, and the drops are counted into the end-of-run summary.
func applyGlobalInstanceLimit(passed []common.Recommendation, cfg Config, rule rankingRule, drops *common.DropSummary) []common.Recommendation {
	if !capBinds(passed, cfg) {
		return passed
	}

	// Savings Plans are exempt from the cap and the post-cap floor: their Count
	// is a placeholder 1, not an instance count. The report and the floor see
	// only the instance-based rows, whose capped result is a prefix of its input.
	others := withoutSavingsPlans(passed)
	totalBefore := CalculateTotalInstances(others)
	limited := ApplyInstanceLimit(others, cfg.MaxInstances)
	limited, belowMin := dropTruncatedBelowMinCount(limited, cfg.MinCount)
	reportInstanceLimit(others, limited, len(belowMin), totalBefore, cfg.MaxInstances, rule, drops)
	reportMinCountDrops(belowMin, cfg.MinCount, drops)
	return mergeKeepingOrder(passed, limited)
}

// dropTruncatedBelowMinCount removes recommendations that the cap truncated to
// fewer instances than --min-count allows, returning the survivors and the
// removed recommendations at their truncated counts.
//
// --min-count is a hard floor everywhere else in the codebase, never advice:
// the scorer rejects recommendations under it outright
// (scorer.filterReason, "count %d below minimum %d"), the scheduler's
// meetsMinCount drops them, and both `docs/cli/filtering.md` and
// `docs/cli/README.md` describe it as dropping recommendations below the
// number. filtering.md applies it to "the adjusted instance count (after
// coverage scaling)", so the floor is meant to gate the *sized* count, and
// truncation by --max-instances is another form of sizing.
//
// Buying a commitment smaller than the operator's stated minimum can be worse
// than buying nothing, which is the whole reason the floor exists, so a
// truncated recommendation is dropped rather than purchased short. The freed
// budget is deliberately not redistributed: the next recommendation would have
// to fit in an even smaller remainder and would fail the same floor.
//
// Removal is always from the tail. ApplyInstanceLimit reduces at most one
// recommendation (the one where the budget runs out, which is the last it
// keeps), and every earlier one still carries the full count that already
// cleared the scorer's floor. Taking only from the tail keeps the result a
// prefix of the input, which reportInstanceLimit relies on.
func dropTruncatedBelowMinCount(limited []common.Recommendation, minCount int) (kept, removed []common.Recommendation) {
	if minCount <= 0 {
		return limited, nil
	}
	kept = limited
	for len(kept) > 0 && kept[len(kept)-1].Count < minCount {
		removed = append(removed, kept[len(kept)-1])
		kept = kept[:len(kept)-1]
	}
	return kept, removed
}

// reportMinCountDrops names each recommendation the --min-count floor rejected
// after --max-instances truncated it. It continues the reportInstanceLimit
// listing, where these already appear as dropped, and explains why they were
// not simply purchased at the reduced count.
func reportMinCountDrops(removed []common.Recommendation, minCount int, drops *common.DropSummary) {
	for i := range removed {
		rec := removed[i]
		AppLogger.Printf("     ↳ %s %s %s: the cap left room for only %d instances, below --min-count %d, so it is dropped rather than purchased short\n",
			rec.Service, rec.Region, rec.ResourceType, rec.Count, minCount)
	}
	drops.Add(common.DropMinCountAfterCap, len(removed))
}

// reportInstanceLimit prints what --max-instances removed from the run.
// after must be the prefix of before produced by ApplyInstanceLimit, so
// after[i] and before[i] describe the same recommendation.
//
// rule is the ordering the caller sorted before by, and is printed verbatim:
// the drop list is unreadable without knowing which key decided it, and the
// two paths do not rank on the same key.
//
// belowMinCount is how many of the missing entries were removed by the
// --min-count floor rather than by the budget. They are still listed here as
// dropped (they were), but they are attributed to --min-count-after-cap by
// reportMinCountDrops, so excluding them from this tally keeps each dropped
// recommendation counted exactly once in the end-of-run summary.
func reportInstanceLimit(before, after []common.Recommendation, belowMinCount, totalBefore int, maxInstances int32, rule rankingRule, drops *common.DropSummary) {
	AppLogger.Printf("\n🔒 --max-instances=%d caps the whole run: the %d recommendations that passed scoring total %d instances.\n",
		maxInstances, len(before), totalBefore)
	AppLogger.Printf("   Keeping the %s. The following are reduced or dropped:\n", rule)

	// after is a prefix of before, so membership is positional: every entry past
	// len(after) was removed, whatever its Count (a zero-Count entry equals its
	// own "kept" count of 0 and must still be counted as removed).
	reduced, dropped := 0, 0
	for i := range before {
		rec := before[i]
		if i >= len(after) {
			dropped++
			AppLogger.Printf("   • dropped: %s %s %s (%d instances)\n", rec.Service, rec.Region, rec.ResourceType, rec.Count)
			continue
		}
		if kept := after[i].Count; kept != rec.Count {
			reduced++
			AppLogger.Printf("   • reduced: %s %s %s %d → %d instances\n", rec.Service, rec.Region, rec.ResourceType, rec.Count, kept)
		}
	}

	// The floor only removes entries from the tail of what the cap kept, so
	// they are a subset of the removed entries. DropSummary.Add panics on a
	// negative count; surface the broken invariant with context instead.
	budgetDropped := dropped - belowMinCount
	if budgetDropped < 0 {
		panic(fmt.Sprintf("reportInstanceLimit: %d recommendations attributed to --min-count-after-cap but only %d were removed (before=%d, after=%d)",
			belowMinCount, dropped, len(before), len(after)))
	}
	drops.Add(common.DropMaxInstances, budgetDropped)
	AppLogger.Printf("   Proceeding with %d instances across %d recommendations (%d reduced, %d dropped).\n",
		CalculateTotalInstances(after), len(after), reduced, dropped)
}

// sumPassedRecs returns total instance count and total estimated savings for passed recs.
func sumPassedRecs(recs []common.Recommendation) (total int, totalSavings float64) {
	for _rvc := range recs {
		r := recs[_rvc]
		total += r.Count
		totalSavings += r.EstimatedSavings
	}
	return
}
