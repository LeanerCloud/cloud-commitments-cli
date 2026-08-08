package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// maxInstancesFixtureServices and maxInstancesFixtureRegions describe the
// multi-service, multi-region fan-out the run-wide cap has to survive. A
// single-service or single-region fixture stays green with the per-region bug
// present, so both dimensions are needed.
var (
	maxInstancesFixtureServices = []common.ServiceType{common.ServiceRDS, common.ServiceElastiCache}
	maxInstancesFixtureRegions  = []string{"us-east-1", "us-west-2", "eu-west-1"}
)

// countPerFixtureRec is the instance count each (service, region) pair returns.
const countPerFixtureRec = 8

// newMaxInstancesMockClientWith returns a recommendations client that answers
// one recommendation per (service, region) pair. savingsFor maps the fan-out
// position (service index, region index) to a savings percentage, which lets a
// test decide whether the best recommendations are fetched first or last.
func newMaxInstancesMockClientWith(count int, savingsFor func(serviceIdx, regionIdx int) float64) *MockRecommendationsClient {
	mockClient := &MockRecommendationsClient{}
	for i, svc := range maxInstancesFixtureServices {
		for j, region := range maxInstancesFixtureRegions {
			svc, region := svc, region
			savings := savingsFor(i, j)
			rec := common.Recommendation{
				Service:           svc,
				Region:            region,
				ResourceType:      "db.t3.small",
				Count:             count,
				EstimatedSavings:  savings * 10,
				SavingsPercentage: savings,
			}
			mockClient.On("GetRecommendations", mock.Anything,
				mock.MatchedBy(func(p *common.RecommendationParams) bool {
					return p != nil && p.Service == svc && p.Region == region
				}),
			).Return([]common.Recommendation{rec}, nil).Once()
		}
	}
	return mockClient
}

// newMaxInstancesMockClient answers with savings descending in fan-out order
// (50, 45, 40, ...), so the best recommendations are also the first fetched.
func newMaxInstancesMockClient() *MockRecommendationsClient {
	return newMaxInstancesMockClientWith(countPerFixtureRec, func(i, j int) float64 {
		return 50.0 - 5*float64(i*len(maxInstancesFixtureRegions)+j)
	})
}

// TestMaxInstancesCapsWholeRunAcrossServicesAndRegions is the regression test
// for #1608.
//
// --max-instances is documented (cmd/main.go, docs/cli/README.md,
// docs/cli/filtering.md) as a hard cap on the *total* number of instances
// purchased across all recommendations. It used to be applied inside the
// per-(service, region) fetch, so every pair independently kept up to
// MaxInstances and the run bought roughly cap x services x regions.
//
// This is the issue's scenario in miniature: 2 services x 3 regions, each
// answering with 8 instances (48 natural total), capped at 10. Pre-fix the run
// kept all 48 (4.8x the cap); post-fix the sum across every service and region
// is exactly 10.
func TestMaxInstancesCapsWholeRunAcrossServicesAndRegions(t *testing.T) {
	const maxInstances = 10

	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = maxInstancesFixtureRegions
	toolCfg.MaxInstances = maxInstances

	mockClient := newMaxInstancesMockClient()
	t.Cleanup(func() { mockClient.AssertExpectations(t) })

	accountCache := NewAccountAliasCache(awsCfg)
	allRecs, drops := fetchAllRecs(ctx, awsCfg, mockClient, accountCache,
		maxInstancesFixtureServices, engineVersionData{}, toolCfg, nil)

	pairs := len(maxInstancesFixtureServices) * len(maxInstancesFixtureRegions)
	naturalTotal := pairs * countPerFixtureRec

	// The fetch stage must hand the *uncapped* set to the run-wide cap. If this
	// fails, the cap has been pushed back down into the per-region path.
	require.Len(t, allRecs, pairs, "fetch stage must not drop recommendations")
	require.Equal(t, naturalTotal, CalculateTotalInstances(allRecs),
		"fetch stage must not apply the cap per region")

	scored := scoreLimitAndDisplay(allRecs, toolCfg, drops)

	total := CalculateTotalInstances(scored.Passed)
	assert.LessOrEqual(t, total, maxInstances,
		"--max-instances must cap the sum across every service and region, got %d instances from %d service/region pairs",
		total, pairs)
	// The cap is a budget to spend, not just a ceiling: with 48 instances
	// available it should be consumed exactly.
	assert.Equal(t, maxInstances, total)

	// Highest-savings recommendations survive, run-wide: the 50% rec keeps all
	// 8 instances and the 45% rec is reduced to the remaining 2.
	require.Len(t, scored.Passed, 2)
	assert.InDelta(t, 50.0, scored.Passed[0].SavingsPercentage, 0.001)
	assert.Equal(t, countPerFixtureRec, scored.Passed[0].Count)
	assert.InDelta(t, 45.0, scored.Passed[1].SavingsPercentage, 0.001)
	assert.Equal(t, maxInstances-countPerFixtureRec, scored.Passed[1].Count)

	// No silent clamping: the four fully-dropped recommendations are counted
	// into the end-of-run summary.
	assert.Contains(t, drops.FormatOneLine(), common.DropMaxInstances+"=4")
}

// TestMaxInstancesKeepsHighestSavingsNotFirstFetched pins the *selection*
// property of the cap, which TestMaxInstancesCapsWholeRunAcrossServicesAndRegions
// cannot see.
//
// ApplyInstanceLimit consumes its input in slice order and drops the tail, so
// whatever ordering it is handed decides which commitments are bought. Capping
// the merged set before scoring and capping the scorer's savings-sorted output
// both satisfy "total <= cap", so the total-focused test passes either way.
// Only this fixture distinguishes them: the first-fetched service and region
// carry the *worst* recommendations, so a cap that consumes in fetch order
// spends the entire budget on them and leaves the best ones unbought.
//
// RDS is fetched first (index 0 of maxInstancesFixtureServices) with 10-12%
// savings; ElastiCache is fetched second with 40-50%. The cap admits 10 of the
// 36 available instances, and every one of them must come from ElastiCache.
func TestMaxInstancesKeepsHighestSavingsNotFirstFetched(t *testing.T) {
	const (
		maxInstances = 10
		countPerRec  = 6
	)

	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = maxInstancesFixtureRegions
	toolCfg.MaxInstances = maxInstances
	toolCfg.MinSavingsPct = 0 // the low-savings recs must reach the cap, not be scored out

	// Worst-first: the first-fetched service gets 10/11/12%, the second gets
	// 40/45/50%. Best-value selection and fetch-order selection now disagree.
	mockClient := newMaxInstancesMockClientWith(countPerRec, func(i, j int) float64 {
		if i == 0 {
			return 10.0 + float64(j)
		}
		return 40.0 + 5*float64(j)
	})
	t.Cleanup(func() { mockClient.AssertExpectations(t) })

	accountCache := NewAccountAliasCache(awsCfg)
	allRecs, drops := fetchAllRecs(ctx, awsCfg, mockClient, accountCache,
		maxInstancesFixtureServices, engineVersionData{}, toolCfg, nil)
	require.Len(t, allRecs, len(maxInstancesFixtureServices)*len(maxInstancesFixtureRegions))

	scored := scoreLimitAndDisplay(allRecs, toolCfg, drops)

	// The total is respected under either placement, so it proves nothing on
	// its own here. It is asserted only to keep the fixture honest.
	require.Equal(t, maxInstances, CalculateTotalInstances(scored.Passed))

	// The property under test: every purchased instance comes from the
	// highest-savings recommendations run-wide, not from the ones fetched
	// first. A pre-scoring cap spends the whole budget on the 10-11% RDS recs.
	for i := range scored.Passed {
		rec := scored.Passed[i]
		assert.Equal(t, common.ServiceElastiCache, rec.Service,
			"the cap must consume the scorer's savings-sorted order, not fetch order; "+
				"%s %s at %.0f%% savings was bought while better recommendations were dropped",
			rec.Service, rec.Region, rec.SavingsPercentage)
		assert.GreaterOrEqual(t, rec.SavingsPercentage, 40.0)
	}

	// Exact survivors: the 50% rec keeps all 6 instances, the 45% rec is
	// reduced to the remaining 4, and everything at or below 40% is dropped.
	require.Len(t, scored.Passed, 2)
	assert.InDelta(t, 50.0, scored.Passed[0].SavingsPercentage, 0.001)
	assert.Equal(t, countPerRec, scored.Passed[0].Count)
	assert.InDelta(t, 45.0, scored.Passed[1].SavingsPercentage, 0.001)
	assert.Equal(t, maxInstances-countPerRec, scored.Passed[1].Count)
}

// TestMaxInstancesNeverPurchasesBelowMinCount pins that the cap cannot deliver
// a purchase smaller than the operator's --min-count floor.
//
// Moving the cap downstream of the scorer means truncation is no longer
// re-filtered by the scorer's --min-count gate, so a survivor the cap trims to
// fit the budget could land under a floor the operator explicitly set. Asking
// for "at least 5" and being sold 2 is wrong regardless of which direction it
// errs in, and a commitment below the minimum can be worse than none at all.
//
// Fixture: 6 recommendations of 6 instances each, cap 10, --min-count 5. The
// budget leaves room for 6 + 4, and that 4 is below the floor, so the second
// recommendation must be dropped rather than purchased short. The run buys 6.
func TestMaxInstancesNeverPurchasesBelowMinCount(t *testing.T) {
	const (
		maxInstances = 10
		minCount     = 5
		countPerRec  = 6
	)

	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = maxInstancesFixtureRegions
	toolCfg.MaxInstances = maxInstances
	toolCfg.MinCount = minCount

	mockClient := newMaxInstancesMockClientWith(countPerRec, func(i, j int) float64 {
		return 50.0 - 5*float64(i*len(maxInstancesFixtureRegions)+j)
	})
	t.Cleanup(func() { mockClient.AssertExpectations(t) })

	accountCache := NewAccountAliasCache(awsCfg)
	allRecs, drops := fetchAllRecs(ctx, awsCfg, mockClient, accountCache,
		maxInstancesFixtureServices, engineVersionData{}, toolCfg, nil)
	require.Len(t, allRecs, len(maxInstancesFixtureServices)*len(maxInstancesFixtureRegions))

	scored := scoreLimitAndDisplay(allRecs, toolCfg, drops)

	// The property under test. Every purchased recommendation honors the
	// floor; none is bought at a truncated count below it.
	for i := range scored.Passed {
		assert.GreaterOrEqual(t, scored.Passed[i].Count, minCount,
			"--min-count %d was set, but %s %s would be purchased at count=%d",
			minCount, scored.Passed[i].Service, scored.Passed[i].Region, scored.Passed[i].Count)
	}

	// The truncated second recommendation is dropped, not reduced to 4.
	require.Len(t, scored.Passed, 1)
	assert.Equal(t, countPerRec, scored.Passed[0].Count)
	assert.Equal(t, countPerRec, CalculateTotalInstances(scored.Passed))

	// Dropped for the min-count reason, not silently, and counted exactly
	// once: 5 of the 6 scored recommendations are gone, 1 of them to the
	// floor and 4 to the budget. Double-counting the floor drop under both
	// reasons would report 6 drops for 5 recommendations.
	summary := drops.FormatOneLine()
	assert.Contains(t, summary, common.DropMinCountAfterCap+"=1")
	assert.Contains(t, summary, common.DropMaxInstances+"=4")
	assert.Equal(t, 5, drops.Total())
}

// TestDropTruncatedBelowMinCount covers the floor helper directly, including
// the boundary (a truncated count exactly equal to the floor is kept) and the
// disabled case.
func TestDropTruncatedBelowMinCount(t *testing.T) {
	tests := []struct {
		name        string
		counts      []int
		minCount    int
		wantKept    []int
		wantRemoved int
	}{
		{name: "floor disabled keeps everything", counts: []int{5, 2}, minCount: 0, wantKept: []int{5, 2}},
		{name: "truncated tail below floor is dropped", counts: []int{6, 4}, minCount: 5, wantKept: []int{6}, wantRemoved: 1},
		{name: "truncated tail exactly at floor is kept", counts: []int{6, 5}, minCount: 5, wantKept: []int{6, 5}},
		{name: "tail above floor is kept", counts: []int{6, 6}, minCount: 5, wantKept: []int{6, 6}},
		{name: "every rec below floor leaves nothing", counts: []int{2}, minCount: 5, wantKept: []int{}, wantRemoved: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := make([]common.Recommendation, len(tt.counts))
			for i, c := range tt.counts {
				recs[i] = common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", Count: c}
			}

			kept, removed := dropTruncatedBelowMinCount(recs, tt.minCount)

			gotCounts := make([]int, len(kept))
			for i := range kept {
				gotCounts[i] = kept[i].Count
			}
			assert.Equal(t, tt.wantKept, gotCounts)
			assert.Len(t, removed, tt.wantRemoved)
		})
	}
}

// TestMaxInstancesNotAppliedWhenUnset guards the other direction: with the flag
// unset the fan-out is purchased in full, so the cap cannot silently shrink a
// run that never asked for one.
func TestMaxInstancesNotAppliedWhenUnset(t *testing.T) {
	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = maxInstancesFixtureRegions
	toolCfg.MaxInstances = 0

	mockClient := newMaxInstancesMockClient()
	t.Cleanup(func() { mockClient.AssertExpectations(t) })

	accountCache := NewAccountAliasCache(awsCfg)
	allRecs, drops := fetchAllRecs(ctx, awsCfg, mockClient, accountCache,
		maxInstancesFixtureServices, engineVersionData{}, toolCfg, nil)

	scored := scoreLimitAndDisplay(allRecs, toolCfg, drops)

	pairs := len(maxInstancesFixtureServices) * len(maxInstancesFixtureRegions)
	assert.Len(t, scored.Passed, pairs)
	assert.Equal(t, pairs*countPerFixtureRec, CalculateTotalInstances(scored.Passed))
	assert.NotContains(t, drops.FormatOneLine(), common.DropMaxInstances)
}

func TestApplyGlobalInstanceLimit(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5},
		{Service: common.ServiceEC2, Region: "us-west-2", ResourceType: "m5.large", Count: 4},
		{Service: common.ServiceEC2, Region: "eu-west-1", ResourceType: "m5.xlarge", Count: 3},
	}

	tests := []struct {
		name          string
		maxInstances  int32
		expectedTotal int
		expectedLen   int
		expectedDrops int
	}{
		{name: "unset leaves the run untouched", maxInstances: 0, expectedTotal: 12, expectedLen: 3},
		{name: "cap above the total leaves the run untouched", maxInstances: 99, expectedTotal: 12, expectedLen: 3},
		{name: "cap truncates the tail", maxInstances: 7, expectedTotal: 7, expectedLen: 2, expectedDrops: 1},
		{name: "cap below the first rec keeps one reduced rec", maxInstances: 2, expectedTotal: 2, expectedLen: 1, expectedDrops: 2},
		{name: "cap equal to the total leaves the run untouched", maxInstances: 12, expectedTotal: 12, expectedLen: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drops := common.NewDropSummary()
			got := applyGlobalInstanceLimit(recs, Config{MaxInstances: tt.maxInstances}, drops)

			assert.Len(t, got, tt.expectedLen)
			assert.Equal(t, tt.expectedTotal, CalculateTotalInstances(got))

			if tt.expectedDrops == 0 {
				assert.Empty(t, drops.FormatOneLine())
				return
			}
			assert.Contains(t, drops.FormatOneLine(), common.DropMaxInstances)
			assert.Equal(t, tt.expectedDrops, drops.Total())
		})
	}

	// The input slice must not be mutated: the caller still renders it.
	assert.Equal(t, 5, recs[0].Count)
	assert.Equal(t, 4, recs[1].Count)
	assert.Equal(t, 3, recs[2].Count)
}

// TestApplyInstanceLimitNonPositiveCountDoesNotCreditBudget pins that a
// non-positive Count cannot raise the remaining budget and let later
// recommendations push the run past the cap.
func TestApplyInstanceLimitNonPositiveCountDoesNotCreditBudget(t *testing.T) {
	recs := []common.Recommendation{
		{ResourceType: "a", Count: 4},
		{ResourceType: "b", Count: -10},
		{ResourceType: "c", Count: 100},
	}

	got := ApplyInstanceLimit(recs, 5)

	total := 0
	for i := range got {
		if got[i].Count > 0 {
			total += got[i].Count
		}
	}
	assert.LessOrEqual(t, total, 5, "a negative Count must not raise the remaining budget")
}

// TestReportInstanceLimitNamesEveryChange checks the no-silent-clamping
// contract: each reduced and each dropped recommendation is named on stdout.
func TestReportInstanceLimitNamesEveryChange(t *testing.T) {
	before := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5},
		{Service: common.ServiceEC2, Region: "us-west-2", ResourceType: "m5.large", Count: 4},
		{Service: common.ServiceEC2, Region: "eu-west-1", ResourceType: "m5.xlarge", Count: 3},
	}
	after := ApplyInstanceLimit(before, 7)

	drops := common.NewDropSummary()
	out := captureAppOutput(t, func() {
		reportInstanceLimit(before, after, 0, CalculateTotalInstances(before), 7, drops)
	})

	assert.Contains(t, out, "--max-instances=7")
	assert.Contains(t, out, "reduced")
	assert.Contains(t, out, "m5.large")
	assert.Contains(t, out, "dropped")
	assert.Contains(t, out, "m5.xlarge")
}
