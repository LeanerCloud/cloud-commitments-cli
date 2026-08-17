package main

import (
	"context"
	"path/filepath"
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
			got := applyGlobalInstanceLimit(recs, Config{MaxInstances: tt.maxInstances}, rankBySavingsPercentage, drops)

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
		reportInstanceLimit(before, after, 0, CalculateTotalInstances(before), 7, rankBySavingsPercentage, drops)
	})

	assert.Contains(t, out, "--max-instances=7")
	assert.Contains(t, out, "reduced")
	assert.Contains(t, out, "m5.large")
	assert.Contains(t, out, "dropped")
	assert.Contains(t, out, "m5.xlarge")
}

// ==================== --input-csv path (#1741) ====================

// csvFixtureHeader matches writeMultiServiceCSVReport's column names for the
// fields these tests exercise, so the fixtures parse the way a real
// round-tripped report does.
const csvFixtureHeader = "Service,Region,ResourceType,Engine,Count,EstimatedSavings,Term,PaymentOption,Account\n"

// csvNoSavingsHeader is csvFixtureHeader without the EstimatedSavings column,
// the shape of a hand-written minimal CSV. getCSVField returns "" for a column
// that is not in the header and parseCSVFloat leaves the field at zero, so
// every row of such a file loads with EstimatedSavings == 0.
const csvNoSavingsHeader = "Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account\n"

// csvRecsFrom writes rows to a temporary recommendations CSV, loads them back
// through the production parser, and returns the recommendations exactly as a
// --input-csv run sees them.
//
// Building the fixture through loadRecommendationsFromCSV rather than by hand
// keeps these tests honest about what a CSV row actually carries: neither
// writeMultiServiceCSVReport nor parseCSVRecord knows a savings-percentage
// column, so every loaded row has SavingsPercentage == 0 and any
// savings-ordered selection has to resolve on EstimatedSavings. A hand-built
// fixture could quietly set SavingsPercentage and prove nothing about the real
// input.
func csvRecsFrom(t *testing.T, rows string) []common.Recommendation {
	t.Helper()
	return csvRecsFromHeader(t, csvFixtureHeader, rows)
}

// csvRecsFromHeader is csvRecsFrom over an arbitrary header, for fixtures that
// exercise a CSV missing a column the selection depends on.
func csvRecsFromHeader(t *testing.T, header, rows string) []common.Recommendation {
	t.Helper()
	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, header+rows))
	require.NoError(t, err)
	require.NotEmpty(t, recs)
	for i := range recs {
		require.Zero(t, recs[i].SavingsPercentage,
			"a CSV row carries no savings percentage; the ordering under test must come from EstimatedSavings")
	}
	return recs
}

// TestCSVCapKeepsHighestSavingsNotFileOrder is the selection-order regression
// for #1741.
//
// filterAndAdjustRecommendations used to hand the load-ordered slice straight
// to ApplyInstanceLimit, which consumes its input in slice order and drops the
// tail, so whichever rows appeared first in the file spent the whole budget.
// Both the old and the new behavior respect the cap total, so only a fixture
// whose best-value rows are *not* first can tell them apart: the $10 row leads
// the file and the $500 row sits in the middle.
func TestCSVCapKeepsHighestSavingsNotFileOrder(t *testing.T) {
	isolateAWSEnv(t)
	const (
		maxInstances = 10
		countPerRow  = 6
	)

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,6,10.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.large,postgres,6,100.00,1yr,All Upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: maxInstances})
	})
	require.NoError(t, err)

	// Asserted only to keep the fixture honest: a file-ordered cap satisfies
	// the total too, so the total on its own proves nothing here.
	require.Equal(t, maxInstances, CalculateTotalInstances(got))

	// The property under test is the selection ORDER. The budget must go to
	// the highest-savings rows, best first, not to the rows the file happened
	// to list first.
	require.Len(t, got, 2)
	assert.Equal(t, "db.t3.medium", got[0].ResourceType,
		"the cap must consume savings order, not file order; got %s first", got[0].ResourceType)
	assert.InDelta(t, 500.00, got[0].EstimatedSavings, 0.001)
	assert.Equal(t, countPerRow, got[0].Count)
	assert.Equal(t, "db.t3.large", got[1].ResourceType)
	assert.InDelta(t, 100.00, got[1].EstimatedSavings, 0.001)
	assert.Equal(t, maxInstances-countPerRow, got[1].Count)

	// Nothing shrinks silently: the row the cap dropped is named.
	assert.Contains(t, out, "db.t3.small")
}

// TestCSVCapDropsTruncationBelowMinCount pins that the CSV path cannot deliver
// a purchase smaller than the operator's --min-count floor, the property #1608
// and #1725 established on the recommendation-driven path.
//
// Fixture: two rows of 6 instances, cap 10, floor 5. The budget leaves room
// for 6 + 4, and 4 is under the floor, so the second row is dropped rather
// than purchased short. The run buys 6.
func TestCSVCapDropsTruncationBelowMinCount(t *testing.T) {
	isolateAWSEnv(t)
	const (
		maxInstances = 10
		minCount     = 5
		countPerRow  = 6
	)

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.large,postgres,6,100.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,All Upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: maxInstances, MinCount: minCount})
	})
	require.NoError(t, err)

	for i := range got {
		assert.GreaterOrEqual(t, got[i].Count, minCount,
			"--min-count %d was set, but %s would be purchased at count=%d",
			minCount, got[i].ResourceType, got[i].Count)
	}

	require.Len(t, got, 1)
	assert.Equal(t, "db.t3.medium", got[0].ResourceType)
	assert.Equal(t, countPerRow, got[0].Count)
	assert.Equal(t, countPerRow, CalculateTotalInstances(got))
	assert.Contains(t, out, "below --min-count 5")
}

// TestCSVMinCountDropsRowsUnderTheFloor pins the floor with no cap in play: a
// CSV row that is already under --min-count is dropped, not purchased. The
// higher-value row is the one under the floor, so a selection that sorts by
// savings without gating on the floor still buys it.
func TestCSVMinCountDropsRowsUnderTheFloor(t *testing.T) {
	isolateAWSEnv(t)
	const minCount = 5

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,2,500.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,100.00,1yr,All Upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MinCount: minCount})
	})
	require.NoError(t, err)

	require.Len(t, got, 1, "the row at count=2 is below --min-count 5 and must not be purchased")
	assert.Equal(t, "db.t3.medium", got[0].ResourceType)
	assert.Equal(t, 6, got[0].Count)
	assert.Contains(t, out, "--min-count dropped")
	assert.Contains(t, out, "db.t3.small")
}

// TestRunToolFromCSVEnforcesMinCountAndCap drives the guards end to end
// through the real --input-csv entry point and asserts on the purchase report,
// in both directions: the guards bind when they should, and a legitimate run
// under the same flags still purchases every row at its full count.
func TestRunToolFromCSVEnforcesMinCountAndCap(t *testing.T) {
	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })
	isolateAWSEnv(t)

	// Worst-value row first, so file order and savings order disagree.
	csvPath := writeTestRecommendationsCSV(t, csvFixtureHeader+
		`rds,us-east-1,db.t3.small,postgres,6,10.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.large,postgres,6,100.00,1yr,All Upfront,123456789012
`)

	tests := []struct {
		name         string
		wantRows     []string
		maxInstances int32
		minCount     int
	}{
		{
			// Budget 10 across rows of 6: the best row takes 6, the next is
			// truncated to 4, and 4 is under the floor, so it is dropped.
			// A file-ordered cap would instead buy db.t3.small at 6.
			name:         "cap and floor bind",
			maxInstances: 10,
			minCount:     5,
			wantRows:     []string{"db.t3.medium@6"},
		},
		{
			// Same floor, cap above the natural total: nothing is dropped and
			// every row is purchased at its CSV count. A guard that
			// over-blocks is as wrong as one that never fires.
			name:         "legitimate run purchases every row",
			maxInstances: 100,
			minCount:     5,
			wantRows:     []string{"db.t3.small@6", "db.t3.medium@6", "db.t3.large@6"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "report.csv")
			toolCfg.CSVInput = csvPath
			toolCfg.CSVOutput = reportPath
			toolCfg.ActualPurchase = false
			toolCfg.Coverage = 100.0
			toolCfg.TargetCoverage = 0
			toolCfg.OverrideCount = 0
			toolCfg.MaxInstances = tt.maxInstances
			toolCfg.MinCount = tt.minCount

			require.NoError(t, runToolFromCSV(context.Background(), toolCfg))

			header, rows := readPurchaseReport(t, reportPath)
			assert.ElementsMatch(t, tt.wantRows, reportRowPairs(t, header, rows))
		})
	}
}

// reportRowPairs pairs each purchase-report row's ResourceType with its Count.
// Asserting the two columns independently would pass a result that put the
// right counts against the wrong rows, which is precisely what a wrong
// selection order produces.
func reportRowPairs(t *testing.T, header []string, rows [][]string) []string {
	t.Helper()
	types := reportColumn(t, header, rows, "ResourceType")
	counts := reportColumn(t, header, rows, "Count")
	require.Len(t, counts, len(types))

	pairs := make([]string, 0, len(types))
	for i := range types {
		pairs = append(pairs, types[i]+"@"+counts[i])
	}
	return pairs
}

// TestCSVCapRanksBySavingsPerInstance pins the ranking key the cap consumes.
//
// --max-instances is a budget in instances, so the rows worth keeping are the
// ones returning the most savings per instance bought. Ranking on the
// EstimatedSavings column directly ranks a whole-row dollar total against a
// per-instance budget: the fixture's bulk row is larger in total ($600) and
// worse per instance ($6) than the rich row ($500 total, $83 each), so a
// total-ranked cap spends the entire 10-instance budget on bulk for about $60
// of real value and drops rich outright.
//
// Every earlier fixture in this file uses equal counts, where ranking by total
// and ranking by rate are indistinguishable. The counts here are deliberately
// unequal, which is the only way to tell the two rules apart.
func TestCSVCapRanksBySavingsPerInstance(t *testing.T) {
	isolateAWSEnv(t)
	const maxInstances = 10

	recs := csvRecsFrom(t, `rds,us-east-1,db.bulk.large,postgres,100,600.00,1yr,All Upfront,123456789012
rds,us-east-1,db.rich.large,postgres,6,500.00,1yr,All Upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: maxInstances})
	})
	require.NoError(t, err)

	require.Equal(t, maxInstances, CalculateTotalInstances(got),
		"the cap total is respected by both the right and the wrong ranking, and is asserted only to keep the fixture honest")

	// The substance: ORDER, and the count each row is bought at. The
	// best-value row must be taken whole before the bulk row gets the
	// remainder.
	require.Len(t, got, 2)
	assert.Equal(t, "db.rich.large", got[0].ResourceType,
		"the budget must go to the highest savings-per-instance row first; got %s", got[0].ResourceType)
	assert.Equal(t, 6, got[0].Count)
	assert.Equal(t, "db.bulk.large", got[1].ResourceType)
	assert.Equal(t, maxInstances-6, got[1].Count, "the bulk row takes only the remaining budget")

	assert.Contains(t, out, "savings-per-instance",
		"the cap must name the ranking rule it applied, not claim a savings percentage a CSV row does not carry")
}

// TestCSVCapRefusesRowsWithoutARankingSignal covers the absent-vs-zero trap.
//
// A CSV with no EstimatedSavings column, or with a blank cell in it, loads
// every affected row at zero savings. Nothing downstream can tell that apart
// from a row genuinely worth $0, so a binding cap would rank on a value that
// is not there and silently fall through to the Service|Region|ResourceType
// tie-break, buying by instance-type name while stdout promises it is buying
// by savings. The run is refused instead.
//
// Both directions are covered: the two refusal cases, a case proving the
// refusal is scoped to a cap that actually binds, and a legitimate file that
// still ranks and caps normally.
func TestCSVCapRefusesRowsWithoutARankingSignal(t *testing.T) {
	isolateAWSEnv(t)

	t.Run("no EstimatedSavings column at all", func(t *testing.T) {
		// Worst alphabetically first, so a name-ordered cap is visible: it
		// keeps db.aaa.large and drops db.zzz.large regardless of file order.
		recs := csvRecsFromHeader(t, csvNoSavingsHeader,
			`rds,us-east-1,db.zzz.large,postgres,6,1yr,All Upfront,123456789012
rds,us-east-1,db.aaa.large,postgres,6,1yr,All Upfront,123456789012
`)

		var err error
		captureAppOutput(t, func() {
			_, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: 6})
		})

		require.Error(t, err, "a binding cap with nothing to rank on must refuse, not pick by name")
		assert.Contains(t, err.Error(), "EstimatedSavings")
		assert.Contains(t, err.Error(), "db.zzz.large", "the error must name the rows it cannot rank")
		assert.Contains(t, err.Error(), "db.aaa.large")
	})

	t.Run("one blank EstimatedSavings cell among populated rows", func(t *testing.T) {
		// The partial case is the worse one: the blank row loads as $0, ranks
		// last, and is dropped first, with nothing saying the value was absent
		// rather than genuinely zero.
		recs := csvRecsFrom(t, `rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.small,postgres,6,,1yr,All Upfront,123456789012
`)

		var err error
		captureAppOutput(t, func() {
			_, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: 10})
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "db.t3.small", "the blank row must be named")
		assert.NotContains(t, err.Error(), "db.t3.medium", "a row with a real savings value is rankable and must not be blamed")
	})

	t.Run("no savings column but the cap does not bind", func(t *testing.T) {
		// Nothing has to be chosen between, so nothing has to be ranked. A
		// guard that over-blocks is as wrong as one that never fires.
		recs := csvRecsFromHeader(t, csvNoSavingsHeader,
			`rds,us-east-1,db.zzz.large,postgres,6,1yr,All Upfront,123456789012
rds,us-east-1,db.aaa.large,postgres,6,1yr,All Upfront,123456789012
`)

		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: 12})
		})

		require.NoError(t, err)
		assert.Len(t, got, 2)
		assert.Equal(t, 12, CalculateTotalInstances(got))
	})

	t.Run("populated savings column still ranks and caps", func(t *testing.T) {
		recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,6,10.00,1yr,All Upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,All Upfront,123456789012
`)

		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: 6})
		})

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "db.t3.medium", got[0].ResourceType)
		assert.Equal(t, 6, got[0].Count)
	})
}

// TestApplyMinCountFloorAfterDuplicateAdjustment covers the floor's second
// call site, the one inside runToolFromCSV's purchase loop.
//
// adjustRecsForDuplicates subtracts recent matching commitments from Count
// after filterAndAdjustRecommendations has already applied the floor, so a row
// that cleared --min-count 5 at Count 6 with 5 existing commitments arrives at
// the purchase loop at Count 1. Without a second application the operator's
// floor is defeated on the very path #1741 is about.
//
// This exercises applyMinCountFloor on the post-deduction counts rather than
// through runToolFromCSV, because createServiceClient is not injectable: there
// is no seam to drive a fake GetExistingCommitments through the CSV entry
// point, and with invalid credentials adjustRecsForDuplicates errors and falls
// back to the unadjusted slice.
func TestApplyMinCountFloorAfterDuplicateAdjustment(t *testing.T) {
	const minCount = 5

	// Counts as adjustRecsForDuplicates leaves them: the first row had 6 and
	// 5 existing commitments deducted, the second was untouched.
	adjusted := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.large", Count: 1, EstimatedSavings: 500},
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.medium", Count: 6, EstimatedSavings: 100},
	}

	// Snapshot before the first call. applyMinCountFloor returns its input slice
	// untouched at a floor of 0, and scorer.Score may reorder adjusted below, so
	// asserting against adjusted itself would compare the result to itself.
	original := append([]common.Recommendation(nil), adjusted...)

	var got []common.Recommendation
	out := captureAppOutput(t, func() {
		got = applyMinCountFloor(adjusted, minCount)
	})

	require.Len(t, got, 1, "the row the deduction left at 1 is below --min-count 5 and must not be purchased")
	assert.Equal(t, "db.t3.medium", got[0].ResourceType)
	assert.Equal(t, 6, got[0].Count)
	assert.Contains(t, out, "db.t3.large", "the drop must be named")
	assert.Contains(t, out, "--min-count")

	// A floor of 0 disables the flag: every row survives, in the order given.
	var unfiltered []common.Recommendation
	quiet := captureAppOutput(t, func() {
		unfiltered = applyMinCountFloor(adjusted, 0)
	})
	assert.Equal(t, original, unfiltered, "--min-count 0 must not drop or reorder anything")
	assert.Empty(t, quiet)
}

// TestCSVCapOrderIsIndependentOfFileOrder pins that equal-rate rows are not
// separated by where they sit in the file.
//
// --min-count 0 skips the scorer, so the per-instance sort is the only
// ordering the cap sees. If it left equal rates in input order, file order
// would still decide which of two equally-valuable rows the budget buys, which
// is the defect #1741 reported, surviving one layer down.
func TestCSVCapOrderIsIndependentOfFileOrder(t *testing.T) {
	isolateAWSEnv(t)

	// Two rows at the identical rate ($100 for 6 instances), listed
	// worst-alphabetically-first.
	rows := []string{
		"rds,us-east-1,db.t3.zulu,postgres,6,100.00,1yr,All Upfront,123456789012\n",
		"rds,us-east-1,db.t3.alpha,postgres,6,100.00,1yr,All Upfront,123456789012\n",
	}

	selection := func(t *testing.T, rows []string) []string {
		t.Helper()
		recs := csvRecsFrom(t, rows[0]+rows[1])
		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(recs, 100.0, Config{MaxInstances: 6})
		})
		require.NoError(t, err)

		names := make([]string, 0, len(got))
		for i := range got {
			names = append(names, got[i].ResourceType)
		}
		return names
	}

	forward := selection(t, rows)
	reversed := selection(t, []string{rows[1], rows[0]})

	assert.Equal(t, []string{"db.t3.alpha"}, forward,
		"equal rates must tie-break on Service|Region|ResourceType, not on file position")
	assert.Equal(t, forward, reversed, "reversing the file must not change what the cap buys")
}
