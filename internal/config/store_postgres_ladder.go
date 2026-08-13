package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LeanerCloud/CUDly/pkg/ladder"
)

// ==========================================
// LADDER CONFIG STORE METHODS
// ==========================================

// GetLadderConfigs returns all ladder_configs rows, newest first.
// Returns an empty slice (not nil) when no rows exist.
func (s *PostgresStore) GetLadderConfigs(ctx context.Context) ([]LadderConfigDB, error) {
	query := `
		SELECT id, cloud_account_id, provider, enabled, mode, cadence,
		       target_coverage, buffer_fraction, baseline_percentile,
		       lookback_days, buffer_utilization_threshold,
		       max_hourly_commit_per_run, max_actions_per_run,
		       ramp_schedule, created_at, updated_at
		FROM ladder_configs
		ORDER BY created_at DESC
	`
	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query ladder_configs: %w", err)
	}
	defer rows.Close()

	var configs []LadderConfigDB
	for rows.Next() {
		cfg, err := scanLadderConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ladder_config row: %w", err)
		}
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating ladder_config rows: %w", err)
	}
	if configs == nil {
		configs = []LadderConfigDB{}
	}
	return configs, nil
}

// GetLadderConfig returns the ladder_config row for the given
// (cloud_account_id, provider) pair. Returns (nil, nil) when no row exists.
func (s *PostgresStore) GetLadderConfig(ctx context.Context, cloudAccountID, provider string) (*LadderConfigDB, error) {
	query := `
		SELECT id, cloud_account_id, provider, enabled, mode, cadence,
		       target_coverage, buffer_fraction, baseline_percentile,
		       lookback_days, buffer_utilization_threshold,
		       max_hourly_commit_per_run, max_actions_per_run,
		       ramp_schedule, created_at, updated_at
		FROM ladder_configs
		WHERE cloud_account_id = $1 AND provider = $2
	`
	row := s.db.QueryRow(ctx, query, cloudAccountID, provider)
	cfg, err := scanLadderConfig(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ladder_config for account=%s provider=%s: %w", cloudAccountID, provider, err)
	}
	return &cfg, nil
}

// UpsertLadderConfig inserts or updates the per-account ladder configuration.
// The upsert key is (cloud_account_id, provider). If ID is empty a new UUID
// is generated; existing rows retain their original id and created_at.
//
// Validate() must be called by the API handler before this method; the store
// does not re-validate to avoid duplicating error messages.
func (s *PostgresStore) UpsertLadderConfig(ctx context.Context, cfg *LadderConfigDB) (*LadderConfigDB, error) {
	if cfg.ID == "" {
		cfg.ID = uuid.New().String()
	}

	rampJSON, err := marshalRampSchedule(cfg.RampSchedule)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ramp_schedule: %w", err)
	}

	query := `
		INSERT INTO ladder_configs (
			id, cloud_account_id, provider, enabled, mode, cadence,
			target_coverage, buffer_fraction, baseline_percentile,
			lookback_days, buffer_utilization_threshold,
			max_hourly_commit_per_run, max_actions_per_run,
			ramp_schedule, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
		ON CONFLICT (cloud_account_id, provider) DO UPDATE SET
			enabled                      = $4,
			mode                         = $5,
			cadence                      = $6,
			target_coverage              = $7,
			buffer_fraction              = $8,
			baseline_percentile          = $9,
			lookback_days                = $10,
			buffer_utilization_threshold = $11,
			max_hourly_commit_per_run    = $12,
			max_actions_per_run          = $13,
			ramp_schedule                = $14,
			updated_at                   = NOW()
		RETURNING id, cloud_account_id, provider, enabled, mode, cadence,
		          target_coverage, buffer_fraction, baseline_percentile,
		          lookback_days, buffer_utilization_threshold,
		          max_hourly_commit_per_run, max_actions_per_run,
		          ramp_schedule, created_at, updated_at
	`

	row := s.db.QueryRow(ctx, query,
		cfg.ID,
		cfg.CloudAccountID,
		cfg.Provider,
		cfg.Enabled,
		cfg.Mode,
		cfg.Cadence,
		cfg.TargetCoverage,
		cfg.BufferFraction,
		cfg.BaselinePercentile,
		cfg.LookbackDays,
		cfg.BufferUtilizationThreshold,
		cfg.MaxHourlyCommitPerRun,
		cfg.MaxActionsPerRun,
		rampJSON,
	)
	result, err := scanLadderConfig(row)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert ladder_config for account=%s provider=%s: %w",
			cfg.CloudAccountID, cfg.Provider, err)
	}
	return &result, nil
}

// scanLadderConfig scans a single ladder_configs row from either a pgx.Row
// or pgx.Rows. Both types satisfy the scannable interface (declared in
// store_postgres_registrations.go). This helper exists to avoid duplicating
// the 16-column scan logic.
func scanLadderConfig(row scannable) (LadderConfigDB, error) {
	var cfg LadderConfigDB
	var rampJSON []byte
	var createdAt, updatedAt time.Time

	err := row.Scan(
		&cfg.ID,
		&cfg.CloudAccountID,
		&cfg.Provider,
		&cfg.Enabled,
		&cfg.Mode,
		&cfg.Cadence,
		&cfg.TargetCoverage,
		&cfg.BufferFraction,
		&cfg.BaselinePercentile,
		&cfg.LookbackDays,
		&cfg.BufferUtilizationThreshold,
		&cfg.MaxHourlyCommitPerRun,
		&cfg.MaxActionsPerRun,
		&rampJSON,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return LadderConfigDB{}, err
	}
	cfg.CreatedAt = createdAt
	cfg.UpdatedAt = updatedAt
	cfg.RampSchedule = json.RawMessage(rampJSON)
	return cfg, nil
}

// ==========================================
// LADDER RUN / TRANCHE STORE METHODS
// ==========================================

// ladderRunInsertQuery INSERTs one ladder_runs row and RETURNs the full row for
// scanLadderRun. Shared by SaveLadderRun and SaveLadderRunWithTranches so the
// column list has a single definition.
const ladderRunInsertQuery = `
	INSERT INTO ladder_runs (
		id, config_id, started_at, completed_at, status, mode, cadence,
		baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
		plan, total_hourly_commit, total_upfront_cost, estimated_savings,
		approval_token_hash, approval_token_expires_at,
		approved_by, cancelled_by, fire_at,
		created_at, updated_at
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7,
		$8, $9, $10, $11,
		$12, $13, $14, $15,
		$16, $17, $18, $19, $20,
		NOW(), NOW()
	)
	RETURNING id, config_id, started_at, completed_at, status, mode, cadence,
	          baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
	          plan, total_hourly_commit, total_upfront_cost, estimated_savings,
	          approval_token_hash, approval_token_expires_at,
	          approved_by, cancelled_by, fire_at,
	          created_at, updated_at
`

// ladderRunPlanJSON returns the plan blob to persist, defaulting an empty plan
// to the JSONB '{}' the schema expects (NOT NULL DEFAULT '{}').
func ladderRunPlanJSON(run *LadderRunDB) json.RawMessage {
	if len(run.Plan) == 0 {
		return json.RawMessage(`{}`)
	}
	return run.Plan
}

// ladderRunInsertArgs returns the positional args for ladderRunInsertQuery in
// column order. Nullable monetary fields stay as *float64 so NULL != $0.
func ladderRunInsertArgs(run *LadderRunDB, planJSON json.RawMessage) []any {
	return []any{
		run.ID,
		run.ConfigID,
		run.StartedAt,
		run.CompletedAt,
		run.Status,
		run.Mode,
		run.Cadence,
		run.BaselineUSDHr,
		run.TargetUSDHr,
		run.ExistingUSDHr,
		run.GapUSDHr,
		planJSON,
		run.TotalHourlyCommit,
		run.TotalUpfrontCost,
		run.EstimatedSavings,
		run.ApprovalTokenHash,
		run.ApprovalTokenExpiresAt,
		run.ApprovedBy,
		run.CancelledBy,
		run.FireAt,
	}
}

// SaveLadderRun inserts a new ladder_runs row. If run.ID is empty a fresh UUID
// is generated. Returns the persisted row with all DB-stamped fields populated.
func (s *PostgresStore) SaveLadderRun(ctx context.Context, run *LadderRunDB) (*LadderRunDB, error) {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	row := s.db.QueryRow(ctx, ladderRunInsertQuery, ladderRunInsertArgs(run, ladderRunPlanJSON(run))...)
	result, err := scanLadderRun(row)
	if err != nil {
		return nil, fmt.Errorf("failed to insert ladder_run id=%s: %w", run.ID, err)
	}
	return &result, nil
}

// GetLadderRun returns the ladder_runs row for the given id, or (nil, nil)
// when no row exists.
func (s *PostgresStore) GetLadderRun(ctx context.Context, id string) (*LadderRunDB, error) {
	query := `
		SELECT id, config_id, started_at, completed_at, status, mode, cadence,
		       baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
		       plan, total_hourly_commit, total_upfront_cost, estimated_savings,
		       approval_token_hash, approval_token_expires_at,
		       approved_by, cancelled_by, fire_at,
		       created_at, updated_at
		FROM ladder_runs
		WHERE id = $1
	`
	row := s.db.QueryRow(ctx, query, id)
	result, err := scanLadderRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ladder_run id=%s: %w", id, err)
	}
	return &result, nil
}

// insertLadderTranchesTx inserts every tranche within the given transaction.
// Each tranche must carry a non-empty ID; duplicate IDs are rejected at the DB
// PRIMARY KEY constraint. Shared by SaveLadderTranches and
// SaveLadderRunWithTranches so both go through identical insert logic.
func insertLadderTranchesTx(ctx context.Context, tx pgx.Tx, tranches []LadderTrancheDB) error {
	for i := range tranches {
		tr := &tranches[i]
		if tr.ID == "" {
			return fmt.Errorf("ladder_tranche at index %d has an empty ID; every tranche must carry a unique non-empty ID", i)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO ladder_tranches (
				id, config_id, run_id, layer_type, amount_usd_hr,
				term, payment_option, scheduled_date, status, execution_id,
				created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		`,
			tr.ID,
			tr.ConfigID,
			tr.RunID,
			string(tr.LayerType),
			tr.AmountUSDHr,
			string(tr.Term),
			string(tr.PaymentOption),
			tr.ScheduledDate,
			string(tr.Status),
			tr.ExecutionID,
		)
		if err != nil {
			return fmt.Errorf("failed to insert ladder_tranche id=%s run_id=%v: %w", tr.ID, tr.RunID, err)
		}
	}
	return nil
}

// SaveLadderTranches inserts a batch of ladder_tranches rows within a
// single transaction. Each tranche must carry a non-empty ID; duplicate
// IDs are rejected at the DB UNIQUE PRIMARY KEY constraint. An empty
// slice is a no-op.
func (s *PostgresStore) SaveLadderTranches(ctx context.Context, tranches []LadderTrancheDB) error {
	if len(tranches) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		return insertLadderTranchesTx(ctx, tx, tranches)
	})
}

// SaveLadderRunWithTranches inserts the ladder_runs row AND its ladder_tranches
// rows in ONE transaction. If any tranche insert fails, the whole transaction
// (including the run row) is rolled back, so a run is never persisted without
// its tranches. This prevents a status=planned run with zero tranches, which
// the cadence self-gate (keyed on any run's started_at) would otherwise use to
// suppress the retry for the full cadence window. If run.ID is empty a fresh
// UUID is generated. Returns the persisted run row.
func (s *PostgresStore) SaveLadderRunWithTranches(ctx context.Context, run *LadderRunDB, tranches []LadderTrancheDB) (*LadderRunDB, error) {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	var result LadderRunDB
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, ladderRunInsertQuery, ladderRunInsertArgs(run, ladderRunPlanJSON(run))...)
		scanned, err := scanLadderRun(row)
		if err != nil {
			return fmt.Errorf("failed to insert ladder_run id=%s: %w", run.ID, err)
		}
		if err := insertLadderTranchesTx(ctx, tx, tranches); err != nil {
			return err
		}
		result = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetInFlightLadderCommitUSDHr returns the total hourly USD commitment already
// in flight for the given config: the SUM of amount_usd_hr for ladder_tranches
// rows where config_id=$1 and status = 'scheduled'.
//
// SCHEDULED ONLY: fired/completed tranches are executed purchases already
// counted in the engine's ExistingUSDPerHour (the provider adapters fold
// payment-pending and active commitments into E). Summing them here as well
// would double-subtract them from the gap and cause under-purchasing. Only
// not-yet-fired (scheduled) tranches are genuinely "in flight" and absent
// from E, so they alone must be netted out of the gap.
//
// "scheduled" means LIVE only because the caller expires long-elapsed tranches
// (ExpireElapsedLadderTranches) before reading this sum. Without that step a
// tranche that was never fired stays scheduled forever and keeps inflating the
// in-flight figure, which zeroes the gap and silently stops planning (#1605).
//
// Returns a non-nil *float64 (zero when no scheduled tranches exist); never
// returns nil without a non-nil error, so callers can pass it directly to
// AllocationInput.InFlightUSDPerHour without an extra nil-guard.
func (s *PostgresStore) GetInFlightLadderCommitUSDHr(ctx context.Context, configID string) (*float64, error) {
	var total float64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_usd_hr), 0)
		FROM ladder_tranches
		WHERE config_id = $1
		  AND status = $2
	`, configID, string(ladder.TrancheStatusScheduled)).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("GetInFlightLadderCommitUSDHr config_id=%s: %w", configID, err)
	}
	return &total, nil
}

// ExpireElapsedLadderTranches transitions the config's scheduled tranches whose
// scheduled_date is strictly before cutoff to status='cancelled', returning the
// number of rows moved.
//
// A tranche only leaves 'scheduled' when it fires. One that is still scheduled
// well past its own fire date never fired and never will, so it is not in
// flight: leaving it scheduled keeps it in the in-flight sum that
// GetInFlightLadderCommitUSDHr feeds to the netting, which zeroes the gap and
// stops the ladder from planning anything ever again (#1605).
//
// 'cancelled' rather than 'failed': TrancheStatus.allowsFiredAt reserves
// 'failed' for tranches that fired and then failed, while 'cancelled' is the
// terminal status for a tranche that never fired. Rows are kept (not deleted)
// so the abandoned ramp stays auditable.
//
// The cutoff is the caller's; it must leave enough slack for a tranche that is
// only just past due to still fire, so this can never race a firing tranche
// into a duplicate purchase.
func (s *PostgresStore) ExpireElapsedLadderTranches(ctx context.Context, configID string, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE ladder_tranches
		SET status = $3
		WHERE config_id = $1
		  AND status = $2
		  AND scheduled_date < $4
	`, configID, string(ladder.TrancheStatusScheduled), string(ladder.TrancheStatusCancelled), cutoff)
	if err != nil {
		return 0, fmt.Errorf("ExpireElapsedLadderTranches config_id=%s cutoff=%s: %w", configID, cutoff.Format(time.RFC3339), err)
	}
	return tag.RowsAffected(), nil
}

// LatestLadderRunStartedAt returns the maximum started_at for the given
// config_id, or nil when no run has been recorded yet. Drives the per-cadence
// self-gate in handleLadderRun (Q6).
func (s *PostgresStore) LatestLadderRunStartedAt(ctx context.Context, configID string) (*time.Time, error) {
	query := `SELECT MAX(started_at) FROM ladder_runs WHERE config_id = $1`
	var ts *time.Time
	err := s.db.QueryRow(ctx, query, configID).Scan(&ts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest ladder_run started_at for config_id=%s: %w", configID, err)
	}
	return ts, nil
}

// TransitionLadderRunStatus atomically transitions a ladder_runs row from
// one of fromStatuses to toStatus via a CAS UPDATE. Returns the updated row
// on success, or (nil, nil) when zero rows are affected (race lost or
// unexpected current status). A hard DB error is returned as a non-nil error.
func (s *PostgresStore) TransitionLadderRunStatus(ctx context.Context, id string, fromStatuses []ladder.RunStatus, toStatus ladder.RunStatus) (*LadderRunDB, error) {
	// Convert the typed enum slice to the plain []string that pgx encodes for
	// the ANY($3) text[] comparison. Keeping the public signature typed while
	// converting here confines the stringly-typed shape to the DB boundary.
	from := make([]string, len(fromStatuses))
	for i, st := range fromStatuses {
		from[i] = string(st)
	}
	query := `
		UPDATE ladder_runs
		SET status = $2, updated_at = NOW()
		WHERE id = $1 AND status = ANY($3)
		RETURNING id, config_id, started_at, completed_at, status, mode, cadence,
		          baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
		          plan, total_hourly_commit, total_upfront_cost, estimated_savings,
		          approval_token_hash, approval_token_expires_at,
		          approved_by, cancelled_by, fire_at,
		          created_at, updated_at
	`
	row := s.db.QueryRow(ctx, query, id, string(toStatus), from)
	result, err := scanLadderRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // CAS race lost
		}
		return nil, fmt.Errorf("failed to transition ladder_run id=%s to status=%s: %w", id, toStatus, err)
	}
	return &result, nil
}

// scanLadderRun scans a single ladder_runs row from a pgx.Row or pgx.Rows.
// Both implement the scannable interface (store_postgres_registrations.go).
func scanLadderRun(row scannable) (LadderRunDB, error) {
	var r LadderRunDB
	var planJSON []byte

	err := row.Scan(
		&r.ID,
		&r.ConfigID,
		&r.StartedAt,
		&r.CompletedAt,
		&r.Status,
		&r.Mode,
		&r.Cadence,
		&r.BaselineUSDHr,
		&r.TargetUSDHr,
		&r.ExistingUSDHr,
		&r.GapUSDHr,
		&planJSON,
		&r.TotalHourlyCommit,
		&r.TotalUpfrontCost,
		&r.EstimatedSavings,
		&r.ApprovalTokenHash,
		&r.ApprovalTokenExpiresAt,
		&r.ApprovedBy,
		&r.CancelledBy,
		&r.FireAt,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	if err != nil {
		return LadderRunDB{}, err
	}
	if planJSON != nil {
		r.Plan = json.RawMessage(planJSON)
	}
	return r, nil
}

// marshalRampSchedule ensures the ramp_schedule value stored in the DB is
// valid JSON. Nil/empty input is rejected; already-valid JSON is passed
// through unchanged. Non-JSON input is rejected with a descriptive error.
func marshalRampSchedule(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ramp_schedule must not be empty")
	}
	// Validate it is parseable JSON before writing.
	if !json.Valid(raw) {
		return nil, fmt.Errorf("ramp_schedule is not valid JSON")
	}
	return raw, nil
}
