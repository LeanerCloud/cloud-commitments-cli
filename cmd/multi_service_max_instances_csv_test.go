package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			"SavingsPercentage must stay zero here; the ordering under test ranks on EstimatedSavings / Count")
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

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,6,10.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.large,postgres,6,100.00,1yr,all-upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: maxInstances})
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
	assert.Equal(t, maxInstances-countPerRow, got[1].Count)
	// This row is truncated, so its savings are the file's figure scaled by
	// the share of the row the budget actually buys (#1830). Asserting the
	// unscaled 100.00 here is what the bug looked like.
	assert.InDelta(t, 100.00*float64(maxInstances-countPerRow)/float64(countPerRow),
		got[1].EstimatedSavings, 0.001)

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

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.large,postgres,6,100.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,all-upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: maxInstances, MinCount: minCount})
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

	recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,2,500.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,100.00,1yr,all-upfront,123456789012
`)

	var (
		got []common.Recommendation
		err error
	)
	out := captureAppOutput(t, func() {
		got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MinCount: minCount})
	})
	require.NoError(t, err)

	require.Len(t, got, 1, "the row at count=2 is below --min-count 5 and must not be purchased")
	assert.Equal(t, "db.t3.medium", got[0].ResourceType)
	assert.Equal(t, 6, got[0].Count)
	assert.Contains(t, out, "--min-count dropped")
	assert.Contains(t, out, "db.t3.small")
}
