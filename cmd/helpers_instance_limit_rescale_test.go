package main

import (
	"math"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// float64Ptr is local to this file so the rescale tests can express "the
// provider returned a monthly breakdown" without borrowing a helper whose
// nil-vs-zero semantics might change elsewhere.
func float64Ptr(v float64) *float64 { return &v }

// TestApplyInstanceLimitRescalesTruncatedRow pins the #1830 invariant: a row
// the cap truncates from N to M carries M/N of the money it entered with.
//
// The assertion is the ratio, not a literal figure, so the test keeps
// meaning if the fixture's dollar values are ever changed.
func TestApplyInstanceLimitRescalesTruncatedRow(t *testing.T) {
	const (
		origCount = 100
		maxKept   = 10
		ratio     = float64(maxKept) / float64(origCount)
	)

	rec := common.Recommendation{
		Service:              common.ServiceEC2,
		Region:               "us-east-1",
		ResourceType:         "m5.large",
		Count:                origCount,
		EstimatedSavings:     600,
		CommitmentCost:       2400,
		OnDemandCost:         3000,
		SavingsPercentage:    20,
		RecurringMonthlyCost: float64Ptr(200),
	}

	got := ApplyInstanceLimit([]common.Recommendation{rec}, maxKept)

	require.Len(t, got, 1)
	require.Equal(t, maxKept, got[0].Count, "the cap must truncate Count to the budget")

	assert.InDelta(t, rec.EstimatedSavings*ratio, got[0].EstimatedSavings, 0.0001,
		"EstimatedSavings is a whole-row total and must scale with the truncated Count")
	assert.InDelta(t, rec.CommitmentCost*ratio, got[0].CommitmentCost, 0.0001,
		"CommitmentCost is a whole-row total and must scale with the truncated Count")
	assert.InDelta(t, rec.OnDemandCost*ratio, got[0].OnDemandCost, 0.0001,
		"OnDemandCost is a whole-row total and must scale with the truncated Count")

	require.NotNil(t, got[0].RecurringMonthlyCost,
		"a present monthly breakdown must stay present after truncation")
	assert.InDelta(t, *rec.RecurringMonthlyCost*ratio, *got[0].RecurringMonthlyCost, 0.0001,
		"RecurringMonthlyCost is a whole-row total and must scale with the truncated Count")

	// SavingsPercentage is a ratio of two figures that scale together, so it
	// is invariant under truncation. Scaling it would be the mirror-image bug.
	assert.InDelta(t, rec.SavingsPercentage, got[0].SavingsPercentage, 0.0001,
		"SavingsPercentage is intensive and must not be scaled")

	// The caller's rec must not be mutated: RecurringMonthlyCost is a pointer
	// and a shared target would corrupt the pre-cap slice the reporter diffs
	// against.
	assert.Equal(t, origCount, rec.Count, "the input rec must not be mutated")
	assert.InDelta(t, 600.0, rec.EstimatedSavings, 0.0001, "the input rec must not be mutated")
	require.NotNil(t, rec.RecurringMonthlyCost)
	assert.InDelta(t, 200.0, *rec.RecurringMonthlyCost, 0.0001,
		"the input rec's pointer target must not be mutated")
}

// TestApplyInstanceLimitLeavesUntruncatedRowsUntouched is the other direction:
// a fix that rescaled every row would satisfy the truncation test above while
// silently shrinking rows that fit inside the budget.
func TestApplyInstanceLimitLeavesUntruncatedRowsUntouched(t *testing.T) {
	monthly := float64Ptr(200)
	rec := common.Recommendation{
		Service:              common.ServiceEC2,
		Region:               "us-east-1",
		ResourceType:         "m5.large",
		Count:                4,
		EstimatedSavings:     600,
		CommitmentCost:       2400,
		OnDemandCost:         3000,
		SavingsPercentage:    20,
		RecurringMonthlyCost: monthly,
	}

	// A budget strictly larger than the row's Count, so the row fits whole.
	got := ApplyInstanceLimit([]common.Recommendation{rec}, 10)

	require.Len(t, got, 1)
	assert.Equal(t, 4, got[0].Count, "a row that fits the budget keeps its Count")
	assert.InDelta(t, 600.0, got[0].EstimatedSavings, 0.0001,
		"an untruncated row must keep its savings; rescaling everything is the mirror-image bug")
	assert.InDelta(t, 2400.0, got[0].CommitmentCost, 0.0001, "an untruncated row must keep its commitment cost")
	assert.InDelta(t, 3000.0, got[0].OnDemandCost, 0.0001, "an untruncated row must keep its on-demand cost")
	require.NotNil(t, got[0].RecurringMonthlyCost)
	assert.InDelta(t, 200.0, *got[0].RecurringMonthlyCost, 0.0001,
		"an untruncated row must keep its monthly cost")
}

// TestApplyInstanceLimitPreservesNilRecurringMonthlyCost pins the
// absent-versus-zero rule: nil means "the provider returned no monthly
// breakdown" and the frontend renders it as "—". Truncation must not turn
// that into a confident $0.
func TestApplyInstanceLimitPreservesNilRecurringMonthlyCost(t *testing.T) {
	recs := []common.Recommendation{
		{
			Service:              common.ServiceEC2,
			Region:               "us-east-1",
			ResourceType:         "truncated",
			Count:                100,
			EstimatedSavings:     600,
			RecurringMonthlyCost: nil,
		},
	}

	got := ApplyInstanceLimit(recs, 10)

	require.Len(t, got, 1)
	require.Equal(t, 10, got[0].Count)
	assert.Nil(t, got[0].RecurringMonthlyCost,
		"a nil monthly cost means absent, not zero, and must survive truncation as nil")
}

// TestApplyInstanceLimitNonPositiveCountIsNotRescaled guards the divide-by-zero
// denominator. A Count of 0 or below buys nothing and cannot be truncated, so
// it must pass through with its money untouched rather than through a ratio
// computed from a zero denominator (NaN) or a negative one (sign flip).
func TestApplyInstanceLimitNonPositiveCountIsNotRescaled(t *testing.T) {
	recs := []common.Recommendation{
		{ResourceType: "zero-count", Count: 0, EstimatedSavings: 600, OnDemandCost: 100},
		{ResourceType: "negative-count", Count: -5, EstimatedSavings: 300, OnDemandCost: 50},
	}

	got := ApplyInstanceLimit(recs, 10)

	require.Len(t, got, 2)
	for i := range got {
		assert.False(t, math.IsNaN(got[i].EstimatedSavings) || math.IsInf(got[i].EstimatedSavings, 0),
			"%s: savings must not become NaN/Inf via a non-positive denominator", got[i].ResourceType)
	}
	assert.Equal(t, 0, got[0].Count)
	assert.InDelta(t, 600.0, got[0].EstimatedSavings, 0.0001, "a zero-count row is not truncated, so it is not rescaled")
	assert.InDelta(t, 100.0, got[0].OnDemandCost, 0.0001, "a zero-count row is not truncated, so it is not rescaled")
	assert.Equal(t, -5, got[1].Count)
	assert.InDelta(t, 300.0, got[1].EstimatedSavings, 0.0001, "a negative-count row is not truncated, so it is not rescaled")
	assert.InDelta(t, 50.0, got[1].OnDemandCost, 0.0001, "a negative-count row is not truncated, so it is not rescaled")
}

// TestApplyInstanceLimitRescalesSavingsPlanHourlyCommitment covers the Savings
// Plan case. SP recs are parsed at Count 1 and so are normally undivisible,
// but --override-count sets Count on every rec without discriminating by
// commitment type (ApplyCountOverride), which lets an SP reach the cap at a
// count the budget can truncate. HourlyCommitment is the SP's actual money
// quantity, so leaving it whole while the cost fields shrink produces exactly
// the internally-inconsistent row #1830 warns about.
func TestApplyInstanceLimitRescalesSavingsPlanHourlyCommitment(t *testing.T) {
	const ratio = 2.0 / 5.0

	rec := common.Recommendation{
		Service:          common.ServiceSavingsPlansCompute,
		Region:           "us-east-1",
		Count:            5,
		EstimatedSavings: 500,
		Details: &common.SavingsPlanDetails{
			PlanType:         "Compute",
			HourlyCommitment: 10,
		},
	}

	got := ApplyInstanceLimit([]common.Recommendation{rec}, 2)

	require.Len(t, got, 1)
	require.Equal(t, 2, got[0].Count)
	assert.InDelta(t, 500*ratio, got[0].EstimatedSavings, 0.0001)

	details, ok := got[0].Details.(*common.SavingsPlanDetails)
	require.True(t, ok, "SP details must survive truncation with their concrete type")
	assert.InDelta(t, 10*ratio, details.HourlyCommitment, 0.0001,
		"an SP's hourly commitment must scale with the truncated Count, like every other extensive figure")

	// The caller's Details must not be mutated through the shared pointer.
	orig, ok := rec.Details.(*common.SavingsPlanDetails)
	require.True(t, ok)
	assert.InDelta(t, 10.0, orig.HourlyCommitment, 0.0001,
		"the input rec's Details pointer target must not be mutated")
}

// TestApplyInstanceLimitTotalMatchesSumOfKeptRows is the run-summary invariant:
// what the summary adds up must equal what the run will actually buy. It is
// asserted against an independently computed expectation rather than against
// the function's own output, so it cannot agree with a wrong implementation.
func TestApplyInstanceLimitTotalMatchesSumOfKeptRows(t *testing.T) {
	recs := []common.Recommendation{
		{ResourceType: "whole", Count: 6, EstimatedSavings: 500},     // fits whole
		{ResourceType: "truncated", Count: 6, EstimatedSavings: 100}, // truncated 6 -> 4
		{ResourceType: "dropped", Count: 6, EstimatedSavings: 900},   // budget exhausted
	}

	got := ApplyInstanceLimit(recs, 10)

	require.Len(t, got, 2, "the third row must be dropped once the budget is spent")
	assert.Equal(t, 10, CalculateTotalInstances(got), "the cap is a hard budget")

	var total float64
	for i := range got {
		total += got[i].EstimatedSavings
	}
	// 500 for the whole row + 100*(4/6) for the truncated one. The dropped
	// row contributes nothing.
	want := 500.0 + 100.0*(4.0/6.0)
	assert.InDelta(t, want, total, 0.0001,
		"the reported total must equal the savings of the instances the run will actually buy")
}
