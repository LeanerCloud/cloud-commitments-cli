//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/analytics"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsNestedRollup_COR02 replicates the COR-02 failing scenario:
// snapshot rows are run-rates written at (account, provider, service, region,
// commitment_type, timestamp) grain, so a breakdown bucket contains several
// rows sharing the SAME timestamp. The pre-fix flat AVG reported the mean
// per-row run-rate instead of the bucket's per-timestamp total, understating
// savings for any multi-region / multi-commitment-type estate.
//
// Fixture: provider aws, service rds, two collection timestamps T1 and T2 in
// the current month.
//
//	T1: us-east-1 RI $100 + us-east-1 SavingsPlan $50 + us-west-2 RI $100 = $250
//	T2: us-east-1 RI $200 + us-east-1 SavingsPlan $100 + us-west-2 RI $100 = $400
//
// Correct rollup (inner SUM per timestamp, outer AVG over timestamps, the H5
// shape): provider/service total = AVG(250, 400) = 325. The pre-fix flat AVG
// returned 650/6 = 108.33.
func TestAnalyticsNestedRollup_COR02(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	require.NoError(t, migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", ""))

	store := analytics.NewPostgresAnalyticsStore(container.DB)

	// Two timestamps inside the current month so the current-month partition
	// (created by the migrations) holds the rows and QueryMonthlyTotals'
	// current-month window matches them.
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	t1 := monthStart.Add(12 * time.Hour)
	t2 := monthStart.Add(13 * time.Hour)

	const account = "123456789012"
	mkSnapshot := func(ts time.Time, region, commitmentType string, savings float64) *analytics.SavingsSnapshot {
		return &analytics.SavingsSnapshot{
			AccountID:       account,
			Timestamp:       ts,
			Provider:        "aws",
			Service:         "rds",
			Region:          region,
			CommitmentType:  commitmentType,
			TotalCommitment: savings * 10,
			TotalSavings:    savings,
		}
	}
	fixture := []*analytics.SavingsSnapshot{
		mkSnapshot(t1, "us-east-1", "RI", 100),
		mkSnapshot(t1, "us-east-1", "SavingsPlan", 50),
		mkSnapshot(t1, "us-west-2", "RI", 100),
		mkSnapshot(t2, "us-east-1", "RI", 200),
		mkSnapshot(t2, "us-east-1", "SavingsPlan", 100),
		mkSnapshot(t2, "us-west-2", "RI", 100),
	}
	for _, snapshot := range fixture {
		require.NoError(t, store.SaveSnapshot(ctx, snapshot))
	}

	accountFilter := map[string][]string{"aws": {account}}
	start := monthStart
	end := monthStart.Add(24 * time.Hour)

	t.Run("QueryByProvider sums rows per timestamp before averaging", func(t *testing.T) {
		breakdowns, err := store.QueryByProvider(ctx, nil, accountFilter, start, end)
		require.NoError(t, err)
		require.Len(t, breakdowns, 1)
		assert.Equal(t, "aws", breakdowns[0].Provider)
		assert.Equal(t, "rds", breakdowns[0].Service)
		// AVG(T1 total $250, T2 total $400) = $325; the pre-fix flat AVG
		// across the 6 rows returned $108.33.
		assert.InDelta(t, 325.0, breakdowns[0].TotalSavings, 0.01)
	})

	t.Run("QueryByService sums commitment types per timestamp before averaging", func(t *testing.T) {
		breakdowns, err := store.QueryByService(ctx, nil, accountFilter, "aws", start, end)
		require.NoError(t, err)
		require.Len(t, breakdowns, 2)

		byRegion := map[string]float64{}
		for _, b := range breakdowns {
			assert.Equal(t, "rds", b.Service)
			byRegion[b.Region] = b.TotalSavings
		}
		// us-east-1: AVG(T1 100+50, T2 200+100) = $225; pre-fix flat AVG
		// across the 4 rows returned $112.50.
		assert.InDelta(t, 225.0, byRegion["us-east-1"], 0.01)
		// us-west-2 has a single row per timestamp, so both shapes agree.
		assert.InDelta(t, 100.0, byRegion["us-west-2"], 0.01)
	})

	t.Run("monthly_savings_summary sums rows per timestamp before averaging", func(t *testing.T) {
		_, err := container.DB.Exec(ctx, "REFRESH MATERIALIZED VIEW monthly_savings_summary")
		require.NoError(t, err)

		summaries, err := store.QueryMonthlyTotals(ctx, nil, accountFilter, 1)
		require.NoError(t, err)
		require.Len(t, summaries, 1)
		assert.Equal(t, "aws", summaries[0].Provider)
		assert.Equal(t, "rds", summaries[0].Service)
		// AVG(T1 total $250, T2 total $400) = $325; the pre-fix flat-AVG view
		// definition reported $108.33.
		assert.InDelta(t, 325.0, summaries[0].TotalSavings, 0.01)
		// snapshot_count keeps its raw-row semantics across the rewrite.
		assert.Equal(t, 6, summaries[0].SnapshotCount)
	})

	t.Run("down restores the flat-AVG view and up reapplies cleanly", func(t *testing.T) {
		// Migrate down to version 77 (just below this migration) so 000078's
		// down runs regardless of how many later migrations sit above it on
		// main; a fixed-step RollbackMigrations would only undo the topmost
		// migration and leave the nested-rollup view in place.
		require.NoError(t, migrations.MigrateToVersion(ctx, container.DB.Pool(), getMigrationsPath(), 77))
		_, err := container.DB.Exec(ctx, "REFRESH MATERIALIZED VIEW monthly_savings_summary")
		require.NoError(t, err)

		summaries, err := store.QueryMonthlyTotals(ctx, nil, accountFilter, 1)
		require.NoError(t, err)
		require.Len(t, summaries, 1)
		// Rolled back to the 000067/000074 flat AVG: 650/6 = 108.33.
		assert.InDelta(t, 650.0/6.0, summaries[0].TotalSavings, 0.01)

		require.NoError(t, migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", ""))
		_, err = container.DB.Exec(ctx, "REFRESH MATERIALIZED VIEW monthly_savings_summary")
		require.NoError(t, err)

		summaries, err = store.QueryMonthlyTotals(ctx, nil, accountFilter, 1)
		require.NoError(t, err)
		require.Len(t, summaries, 1)
		assert.InDelta(t, 325.0, summaries[0].TotalSavings, 0.01)
	})
}
