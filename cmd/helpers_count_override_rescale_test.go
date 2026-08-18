package main

import (
	"math"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countDenominatedRec is the fixture the override tests scale. Every money
// field is a whole-row total for Count, which is exactly the property
// --override-count has to preserve.
func countDenominatedRec(count int) common.Recommendation {
	return common.Recommendation{
		Service:              common.ServiceEC2,
		Region:               "us-east-1",
		ResourceType:         "m5.large",
		Count:                count,
		RecommendedCount:     count,
		EstimatedSavings:     600,
		CommitmentCost:       2400,
		OnDemandCost:         3000,
		SavingsPercentage:    20,
		RecurringMonthlyCost: float64Ptr(200),
	}
}

// assertScaledBy checks that every extensive money field of got is in's value
// times ratio, and that the intensive ones are untouched. The assertion is the
// ratio rather than a literal figure, so it keeps its meaning if the fixture's
// dollar values change.
func assertScaledBy(t *testing.T, in, got common.Recommendation, ratio float64) {
	t.Helper()

	assert.InDelta(t, in.EstimatedSavings*ratio, got.EstimatedSavings, 0.0001,
		"EstimatedSavings is a whole-row total and must scale with the overridden Count")
	assert.InDelta(t, in.CommitmentCost*ratio, got.CommitmentCost, 0.0001,
		"CommitmentCost is a whole-row total and must scale with the overridden Count")
	assert.InDelta(t, in.OnDemandCost*ratio, got.OnDemandCost, 0.0001,
		"OnDemandCost is a whole-row total and must scale with the overridden Count")

	require.NotNil(t, got.RecurringMonthlyCost,
		"a present monthly breakdown must stay present after the override")
	assert.InDelta(t, *in.RecurringMonthlyCost*ratio, *got.RecurringMonthlyCost, 0.0001,
		"RecurringMonthlyCost is a whole-row total and must scale with the overridden Count")

	// SavingsPercentage is a ratio of two figures that scale together, so it is
	// invariant. Scaling it would be the mirror-image bug.
	assert.InDelta(t, in.SavingsPercentage, got.SavingsPercentage, 0.0001,
		"SavingsPercentage is intensive and must not be scaled")

	// RecommendedCount is a frozen record of the provider's own proposal. The
	// override replaces what we will buy, not what the provider proposed.
	assert.Equal(t, in.RecommendedCount, got.RecommendedCount,
		"RecommendedCount records the provider's proposal and must survive the override")
}

// TestApplyCountOverrideRescalesDownscaledRow pins the #1844 invariant in the
// direction the issue calls unambiguous: a row overridden from N to a smaller M
// carries M/N of the money it entered with, so the per-instance rate the row
// implies is unchanged.
func TestApplyCountOverrideRescalesDownscaledRow(t *testing.T) {
	const (
		origCount = 100
		override  = 5
		ratio     = float64(override) / float64(origCount)
	)

	rec := countDenominatedRec(origCount)
	perInstanceBefore := rec.EstimatedSavings / float64(rec.Count)

	got := ApplyCountOverride([]common.Recommendation{rec}, override)

	require.Len(t, got, 1)
	require.Equal(t, override, got[0].Count, "the override must replace Count")
	assertScaledBy(t, rec, got[0], ratio)

	assert.InDelta(t, perInstanceBefore, got[0].EstimatedSavings/float64(got[0].Count), 0.0001,
		"savings per instance is the rate the row asserts and must survive the override")
}

// TestApplyCountOverrideRescalesUpscaledRow covers the direction --max-instances
// never exercises. An override can raise the count above the provider's
// proposal, and leaving the money at the smaller quantity understates both the
// savings and, more dangerously, what the run will be charged.
func TestApplyCountOverrideRescalesUpscaledRow(t *testing.T) {
	const (
		origCount = 10
		override  = 20
		ratio     = float64(override) / float64(origCount)
	)

	rec := countDenominatedRec(origCount)

	got := ApplyCountOverride([]common.Recommendation{rec}, override)

	require.Len(t, got, 1)
	require.Equal(t, override, got[0].Count)
	assertScaledBy(t, rec, got[0], ratio)

	assert.Greater(t, got[0].CommitmentCost, rec.CommitmentCost,
		"buying more than the provider proposed must not report the smaller quantity's cost")
}

// TestApplyCountOverrideLeavesSavingsPlansUntouched is the both-directions
// assertion, in one call so a fix that rescales every row cannot pass it. An SP
// commitment is priced in dollars per hour rather than in instances: its Count
// is a placeholder the SP purchase call never reads, so sizing it by an
// instance count would multiply the dollars actually committed by an unrelated
// number.
func TestApplyCountOverrideLeavesSavingsPlansUntouched(t *testing.T) {
	const override = 20

	ri := countDenominatedRec(10)
	sp := common.Recommendation{
		Service:          common.ServiceSavingsPlansCompute,
		Region:           "us-east-1",
		Count:            1,
		EstimatedSavings: 500,
		CommitmentCost:   1200,
		Details: &common.SavingsPlanDetails{
			PlanType:         "Compute",
			HourlyCommitment: 10,
		},
	}

	got := ApplyCountOverride([]common.Recommendation{ri, sp}, override)

	require.Len(t, got, 2)

	// The count-denominated row is rescaled.
	require.Equal(t, override, got[0].Count)
	assertScaledBy(t, ri, got[0], float64(override)/float64(ri.Count))

	// The dollar-denominated row is not touched at all, Count included.
	assert.Equal(t, 1, got[1].Count,
		"an SP is one commitment, not N instances, so the override must not set its Count")
	assert.InDelta(t, 500.0, got[1].EstimatedSavings, 0.0001, "an SP's savings must not be scaled by an instance count")
	assert.InDelta(t, 1200.0, got[1].CommitmentCost, 0.0001, "an SP's cost must not be scaled by an instance count")

	details, ok := got[1].Details.(*common.SavingsPlanDetails)
	require.True(t, ok, "SP details must survive with their concrete type")
	assert.InDelta(t, 10.0, details.HourlyCommitment, 0.0001,
		"the hourly commitment is what the SP purchase actually buys and must not move with --override-count")
}

// TestApplyCountOverrideNonPositiveCountIsNotRescaled guards the divide-by-zero
// denominator. A Count of 0 or below carries no per-unit rate to scale from, so
// the row passes through untouched rather than through a ratio computed from a
// zero denominator (Inf) or a negative one (sign flip). Setting Count without
// scaling would reintroduce the very misstatement #1844 is about.
func TestApplyCountOverrideNonPositiveCountIsNotRescaled(t *testing.T) {
	recs := []common.Recommendation{
		{ResourceType: "zero-count", Count: 0, EstimatedSavings: 600, OnDemandCost: 100},
		{ResourceType: "negative-count", Count: -5, EstimatedSavings: 300, OnDemandCost: 50},
	}

	got := ApplyCountOverride(recs, 10)

	require.Len(t, got, 2)
	for i := range got {
		assert.False(t, math.IsNaN(got[i].EstimatedSavings) || math.IsInf(got[i].EstimatedSavings, 0),
			"%s: savings must not become NaN/Inf via a non-positive denominator", got[i].ResourceType)
		assert.False(t, math.IsNaN(got[i].OnDemandCost) || math.IsInf(got[i].OnDemandCost, 0),
			"%s: on-demand cost must not become NaN/Inf via a non-positive denominator", got[i].ResourceType)
	}

	assert.Equal(t, 0, got[0].Count, "a row with no quantity to scale from keeps its count")
	assert.InDelta(t, 600.0, got[0].EstimatedSavings, 0.0001, "a zero-count row is not rescaled")
	assert.InDelta(t, 100.0, got[0].OnDemandCost, 0.0001, "a zero-count row is not rescaled")
	assert.Equal(t, -5, got[1].Count, "a row with no quantity to scale from keeps its count")
	assert.InDelta(t, 300.0, got[1].EstimatedSavings, 0.0001, "a negative-count row is not rescaled")
	assert.InDelta(t, 50.0, got[1].OnDemandCost, 0.0001, "a negative-count row is not rescaled")
}

// TestApplyCountOverridePreservesNilRecurringMonthlyCost pins the
// absent-versus-zero rule: nil means "the provider returned no monthly
// breakdown" and renders as an em dash rather than $0. The override must not
// turn that into a confident zero.
func TestApplyCountOverridePreservesNilRecurringMonthlyCost(t *testing.T) {
	recs := []common.Recommendation{
		{
			Service:              common.ServiceEC2,
			ResourceType:         "no-monthly-breakdown",
			Count:                100,
			EstimatedSavings:     600,
			RecurringMonthlyCost: nil,
		},
	}

	got := ApplyCountOverride(recs, 5)

	require.Len(t, got, 1)
	require.Equal(t, 5, got[0].Count)
	assert.Nil(t, got[0].RecurringMonthlyCost,
		"a nil monthly cost means absent, not zero, and must survive the override as nil")
}

// TestApplyCountOverrideDoesNotAliasCallerRecs pins that the caller's slice
// survives the override intact. The pipeline hands the pre-override slice on to
// the cap, whose reporter diffs one against the other, so a shared pointer
// target would corrupt what the operator is shown was reduced.
func TestApplyCountOverrideDoesNotAliasCallerRecs(t *testing.T) {
	before := []common.Recommendation{countDenominatedRec(100)}

	got := ApplyCountOverride(before, 5)

	require.Len(t, got, 1)
	require.NotNil(t, got[0].RecurringMonthlyCost)
	require.NotNil(t, before[0].RecurringMonthlyCost)

	assert.NotSame(t, before[0].RecurringMonthlyCost, got[0].RecurringMonthlyCost,
		"the scaled monthly cost must be a fresh pointer, not a write through the caller's")
	assert.Equal(t, 100, before[0].Count, "the input rec must not be mutated")
	assert.InDelta(t, 600.0, before[0].EstimatedSavings, 0.0001, "the input rec must not be mutated")
	assert.InDelta(t, 200.0, *before[0].RecurringMonthlyCost, 0.0001,
		"the input rec's pointer target must not be mutated")
}

// TestApplyCountOverrideReportsExtrapolationPastProviderEvidence pins the
// disclosure that makes scaling up defensible. Costs stay accurate at any
// quantity because unit prices are linear, but savings only accrue on hours a
// matching instance runs, so an override past the demand the provider measured
// produces a savings figure nobody measured. Silently presenting it would be
// the fabricated figure #1844 set out to remove.
//
// The boundary is the provider's own proposal, not the row's current count: a
// row sized down to 80 by --coverage from a proposal of 100 is still inside
// what the provider's figures cover at 100, so overriding back up to 100 is
// interpolation and must stay quiet.
func TestApplyCountOverrideReportsExtrapolationPastProviderEvidence(t *testing.T) {
	sizedDownFromProposal := common.Recommendation{
		Service:          common.ServiceEC2,
		ResourceType:     "within-proposal",
		Count:            80,
		RecommendedCount: 100,
		EstimatedSavings: 480,
	}

	quiet := captureAppOutput(t, func() {
		got := ApplyCountOverride([]common.Recommendation{sizedDownFromProposal}, 100)
		require.Len(t, got, 1)
		require.Equal(t, 100, got[0].Count)
	})
	assert.NotContains(t, quiet, "exceeds the quantity",
		"overriding back up to the provider's own proposal is interpolation, not extrapolation")

	loud := captureAppOutput(t, func() {
		got := ApplyCountOverride([]common.Recommendation{sizedDownFromProposal}, 101)
		require.Len(t, got, 1)
		require.Equal(t, 101, got[0].Count)
	})
	assert.Contains(t, loud, "exceeds the quantity",
		"an override past the provider's proposal extrapolates the savings and must say so")
}

// TestApplyCountOverrideReportsSkippedRows pins that the two exempt classes are
// named rather than silently passed through. An operator who set
// --override-count and got a differently-sized run than they asked for has to
// be told which rows the flag could not size and why.
func TestApplyCountOverrideReportsSkippedRows(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceSavingsPlansCompute, Count: 1, EstimatedSavings: 500},
		{Service: common.ServiceEC2, ResourceType: "zero-count", Count: 0, EstimatedSavings: 600},
	}

	out := captureAppOutput(t, func() {
		got := ApplyCountOverride(recs, 10)
		require.Len(t, got, 2)
	})

	assert.Contains(t, out, "Savings Plans recommendation(s) unchanged",
		"an SP the override could not size must be named")
	assert.Contains(t, out, "non-positive count unchanged",
		"a row with no quantity to rescale from must be named")
}

// TestApplyCountOverrideThenInstanceLimitScalesOnce pins the ordering both call
// sites establish: the override runs first and the run-wide cap second. The two
// ratios have to compose to a single net ratio against the provider's figures,
// rather than the cap re-deriving its ratio from a base the override already
// corrupted.
//
// The expectation is computed independently of either function, so it cannot
// agree with a wrong implementation.
func TestApplyCountOverrideThenInstanceLimitScalesOnce(t *testing.T) {
	const (
		origCount = 100
		override  = 20
		capTo     = 8
	)

	rec := countDenominatedRec(origCount)

	overridden := ApplyCountOverride([]common.Recommendation{rec}, override)
	require.Len(t, overridden, 1)
	require.Equal(t, override, overridden[0].Count)

	capped := ApplyInstanceLimit(overridden, capTo)
	require.Len(t, capped, 1)
	require.Equal(t, capTo, capped[0].Count, "the cap truncates the overridden count")

	// Net ratio is capTo/origCount: the override's 20/100 composed with the
	// cap's 8/20. Anything else means one stage scaled against a wrong base.
	netRatio := float64(capTo) / float64(origCount)
	assertScaledBy(t, rec, capped[0], netRatio)
}
