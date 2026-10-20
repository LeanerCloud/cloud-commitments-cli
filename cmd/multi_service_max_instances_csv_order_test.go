package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			`rds,us-east-1,db.zzz.large,postgres,6,1yr,all-upfront,123456789012
rds,us-east-1,db.aaa.large,postgres,6,1yr,all-upfront,123456789012
`)

		var err error
		captureAppOutput(t, func() {
			_, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: 6})
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
		recs := csvRecsFrom(t, `rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.small,postgres,6,,1yr,all-upfront,123456789012
`)

		var err error
		captureAppOutput(t, func() {
			_, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: 10})
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "db.t3.small", "the blank row must be named")
		assert.NotContains(t, err.Error(), "db.t3.medium", "a row with a real savings value is rankable and must not be blamed")
	})

	t.Run("no savings column but the cap does not bind", func(t *testing.T) {
		// Nothing has to be chosen between, so nothing has to be ranked. A
		// guard that over-blocks is as wrong as one that never fires.
		recs := csvRecsFromHeader(t, csvNoSavingsHeader,
			`rds,us-east-1,db.zzz.large,postgres,6,1yr,all-upfront,123456789012
rds,us-east-1,db.aaa.large,postgres,6,1yr,all-upfront,123456789012
`)

		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: 12})
		})

		require.NoError(t, err)
		assert.Len(t, got, 2)
		assert.Equal(t, 12, CalculateTotalInstances(got))
	})

	t.Run("populated savings column still ranks and caps", func(t *testing.T) {
		recs := csvRecsFrom(t, `rds,us-east-1,db.t3.small,postgres,6,10.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,postgres,6,500.00,1yr,all-upfront,123456789012
`)

		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: 6})
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
		"rds,us-east-1,db.t3.zulu,postgres,6,100.00,1yr,all-upfront,123456789012\n",
		"rds,us-east-1,db.t3.alpha,postgres,6,100.00,1yr,all-upfront,123456789012\n",
	}

	selection := func(t *testing.T, rows []string) []string {
		t.Helper()
		recs := csvRecsFrom(t, rows[0]+rows[1])
		var (
			got []common.Recommendation
			err error
		)
		captureAppOutput(t, func() {
			got, err = filterAndAdjustRecommendations(context.Background(), recs, 100.0, Config{MaxInstances: 6})
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
