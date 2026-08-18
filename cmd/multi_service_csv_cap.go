package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/scorer"
)

// scoreAndLimitCSVRecs enforces --min-count and the run-wide --max-instances
// cap on recommendations loaded from --input-csv, so both spend guards behave
// on the CSV path as they do on the recommendation-driven path. Both are
// documented in docs/cli/filtering.md as flags of the tool, not of a mode.
//
// Previously the CSV path handed the load-ordered slice straight to
// ApplyInstanceLimit, which consumes its input in slice order and drops the
// tail: whichever rows appeared first in the file spent the budget, and
// --min-count was never consulted, so a row the cap truncated below the floor
// was purchased short.
//
// Only MinCount is enforced here. A CSV row carries no savings percentage and
// no break-even figure (writeMultiServiceCSVReport emits neither column and
// parseCSVRecord reads neither), so both fields load as zero and gating on
// them would reject every row of every file. --min-savings-pct and
// --max-break-even-months are therefore refused up front by
// validateCSVModeFilterFlags rather than accepted and ignored here; #1819
// tracks teaching the CSV format to carry those columns.
//
// scorer.Score's own ordering is useless here: SavingsPercentage is uniformly
// zero on loaded rows, so it resolves on EstimatedSavings, a whole-row dollar
// total, while --max-instances is a budget in instances. Ranking a total
// against a per-instance budget spends the whole budget on whichever row is
// merely biggest, not on the rows that return the most per instance bought.
// sortBySavingsPerInstance therefore re-orders the survivors on the rate
// derived from the two columns a CSV does carry, which is the greedy the flag
// implies, and applyGlobalInstanceLimit consumes that order: the cap keeps the
// best-value rows, names every row it reduces or drops, and drops rather than
// shortens anything truncated below --min-count.
func scoreAndLimitCSVRecs(recs []common.Recommendation, cfg Config) ([]common.Recommendation, error) {
	passed := applyMinCountFloor(recs, cfg.MinCount)
	if err := requireRankingSignal(passed, cfg); err != nil {
		return nil, err
	}
	sortBySavingsPerInstance(passed)
	return applyGlobalInstanceLimit(passed, cfg, rankBySavingsPerInstance, nil), nil
}

// applyMinCountFloor drops recommendations below the --min-count floor and
// names each drop on stdout. A floor of 0 disables the flag, and returns recs
// untouched rather than re-ordering them for nothing.
//
// The floor itself is scorer.Score's, so the CSV path and the
// recommendation-driven path reject on the identical predicate and reason
// string. MinCount is the only scorer.Config field this helper ever sets, so
// the "--min-count dropped" prefix cannot come to describe some other filter.
func applyMinCountFloor(recs []common.Recommendation, minCount int) []common.Recommendation {
	if minCount <= 0 {
		return recs
	}
	scored := scorer.Score(recs, scorer.Config{MinCount: minCount})
	for i := range scored.Filtered {
		f := scored.Filtered[i]
		AppLogger.Printf("🔒 --min-count dropped %s %s %s: %s\n",
			f.Recommendation.Service, f.Recommendation.Region, f.Recommendation.ResourceType, f.FilterReason)
	}
	return scored.Passed
}

// savingsPerInstance is the ranking key for CSV rows: the row's monthly
// savings divided by the instances it would buy. --max-instances is a budget
// in instances, so the rows worth keeping are the ones returning the most per
// instance, not the ones whose total happens to be largest.
//
// A non-positive Count buys nothing and has no rate, so it ranks last rather
// than dividing by zero. ApplyInstanceLimit already refuses to credit budget
// back for such a row.
func savingsPerInstance(rec common.Recommendation) float64 {
	if rec.Count <= 0 {
		return 0
	}
	return rec.EstimatedSavings / float64(rec.Count)
}

// sortBySavingsPerInstance orders recs best-value-first, in place.
//
// Rows with equal rates fall back to the same Service|Region|ResourceType key
// scorer.Score tie-breaks on, so the selection is deterministic whatever order
// the file listed them in. The tie-break is spelled out here rather than
// inherited from an upstream sort because --min-count 0 skips the scorer
// entirely, which would otherwise leave file order deciding between equals on
// exactly the path #1741 is about.
//
// It must run before the cap, never after: ApplyInstanceLimit consumes its
// input in slice order, so by the time it returns the selection has already
// been made and re-ordering the survivors decides nothing. The rate values
// themselves are invariant across the cap since #1830 (a truncated row's
// savings and Count are scaled by the same ratio, and savings/count is
// unchanged by scaling both), but that only means a post-cap sort would be
// harmless rather than useful. Ordering still has to happen first.
func sortBySavingsPerInstance(recs []common.Recommendation) {
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if rateA, rateB := savingsPerInstance(a), savingsPerInstance(b); rateA != rateB {
			return rateA > rateB
		}
		keyA := string(a.Service) + "|" + a.Region + "|" + a.ResourceType
		keyB := string(b.Service) + "|" + b.Region + "|" + b.ResourceType
		return keyA < keyB
	})
}

// maxNamedUnrankableRows bounds how many offending rows requireRankingSignal
// names before summarizing the rest, so a large file produces a readable error.
const maxNamedUnrankableRows = 5

// requireRankingSignal refuses a run whose --max-instances cap has to choose
// between rows it cannot rank.
//
// parseCSVFloat leaves EstimatedSavings at zero for a blank cell, and
// getCSVField returns "" for a column that is not in the header at all, so a
// CSV written without an EstimatedSavings column loads every row at zero.
// Nothing downstream can tell that apart from a row genuinely worth $0: the
// value is absent, not zero. With every rate equal, sortBySavingsPerInstance
// and scorer.Score both fall through to the Service|Region|ResourceType
// tie-break, and the cap silently buys by instance-type name while stdout and
// docs/cli/filtering.md both promise it is buying by savings.
//
// Ranking only decides anything when the cap actually binds, so that is the
// only case this refuses; a file with no savings column still runs uncapped,
// and so does one whose total already fits. Money paths in this project fail
// loud rather than picking a defensible-looking default, and #1741's own
// framing is that silent partial enforcement of a spend guard is worse than
// not offering the guard.
func requireRankingSignal(recs []common.Recommendation, cfg Config) error {
	if !capBinds(recs, cfg) {
		return nil
	}

	unrankable := make([]string, 0)
	for i := range recs {
		if recs[i].EstimatedSavings <= 0 {
			unrankable = append(unrankable, fmt.Sprintf("%s %s %s",
				recs[i].Service, recs[i].Region, recs[i].ResourceType))
		}
	}
	if len(unrankable) == 0 {
		return nil
	}

	named := unrankable
	suffix := ""
	if len(named) > maxNamedUnrankableRows {
		named = named[:maxNamedUnrankableRows]
		suffix = fmt.Sprintf(" (and %d more)", len(unrankable)-maxNamedUnrankableRows)
	}
	return fmt.Errorf(
		"--max-instances=%d has to choose which of %d recommendations to buy, but %d row(s) of %s carry no usable EstimatedSavings value: %s%s. "+
			"A blank or missing EstimatedSavings cell is indistinguishable from $0 of savings, so capping on it would pick by instance-type name rather than by value. "+
			"Populate EstimatedSavings for every row, or drop --max-instances and cap the file itself",
		cfg.MaxInstances, len(recs), len(unrankable), cfg.CSVInput, strings.Join(named, ", "), suffix)
}
