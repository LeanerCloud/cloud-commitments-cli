package main

import (
	"github.com/LeanerCloud/CUDly/pkg/common"
)

// ApplyCountOverride replaces the count on every count-denominated
// recommendation with overrideCount, rescaling the money that count derives so
// the row describes the quantity the run will actually buy.
//
// EstimatedSavings, CommitmentCost, OnDemandCost and RecurringMonthlyCost are
// whole-row totals for the count the provider proposed, so replacing Count
// alone leaves the row claiming the money of a quantity nobody will buy
// (#1844). The scaling goes through common.ScaleRecommendationCosts, the same
// helper ApplyInstanceLimit and the coverage paths use, so the arithmetic
// cannot drift between the flags. ProjectedCoverage / ProjectedUtilization are
// count-linear but not re-derived here, exactly as after a cap (#1845).
//
// Savings Plans are left entirely alone, Count included. The flag is
// documented as an override for "all selected RIs", an SP commitment is
// dollar-denominated rather than count-denominated, its Count is a fixed
// placeholder 1 set by the parser, and the SP purchase call reads
// HourlyCommitment rather than Count. Scaling an SP by an instance count would
// multiply the dollars actually committed by an unrelated number.
//
// A row with a non-positive Count is left alone for the same reason
// ApplyInstanceLimit never rescales one: there is no denominator to form a
// ratio from, and setting Count without scaling would reintroduce precisely
// the misstatement above.
//
// Scaling down is unambiguous. Scaling up past the quantity the provider's own
// figures cover is an extrapolation: unit prices are linear so CommitmentCost
// stays true, but savings only accrue on hours a matching resource actually
// runs, so units beyond the observed demand cost money and may save nothing.
// Those rows are reported rather than passed off as measured savings.
//
// Both call sites run this before the run-wide --max-instances cap, so a run
// using both flags scales once from the override ratio and once from the cap
// ratio, which compose to a single net ratio against the provider's figures.
func ApplyCountOverride(recs []common.Recommendation, overrideCount int32) []common.Recommendation {
	if overrideCount <= 0 {
		return recs
	}
	result := make([]common.Recommendation, len(recs))
	var skippedSP, skippedNonPositive, extrapolated int
	for i := range recs {
		rec := recs[i]
		switch {
		case common.IsSavingsPlan(rec.Service):
			result[i] = rec
			skippedSP++
		case rec.Count <= 0:
			result[i] = rec
			skippedNonPositive++
		default:
			result[i] = common.ScaleRecommendationCosts(rec, float64(overrideCount)/float64(rec.Count))
			result[i].Count = int(overrideCount)
			if int(overrideCount) > evidencedCount(rec) {
				extrapolated++
			}
		}
	}
	reportCountOverride(overrideCount, skippedSP, skippedNonPositive, extrapolated)
	return result
}

// evidencedCount is the largest count a recommendation's money figures are
// evidence for: the provider's own pre-sizing proposal when it recorded one,
// otherwise the count the row currently carries. RecommendedCount is populated
// only on the AWS RI path, so the fallback is what the --input-csv and
// non-AWS paths use.
func evidencedCount(rec common.Recommendation) int {
	if rec.RecommendedCount > rec.Count {
		return rec.RecommendedCount
	}
	return rec.Count
}

// reportCountOverride names what --override-count could not honor and what it
// honored only by extrapolation. Nothing here is fatal, but every case would
// otherwise change or fail to change a money figure without telling anyone.
func reportCountOverride(overrideCount int32, skippedSP, skippedNonPositive, extrapolated int) {
	if skippedSP > 0 {
		AppLogger.Printf("⚠️  --override-count left %d Savings Plans recommendation(s) unchanged: an SP commitment is priced in dollars per hour, not in instances, so an instance count cannot size it.\n", skippedSP)
	}
	if skippedNonPositive > 0 {
		AppLogger.Printf("⚠️  --override-count left %d recommendation(s) with a non-positive count unchanged: there is no quantity to rescale their costs from.\n", skippedNonPositive)
	}
	if extrapolated > 0 {
		AppLogger.Printf("⚠️  --override-count=%d exceeds the quantity the provider's figures cover on %d recommendation(s). Their costs scale with the count and stay accurate, but the savings are extrapolated past the observed demand: instances beyond it are billed and may save nothing.\n", overrideCount, extrapolated)
	}
}
