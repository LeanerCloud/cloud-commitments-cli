package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dropRec(rt string, count int) common.Recommendation {
	return common.Recommendation{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: rt, Count: count}
}

// A zero-Count entry swept out by the --min-count floor (behind a truncated
// tail) used to be uncounted as a budget drop (kept == rec.Count == 0) yet
// subtracted as a floor drop, driving the --max-instances tally to -1 and
// panicking in DropSummary.Add (#1742).
func TestApplyGlobalInstanceLimitZeroCountTailDoesNotPanic(t *testing.T) {
	drops := common.NewDropSummary()
	recs := []common.Recommendation{dropRec("m5.large", 4), dropRec("c5.large", 0), dropRec("m5.xlarge", 6)}

	var got []common.Recommendation
	require.NotPanics(t, func() {
		got = applyGlobalInstanceLimit(recs, Config{MaxInstances: 7, MinCount: 4}, rankBySavingsPercentage, drops)
	})
	require.Len(t, got, 1)
	assert.Equal(t, "m5.large", got[0].ResourceType)
	// The truncated m5.xlarge and the zero-Count c5.large are both floor
	// drops, counted once each and none attributed to the budget.
	assert.Equal(t, 2, drops.Total())
	assert.Contains(t, drops.FormatOneLine(), "min-count-after-cap=2")
	assert.NotContains(t, drops.FormatOneLine(), "--max-instances")
}

// A zero-Count entry the budget left out is a removed recommendation and is
// attributed to --max-instances, not skipped by the kept == rec.Count check.
func TestReportInstanceLimitCountsZeroCountBudgetDrop(t *testing.T) {
	before := []common.Recommendation{dropRec("m5.large", 6), dropRec("m5.xlarge", 6), dropRec("c5.large", 0)}
	after := ApplyInstanceLimit(before, 6)
	require.Len(t, after, 1)

	drops := common.NewDropSummary()
	out := captureAppOutput(t, func() {
		reportInstanceLimit(before, after, 0, CalculateTotalInstances(before), 6, rankBySavingsPercentage, drops)
	})
	assert.Equal(t, 2, drops.Total())
	assert.Contains(t, out, "c5.large")
	assert.Contains(t, out, "2 dropped")
}

// More floor drops than removed entries is an impossible state and must fail
// loudly with context rather than clamp or hit DropSummary.Add's bare panic.
func TestReportInstanceLimitImpossibleAttributionPanicsWithContext(t *testing.T) {
	before := []common.Recommendation{dropRec("m5.large", 6)}
	assert.PanicsWithValue(t,
		"reportInstanceLimit: 2 recommendations attributed to --min-count-after-cap but only 0 were removed (before=1, after=1)",
		func() {
			reportInstanceLimit(before, before, 2, 6, 6, rankBySavingsPercentage, common.NewDropSummary())
		})
}
