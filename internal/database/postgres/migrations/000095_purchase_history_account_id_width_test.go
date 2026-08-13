//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitmentRow is the provider-shaped part of a purchase_history row: the
// account identifier under test plus the provider/service/region/resource_type
// that a real commitment from that provider carries. Keeping them together
// means the GCP case inserts a GCP-shaped row rather than borrowing Azure's,
// so the row each assertion exercises matches the scenario it claims to cover.
type commitmentRow struct {
	provider     string
	service      string
	region       string
	resourceType string
	accountID    string
}

// The account identifiers that do not fit VARCHAR(20):
//
//   - azureCommitment carries a 36-character subscription GUID, the shape every
//     Azure subscription ID has (cloud_accounts.azure_subscription_id is
//     VARCHAR(36)).
//   - gcpCommitment carries a 30-character project ID, the documented GCP
//     maximum (6-30 chars, lowercase letters / digits / hyphens, starting with
//     a letter).
//
// Both reach SavePurchaseHistory as cloud_accounts.external_id, which is
// VARCHAR(255) at its source, so nothing upstream trims them. awsCommitment is
// the 12-digit control that has always fit.
var (
	azureCommitment = commitmentRow{
		provider: "azure", service: "compute", region: "westeurope",
		resourceType: "Standard_D4s_v3",
		accountID:    "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
	}
	gcpCommitment = commitmentRow{
		provider: "gcp", service: "compute", region: "europe-west1",
		resourceType: "n2-standard-4",
		accountID:    "cudly-production-analytics-001",
	}
	awsCommitment = commitmentRow{
		provider: "aws", service: "ec2", region: "us-east-1",
		resourceType: "m5.large",
		accountID:    "123456789012",
	}
)

// insertPurchaseHistoryRow issues the same INSERT column set that
// PostgresStore.SavePurchaseHistory uses for its NOT-NULL-without-default
// columns, with row.accountID as purchase_history.account_id. plan_id and
// cloud_account_id are omitted (nullable FKs) so the test needs no fixtures.
func insertPurchaseHistoryRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, row commitmentRow, purchaseID string) error {
	t.Helper()
	const insertSQL = `
		INSERT INTO purchase_history (
			account_id, purchase_id, timestamp, provider, service, region,
			resource_type, term, payment
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 12, 'All Upfront')`
	_, err := pool.Exec(ctx, insertSQL, row.accountID, purchaseID, time.Now(),
		row.provider, row.service, row.region, row.resourceType)
	return err
}

// accountIDMaxLength reads the declared width of purchase_history.account_id
// straight from the catalog, so the assertion is about the real schema rather
// than about what the migration file claims to do. It deliberately probes via
// information_schema rather than reusing the migration's own pg_attribute /
// regclass expression: an independent lookup cannot rubber-stamp a bug in the
// migration's probe.
func accountIDMaxLength(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var maxLen int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT character_maximum_length
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'purchase_history'
		  AND column_name = 'account_id'`).Scan(&maxLen))
	return maxLen
}

// TestMigration095_PurchaseHistoryAccountIDWidth is the fail-before/pass-after
// regression test for issue #1603.
//
// It replicates the real failing scenario rather than a narrower unit: the
// exact INSERT column set SavePurchaseHistory issues, carrying the account
// identifier an Azure or GCP purchase actually produces. On the pre-migration
// schema (version 000092) both inserts are rejected by Postgres with SQLSTATE
// 22001, which is precisely how the audit row for an already-billed commitment
// is lost. After 000095 both succeed and round-trip untruncated.
func TestMigration095_PurchaseHistoryAccountIDWidth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	pool := container.DB.Pool()

	// ---- Pre-migration state: pinned at 000092, the last version whose
	// schema this test depends on. Pinned to an explicit version rather than
	// migrated to HEAD so the next migration PR does not break this test's CI
	// (feedback_migration_test_pin_version); any migration landing between 92
	// and 95 is applied by the MigrateToVersion(95) call below.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, getMigrationsPath(), 92),
		"migrations must apply cleanly through 000092")

	require.Equal(t, 20, accountIDMaxLength(ctx, t, pool),
		"pre-fix schema must still have the VARCHAR(20) account_id this migration exists to widen")

	// The bug: both non-AWS identifiers are rejected outright.
	for _, tc := range []struct {
		name string
		row  commitmentRow
	}{
		{"azure subscription GUID (36 chars)", azureCommitment},
		{"gcp project ID (30 chars)", gcpCommitment},
	} {
		t.Run("pre-migration/"+tc.name, func(t *testing.T) {
			insErr := insertPurchaseHistoryRow(ctx, t, pool, tc.row, "pre-"+tc.row.accountID)
			require.Error(t, insErr,
				"pre-fix schema must reject the audit row -- this is the data loss in issue #1603")

			t.Logf("pre-fix rejection for %s: %v", tc.row.accountID, insErr)

			var pgErr *pgconn.PgError
			require.True(t, errors.As(insErr, &pgErr), "expected a Postgres error, got %v", insErr)
			assert.Equal(t, "22001", pgErr.Code,
				"expected string_data_right_truncation; the purchase is already billed when this INSERT runs")
		})
	}

	// ---- Apply 000095.
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, getMigrationsPath(), 95),
		"migration 000095 must apply cleanly on top of 000092")

	assert.Equal(t, 255, accountIDMaxLength(ctx, t, pool),
		"account_id must be VARCHAR(255), matching cloud_accounts.external_id and savings_snapshots.account_id")

	// ---- Post-migration: the same inserts succeed and round-trip intact.
	for _, tc := range []struct {
		name       string
		row        commitmentRow
		purchaseID string
	}{
		{"azure subscription GUID (36 chars)", azureCommitment, "azure-ri-1603"},
		{"gcp project ID (30 chars)", gcpCommitment, "gcp-cud-1603"},
	} {
		t.Run("post-migration/"+tc.name, func(t *testing.T) {
			require.NoError(t,
				insertPurchaseHistoryRow(ctx, t, pool, tc.row, tc.purchaseID),
				"audit row for an already-billed commitment must persist after 000095")

			var stored string
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT account_id FROM purchase_history WHERE purchase_id = $1`,
				tc.purchaseID).Scan(&stored))
			assert.Equal(t, tc.row.accountID, stored,
				"account_id must round-trip untruncated")
		})
	}

	// ---- Re-running the up migration must be a no-op, not a failure. The
	// header claims correctness "on re-run under the auto-heal path"; this
	// exercises that claim directly by executing the file's SQL a second time
	// against an already-widened column.
	upSQL, err := os.ReadFile(filepath.Join(getMigrationsPath(),
		"000095_purchase_history_account_id_width.up.sql"))
	require.NoError(t, err, "the up migration file must be readable")
	_, err = pool.Exec(ctx, string(upSQL))
	require.NoError(t, err, "re-running 000095 on an already-widened column must be a no-op")
	assert.Equal(t, 255, accountIDMaxLength(ctx, t, pool),
		"a re-run must leave account_id at VARCHAR(255)")
}

// TestMigration095_DownNarrowsWhenSafe covers the rollback's success path: with
// no over-long value present, narrowing back to VARCHAR(20) is lossless and
// must actually happen. Without this the ALTER branch after the guard is never
// executed by any test and a typo in it would ship green.
func TestMigration095_DownNarrowsWhenSafe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, getMigrationsPath(), 95),
		"migrations must apply cleanly through 000095")

	// A 12-digit AWS account ID fits VARCHAR(20), so the rollback is lossless.
	require.NoError(t,
		insertPurchaseHistoryRow(ctx, t, pool, awsCommitment, "aws-ri-down-1603"),
		"setup: an AWS audit row must be insertable")

	// One step, so exactly 000095's down migration runs. Targeting a version
	// would also run the downs of any migration that lands between 92 and 95,
	// coupling this test to unrelated future rollbacks.
	require.NoError(t, migrations.RollbackMigrations(ctx, pool, getMigrationsPath(), 1),
		"rollback must succeed when every account_id fits VARCHAR(20)")

	assert.Equal(t, 20, accountIDMaxLength(ctx, t, pool),
		"the rollback must actually narrow the column, not silently no-op")

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT account_id FROM purchase_history WHERE purchase_id = 'aws-ri-down-1603'`).Scan(&stored))
	assert.Equal(t, awsCommitment.accountID, stored, "the lossless rollback must preserve the row")
}

// TestMigration095_DownRefusesToTruncate verifies that the rollback fails
// loudly instead of silently truncating. Narrowing account_id back to
// VARCHAR(20) with an Azure subscription GUID present would destroy the audit
// trail this table exists to provide, so the down migration must refuse.
func TestMigration095_DownRefusesToTruncate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	defer container.Cleanup(ctx)

	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, getMigrationsPath(), 95),
		"migrations must apply cleanly through 000095")

	require.NoError(t,
		insertPurchaseHistoryRow(ctx, t, pool, azureCommitment, "azure-ri-down-1603"),
		"setup: an Azure audit row must be insertable after 000095")

	// One step, so exactly 000095's down migration runs (see the sibling test).
	err = migrations.RollbackMigrations(ctx, pool, getMigrationsPath(), 1)
	require.Error(t, err,
		"rolling back 000095 with an over-long account_id present must fail rather than truncate")
	assert.Contains(t, err.Error(), "refusing to narrow purchase_history.account_id",
		"the failure must name the reason so an operator does not reach for a truncating USING clause")

	// The row is still intact: the guard fires before any rewrite.
	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT account_id FROM purchase_history WHERE purchase_id = 'azure-ri-down-1603'`).Scan(&stored))
	assert.Equal(t, azureCommitment.accountID, stored,
		"the refused rollback must leave the audit row untouched")
}
