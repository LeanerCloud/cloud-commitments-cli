package main

import (
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typedNilSPDetails returns a ServiceDetails interface holding a TYPED nil
// *SavingsPlanDetails. This is not the same value as a plain nil interface:
// a type assertion to *SavingsPlanDetails succeeds on this one (ok == true)
// and yields a nil pointer, so any unguarded dereference panics. A plain nil
// interface fails the assertion and takes the warning path instead, which is
// why only the typed form reproduces the crash.
func typedNilSPDetails() common.ServiceDetails {
	var d *common.SavingsPlanDetails // nil pointer of concrete type
	return d
}

// TestTypedNilSPDetailsAssertionSucceeds pins the Go semantic the other tests
// in this file depend on. If this ever stops holding, the guards below are
// guarding nothing and the tests would pass vacuously.
func TestTypedNilSPDetailsAssertionSucceeds(t *testing.T) {
	details, ok := typedNilSPDetails().(*common.SavingsPlanDetails)
	require.True(t, ok, "a typed nil must still satisfy the type assertion")
	require.Nil(t, details, "and must yield a nil pointer, which is what makes an unguarded deref panic")

	var plain common.ServiceDetails
	_, plainOK := plain.(*common.SavingsPlanDetails)
	require.False(t, plainOK, "a plain nil interface must NOT satisfy it; the two values differ")
}

// TestScaleRecommendationCostsSurvivesTypedNilDetails covers the helper
// itself. A malformed recommendation must degrade, not crash: the cost fields
// still scale (they do not depend on Details) and Details is left exactly as
// it was rather than being replaced with a fabricated zero-value struct.
func TestScaleRecommendationCostsSurvivesTypedNilDetails(t *testing.T) {
	rec := common.Recommendation{
		Service:          common.ServiceSavingsPlansCompute,
		Count:            4,
		EstimatedSavings: 500,
		CommitmentCost:   200,
		OnDemandCost:     700,
		Details:          typedNilSPDetails(),
	}

	var got common.Recommendation
	require.NotPanics(t, func() {
		got = common.ScaleRecommendationCosts(rec, 0.5)
	}, "a typed nil *SavingsPlanDetails must not panic the sizing helper")

	assert.InDelta(t, 250.0, got.EstimatedSavings, 0.0001, "cost fields do not depend on Details and must still scale")
	assert.InDelta(t, 100.0, got.CommitmentCost, 0.0001)
	assert.InDelta(t, 350.0, got.OnDemandCost, 0.0001)

	details, ok := got.Details.(*common.SavingsPlanDetails)
	require.True(t, ok)
	assert.Nil(t, details,
		"a nil commitment must stay nil rather than becoming a fabricated zero-value struct")
}

// TestApplyInstanceLimitSurvivesTypedNilSPDetails is the #1830 path. A capped
// run must still produce a report when one recommendation is malformed;
// panicking mid-run leaves the operator with no report at all rather than a
// degraded one.
func TestApplyInstanceLimitSurvivesTypedNilSPDetails(t *testing.T) {
	recs := []common.Recommendation{
		{
			Service:          common.ServiceSavingsPlansCompute,
			ResourceType:     "malformed",
			Count:            10,
			EstimatedSavings: 500,
			Details:          typedNilSPDetails(),
		},
		{
			Service:          common.ServiceEC2,
			ResourceType:     "healthy",
			Count:            4,
			EstimatedSavings: 100,
		},
	}

	var got []common.Recommendation
	require.NotPanics(t, func() {
		got = ApplyInstanceLimit(recs, 4)
	}, "a malformed row must not crash the whole capped run")

	require.Len(t, got, 1, "the budget is spent by the first row")
	assert.Equal(t, 4, got[0].Count)
	assert.InDelta(t, 500.0*4.0/10.0, got[0].EstimatedSavings, 0.0001,
		"the truncated row's money still rescales even though its Details are malformed")
}

// TestApplyCoverageSurvivesTypedNilSPDetails covers the applyCoverage SP
// branch, which discards the asserted pointer and so cannot see the nil
// itself. A typed nil must take the same warning-and-pass-through path as a
// wrong Details type, since neither can have its commitment scaled.
func TestApplyCoverageSurvivesTypedNilSPDetails(t *testing.T) {
	rec := common.Recommendation{
		Service:          common.ServiceSavingsPlansCompute,
		Count:            1,
		EstimatedSavings: 500,
		CommitmentCost:   200,
		Details:          typedNilSPDetails(),
	}

	var got []common.Recommendation
	require.NotPanics(t, func() {
		got = ApplyCoverage([]common.Recommendation{rec}, 50)
	}, "a typed nil must not panic the coverage sizing path")

	require.Len(t, got, 1, "a malformed rec is preserved, not dropped")
	assert.InDelta(t, 500.0, got[0].EstimatedSavings, 0.0001,
		"an unscalable commitment must pass through UNSCALED rather than scaling costs it cannot match")
	assert.InDelta(t, 200.0, got[0].CommitmentCost, 0.0001)
}

// TestApplyTargetCoverageSurvivesTypedNilSPDetails covers applyTargetCoverageSP,
// which reads HourlyCommitment off the asserted pointer for its no-signal
// guard and so dereferences before any nil check.
func TestApplyTargetCoverageSurvivesTypedNilSPDetails(t *testing.T) {
	rec := common.Recommendation{
		Service:                common.ServiceSavingsPlansCompute,
		Count:                  1,
		RecommendedUtilization: 90,
		EstimatedSavings:       500,
		CommitmentCost:         200,
		Details:                typedNilSPDetails(),
	}

	var got []common.Recommendation
	require.NotPanics(t, func() {
		got = ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
	}, "a typed nil must not panic the target-coverage sizing path")

	require.Len(t, got, 1, "a malformed rec is preserved, not dropped")
	assert.InDelta(t, 500.0, got[0].EstimatedSavings, 0.0001,
		"an unscalable commitment must pass through UNSCALED")
	assert.InDelta(t, 200.0, got[0].CommitmentCost, 0.0001)
	assert.InDelta(t, 0.0, got[0].ProjectedUtilization, 0.0001,
		"projection fields stay zero on a rec whose commitment could not be scaled")
}
