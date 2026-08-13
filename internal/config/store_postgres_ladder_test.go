//go:build integration
// +build integration

package config

// Real-DB integration tests for the ladder_runs / ladder_tranches store
// methods added in PR-2 (migrations 000080/000081). They run against a
// throwaway PostgreSQL container (testhelpers.SetupPostgresContainer), the
// same harness the other *_db_test.go / *_unassigned_test.go integration
// tests use, and are gated behind the `integration` build tag so the default
// `go test ./...` run stays hermetic.
//
// Coverage:
//   - SaveLadderRun round-trips a run with all NULL monetary columns preserved
//     as nil (never 0-coerced) AND a run with populated monetary columns.
//   - LatestLadderRunStartedAt returns nil with no rows and the MAX(started_at)
//     across multiple runs.
//   - SaveLadderTranches batch-inserts every row in one transaction.
//   - TransitionLadderRunStatus performs a CAS: it updates when the current
//     status is in the from-set (win) and is a no-op returning (nil, nil) when
//     it is not (lose).

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/LeanerCloud/CUDly/pkg/ladder"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLadderStore starts a container, runs migrations, and returns a store.
// It skips (not fails) only when this environment has no usable Docker daemon,
// so the suite degrades gracefully without a container runtime. A migration that
// will not apply is a broken schema, not a missing runtime, and fails loudly
// (issue #1597).
func setupLadderStore(ctx context.Context, t *testing.T) *PostgresStore {
	t.Helper()
	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() { container.Cleanup(context.Background()) })

	require.NoError(t,
		migrations.RunMigrations(ctx, container.DB.Pool(), getTestMigrationsPath(), "", ""),
		"migrations failed to apply to a fresh database")
	return NewPostgresStore(container.DB)
}

// seedLadderConfig inserts a cloud_account and a ladder_config, returning the
// ladder_config id for use as a run/tranche config_id FK.
func seedLadderConfig(ctx context.Context, t *testing.T, store *PostgresStore) string {
	t.Helper()
	acctID := uuid.New().String()
	_, err := store.db.Exec(ctx, `
		INSERT INTO cloud_accounts
			(id, name, enabled, provider, external_id, aws_is_org_root, created_at, updated_at)
		VALUES ($1, 'Ladder Test Account', true, 'aws', '123456789012', false, now(), now())
	`, acctID)
	require.NoError(t, err, "seed cloud_accounts")

	cfg := &LadderConfigDB{
		CloudAccountID:             acctID,
		Provider:                   "aws",
		Enabled:                    true,
		Mode:                       "email_approval",
		Cadence:                    "daily",
		TargetCoverage:             80.0,
		BufferFraction:             0.1,
		BaselinePercentile:         5.0,
		LookbackDays:               30,
		BufferUtilizationThreshold: 50.0,
		MaxActionsPerRun:           5,
		RampSchedule:               []byte(`{"steps":[{"after_days":0,"fraction":1.0}]}`),
	}
	saved, err := store.UpsertLadderConfig(ctx, cfg)
	require.NoError(t, err, "seed ladder_config")
	require.NotEmpty(t, saved.ID)
	return saved.ID
}

func TestPostgresStore_SaveLadderRun_NullMonetaryPreserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)

	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("NULL monetary columns round-trip as nil", func(t *testing.T) {
		run := &LadderRunDB{
			StartedAt: now,
			Status:    ladder.RunStatusPlanned,
			// All monetary snapshot fields intentionally nil.
		}
		saved, err := store.SaveLadderRun(ctx, run)
		require.NoError(t, err)
		require.NotEmpty(t, saved.ID, "SaveLadderRun must generate an ID")

		got, err := store.GetLadderRun(ctx, saved.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, ladder.RunStatusPlanned, got.Status)
		// The core assertion: NULL != 0. Absent monetary values must stay nil.
		assert.Nil(t, got.BaselineUSDHr, "baseline_usd_hr NULL must scan as nil, not 0")
		assert.Nil(t, got.TargetUSDHr, "target_usd_hr NULL must scan as nil, not 0")
		assert.Nil(t, got.ExistingUSDHr, "existing_usd_hr NULL must scan as nil, not 0")
		assert.Nil(t, got.GapUSDHr, "gap_usd_hr NULL must scan as nil, not 0")
		assert.Nil(t, got.ConfigID, "config_id NULL must scan as nil")
		// NOT NULL accumulator totals default to 0.
		assert.Equal(t, 0.0, got.TotalHourlyCommit)
		// Plan defaults to '{}' JSONB.
		assert.JSONEq(t, `{}`, string(got.Plan))
	})

	t.Run("populated monetary columns round-trip by value", func(t *testing.T) {
		baseline := 12.5
		target := 10.0
		existing := 3.0
		gap := 7.0
		run := &LadderRunDB{
			StartedAt:         now,
			Status:            ladder.RunStatusPlanned,
			BaselineUSDHr:     &baseline,
			TargetUSDHr:       &target,
			ExistingUSDHr:     &existing,
			GapUSDHr:          &gap,
			TotalHourlyCommit: 7.0,
			Plan:              []byte(`{"actions":[]}`),
		}
		saved, err := store.SaveLadderRun(ctx, run)
		require.NoError(t, err)

		got, err := store.GetLadderRun(ctx, saved.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.BaselineUSDHr)
		assert.InDelta(t, baseline, *got.BaselineUSDHr, 1e-6)
		require.NotNil(t, got.GapUSDHr)
		assert.InDelta(t, gap, *got.GapUSDHr, 1e-6)
		assert.InDelta(t, 7.0, got.TotalHourlyCommit, 1e-6)
	})

	t.Run("GetLadderRun returns nil for a missing id", func(t *testing.T) {
		got, err := store.GetLadderRun(ctx, uuid.New().String())
		require.NoError(t, err)
		assert.Nil(t, got, "GetLadderRun must return (nil, nil) for a missing row")
	})
}

func TestPostgresStore_LatestLadderRunStartedAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	configID := seedLadderConfig(ctx, t, store)

	t.Run("nil when no runs exist for the config", func(t *testing.T) {
		latest, err := store.LatestLadderRunStartedAt(ctx, configID)
		require.NoError(t, err)
		assert.Nil(t, latest, "no runs -> nil (never a zero time)")
	})

	t.Run("returns MAX(started_at) across multiple runs", func(t *testing.T) {
		older := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
		newer := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)

		for _, ts := range []time.Time{older, newer} {
			ts := ts
			cfgID := configID
			_, err := store.SaveLadderRun(ctx, &LadderRunDB{
				ConfigID:  &cfgID,
				StartedAt: ts,
				Status:    ladder.RunStatusPlanned,
			})
			require.NoError(t, err)
		}

		latest, err := store.LatestLadderRunStartedAt(ctx, configID)
		require.NoError(t, err)
		require.NotNil(t, latest)
		assert.WithinDuration(t, newer, *latest, time.Second, "must return the most recent started_at")
	})
}

func TestPostgresStore_SaveLadderTranches_Batch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	configID := seedLadderConfig(ctx, t, store)

	// A tranche run_id FK requires an existing run row.
	cfgID := configID
	run, err := store.SaveLadderRun(ctx, &LadderRunDB{
		ConfigID:  &cfgID,
		StartedAt: time.Now().UTC(),
		Status:    ladder.RunStatusPlanned,
	})
	require.NoError(t, err)

	runID := run.ID
	fire := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)
	tranches := []LadderTrancheDB{
		{
			ID:            uuid.New().String(),
			ConfigID:      &cfgID,
			RunID:         &runID,
			LayerType:     ladder.LayerConvertibleRI,
			Term:          ladder.Term1Year,
			PaymentOption: ladder.PaymentNoUpfront,
			Status:        ladder.TrancheStatusScheduled,
			AmountUSDHr:   1.5,
			ScheduledDate: fire,
		},
		{
			ID:            uuid.New().String(),
			ConfigID:      &cfgID,
			RunID:         &runID,
			LayerType:     ladder.LayerEC2InstanceSP,
			Term:          ladder.Term1Year,
			PaymentOption: ladder.PaymentNoUpfront,
			Status:        ladder.TrancheStatusScheduled,
			AmountUSDHr:   2.5,
			ScheduledDate: fire,
		},
	}

	require.NoError(t, store.SaveLadderTranches(ctx, tranches))

	// Verify both rows landed and reference the run.
	var count int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM ladder_tranches WHERE run_id = $1`, runID).Scan(&count))
	assert.Equal(t, 2, count, "both tranches must be inserted in the batch")

	t.Run("empty slice is a no-op", func(t *testing.T) {
		require.NoError(t, store.SaveLadderTranches(ctx, nil))
	})

	t.Run("empty tranche ID is rejected", func(t *testing.T) {
		err := store.SaveLadderTranches(ctx, []LadderTrancheDB{
			{
				ID:            "", // invalid
				RunID:         &runID,
				LayerType:     ladder.LayerConvertibleRI,
				Term:          ladder.Term1Year,
				PaymentOption: ladder.PaymentNoUpfront,
				Status:        ladder.TrancheStatusScheduled,
				AmountUSDHr:   1.0,
				ScheduledDate: fire,
			},
		})
		require.Error(t, err, "an empty tranche ID must be rejected")
	})
}

func TestPostgresStore_TransitionLadderRunStatus_CAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)

	run, err := store.SaveLadderRun(ctx, &LadderRunDB{
		StartedAt: time.Now().UTC(),
		Status:    ladder.RunStatusPlanned,
	})
	require.NoError(t, err)

	t.Run("CAS win: current status is in the from-set", func(t *testing.T) {
		updated, err := store.TransitionLadderRunStatus(ctx, run.ID,
			[]ladder.RunStatus{ladder.RunStatusPlanned}, ladder.RunStatusAwaitingApproval)
		require.NoError(t, err)
		require.NotNil(t, updated, "transition from the correct status must update the row")
		assert.Equal(t, ladder.RunStatusAwaitingApproval, updated.Status)
	})

	t.Run("CAS lose: current status not in the from-set", func(t *testing.T) {
		// The row is now awaiting_approval; a transition expecting planned must
		// affect zero rows and signal the miss with (nil, nil).
		updated, err := store.TransitionLadderRunStatus(ctx, run.ID,
			[]ladder.RunStatus{ladder.RunStatusPlanned}, ladder.RunStatusApproved)
		require.NoError(t, err)
		assert.Nil(t, updated, "CAS miss must return (nil, nil), not an error")

		// The stored status must be unchanged from the winning transition.
		got, err := store.GetLadderRun(ctx, run.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, ladder.RunStatusAwaitingApproval, got.Status,
			"a lost CAS must not mutate the row")
	})
}

// TestPostgresStore_SaveLadderRunWithTranches_Atomicity proves the run+tranche
// persist is transactional (B2): on success both land, and on a tranche-insert
// failure the run row is rolled back too (never a status=planned run with no
// tranches, which the cadence gate would then suppress a retry of).
func TestPostgresStore_SaveLadderRunWithTranches_Atomicity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	configID := seedLadderConfig(ctx, t, store)
	cfgID := configID
	fire := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)

	newTranche := func(runID *string, id string) LadderTrancheDB {
		return LadderTrancheDB{
			ID:            id,
			ConfigID:      &cfgID,
			RunID:         runID,
			LayerType:     ladder.LayerConvertibleRI,
			Term:          ladder.Term1Year,
			PaymentOption: ladder.PaymentNoUpfront,
			Status:        ladder.TrancheStatusScheduled,
			AmountUSDHr:   1.5,
			ScheduledDate: fire,
		}
	}

	t.Run("success: run and tranches both persist", func(t *testing.T) {
		runID := uuid.New().String()
		run := &LadderRunDB{ID: runID, ConfigID: &cfgID, StartedAt: time.Now().UTC(), Status: ladder.RunStatusPlanned}
		tranches := []LadderTrancheDB{newTranche(&runID, uuid.New().String()), newTranche(&runID, uuid.New().String())}

		saved, err := store.SaveLadderRunWithTranches(ctx, run, tranches)
		require.NoError(t, err)
		require.NotNil(t, saved)

		got, err := store.GetLadderRun(ctx, runID)
		require.NoError(t, err)
		require.NotNil(t, got, "the run row must be persisted")

		var count int
		require.NoError(t, store.db.QueryRow(ctx,
			`SELECT count(*) FROM ladder_tranches WHERE run_id = $1`, runID).Scan(&count))
		assert.Equal(t, 2, count, "both tranches must be persisted in the same tx")
	})

	t.Run("rollback: a duplicate tranche ID rolls back the run row too", func(t *testing.T) {
		runID := uuid.New().String()
		run := &LadderRunDB{ID: runID, ConfigID: &cfgID, StartedAt: time.Now().UTC(), Status: ladder.RunStatusPlanned}
		// Two tranches with the SAME id: the second insert violates the PK,
		// failing the transaction after the run row was inserted.
		dupID := uuid.New().String()
		tranches := []LadderTrancheDB{newTranche(&runID, dupID), newTranche(&runID, dupID)}

		saved, err := store.SaveLadderRunWithTranches(ctx, run, tranches)
		require.Error(t, err, "a duplicate tranche ID must fail the transaction")
		assert.Nil(t, saved)

		// The run row must NOT exist: the whole tx rolled back.
		got, err := store.GetLadderRun(ctx, runID)
		require.NoError(t, err)
		assert.Nil(t, got, "the run row must be rolled back when a tranche insert fails")

		var count int
		require.NoError(t, store.db.QueryRow(ctx,
			`SELECT count(*) FROM ladder_tranches WHERE run_id = $1`, runID).Scan(&count))
		assert.Equal(t, 0, count, "no tranche may survive a rolled-back transaction")
	})
}

// seedLadderConfigWithExtID seeds a cloud_account (with the given externalID
// to avoid the UNIQUE(provider, external_id) constraint) and a ladder_config,
// returning the ladder_config id.
func seedLadderConfigWithExtID(ctx context.Context, t *testing.T, store *PostgresStore, externalID string) string {
	t.Helper()
	acctID := uuid.New().String()
	_, err := store.db.Exec(ctx, `
		INSERT INTO cloud_accounts
			(id, name, enabled, provider, external_id, aws_is_org_root, created_at, updated_at)
		VALUES ($1, 'Ladder Test Account', true, 'aws', $2, false, now(), now())
	`, acctID, externalID)
	require.NoError(t, err, "seed cloud_accounts (ext=%s)", externalID)

	cfg := &LadderConfigDB{
		CloudAccountID:             acctID,
		Provider:                   "aws",
		Enabled:                    true,
		Mode:                       "email_approval",
		Cadence:                    "daily",
		TargetCoverage:             80.0,
		BufferFraction:             0.1,
		BaselinePercentile:         5.0,
		LookbackDays:               30,
		BufferUtilizationThreshold: 50.0,
		MaxActionsPerRun:           5,
		RampSchedule:               []byte(`{"steps":[{"after_days":0,"fraction":1.0}]}`),
	}
	saved, err := store.UpsertLadderConfig(ctx, cfg)
	require.NoError(t, err, "seed ladder_config (ext=%s)", externalID)
	require.NotEmpty(t, saved.ID)
	return saved.ID
}

// TestPostgresStore_GetInFlightLadderCommitUSDHr tests the in-flight sum
// query (L5 netting). It verifies:
//   - zero is returned (not nil) when no scheduled tranches exist.
//   - SCHEDULED tranches ONLY are summed; all non-scheduled statuses are
//     excluded (fired/completed are already in ExistingUSDPerHour).
//   - tranches from a different config are not included.
func TestPostgresStore_GetInFlightLadderCommitUSDHr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	// Use distinct external_ids to satisfy UNIQUE(provider, external_id).
	configID := seedLadderConfigWithExtID(ctx, t, store, "111111111111")
	otherConfigID := seedLadderConfigWithExtID(ctx, t, store, "222222222222")
	cfgID := configID
	fire := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)

	insertTranche := func(t *testing.T, cID *string, amount float64, status ladder.TrancheStatus) {
		t.Helper()
		tr := LadderTrancheDB{
			ID:            uuid.New().String(),
			ConfigID:      cID,
			LayerType:     ladder.LayerConvertibleRI,
			Term:          ladder.Term1Year,
			PaymentOption: ladder.PaymentNoUpfront,
			Status:        status,
			AmountUSDHr:   amount,
			ScheduledDate: fire,
		}
		require.NoError(t, store.SaveLadderTranches(ctx, []LadderTrancheDB{tr}))
	}

	t.Run("zero when no tranches exist", func(t *testing.T) {
		// Use the other config which has no tranches yet.
		otherID := otherConfigID
		result, err := store.GetInFlightLadderCommitUSDHr(ctx, otherID)
		require.NoError(t, err)
		require.NotNil(t, result, "must return non-nil *float64 even when sum is zero")
		assert.InDelta(t, 0.0, *result, 1e-6)
	})

	// Insert tranches with various statuses for the main config.
	insertTranche(t, &cfgID, 3.0, ladder.TrancheStatusScheduled) // included
	insertTranche(t, &cfgID, 2.0, ladder.TrancheStatusFired)     // excluded (already in E)
	insertTranche(t, &cfgID, 5.0, ladder.TrancheStatusCompleted) // excluded (already in E)
	insertTranche(t, &cfgID, 1.0, ladder.TrancheStatusCancelled) // excluded (terminal)
	insertTranche(t, &cfgID, 4.0, ladder.TrancheStatusFailed)    // excluded (terminal)

	// A tranche belonging to a different config must not be summed in.
	otherID := otherConfigID
	insertTranche(t, &otherID, 99.0, ladder.TrancheStatusScheduled)

	t.Run("sums scheduled only", func(t *testing.T) {
		result, err := store.GetInFlightLadderCommitUSDHr(ctx, configID)
		require.NoError(t, err)
		require.NotNil(t, result)
		// Only the 3.0 scheduled tranche must be returned. Fired/completed are
		// executed purchases already counted in ExistingUSDPerHour, so netting
		// them again would double-subtract.
		assert.InDelta(t, 3.0, *result, 1e-6, "in-flight sum must include scheduled tranches ONLY")
	})

	t.Run("other config is not contaminated", func(t *testing.T) {
		result, err := store.GetInFlightLadderCommitUSDHr(ctx, otherConfigID)
		require.NoError(t, err)
		require.NotNil(t, result)
		// Only the 99.0 (scheduled) tranche we just inserted for the other config.
		assert.InDelta(t, 99.0, *result, 1e-6, "other config must see only its own tranches")
	})
}

// TestPostgresStore_SaveLadderRunWithTranches_AppendOnly proves the L5
// append-only contract at the store layer: persisting a new run's tranches for
// a config MUST NOT touch that config's existing scheduled tranches. (The
// cancel-and-replace supersede variant was removed; netting caps the appends.)
func TestPostgresStore_SaveLadderRunWithTranches_AppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	configID := seedLadderConfigWithExtID(ctx, t, store, "333333333333")
	cfgID := configID
	fire := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)

	statusOf := func(t *testing.T, id string) string {
		t.Helper()
		var s string
		require.NoError(t, store.db.QueryRow(ctx,
			`SELECT status FROM ladder_tranches WHERE id = $1`, id).Scan(&s))
		return s
	}

	// Generation 1: an existing scheduled tranche (as if from a prior run).
	gen1ID := uuid.New().String()
	require.NoError(t, store.SaveLadderTranches(ctx, []LadderTrancheDB{{
		ID: gen1ID, ConfigID: &cfgID, LayerType: ladder.LayerConvertibleRI,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront,
		Status: ladder.TrancheStatusScheduled, AmountUSDHr: 3.0, ScheduledDate: fire,
	}}))

	// Generation 2: a new run appends its tranche. Gen1 must be untouched.
	runID := uuid.New().String()
	run := &LadderRunDB{ID: runID, ConfigID: &cfgID, StartedAt: time.Now().UTC(), Status: ladder.RunStatusPlanned}
	gen2ID := uuid.New().String()
	newTr := LadderTrancheDB{
		ID: gen2ID, ConfigID: &cfgID, RunID: &runID, LayerType: ladder.LayerConvertibleRI,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront,
		Status: ladder.TrancheStatusScheduled, AmountUSDHr: 2.5, ScheduledDate: fire,
	}
	saved, err := store.SaveLadderRunWithTranches(ctx, run, []LadderTrancheDB{newTr})
	require.NoError(t, err)
	require.NotNil(t, saved)

	// Both generations must be live (scheduled): append-only never supersedes.
	assert.Equal(t, string(ladder.TrancheStatusScheduled), statusOf(t, gen1ID), "existing scheduled tranche must be preserved, never superseded")
	assert.Equal(t, string(ladder.TrancheStatusScheduled), statusOf(t, gen2ID), "new tranche must be persisted as scheduled")

	// In-flight sum reflects BOTH generations (append-only accumulation until
	// tranches fire); netting will cap future runs, not the store.
	inFlight, err := store.GetInFlightLadderCommitUSDHr(ctx, cfgID)
	require.NoError(t, err)
	require.NotNil(t, inFlight)
	assert.InDelta(t, 5.5, *inFlight, 1e-6, "in-flight must sum both live scheduled generations (3.0 + 2.5)")
}
