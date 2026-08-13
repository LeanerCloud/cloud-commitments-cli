//go:build integration
// +build integration

package analytics_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/analytics"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run against a real PostgreSQL database using testcontainers.
// They will be skipped if Docker is not available or if SKIP_DB_TESTS is set.
// To run these tests: go test ./internal/analytics/ -v
// To skip these tests: SKIP_DB_TESTS=1 go test ./internal/analytics/

func skipIfNoDocker(t *testing.T) {
	t.Helper()

	// Skip if SKIP_DB_TESTS is set (CI without a live DB, or local opt-out).
	if os.Getenv("SKIP_DB_TESTS") != "" {
		t.Skip("Skipping database tests (SKIP_DB_TESTS is set)")
	}
}

// getMigrationsPath returns the absolute path to migrations directory.
func getMigrationsPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "database", "postgres", "migrations")
}

// f64ptr returns a pointer to v for the nullable *float64 snapshot fields.
func f64ptr(v float64) *float64 { return &v }

func TestPostgresAnalyticsStore_SaveSnapshot_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	t.Run("save snapshot with all fields", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		snapshot := &analytics.SavingsSnapshot{
			AccountID:          "123456789012",
			Timestamp:          now,
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalCommitment:    1000.00,
			TotalUsage:         f64ptr(800.00),
			TotalSavings:       200.00,
			CoveragePercentage: f64ptr(80.00),
			Metadata: map[string]interface{}{
				"active_purchases": 5,
				"collection_time":  now.Format(time.RFC3339),
			},
		}

		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
		assert.NotEmpty(t, snapshot.ID)
	})

	t.Run("save snapshot without metadata", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		snapshot := &analytics.SavingsSnapshot{
			AccountID:          "123456789012",
			Timestamp:          now,
			Provider:           "gcp",
			Service:            "cloudsql",
			Region:             "us-central1",
			CommitmentType:     "RI",
			TotalCommitment:    500.00,
			TotalUsage:         f64ptr(400.00),
			TotalSavings:       100.00,
			CoveragePercentage: f64ptr(80.00),
		}

		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
		assert.NotEmpty(t, snapshot.ID)
	})

	t.Run("save SavingsPlan commitment type", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		snapshot := &analytics.SavingsSnapshot{
			AccountID:          "123456789012",
			Timestamp:          now,
			Provider:           "aws",
			Service:            "SavingsPlans",
			Region:             "us-east-1",
			CommitmentType:     "SavingsPlan",
			TotalCommitment:    2000.00,
			TotalUsage:         f64ptr(1500.00),
			TotalSavings:       500.00,
			CoveragePercentage: f64ptr(75.00),
		}

		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
	})
}

func TestPostgresAnalyticsStore_QuerySavings_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	// Insert test data
	now := time.Now().UTC().Truncate(time.Microsecond)
	testSnapshots := []*analytics.SavingsSnapshot{
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-1 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalCommitment:    1000.00,
			TotalUsage:         f64ptr(800.00),
			TotalSavings:       200.00,
			CoveragePercentage: f64ptr(80.00),
		},
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-2 * time.Hour),
			Provider:           "aws",
			Service:            "elasticache",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalCommitment:    500.00,
			TotalUsage:         f64ptr(400.00),
			TotalSavings:       100.00,
			CoveragePercentage: f64ptr(80.00),
		},
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-3 * time.Hour),
			Provider:           "gcp",
			Service:            "cloudsql",
			Region:             "us-central1",
			CommitmentType:     "RI",
			TotalCommitment:    750.00,
			TotalUsage:         f64ptr(600.00),
			TotalSavings:       150.00,
			CoveragePercentage: f64ptr(80.00),
		},
	}

	for _, snapshot := range testSnapshots {
		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
	}

	t.Run("query all snapshots for account", func(t *testing.T) {
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}},
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
		}

		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("query with provider filter", func(t *testing.T) {
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}},
			Provider:                     "aws",
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
		}

		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Len(t, results, 2)
		for _, r := range results {
			assert.Equal(t, "aws", r.Provider)
		}
	})

	t.Run("query with service filter", func(t *testing.T) {
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}},
			Service:                      "rds",
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
		}

		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, "rds", results[0].Service)
	})

	t.Run("query with limit", func(t *testing.T) {
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}},
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
			Limit:                        2,
		}

		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("query returns empty for non-existent account", func(t *testing.T) {
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"999999999999"}, "gcp": {"999999999999"}},
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
		}

		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestPostgresAnalyticsStore_QueryByProvider_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	// Insert test data
	now := time.Now().UTC().Truncate(time.Microsecond)
	testSnapshots := []*analytics.SavingsSnapshot{
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-1 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalSavings:       200.00,
			CoveragePercentage: f64ptr(80.00),
		},
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-2 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalSavings:       100.00,
			CoveragePercentage: f64ptr(75.00),
		},
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-3 * time.Hour),
			Provider:           "gcp",
			Service:            "cloudsql",
			Region:             "us-central1",
			CommitmentType:     "RI",
			TotalSavings:       150.00,
			CoveragePercentage: f64ptr(70.00),
		},
	}

	for _, snapshot := range testSnapshots {
		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
	}

	t.Run("query by provider aggregates correctly", func(t *testing.T) {
		breakdowns, err := store.QueryByProvider(ctx, nil, map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}}, now.Add(-24*time.Hour), now)
		require.NoError(t, err)
		assert.NotEmpty(t, breakdowns)

		// Find aws/rds breakdown
		var found bool
		for _, b := range breakdowns {
			if b.Provider == "aws" && b.Service == "rds" {
				found = true
				// Snapshots are run-rates: aggregation over time uses AVG,
				// not SUM (see migration 000074 / PR #1127).
				assert.InDelta(t, 150.00, b.TotalSavings, 0.01) // AVG(200, 100)
				require.NotNil(t, b.AvgCoverage)
				assert.InDelta(t, 77.5, *b.AvgCoverage, 0.01) // (80 + 75) / 2
			}
		}
		assert.True(t, found, "aws/rds breakdown not found")
	})
}

func TestPostgresAnalyticsStore_QueryByService_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	// Insert test data
	now := time.Now().UTC().Truncate(time.Microsecond)
	testSnapshots := []*analytics.SavingsSnapshot{
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-1 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalSavings:       200.00,
			CoveragePercentage: f64ptr(80.00),
		},
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-2 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-west-2",
			CommitmentType:     "RI",
			TotalSavings:       150.00,
			CoveragePercentage: f64ptr(75.00),
		},
	}

	for _, snapshot := range testSnapshots {
		err := store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
	}

	t.Run("query by service groups by region", func(t *testing.T) {
		breakdowns, err := store.QueryByService(ctx, nil, map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}}, "aws", now.Add(-24*time.Hour), now)
		require.NoError(t, err)
		assert.Len(t, breakdowns, 2) // Two regions

		regions := make(map[string]float64)
		for _, b := range breakdowns {
			regions[b.Region] = b.TotalSavings
		}

		assert.InDelta(t, 200.00, regions["us-east-1"], 0.01)
		assert.InDelta(t, 150.00, regions["us-west-2"], 0.01)
	})
}

func TestPostgresAnalyticsStore_BulkInsertSnapshots_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	t.Run("bulk insert empty slice", func(t *testing.T) {
		err := store.BulkInsertSnapshots(ctx, []analytics.SavingsSnapshot{})
		assert.NoError(t, err)
	})

	t.Run("bulk insert multiple snapshots", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		snapshots := []analytics.SavingsSnapshot{
			{
				AccountID:      "123456789012",
				Timestamp:      now.Add(-1 * time.Hour),
				Provider:       "aws",
				Service:        "rds",
				Region:         "us-east-1",
				CommitmentType: "RI",
				TotalSavings:   100.00,
			},
			{
				AccountID:      "123456789012",
				Timestamp:      now.Add(-2 * time.Hour),
				Provider:       "aws",
				Service:        "elasticache",
				Region:         "us-east-1",
				CommitmentType: "RI",
				TotalSavings:   200.00,
			},
		}

		err := store.BulkInsertSnapshots(ctx, snapshots)
		require.NoError(t, err)

		// Verify data was inserted
		req := analytics.QueryRequest{
			AccountExternalIDsByProvider: map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}},
			StartDate:                    now.Add(-24 * time.Hour),
			EndDate:                      now,
		}
		results, err := store.QuerySavings(ctx, req)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})
}

func TestPostgresAnalyticsStore_PartitionManagement_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	t.Run("create partition for specific month", func(t *testing.T) {
		// Create partition for a future month
		futureMonth := time.Now().AddDate(1, 0, 0)
		err := store.CreatePartition(ctx, futureMonth)
		assert.NoError(t, err)

		// Creating the same partition again should not error
		err = store.CreatePartition(ctx, futureMonth)
		assert.NoError(t, err)
	})

	t.Run("create partitions for range", func(t *testing.T) {
		startDate := time.Now().AddDate(0, 6, 0)
		endDate := time.Now().AddDate(0, 8, 0)

		err := store.CreatePartitionsForRange(ctx, startDate, endDate)
		assert.NoError(t, err)
	})

	t.Run("drop old partitions with high retention", func(t *testing.T) {
		// Use high retention value so nothing gets dropped
		err := store.DropOldPartitions(ctx, 120) // 10 years
		assert.NoError(t, err)
	})
}

func TestPostgresAnalyticsStore_QueryMonthlyTotals_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	// Insert test data
	now := time.Now().UTC().Truncate(time.Microsecond)
	testSnapshots := []*analytics.SavingsSnapshot{
		{
			AccountID:          "123456789012",
			Timestamp:          now.Add(-1 * time.Hour),
			Provider:           "aws",
			Service:            "rds",
			Region:             "us-east-1",
			CommitmentType:     "RI",
			TotalCommitment:    1000.00,
			TotalUsage:         f64ptr(800.00),
			TotalSavings:       200.00,
			CoveragePercentage: f64ptr(80.00),
		},
	}

	for _, snapshot := range testSnapshots {
		err = store.SaveSnapshot(ctx, snapshot)
		require.NoError(t, err)
	}

	// Refresh materialized views so data is available
	err = store.RefreshMaterializedViews(ctx)
	require.NoError(t, err)

	t.Run("query monthly totals from materialized view", func(t *testing.T) {
		summaries, err := store.QueryMonthlyTotals(ctx, nil, map[string][]string{"aws": {"123456789012"}, "gcp": {"123456789012"}}, 6)
		require.NoError(t, err)
		// May be empty if materialized view refresh happens before data is visible
		assert.NotNil(t, summaries)
	})

	t.Run("query monthly totals for non-existent account", func(t *testing.T) {
		summaries, err := store.QueryMonthlyTotals(ctx, nil, map[string][]string{"aws": {"999999999999"}, "gcp": {"999999999999"}}, 6)
		require.NoError(t, err)
		assert.Empty(t, summaries)
	})
}

func TestPostgresAnalyticsStore_RefreshMaterializedViews_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Run migrations
	err = migrations.RunMigrations(ctx, container.DB.Pool(), getMigrationsPath(), "", "")
	require.NoError(t, err)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	t.Run("refresh materialized views", func(t *testing.T) {
		err := store.RefreshMaterializedViews(ctx)
		assert.NoError(t, err)
	})
}

func TestPostgresAnalyticsStore_Close_DB(t *testing.T) {
	skipIfNoDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Setup test container
	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	// Create store
	store := analytics.NewPostgresAnalyticsStore(container.DB)

	t.Run("close returns nil", func(t *testing.T) {
		err := store.Close()
		assert.NoError(t, err)
	})
}
