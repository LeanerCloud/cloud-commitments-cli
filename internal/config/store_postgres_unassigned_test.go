//go:build integration
// +build integration

package config

// TestPostgresStoreDB_ListPurchasePlans_UnassignedBucket is a real-DB
// integration test that exercises the LEFT JOIN + OR NOT EXISTS query
// introduced in #973. It is the discriminating regression guard that
// would FAIL on the pre-fix INNER JOIN code, where plan B (zero
// plan_accounts rows) would be absent from the filtered result entirely.
//
// Seed layout:
//   - accountX: a cloud_accounts row whose UUID is used as the filter.
//   - accountY: a second cloud_accounts row.
//   - planA: assigned to accountX via plan_accounts -> must appear with Unassigned=false.
//   - planB: ZERO plan_accounts rows (legacy/unassigned) -> must appear with Unassigned=true.
//   - planC: assigned only to accountY -> must NOT appear when filtering by [accountX].
//
// Three assertions prove the SQL is correct:
//  1. planB is present in the accountX-filtered result (fails on INNER JOIN).
//  2. planB.Unassigned == true.
//  3. planC is absent from the accountX-filtered result.

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresStoreDB_ListPurchasePlans_UnassignedBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() { container.Cleanup(context.Background()) })

	// A migration that will not apply is a broken schema, not a missing
	// container runtime: fail rather than report the suite green (issue #1597).
	require.NoError(t,
		migrations.RunMigrations(ctx, container.DB.Pool(), getTestMigrationsPath(), "", ""),
		"migrations failed to apply to a fresh database")

	conn := container.DB
	store := NewPostgresStore(conn)

	// ---- Seed cloud_accounts ----
	// These are inserted directly so we control the UUIDs used in plan_accounts.
	accountXID := uuid.New().String()
	accountYID := uuid.New().String()
	insertAccount := func(id, name, externalID string) {
		t.Helper()
		_, err := conn.Exec(ctx, `
			INSERT INTO cloud_accounts
				(id, name, enabled, provider, external_id, aws_is_org_root, created_at, updated_at)
			VALUES ($1, $2, true, 'aws', $3, false, now(), now())
		`, id, name, externalID)
		require.NoError(t, err, "seed cloud_accounts: %s", name)
	}
	insertAccount(accountXID, "Account X", "111111111111")
	insertAccount(accountYID, "Account Y", "222222222222")

	t.Cleanup(func() {
		// plan_accounts rows cascade on purchase_plan delete; cloud_accounts rows do not.
		conn.Exec(context.Background(), "DELETE FROM cloud_accounts WHERE id = ANY($1::uuid[])",
			[]string{accountXID, accountYID})
	})

	// ---- Seed purchase_plans ----
	makePlan := func(name string) *PurchasePlan {
		return &PurchasePlan{
			Name:                   name,
			Enabled:                true,
			NotificationDaysBefore: 3,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           PresetRampSchedules["immediate"],
		}
	}
	planA := makePlan("Plan A - assigned to X")
	planB := makePlan("Plan B - unassigned (legacy)")
	planC := makePlan("Plan C - assigned to Y only")

	require.NoError(t, store.CreatePurchasePlan(ctx, planA))
	require.NoError(t, store.CreatePurchasePlan(ctx, planB))
	require.NoError(t, store.CreatePurchasePlan(ctx, planC))

	t.Cleanup(func() {
		conn.Exec(context.Background(), "DELETE FROM purchase_plans WHERE id = ANY($1::uuid[])",
			[]string{planA.ID, planB.ID, planC.ID})
	})

	// ---- Seed plan_accounts ----
	insertPlanAccount := func(planID, accountID string) {
		t.Helper()
		_, err := conn.Exec(ctx, `
			INSERT INTO plan_accounts (plan_id, account_id)
			VALUES ($1::uuid, $2::uuid)
			ON CONFLICT DO NOTHING
		`, planID, accountID)
		require.NoError(t, err, "seed plan_accounts (%s -> %s)", planID, accountID)
	}
	insertPlanAccount(planA.ID, accountXID) // planA -> accountX
	insertPlanAccount(planC.ID, accountYID) // planC -> accountY only
	// planB gets NO plan_accounts row -- that is the legacy case.

	// ====================================================================
	// Sub-test 1: account-filtered query (filter = [accountX])
	// ====================================================================
	t.Run("account-filtered result includes unassigned plan and excludes wrong-account plan", func(t *testing.T) {
		plans, err := store.ListPurchasePlans(ctx, PurchasePlanFilter{AccountIDs: []string{accountXID}})
		require.NoError(t, err)

		byID := make(map[string]PurchasePlan, len(plans))
		for _, p := range plans {
			byID[p.ID] = p
		}

		// planA must appear, assigned (not unassigned).
		if pa, ok := byID[planA.ID]; assert.True(t, ok, "planA (assigned to accountX) must be in result") {
			assert.False(t, pa.Unassigned, "planA is assigned; Unassigned must be false")
		}

		// planB must appear, flagged as unassigned.
		// DISCRIMINATING ASSERTION: on pre-fix INNER JOIN this row is absent.
		if pb, ok := byID[planB.ID]; assert.True(t, ok,
			"planB (zero plan_accounts rows) must be included in filtered result (regression guard for #973 INNER JOIN bug)") {
			assert.True(t, pb.Unassigned, "planB has no plan_accounts rows; Unassigned must be true")
		}

		// planC must NOT appear (it belongs only to accountY).
		_, planCPresent := byID[planC.ID]
		assert.False(t, planCPresent, "planC (assigned to accountY only) must NOT appear when filtering by accountX")
	})

	// ====================================================================
	// Sub-test 2: no-filter query returns all three plans
	// ====================================================================
	t.Run("no-filter result returns all plans; assigned plans have Unassigned=false", func(t *testing.T) {
		plans, err := store.ListPurchasePlans(ctx, PurchasePlanFilter{})
		require.NoError(t, err)

		byID := make(map[string]PurchasePlan, len(plans))
		for _, p := range plans {
			byID[p.ID] = p
		}

		// All three plans must be present.
		assert.Contains(t, byID, planA.ID, "planA must appear in no-filter result")
		assert.Contains(t, byID, planB.ID, "planB must appear in no-filter result")
		assert.Contains(t, byID, planC.ID, "planC must appear in no-filter result")

		// In the no-filter path, all plans are returned with Unassigned=false
		// (the query hard-codes `false AS unassigned`).
		for _, p := range []PurchasePlan{byID[planA.ID], byID[planB.ID], byID[planC.ID]} {
			assert.False(t, p.Unassigned,
				"no-filter path hard-codes false AS unassigned; plan %s must have Unassigned=false", p.ID)
		}
	})
}
