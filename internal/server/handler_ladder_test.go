package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/testutil"
	pkgcommon "github.com/LeanerCloud/CUDly/pkg/common"
	pkgladder "github.com/LeanerCloud/CUDly/pkg/ladder"
)

// ============================================================
// Fake LadderCapability
// ============================================================

// fakeLadderCapability is an in-process fake implementing pkgladder.LadderCapability.
// PurchaseLayer and ReshapeBuffer call t.Fatal so that any test which exercises them
// fails immediately, asserting the ABSENCE of those calls in the plan-only phase.
type fakeLadderCapability struct {
	baseline        pkgladder.UsageBaseline
	baselineErr     error
	layerStates     map[pkgladder.LayerType]pkgladder.LayerState
	layerStatesErr  error
	supportedLayers []pkgladder.LayerSpec
	t               *testing.T
}

func (f *fakeLadderCapability) Provider() pkgcommon.ProviderType {
	return pkgcommon.ProviderAWS
}

func (f *fakeLadderCapability) SupportedLayers() []pkgladder.LayerSpec {
	if f.supportedLayers != nil {
		return f.supportedLayers
	}
	// Default: single RI layer as base.
	return []pkgladder.LayerSpec{
		{
			Type:  pkgladder.LayerConvertibleRI,
			Roles: []pkgladder.LayerRole{pkgladder.RoleBase},
		},
		{
			Type:  pkgladder.LayerEC2InstanceSP,
			Roles: []pkgladder.LayerRole{pkgladder.RoleFlex},
		},
	}
}

func (f *fakeLadderCapability) ListCommitments(_ context.Context, _ pkgladder.Scope) ([]pkgcommon.Commitment, error) {
	return nil, nil
}

func (f *fakeLadderCapability) GetLayerStates(_ context.Context, _ pkgladder.Scope) (map[pkgladder.LayerType]pkgladder.LayerState, error) {
	if f.layerStatesErr != nil {
		return nil, f.layerStatesErr
	}
	if f.layerStates != nil {
		return f.layerStates, nil
	}
	// Default: both layers have zero existing commitment (explicit zero, not nil).
	zero := 0.0
	return map[pkgladder.LayerType]pkgladder.LayerState{
		pkgladder.LayerConvertibleRI: {
			Layer:              pkgladder.LayerConvertibleRI,
			ExistingUSDPerHour: &zero,
			ExpiringUSDPerHour: &zero,
		},
		pkgladder.LayerEC2InstanceSP: {
			Layer:              pkgladder.LayerEC2InstanceSP,
			ExistingUSDPerHour: &zero,
			ExpiringUSDPerHour: &zero,
		},
	}, nil
}

func (f *fakeLadderCapability) GetUsageBaseline(_ context.Context, _ pkgladder.Scope, _ int, _ float64) (pkgladder.UsageBaseline, error) {
	return f.baseline, f.baselineErr
}

// PurchaseLayer must NOT be called in the plan-only phase.
func (f *fakeLadderCapability) PurchaseLayer(_ context.Context, _ pkgladder.LayerType, _ pkgcommon.Recommendation, _ pkgcommon.PurchaseOptions) (pkgcommon.PurchaseResult, error) {
	f.t.Fatal("PurchaseLayer called in plan-only PR-2: purchases must never fire during planning")
	return pkgcommon.PurchaseResult{}, nil
}

// ReshapeBuffer must NOT be called in the plan-only phase.
func (f *fakeLadderCapability) ReshapeBuffer(_ context.Context, _ pkgladder.Scope, _ pkgladder.BufferReshapeConfig) (pkgladder.ReshapeSummary, error) {
	f.t.Fatal("ReshapeBuffer called in plan-only PR-2: reshapes must never execute during planning")
	return pkgladder.ReshapeSummary{}, nil
}

// ============================================================
// Inline ladder-aware mock store
// ============================================================

// ladderTestStore extends mockConfigStoreForHealth with capturable ladder run methods.
// Capture fields (saveLadderRunFn, saveLadderTranchesFn) let individual tests
// assert on what was persisted without importing a testify mock.
type ladderTestStore struct {
	mockConfigStoreForHealth
	globalCfg        *config.GlobalConfig
	globalCfgErr     error
	ladderConfigs    []config.LadderConfigDB
	ladderConfigsErr error
	cloudAcct        *config.CloudAccount
	cloudAcctErr     error
	latestRunAt      *time.Time
	latestRunAtErr   error

	// Per-cloud-account-id overrides, keyed by LadderConfigDB.CloudAccountID.
	// When a config's CloudAccountID is present here, the mapped account/error
	// takes precedence over the single cloudAcct/cloudAcctErr fields. This lets
	// a multi-config test give each config a distinct account outcome.
	cloudAcctByID    map[string]*config.CloudAccount
	cloudAcctErrByID map[string]error

	// L5: configurable in-flight hourly commitment returned by
	// GetInFlightLadderCommitUSDHr. Defaults to zero (nil -> 0.0 pointer).
	inFlightUSDHr *float64
	inFlightErr   error

	// Capture fields: non-nil if the call was made. savedRuns accumulates every
	// SaveLadderRun call so multi-config tests can assert on each persisted run;
	// savedRun mirrors the most recent one for single-config convenience.
	savedRun              *config.LadderRunDB
	savedRuns             []*config.LadderRunDB
	savedTranches         []config.LadderTrancheDB
	saveLadderRunErr      error
	saveLadderTranchesErr error
}

func (s *ladderTestStore) GetGlobalConfig(_ context.Context) (*config.GlobalConfig, error) {
	if s.globalCfgErr != nil {
		return nil, s.globalCfgErr
	}
	if s.globalCfg != nil {
		return s.globalCfg, nil
	}
	return &config.GlobalConfig{}, nil
}

func (s *ladderTestStore) GetLadderConfigs(_ context.Context) ([]config.LadderConfigDB, error) {
	return s.ladderConfigs, s.ladderConfigsErr
}

func (s *ladderTestStore) GetCloudAccount(_ context.Context, id string) (*config.CloudAccount, error) {
	if s.cloudAcctErrByID != nil {
		if err, ok := s.cloudAcctErrByID[id]; ok {
			return nil, err
		}
	}
	if s.cloudAcctByID != nil {
		if acct, ok := s.cloudAcctByID[id]; ok {
			return acct, nil
		}
	}
	return s.cloudAcct, s.cloudAcctErr
}

func (s *ladderTestStore) LatestLadderRunStartedAt(_ context.Context, _ string) (*time.Time, error) {
	return s.latestRunAt, s.latestRunAtErr
}

func (s *ladderTestStore) SaveLadderRun(_ context.Context, run *config.LadderRunDB) (*config.LadderRunDB, error) {
	if s.saveLadderRunErr != nil {
		return nil, s.saveLadderRunErr
	}
	s.savedRun = run
	s.savedRuns = append(s.savedRuns, run)
	return run, nil
}

func (s *ladderTestStore) SaveLadderTranches(_ context.Context, tranches []config.LadderTrancheDB) error {
	if s.saveLadderTranchesErr != nil {
		return s.saveLadderTranchesErr
	}
	s.savedTranches = tranches
	return nil
}

// SaveLadderRunWithTranches models the L5 append-only persist: it captures the
// run and its new tranches and leaves existing tranches untouched (there is no
// supersede path anymore).
func (s *ladderTestStore) SaveLadderRunWithTranches(_ context.Context, run *config.LadderRunDB, tranches []config.LadderTrancheDB) (*config.LadderRunDB, error) {
	if s.saveLadderRunErr != nil {
		return nil, s.saveLadderRunErr
	}
	if s.saveLadderTranchesErr != nil {
		return nil, s.saveLadderTranchesErr
	}
	s.savedRun = run
	s.savedRuns = append(s.savedRuns, run)
	s.savedTranches = tranches
	return run, nil
}

// GetInFlightLadderCommitUSDHr returns the configurable in-flight value.
// When inFlightUSDHr is nil, returns a pointer to 0.0 (no in-flight default).
func (s *ladderTestStore) GetInFlightLadderCommitUSDHr(_ context.Context, _ string) (*float64, error) {
	if s.inFlightErr != nil {
		return nil, s.inFlightErr
	}
	if s.inFlightUSDHr != nil {
		return s.inFlightUSDHr, nil
	}
	zero := 0.0
	return &zero, nil
}

// ============================================================
// Shared test helpers
// ============================================================

// validTestDBConfig returns a LadderConfigDB with all required fields set to
// valid values. Tests override specific fields as needed.
func validTestDBConfig(id string) config.LadderConfigDB {
	rampJSON, _ := json.Marshal(pkgladder.RampSchedule{
		Steps: []pkgladder.RampStep{{AfterDays: 0, Fraction: 1.0}},
	})
	return config.LadderConfigDB{
		ID:                         id,
		CloudAccountID:             "cloud-acct-uuid",
		Provider:                   "aws",
		Enabled:                    true,
		Mode:                       "email_approval",
		Cadence:                    "daily",
		TargetCoverage:             80.0,
		BufferFraction:             0.0, // no buffer role needed for base/flex-only fake
		BaselinePercentile:         5.0,
		LookbackDays:               30,
		MaxActionsPerRun:           5,
		BufferUtilizationThreshold: 50.0,
		RampSchedule:               rampJSON,
	}
}

// validTestGlobalCfg returns a GlobalConfig that enables laddering and has a
// known default term and payment option.
func validTestGlobalCfg() *config.GlobalConfig {
	return &config.GlobalConfig{
		LadderingEnabled: true,
		DefaultTerm:      1,
		DefaultPayment:   "no-upfront",
	}
}

// validTestCloudAccount returns a CloudAccount whose ExternalID matches the
// given AWS account ID (simulating resolveAccountID).
func validTestCloudAccount(externalID string) *config.CloudAccount {
	return &config.CloudAccount{
		ID:         "cloud-acct-uuid",
		Provider:   "aws",
		ExternalID: externalID,
		Enabled:    true,
	}
}

// nonZeroFloat64 is a helper to create a *float64 from a literal.
func nonZeroFloat64(f float64) *float64 { return &f }

// testBaselineLowWaterUSDHr is the LowWaterUSDPerHour every testBaseline
// fixture uses; all callers share the same value so the helper takes no
// parameter (unparam).
const testBaselineLowWaterUSDHr = 10.0

// testBaseline returns a UsageBaseline with a non-nil LowWaterUSDPerHour.
func testBaseline() pkgladder.UsageBaseline {
	return testBaselineLowWater(testBaselineLowWaterUSDHr)
}

// testBaselineLowWater returns a UsageBaseline with the given LowWaterUSDPerHour.
// The L5 drift-up tests need distinct low-water values across runs to exercise
// the netted gap re-opening, so the low-water is parameterized here.
func testBaselineLowWater(lowWater float64) pkgladder.UsageBaseline {
	return pkgladder.UsageBaseline{
		LowWaterUSDPerHour: nonZeroFloat64(lowWater),
		StableUSDPerHour:   nonZeroFloat64(lowWater * 0.9),
		LookbackDays:       30,
		Percentile:         5.0,
	}
}

// ============================================================
// ParseScheduledEvent registration
// ============================================================

func TestHandleLadderRun_ParseScheduledEvent(t *testing.T) {
	taskType, _, err := ParseScheduledEvent([]byte(`{"action":"ladder_run"}`))
	require.NoError(t, err)
	assert.Equal(t, TaskLadderRun, taskType)
}

// ============================================================
// handleLadderRun: global kill-switch
// ============================================================

func TestHandleLadderRun_GlobalDisabled(t *testing.T) {
	ctx := testutil.TestContext(t)
	store := &ladderTestStore{
		globalCfg: &config.GlobalConfig{LadderingEnabled: false},
	}
	app := &Application{Config: store}

	result, err := app.handleLadderRun(ctx)

	require.NoError(t, err)
	require.NotNil(t, result)
	// Nothing should be planned or skipped when the global flag is off.
	assert.Equal(t, 0, result.Planned)
	assert.Equal(t, 0, result.SkippedCadence)
	assert.Equal(t, 0, result.SkippedDisabled)
	// SaveLadderRun must not be called.
	assert.Nil(t, store.savedRun, "SaveLadderRun must not be called when laddering is globally disabled")
}

func TestHandleLadderRun_GlobalConfigError(t *testing.T) {
	ctx := testutil.TestContext(t)
	store := &ladderTestStore{
		globalCfgErr: errors.New("db error"),
	}
	app := &Application{Config: store}

	_, err := app.handleLadderRun(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}

// B1: a strict caller-account resolution failure (e.g. transient STS error)
// must fail the WHOLE task loud. It must NOT fall through and skip every config
// as multi-account (which would return success and let EventBridge see a
// false no-op on a money path). Nothing may be persisted or marked skipped.
func TestHandleLadderRun_AccountResolutionFailure(t *testing.T) {
	ctx := testutil.TestContext(t)
	dbCfg := validTestDBConfig("cfg-1")
	store := &ladderTestStore{
		globalCfg:     validTestGlobalCfg(),
		ladderConfigs: []config.LadderConfigDB{dbCfg},
	}
	app := &Application{
		Config: store,
		LadderAccountResolver: func(_ context.Context) (string, string, error) {
			return "", "", errors.New("STS GetCallerIdentity failed")
		},
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			t.Fatal("factory must not be called when account resolution fails")
			return nil, nil
		},
	}

	result, err := app.handleLadderRun(ctx)

	require.Error(t, err, "an unresolved caller account must fail the task loud")
	assert.Contains(t, err.Error(), "resolve caller AWS account")
	assert.Nil(t, result, "no result struct on a hard failure (not a false success)")
	assert.Nil(t, store.savedRun, "nothing may be persisted when the account is unresolved")
	assert.Empty(t, store.savedRuns)
}

// resolveLadderIdentity must fail loud on an empty region BEFORE calling STS:
// an empty region would make NewFromAWSConfig reject every config later, turning
// the task into a silent all-Errored no-op that still reports success. The
// region check short-circuits, so this exercises the new guard with no
// credentials/STS involved.
func TestResolveLadderIdentity_EmptyRegion_FailsLoud(t *testing.T) {
	ctx := testutil.TestContext(t)

	accountID, region, err := resolveLadderIdentity(ctx, aws.Config{Region: ""})

	require.Error(t, err, "an empty region must fail loud, not fall through to STS")
	assert.Contains(t, err.Error(), "region")
	assert.Empty(t, accountID, "no account ID may be returned on the empty-region failure")
	assert.Empty(t, region, "no region may be returned on the empty-region failure")
}

// B1 (region): an empty-region resolution failure must abort the whole
// handleLadderRun task loud, mirroring TestHandleLadderRun_AccountResolutionFailure.
// Nothing may be persisted and no config may be marked skipped.
func TestHandleLadderRun_EmptyRegionResolution_FailsLoud(t *testing.T) {
	ctx := testutil.TestContext(t)
	dbCfg := validTestDBConfig("cfg-1")
	store := &ladderTestStore{
		globalCfg:     validTestGlobalCfg(),
		ladderConfigs: []config.LadderConfigDB{dbCfg},
	}
	app := &Application{
		Config: store,
		// The default resolver would return this exact error for an empty region;
		// the stub keeps the test off STS while asserting the fail-loud contract.
		LadderAccountResolver: func(c context.Context) (string, string, error) {
			return resolveLadderIdentity(c, aws.Config{Region: ""})
		},
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			t.Fatal("factory must not be called when the region is unresolved")
			return nil, nil
		},
	}

	result, err := app.handleLadderRun(ctx)

	require.Error(t, err, "an empty region must fail the whole task loud")
	assert.Contains(t, err.Error(), "region")
	assert.Nil(t, result, "no result struct on a hard failure (not a false success)")
	assert.Nil(t, store.savedRun, "nothing may be persisted when the region is unresolved")
	assert.Empty(t, store.savedRuns)
}

// ============================================================
// handleLadderRun: per-config disabled/multi-account skipping
// ============================================================

func TestHandleLadderRun_AllConfigsDisabled(t *testing.T) {
	ctx := testutil.TestContext(t)
	dbCfg := validTestDBConfig("cfg-1")
	dbCfg.Enabled = false

	store := &ladderTestStore{
		globalCfg:     validTestGlobalCfg(),
		ladderConfigs: []config.LadderConfigDB{dbCfg},
	}
	app := &Application{
		Config: store,
		// Inject a stub resolver so the strict account gate does not hit STS.
		LadderAccountResolver: func(_ context.Context) (string, string, error) {
			return "123456789012", "us-east-1", nil
		},
		// No LadderCapabilityFactory: must not be reached.
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			t.Fatal("factory called on a disabled config")
			return nil, nil
		},
	}

	result, err := app.handleLadderRun(ctx)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Planned)
	assert.Equal(t, 1, result.SkippedDisabled)
	assert.Nil(t, store.savedRun)
}

// TestHandleLadderRun_MultiAccountSkip_CountedAndIsolated verifies that:
// (a) an enabled ladder config whose cloud account ExternalID does NOT match the
//
//	resolved caller account is counted as SkippedMultiAccount (not Errored),
//
// (b) nothing is persisted for that config, and
// (c) a second healthy config present in the same run is not affected -- it still
//
//	plans successfully (per-config isolation).
func TestHandleLadderRun_MultiAccountSkip_CountedAndIsolated(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const ownAccount = "123456789012"
	const foreignAccount = "999999999999"

	// cfgForeign: enabled, but its cloud account ExternalID is a different AWS account.
	cfgForeign := validTestDBConfig("cfg-foreign")
	cfgForeign.CloudAccountID = "acct-foreign"

	// cfgHealthy: enabled, cloud account matches the caller account.
	cfgHealthy := validTestDBConfig("cfg-healthy-ma")
	cfgHealthy.CloudAccountID = "acct-own"

	store := &ladderTestStore{
		cloudAcctByID: map[string]*config.CloudAccount{
			"acct-foreign": {
				ID:         "acct-foreign",
				Provider:   "aws",
				ExternalID: foreignAccount,
				Enabled:    true,
			},
			"acct-own": validTestCloudAccount(ownAccount),
		},
	}
	app := &Application{
		Config: store,
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			return &fakeLadderCapability{t: t, baseline: testBaseline()}, nil
		},
	}

	// Put the foreign config first to prove isolation is order-independent.
	configs := []config.LadderConfigDB{cfgForeign, cfgHealthy}
	result := app.runLadderConfigs(ctx, configs, ownAccount, "us-east-1", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now, false)

	require.NotNil(t, result)
	// (a) The foreign config must be counted as SkippedMultiAccount.
	assert.Equal(t, 1, result.SkippedMultiAccount, "foreign-account config must be counted SkippedMultiAccount")
	assert.Equal(t, 0, result.Errored, "a multi-account skip is not an error")
	// (b) Nothing persisted for the foreign config; only the healthy config run.
	require.Len(t, store.savedRuns, 1, "only the healthy config may persist a run")
	require.NotNil(t, store.savedRuns[0].ConfigID)
	assert.Equal(t, "cfg-healthy-ma", *store.savedRuns[0].ConfigID, "persisted run must be from the healthy config, not the skipped one")
	// (c) The healthy config still processes successfully despite the skip.
	assert.Equal(t, 1, result.Planned, "the healthy config must still be planned")
	assert.Equal(t, 0, result.SkippedDisabled)
	assert.Equal(t, 0, result.SkippedCadence)
}

// ============================================================
// executeLadderRun: healthy single-config run
// ============================================================

func TestExecuteLadderRun_HealthyRun(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-healthy")
	// Two-step ramp with a DELAYED step: an AfterDays==0 step becomes a BuyNow
	// action, only AfterDays>0 steps become persisted tranches. This ensures
	// SaveLadderTranches receives a non-empty batch so the assertion below
	// exercises real tranche persistence.
	dbCfg.RampSchedule, _ = json.Marshal(pkgladder.RampSchedule{
		Steps: []pkgladder.RampStep{
			{AfterDays: 0, Fraction: 0.5},
			{AfterDays: 7, Fraction: 0.5},
		},
	})

	store := &ladderTestStore{}
	app := &Application{Config: store}

	cap := &fakeLadderCapability{
		t:        t,
		baseline: testBaseline(),
	}

	err := app.executeLadderRun(ctx, &dbCfg, cap, "123456789012", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.NoError(t, err)

	// SaveLadderRun must have been called exactly once.
	require.NotNil(t, store.savedRun, "SaveLadderRun must be called for a healthy run")
	run := store.savedRun
	assert.Equal(t, pkgladder.RunStatusPlanned, run.Status, "run status must be planned (plan-only milestone)")
	assert.Equal(t, "cfg-healthy", *run.ConfigID)
	assert.Equal(t, "email_approval", *run.Mode)
	assert.Equal(t, "daily", *run.Cadence)

	// Monetary snapshot: baseline-derived fields must be non-nil (we have a valid baseline).
	assert.NotNil(t, run.BaselineUSDHr, "baseline_usd_hr must be non-nil when baseline is available")
	require.NotNil(t, run.TargetUSDHr, "target_usd_hr must be non-nil when baseline is available")
	// target = low-water (10) * target_coverage (80%) = 8.0, derived consistently
	// with TargetCoveragePct rather than the raw low-water.
	assert.InDelta(t, 8.0, *run.TargetUSDHr, 1e-9, "target must be low-water * coverage%%, not raw low-water")
	require.NotNil(t, run.GapUSDHr, "gap_usd_hr must be non-nil when baseline is available")
	// gap (Σ planned allocation gaps) must equal total_hourly_commit exactly.
	assert.InDelta(t, run.TotalHourlyCommit, *run.GapUSDHr, 1e-9, "gap must equal total_hourly_commit (both Σ allocation gaps)")
	assert.Greater(t, run.TotalHourlyCommit, 0.0, "a healthy run below target must plan a positive commitment")

	// Plan JSON must be valid non-empty JSON.
	require.True(t, len(run.Plan) > 2, "plan JSON must be non-empty")
	var planDTO ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(run.Plan, &planDTO), "plan JSON must unmarshal cleanly")
	assert.Equal(t, "aws", string(planDTO.Scope.Provider))
	assert.Equal(t, "123456789012", planDTO.Scope.AccountID)
	assert.False(t, planDTO.GeneratedAt.IsZero(), "plan generated_at must not be zero")

	// Plan must contain at least one purchase action stamped with term/payment.
	var purchaseActions int
	for _, a := range planDTO.Actions {
		if a.Action == string(pkgladder.ActionPurchase) {
			purchaseActions++
			assert.Equal(t, string(pkgladder.Term1Year), a.Term, "purchase action must carry the term")
			assert.Equal(t, string(pkgladder.PaymentNoUpfront), a.PaymentOption, "purchase action must carry the payment option")
			require.NotNil(t, a.AmountUSDHr, "purchase action must carry a non-nil amount")
		}
	}
	assert.Positive(t, purchaseActions, "a healthy below-target run must produce purchase actions")

	// SaveLadderTranches must have been called with the ramp tranches produced
	// from the allocations (non-empty for a below-target run).
	require.NotNil(t, store.savedTranches, "SaveLadderTranches must be called for a healthy run")
	assert.NotEmpty(t, store.savedTranches, "healthy below-target run must persist tranche audit rows")
	for _, tr := range store.savedTranches {
		assert.Equal(t, pkgladder.TrancheStatusScheduled, tr.Status, "PR-2 tranches are scheduled-only (no firing)")
		assert.Greater(t, tr.AmountUSDHr, 0.0, "each tranche must carry a positive amount")
	}
	// No panic means PurchaseLayer and ReshapeBuffer were not called.
}

// ============================================================
// executeLadderRun: GetUsageBaseline ERROR -> no run persisted
// ============================================================

// TestExecuteLadderRun_BaselineError_NoRunPersisted covers the case where
// GetUsageBaseline itself returns an ERROR (e.g. the CE adapter is not wired).
// This is distinct from a nil-baseline-with-no-error (which the engine treats
// as a Hold, see TestExecuteLadderRun_NilBaseline_HoldPlan). On a baseline
// error the run must fail loud and persist NOTHING.
func TestExecuteLadderRun_BaselineError_NoRunPersisted(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-baseline-err")

	store := &ladderTestStore{}
	app := &Application{Config: store}

	cap := &fakeLadderCapability{
		t:           t,
		baselineErr: errors.New("CE on-demand series not yet wired"),
	}

	err := app.executeLadderRun(ctx, &dbCfg, cap, "123456789012", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GetUsageBaseline")
	// Fail-loud: nothing may be persisted when the baseline lookup errors.
	assert.Nil(t, store.savedRun, "no ladder_runs row may be persisted on a baseline error")
	assert.Nil(t, store.savedTranches, "no tranches may be persisted on a baseline error")
}

// TestExecuteLadderRun_NilBaseline_HoldPlan covers GetUsageBaseline returning a
// baseline with a nil LowWaterUSDPerHour and NO error. The engine treats this
// as a Hold (explainable no-op), so executeLadderRun must persist a
// status=planned run with NULL target/gap monetary cols and no purchase
// actions -- NOT an error.
func TestExecuteLadderRun_NilBaseline_HoldPlan(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-nilbaseline")

	store := &ladderTestStore{}
	app := &Application{Config: store}

	cap := &fakeLadderCapability{
		t: t,
		// Baseline present but LowWaterUSDPerHour nil -> engine holds.
		baseline: pkgladder.UsageBaseline{
			LowWaterUSDPerHour: nil,
			LookbackDays:       30,
			Percentile:         5.0,
		},
	}

	err := app.executeLadderRun(ctx, &dbCfg, cap, "123456789012", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.NoError(t, err, "nil baseline with no error must be a Hold, not an error")

	require.NotNil(t, store.savedRun, "a Hold run must still be persisted")
	run := store.savedRun
	assert.Equal(t, pkgladder.RunStatusPlanned, run.Status, "Hold run status must be planned")
	// Monetary snapshot: baseline/target/gap must be NULL (nil), never 0-coerced.
	assert.Nil(t, run.BaselineUSDHr, "baseline_usd_hr must be nil when low-water is nil")
	assert.Nil(t, run.TargetUSDHr, "target_usd_hr must be nil when baseline is unavailable")
	assert.Nil(t, run.GapUSDHr, "gap_usd_hr must be nil when baseline is unavailable")
	assert.Equal(t, 0.0, run.TotalHourlyCommit, "a Hold run plans zero new commitment")

	// Plan must contain no purchase actions (Hold-only).
	var planDTO ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(run.Plan, &planDTO))
	for _, a := range planDTO.Actions {
		assert.NotEqual(t, string(pkgladder.ActionPurchase), a.Action, "a Hold-only plan must contain no purchase actions")
	}
	// No tranches produced from an empty allocation set.
	assert.Empty(t, store.savedTranches, "a Hold-only run produces no tranches")
}

// TestExecuteLadderRun_ZeroGap_HoldPlan covers a run where existing commitment
// already meets or exceeds the target, so the gap is <= the minimum and the
// engine returns a Hold. The run must persist as status=planned with no
// purchase actions.
func TestExecuteLadderRun_ZeroGap_HoldPlan(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-zerogap")

	store := &ladderTestStore{}
	app := &Application{Config: store}

	// Existing base commitment (10) already meets the low-water (10); with
	// target coverage 80% the target (8) is below existing, so gap <= 0 -> Hold.
	tenUSD := 10.0
	zero := 0.0
	cap := &fakeLadderCapability{
		t:        t,
		baseline: testBaseline(),
		layerStates: map[pkgladder.LayerType]pkgladder.LayerState{
			pkgladder.LayerConvertibleRI: {
				Layer:              pkgladder.LayerConvertibleRI,
				ExistingUSDPerHour: &tenUSD,
				ExpiringUSDPerHour: &zero,
			},
			pkgladder.LayerEC2InstanceSP: {
				Layer:              pkgladder.LayerEC2InstanceSP,
				ExistingUSDPerHour: &zero,
				ExpiringUSDPerHour: &zero,
			},
		},
	}

	err := app.executeLadderRun(ctx, &dbCfg, cap, "123456789012", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.NoError(t, err)

	require.NotNil(t, store.savedRun, "a zero-gap Hold run must still be persisted")
	run := store.savedRun
	assert.Equal(t, pkgladder.RunStatusPlanned, run.Status)
	assert.Equal(t, 0.0, run.TotalHourlyCommit, "zero-gap run plans zero new commitment")

	var planDTO ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(run.Plan, &planDTO))
	for _, a := range planDTO.Actions {
		assert.NotEqual(t, string(pkgladder.ActionPurchase), a.Action, "zero-gap plan must contain no purchase actions")
	}
	assert.Empty(t, store.savedTranches, "a zero-gap Hold run produces no tranches")
}

// TestExecuteLadderRun_MaxActionsExceeded_TruncatesAndPersists covers the
// engine's truncation contract at MaxActionsPerRun: when the split produces
// more allocations than the cap, Allocate no longer errors. It keeps the
// largest-gap allocations, converts each dropped one into an ActionHold
// ("deferred to a later run"), and executeLadderRun persists the truncated
// plan normally. The over-cap config keeps running every cadence instead of
// stalling forever.
func TestExecuteLadderRun_MaxActionsExceeded_TruncatesAndPersists(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-maxactions")
	dbCfg.MaxActionsPerRun = 1 // base+flex split yields 2 allocations > 1
	// A future-only ramp step so the kept allocation lands as a scheduled
	// tranche row (an AfterDays==0 step becomes a BuyNow action instead,
	// which produces no ladder_tranches rows).
	rampJSON, err := json.Marshal(pkgladder.RampSchedule{
		Steps: []pkgladder.RampStep{{AfterDays: 30, Fraction: 1.0}},
	})
	require.NoError(t, err)
	dbCfg.RampSchedule = rampJSON

	store := &ladderTestStore{}
	app := &Application{Config: store}

	// Stable (10) is far below the target (80% of low-water 100 = 80), so the
	// base layer absorbs up to stable (10) and the remainder (70) spills to
	// flex, producing TWO allocations. With MaxActionsPerRun=1 the engine
	// keeps the larger flex allocation and holds the smaller base one.
	cap := &fakeLadderCapability{
		t: t,
		baseline: pkgladder.UsageBaseline{
			LowWaterUSDPerHour: nonZeroFloat64(100.0),
			StableUSDPerHour:   nonZeroFloat64(10.0),
			LookbackDays:       30,
			Percentile:         5.0,
		},
	}

	err = app.executeLadderRun(ctx, &dbCfg, cap, "123456789012", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.NoError(t, err, "over-cap plans must truncate, not error (permanent-stall regression)")

	require.NotNil(t, store.savedRun, "the truncated run must be persisted")
	run := store.savedRun
	assert.Equal(t, pkgladder.RunStatusPlanned, run.Status)

	var planDTO ladderPlanJSONDTO
	require.NoError(t, json.Unmarshal(run.Plan, &planDTO))

	// Exactly ONE purchase survives: the larger-gap flex allocation ($70/hr on
	// EC2InstanceSP, the fake's RoleFlex layer); the $10/hr base allocation is
	// dropped. At least one hold must record the truncation for audit.
	var purchases []ladderPlanJSONAction
	var truncHolds []ladderPlanJSONAction
	for _, a := range planDTO.Actions {
		switch a.Action {
		case string(pkgladder.ActionPurchase):
			purchases = append(purchases, a)
		case string(pkgladder.ActionHold):
			if strings.Contains(a.Rationale, "max_actions_per_run=1") {
				truncHolds = append(truncHolds, a)
			}
		}
	}
	require.Len(t, purchases, 1, "exactly MaxActionsPerRun purchase actions must survive")
	assert.Equal(t, string(pkgladder.LayerEC2InstanceSP), purchases[0].Layer,
		"the largest-gap (flex, $70/hr) allocation must be the survivor")
	require.NotNil(t, purchases[0].AmountUSDHr)
	assert.InDelta(t, 70.0, *purchases[0].AmountUSDHr, 1e-9)
	require.NotEmpty(t, truncHolds, "each dropped allocation must leave a max_actions_per_run hold")
	assert.Contains(t, truncHolds[0].Rationale, "deferred to a later run")

	// TotalHourlyCommit counts only the KEPT allocation ($70/hr), not the full
	// pre-truncation gap ($80/hr).
	assert.InDelta(t, 70.0, run.TotalHourlyCommit, 1e-9,
		"total_hourly_commit must reflect the truncated plan only")

	// Tranches exist for the kept allocation.
	assert.NotEmpty(t, store.savedTranches, "the kept allocation must produce tranches")
}

// ============================================================
// cadence gate
// ============================================================

func TestLadderWithinCadenceWindow_Daily_TooRecent(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-cadence")
	dbCfg.Cadence = "daily"

	recent := now.Add(-19 * time.Hour) // 19h ago < 20h threshold
	store := &ladderTestStore{latestRunAt: &recent}

	skip, reason, err := ladderWithinCadenceWindow(ctx, store, &dbCfg, now)

	require.NoError(t, err)
	assert.True(t, skip, "must skip a daily config run within 20h of the last run")
	assert.Contains(t, reason, "cadence=daily")
}

func TestLadderWithinCadenceWindow_Daily_Eligible(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-cadence")
	dbCfg.Cadence = "daily"

	lastRun := now.Add(-25 * time.Hour) // 25h ago > 20h threshold
	store := &ladderTestStore{latestRunAt: &lastRun}

	skip, _, err := ladderWithinCadenceWindow(ctx, store, &dbCfg, now)

	require.NoError(t, err)
	assert.False(t, skip, "must run a daily config when last run was > 20h ago")
}

func TestLadderWithinCadenceWindow_Weekly_TooRecent(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-cadence-w")
	dbCfg.Cadence = "weekly"

	recent := now.Add(-5 * 24 * time.Hour) // 5 days ago < 6d20h threshold
	store := &ladderTestStore{latestRunAt: &recent}

	skip, reason, err := ladderWithinCadenceWindow(ctx, store, &dbCfg, now)

	require.NoError(t, err)
	assert.True(t, skip, "must skip a weekly config run within 6d20h of the last run")
	assert.Contains(t, reason, "cadence=weekly")
}

func TestLadderWithinCadenceWindow_NoHistory_AlwaysEligible(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-cadence-new")
	dbCfg.Cadence = "daily"

	store := &ladderTestStore{latestRunAt: nil} // no previous run

	skip, _, err := ladderWithinCadenceWindow(ctx, store, &dbCfg, now)

	require.NoError(t, err)
	assert.False(t, skip, "must always run when there is no previous ladder run")
}

// B4: a LatestLadderRunStartedAt lookup error must fail CLOSED (propagate the
// error) so the caller can count the config Errored instead of double-running.
func TestLadderWithinCadenceWindow_DBError_FailsClosed(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dbCfg := validTestDBConfig("cfg-cadence-dberr")
	dbCfg.Cadence = "daily"

	store := &ladderTestStore{latestRunAtErr: errors.New("db unavailable")}

	skip, _, err := ladderWithinCadenceWindow(ctx, store, &dbCfg, now)

	require.Error(t, err, "a lookup error must be propagated, not swallowed")
	assert.False(t, skip, "must not signal skip on a lookup error")
	assert.Contains(t, err.Error(), "LatestLadderRunStartedAt")
}

// B4 (handler level): a config whose cadence lookup errors must be counted
// Errored with no run persisted (fail closed, per-config isolation).
func TestProcessOneLadderConfig_CadenceDBError_Errored(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const ownAccount = "123456789012"
	dbCfg := validTestDBConfig("cfg-cadence-dberr")

	store := &ladderTestStore{
		cloudAcct:      validTestCloudAccount(ownAccount),
		latestRunAtErr: errors.New("db unavailable"),
	}
	app := &Application{
		Config: store,
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			t.Fatal("factory must not be called when the cadence gate fails closed")
			return nil, nil
		},
	}

	result := app.runLadderConfigs(ctx, []config.LadderConfigDB{dbCfg}, ownAccount, "us-east-1", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now, false)

	assert.Equal(t, 1, result.Errored, "a cadence lookup error must count the config Errored")
	assert.Equal(t, 0, result.Planned)
	assert.Equal(t, 0, result.SkippedCadence, "a lookup error is not a cadence skip")
	assert.Nil(t, store.savedRun, "no run may be persisted when the cadence gate fails closed")
}

// ============================================================
// ladderConfigToEngine: parse errors fail loud
// ============================================================

func TestLadderConfigToEngine_BadMode(t *testing.T) {
	dbCfg := validTestDBConfig("cfg-bad-mode")
	dbCfg.Mode = "invalid_mode"

	_, err := ladderConfigToEngine(&dbCfg, "123456789012")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode")
}

func TestLadderConfigToEngine_BadCadence(t *testing.T) {
	dbCfg := validTestDBConfig("cfg-bad-cadence")
	dbCfg.Cadence = "monthly"

	_, err := ladderConfigToEngine(&dbCfg, "123456789012")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cadence")
}

func TestLadderConfigToEngine_BadRampJSON(t *testing.T) {
	dbCfg := validTestDBConfig("cfg-bad-ramp")
	dbCfg.RampSchedule = []byte(`not-json`)

	_, err := ladderConfigToEngine(&dbCfg, "123456789012")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ramp_schedule")
}

func TestLadderConfigToEngine_ValidConfig(t *testing.T) {
	dbCfg := validTestDBConfig("cfg-valid")
	engineCfg, err := ladderConfigToEngine(&dbCfg, "123456789012")

	require.NoError(t, err)
	assert.Equal(t, pkgcommon.ProviderAWS, engineCfg.Scope.Provider)
	assert.Equal(t, "123456789012", engineCfg.Scope.AccountID)
	assert.Equal(t, pkgladder.ModeEmailApproval, engineCfg.Mode)
	assert.Equal(t, pkgladder.CadenceDaily, engineCfg.Cadence)
	assert.Equal(t, 80.0, engineCfg.TargetCoveragePct)
	assert.Equal(t, 5.0, engineCfg.BaselinePercentile)
	assert.Equal(t, 30, engineCfg.LookbackDays)
}

// ============================================================
// ladderTermFromYears
// ============================================================

func TestLadderTermFromYears(t *testing.T) {
	tests := []struct {
		years   int
		want    pkgladder.Term
		wantErr bool
	}{
		{1, pkgladder.Term1Year, false},
		{3, pkgladder.Term3Year, false},
		{2, "", true},
		{0, "", true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run("", func(t *testing.T) {
			got, err := ladderTermFromYears(tc.years)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ============================================================
// Multi-config isolation: one error does not abort others
// ============================================================

func TestHandleLadderRun_MultiConfigIsolation(t *testing.T) {
	// runLadderConfigs is the loop that handleLadderRun runs after the AWS SDK
	// account-resolution step. Testing it directly (rather than handleLadderRun)
	// avoids awsconfig.LoadDefaultConfig / STS while still proving the isolation
	// guarantee: an error on one config must not abort the others.
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const ownAccount = "123456789012"

	// Config A: healthy -> plans a run.
	cfgHealthy := validTestDBConfig("cfg-healthy")
	cfgHealthy.CloudAccountID = "acct-ok"
	// Config B: its cloud account lookup errors -> counted Errored, must not
	// prevent config A from planning.
	cfgBroken := validTestDBConfig("cfg-broken")
	cfgBroken.CloudAccountID = "acct-bad"

	store := &ladderTestStore{
		cloudAcctByID: map[string]*config.CloudAccount{
			"acct-ok": validTestCloudAccount(ownAccount),
		},
		cloudAcctErrByID: map[string]error{
			"acct-bad": errors.New("cloud account lookup failed"),
		},
	}
	app := &Application{
		Config: store,
		LadderCapabilityFactory: func(_ context.Context, _, _ string) (pkgladder.LadderCapability, error) {
			return &fakeLadderCapability{t: t, baseline: testBaseline()}, nil
		},
	}

	// Order the broken config first to prove a leading failure does not abort
	// the healthy config that follows.
	configs := []config.LadderConfigDB{cfgBroken, cfgHealthy}
	result := app.runLadderConfigs(ctx, configs, ownAccount, "us-east-1", pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now, false)

	require.NotNil(t, result)
	assert.Equal(t, 1, result.Planned, "the healthy config must still be planned despite the broken one")
	assert.Equal(t, 1, result.Errored, "the broken config must be counted Errored")
	assert.Equal(t, 0, result.SkippedDisabled)
	assert.Equal(t, 0, result.SkippedMultiAccount)

	// Exactly one run persisted (the healthy config); it must be the healthy one.
	require.Len(t, store.savedRuns, 1, "only the healthy config persists a run")
	require.NotNil(t, store.savedRuns[0].ConfigID)
	assert.Equal(t, "cfg-healthy", *store.savedRuns[0].ConfigID)
}

// ============================================================
// buildTrancheDBRows: malformed amount fails loud (no $0 row)
// ============================================================

func TestBuildTrancheDBRows_MalformedAmount_Errors(t *testing.T) {
	runID := "run-1"
	cfgID := "cfg-1"
	tranches := []pkgladder.Tranche{
		{
			ID:               "tr-good",
			RunID:            runID,
			Layer:            pkgladder.LayerConvertibleRI,
			Term:             pkgladder.Term1Year,
			PaymentOption:    pkgladder.PaymentNoUpfront,
			Status:           pkgladder.TrancheStatusScheduled,
			AmountUSDPerHour: "3/2",
			FireAfter:        time.Now(),
		},
		{
			ID:               "tr-bad",
			RunID:            runID,
			Layer:            pkgladder.LayerConvertibleRI,
			Term:             pkgladder.Term1Year,
			PaymentOption:    pkgladder.PaymentNoUpfront,
			Status:           pkgladder.TrancheStatusScheduled,
			AmountUSDPerHour: "not-a-number",
			FireAfter:        time.Now(),
		},
	}

	rows, err := buildTrancheDBRows(tranches, runID, &cfgID)
	require.Error(t, err, "a malformed amount must fail loud, not write a $0 tranche")
	assert.Contains(t, err.Error(), "tr-bad")
	assert.Nil(t, rows, "no rows may be returned when any tranche amount is malformed")
}

// ============================================================
// ratToFloat64Ptr: nil stays nil, non-nil converts correctly
// ============================================================

func TestRatToFloat64Ptr_Nil(t *testing.T) {
	assert.Nil(t, ratToFloat64Ptr(nil), "nil *big.Rat must produce nil *float64")
}

func TestRatToFloat64Ptr_PositiveValue(t *testing.T) {
	// Build a *big.Rat from an Allocation's GapUSDPerHour (same boundary path
	// used in the handler). ratToFloat64Ptr lives in this package so no import
	// of math/big is needed in the test.
	zeroInflight := 0.0
	allocResult, err := pkgladder.Allocate(&pkgladder.AllocationInput{
		Now:                time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		InFlightUSDPerHour: &zeroInflight,
		Baseline: pkgladder.UsageBaseline{
			LowWaterUSDPerHour: nonZeroFloat64(5.0),
			StableUSDPerHour:   nonZeroFloat64(4.5),
			LookbackDays:       30,
			Percentile:         5.0,
		},
		LayerStates: map[pkgladder.LayerType]pkgladder.LayerState{
			pkgladder.LayerConvertibleRI: {
				Layer:              pkgladder.LayerConvertibleRI,
				ExistingUSDPerHour: nonZeroFloat64(0.0),
				ExpiringUSDPerHour: nonZeroFloat64(0.0),
			},
			pkgladder.LayerEC2InstanceSP: {
				Layer:              pkgladder.LayerEC2InstanceSP,
				ExistingUSDPerHour: nonZeroFloat64(0.0),
				ExpiringUSDPerHour: nonZeroFloat64(0.0),
			},
		},
		Layers: []pkgladder.LayerSpec{
			{Type: pkgladder.LayerConvertibleRI, Roles: []pkgladder.LayerRole{pkgladder.RoleBase}},
			{Type: pkgladder.LayerEC2InstanceSP, Roles: []pkgladder.LayerRole{pkgladder.RoleFlex}},
		},
		Config: pkgladder.LadderConfig{
			Scope:                         pkgladder.Scope{Provider: pkgcommon.ProviderAWS, AccountID: "123456789012"},
			Mode:                          pkgladder.ModeEmailApproval,
			Cadence:                       pkgladder.CadenceDaily,
			TargetCoveragePct:             100.0,
			BufferFraction:                0.0,
			BaselinePercentile:            5.0,
			LookbackDays:                  30,
			MaxActionsPerRun:              10,
			BufferUtilizationThresholdPct: 50.0,
			Ramp:                          pkgladder.RampSchedule{Steps: []pkgladder.RampStep{{AfterDays: 0, Fraction: 1.0}}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, allocResult.Allocations, "should have allocations when existing=0 < baseline")

	gap := allocResult.Allocations[0].GapUSDPerHour
	require.NotNil(t, gap)

	ptr := ratToFloat64Ptr(gap)
	require.NotNil(t, ptr, "non-nil big.Rat must produce non-nil *float64")
	assert.Greater(t, *ptr, 0.0, "gap must be positive")
}

// ============================================================
// L5 netting + append-only convergence (handler-level, real pipeline)
// ============================================================

// ladderStatefulStore is a faithful in-memory stand-in for the ladder tranche
// persistence that executeLadderRun drives. Unlike ladderTestStore (which
// returns a FIXED in-flight value), it tracks each persisted tranche's live
// status so the L5 netting + append-only interplay can be exercised end-to-end
// across multiple sequential runs. It mirrors PostgresStore semantics exactly:
//   - GetInFlightLadderCommitUSDHr sums SCHEDULED tranches only, per config.
//   - SaveLadderRunWithTranches APPENDS the run + its new tranches and NEVER
//     cancels existing scheduled tranches (there is no supersede path).
//
// The tests below drive the REAL executeLadderRun -> Allocate -> BuildTranches
// -> persist pipeline; only the store and capability are fakes.
type ladderStatefulStore struct {
	mockConfigStoreForHealth
	expireErr error                    // when set, ExpireElapsedLadderTranches fails
	tranches  []config.LadderTrancheDB // every persisted tranche, live status
	runs      []*config.LadderRunDB
}

// ExpireElapsedLadderTranches mirrors the production UPDATE: scheduled tranches
// of THIS config whose scheduled_date is strictly before the cutoff become
// cancelled; the rows are kept for audit. Nothing else is touched.
func (s *ladderStatefulStore) ExpireElapsedLadderTranches(_ context.Context, configID string, cutoff time.Time) (int64, error) {
	if s.expireErr != nil {
		return 0, s.expireErr
	}
	var n int64
	for i := range s.tranches {
		tr := &s.tranches[i]
		if tr.ConfigID != nil && *tr.ConfigID == configID &&
			tr.Status == pkgladder.TrancheStatusScheduled &&
			tr.ScheduledDate.Before(cutoff) {
			tr.Status = pkgladder.TrancheStatusCancelled
			n++
		}
	}
	return n, nil
}

// trancheStatuses maps each persisted tranche ID to its live status, so a test
// can assert that an abandoned tranche was retired (not deleted).
func (s *ladderStatefulStore) trancheStatuses() map[string]pkgladder.TrancheStatus {
	out := make(map[string]pkgladder.TrancheStatus, len(s.tranches))
	for i := range s.tranches {
		out[s.tranches[i].ID] = s.tranches[i].Status
	}
	return out
}

// scheduledSum totals the config's currently-scheduled tranche amounts. It is
// the single source of truth reused by GetInFlightLadderCommitUSDHr so the fake
// cannot drift from its own netting view.
func (s *ladderStatefulStore) scheduledSum(configID string) float64 {
	total := 0.0
	for i := range s.tranches {
		tr := &s.tranches[i]
		if tr.ConfigID != nil && *tr.ConfigID == configID && tr.Status == pkgladder.TrancheStatusScheduled {
			total += tr.AmountUSDHr
		}
	}
	return total
}

// scheduledTrancheIDs returns the set of currently-scheduled tranche IDs for
// the config, so preservation across runs can be asserted by identity.
func (s *ladderStatefulStore) scheduledTrancheIDs(configID string) map[string]bool {
	ids := map[string]bool{}
	for i := range s.tranches {
		tr := &s.tranches[i]
		if tr.ConfigID != nil && *tr.ConfigID == configID && tr.Status == pkgladder.TrancheStatusScheduled {
			ids[tr.ID] = true
		}
	}
	return ids
}

func (s *ladderStatefulStore) GetInFlightLadderCommitUSDHr(_ context.Context, configID string) (*float64, error) {
	total := s.scheduledSum(configID)
	return &total, nil
}

// SaveLadderRunWithTranches is APPEND-ONLY: it records the run and appends its
// new tranches without touching any existing tranche's status. This mirrors the
// production store, whose supersede/cancel path was removed.
func (s *ladderStatefulStore) SaveLadderRunWithTranches(_ context.Context, run *config.LadderRunDB, tranches []config.LadderTrancheDB) (*config.LadderRunDB, error) {
	s.runs = append(s.runs, run)
	s.tranches = append(s.tranches, tranches...)
	return run, nil
}

// fullyDelayedRamp is a 2-step ramp with NO AfterDays==0 step, so ALL planned
// commitment lands as SCHEDULED tranches (no buy-now). With a static E=0 fake
// capability, that scheduled ramp is the only in-flight commitment, so the L5
// netting alone decides whether a follow-up run Holds or tops up.
func fullyDelayedRamp(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(pkgladder.RampSchedule{
		Steps: []pkgladder.RampStep{
			{AfterDays: 1, Fraction: 0.5},
			{AfterDays: 7, Fraction: 0.5},
		},
	})
	require.NoError(t, err)
	return raw
}

// TestExecuteLadderRun_L5Convergence_PreservesRampAcrossHolds is the core L5
// regression: three sequential runs with CONSTANT usage must build the ramp
// once (run1) and then PRESERVE it (run2, run3 Hold), never accumulating and
// never discarding the live scheduled ramp.
//
// Guards the append-only + netting invariant: a Hold run must leave the config's
// existing scheduled tranches exactly as they were (same IDs, same total). A
// regression that discarded or re-planned the ramp on a Hold (the old
// cancel-and-replace bug) would drop the scheduled sum, failing these
// preservation assertions.
func TestExecuteLadderRun_L5Convergence_PreservesRampAcrossHolds(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-l5-converge"
	dbCfg := validTestDBConfig(cfgID)
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{}
	app := &Application{Config: store}
	// Constant usage across all three runs: low-water 10 -> target 8 (80%).
	capability := &fakeLadderCapability{t: t, baseline: testBaseline()}
	run := func(at time.Time) {
		require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
			pkgladder.Term1Year, pkgladder.PaymentNoUpfront, at))
	}

	// Run 1: builds the scheduled ramp. gap = 8, all delayed -> scheduled.
	run(now)
	gen1 := store.scheduledTrancheIDs(cfgID)
	require.NotEmpty(t, gen1, "run1 must build a scheduled ramp")
	assert.InDelta(t, 8.0, store.scheduledSum(cfgID), 1e-9, "run1 ramp must sum to the full gap (8)")
	require.Len(t, store.runs, 1)

	// Run 2: SAME usage. Netting sees the 8 already scheduled -> Hold. The run
	// row persists, but the run1 tranches must REMAIN scheduled (same IDs).
	run(now.Add(24 * time.Hour))
	gen2 := store.scheduledTrancheIDs(cfgID)
	assert.Equal(t, gen1, gen2, "run2 Hold must PRESERVE the exact run1 scheduled tranches (same IDs)")
	assert.InDelta(t, 8.0, store.scheduledSum(cfgID), 1e-9, "run2 Hold must not change the scheduled ramp total")
	require.Len(t, store.runs, 2, "run2 must still persist a run row")
	assert.Equal(t, 0.0, store.runs[1].TotalHourlyCommit, "run2 must be a Hold (zero new commitment)")

	// Run 3: SAME usage again -> still Hold, still preserved. No accumulation.
	run(now.Add(48 * time.Hour))
	gen3 := store.scheduledTrancheIDs(cfgID)
	assert.Equal(t, gen1, gen3, "run3 Hold must still preserve the original ramp (same IDs)")
	assert.InDelta(t, 8.0, store.scheduledSum(cfgID), 1e-9, "three constant-usage runs must not accumulate commitment")
	assert.Equal(t, 0.0, store.runs[2].TotalHourlyCommit, "run3 must be a Hold (zero new commitment)")
}

// TestExecuteLadderRun_L5DriftUp_AppendsToTargetAndConverges verifies the
// drift-UP path under append-only: when usage rises so the netted gap re-opens,
// the run APPENDS exactly the delta (target-E minus already-scheduled) and the
// prior generation is PRESERVED (not superseded), so the total scheduled
// commitment converges UP to the new target rather than being capped at the old
// ramp's size.
//
// This codifies the corrected behavior: the earlier cancel-and-replace design
// discarded the old 8 and inserted a new 8, capping coverage at 8 while target
// was 16 (coverage decay). Append-only reaches 16 = target.
func TestExecuteLadderRun_L5DriftUp_AppendsToTargetAndConverges(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-l5-driftup"
	dbCfg := validTestDBConfig(cfgID)
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{}
	app := &Application{Config: store}

	// Run 1: low-water 10 -> target 8. Build a scheduled ramp summing 8.
	lowCap := &fakeLadderCapability{t: t, baseline: testBaselineLowWater(10.0)}
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, lowCap, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now))
	gen1 := store.scheduledTrancheIDs(cfgID)
	require.NotEmpty(t, gen1, "run1 must build a scheduled ramp")
	assert.InDelta(t, 8.0, store.scheduledSum(cfgID), 1e-9, "run1 ramp must sum to target-E (8)")

	// Run 2: usage DRIFTS UP (low-water 20 -> target 16). Netting subtracts the
	// 8 already scheduled, leaving a fresh gap of 8 that APPENDS a new tranche
	// generation on top of the preserved run1 generation. Total scheduled must
	// converge to the new target (16), NOT stay capped at 8.
	highCap := &fakeLadderCapability{t: t, baseline: testBaselineLowWater(20.0)}
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, highCap, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now.Add(24*time.Hour)))

	sched2 := store.scheduledTrancheIDs(cfgID)
	// Run1's tranches must STILL be scheduled (append-only preserves them).
	for id := range gen1 {
		assert.True(t, sched2[id], "drift-up must PRESERVE the run1 tranche %s (append-only, no cancel)", id)
	}
	// The new generation adds tranches on top of run1's.
	assert.Greater(t, len(sched2), len(gen1), "drift-up must append a new generation, not replace")
	// Total scheduled commitment converges to the new target-E (16), not 8.
	assert.InDelta(t, 16.0, store.scheduledSum(cfgID), 1e-9,
		"append-only drift-up must converge scheduled commitment to target (16), not cap at the old ramp (8)")
	assert.Greater(t, store.runs[1].TotalHourlyCommit, 0.0, "drift-up run must plan positive new commitment")

	// Run 3: SAME (drifted-up) usage. Netting now sees 16 scheduled == target-E
	// -> Hold. Total must stay at 16 (no further accumulation), ramp preserved.
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, highCap, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now.Add(48*time.Hour)))
	assert.InDelta(t, 16.0, store.scheduledSum(cfgID), 1e-9,
		"run3 must Hold at target (16); netting prevents overshoot past the target")
	assert.Equal(t, 0.0, store.runs[2].TotalHourlyCommit, "run3 must be a Hold (zero new commitment)")
}

// TestExecuteLadderRun_L5OverCommitGuard_NettingCapsAppends proves the netting
// caps appends: under CONSTANT usage, three sequential runs must never let the
// total scheduled commitment exceed target-E, and Hold runs must append NO new
// tranches. Without netting, each run would re-plan the full gap and pile up
// (8 -> 16 -> 24); netting keeps it pinned at target-E after the first build.
func TestExecuteLadderRun_L5OverCommitGuard_NettingCapsAppends(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-l5-overcommit"
	// target-E = low-water(10) * 80% - 0 = 8.
	const targetMinusE = 8.0
	dbCfg := validTestDBConfig(cfgID)
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{}
	app := &Application{Config: store}
	capability := &fakeLadderCapability{t: t, baseline: testBaseline()}

	// Run 1: builds the ramp to target-E.
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now))
	assert.LessOrEqual(t, store.scheduledSum(cfgID), targetMinusE+1e-9,
		"run1 scheduled commitment must not exceed target-E")
	trancheCountAfterBuild := len(store.tranches)
	require.Positive(t, trancheCountAfterBuild, "run1 must persist tranches")

	// Runs 2 and 3: constant usage -> Hold -> netting caps appends at target-E,
	// and no new tranche rows are appended on a Hold.
	for i, at := range []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour)} {
		require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
			pkgladder.Term1Year, pkgladder.PaymentNoUpfront, at))
		assert.LessOrEqual(t, store.scheduledSum(cfgID), targetMinusE+1e-9,
			"run %d: netting must cap total scheduled commitment at target-E (no over-commit)", i+2)
		assert.Equal(t, trancheCountAfterBuild, len(store.tranches),
			"run %d Hold must append NO new tranches (netting closed the gap)", i+2)
	}
	// Final: still pinned exactly at target-E.
	assert.InDelta(t, targetMinusE, store.scheduledSum(cfgID), 1e-9,
		"after 3 constant-usage runs the scheduled commitment must equal target-E, never a multiple")
}

// ============================================================
// Tranche expiry: un-fired tranches must stop being netted (#1605)
// ============================================================

// TestExecuteLadderRun_ElapsedRampExpires_PlanningResumes is the #1605
// regression. A tranche only leaves 'scheduled' by firing, and nothing fires
// today, so a planned ramp that is never executed used to stay in the in-flight
// sum forever: the netting kept zeroing the gap and the ladder stopped planning
// after its very first run, with no error and no way to tell it apart from
// "target coverage already met".
//
// Run 1 builds the ramp; run 2 happens a month later with the ramp still
// un-fired. The dead tranches must be retired so the run plans the still-needed
// commitment again. Before the fix run 2 is a Hold (zero new commitment), and
// so is every run after it, forever.
func TestExecuteLadderRun_ElapsedRampExpires_PlanningResumes(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-1605-elapsed"
	const targetMinusE = 8.0
	dbCfg := validTestDBConfig(cfgID) // cadence=daily -> one-day expiry grace
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{}
	app := &Application{Config: store}
	capability := &fakeLadderCapability{t: t, baseline: testBaseline()}

	// Run 1: builds the ramp (tranches due at +1d and +7d) summing target-E.
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now))
	gen1 := store.scheduledTrancheIDs(cfgID)
	require.NotEmpty(t, gen1, "run1 must build a scheduled ramp")
	require.InDelta(t, targetMinusE, store.scheduledSum(cfgID), 1e-9, "run1 ramp must sum to target-E")

	// Run 2, 30 days later. Nothing ever fired: both tranches are weeks past
	// their fire date, so none of that commitment exists and none of it is in
	// flight. The run MUST plan the gap again.
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now.Add(30*24*time.Hour)))

	require.Len(t, store.runs, 2)
	assert.InDelta(t, targetMinusE, store.runs[1].TotalHourlyCommit, 1e-9,
		"run2 must re-plan the full gap: the run1 ramp never fired, so it is not in flight")

	// The abandoned generation is retired (kept for audit, not deleted) and no
	// longer counted, and the fresh ramp is exactly target-E: not 0 (silent
	// stop) and not 16 (double-counting the dead ramp).
	statuses := store.trancheStatuses()
	for id := range gen1 {
		assert.Equal(t, pkgladder.TrancheStatusCancelled, statuses[id],
			"un-fired elapsed tranche %s must be retired, not left scheduled forever", id)
	}
	gen2 := store.scheduledTrancheIDs(cfgID)
	require.NotEmpty(t, gen2, "run2 must build a replacement ramp")
	for id := range gen1 {
		assert.NotContains(t, gen2, id, "the expired tranche %s must not still count as in flight", id)
	}
	assert.InDelta(t, targetMinusE, store.scheduledSum(cfgID), 1e-9,
		"live scheduled commitment must be back at target-E after the dead ramp is retired")
	assert.Len(t, store.tranches, len(gen1)+len(gen2),
		"expired tranches must be retained as audit rows, never deleted")
}

// TestExecuteLadderRun_PastDueWithinGrace_StaysInFlight guards the opposite
// direction of the same money path: a tranche that is only slightly past due
// can still fire, so expiring it would let the run re-plan an amount that is
// about to be purchased, committing it twice. Only tranches more than a full
// planning period past due are retired.
func TestExecuteLadderRun_PastDueWithinGrace_StaysInFlight(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-1605-grace"
	const targetMinusE = 8.0
	dbCfg := validTestDBConfig(cfgID) // cadence=daily -> 24h grace past the fire date
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{}
	app := &Application{Config: store}
	capability := &fakeLadderCapability{t: t, baseline: testBaseline()}

	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now))
	gen1 := store.scheduledTrancheIDs(cfgID)
	require.Len(t, gen1, 2, "run1 must build both ramp steps")
	require.InDelta(t, targetMinusE, store.scheduledSum(cfgID), 1e-9)

	// Run 2 at +36h: the +1d tranche is 12h past due, well inside the one-day
	// grace, and the +7d tranche is not due at all. Both are still in flight.
	require.NoError(t, app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now.Add(36*time.Hour)))

	assert.Equal(t, gen1, store.scheduledTrancheIDs(cfgID),
		"a tranche that is only just past due must stay in flight (expiring it risks a double purchase)")
	assert.InDelta(t, targetMinusE, store.scheduledSum(cfgID), 1e-9,
		"in-flight commitment must be unchanged while the ramp can still fire")
	require.Len(t, store.runs, 2)
	assert.Equal(t, 0.0, store.runs[1].TotalHourlyCommit,
		"run2 must Hold: the live ramp already covers target-E")
}

// TestExecuteLadderRun_ExpiryFailure_FailsLoud proves the expiry error is not
// swallowed. A failed expiry means the in-flight sum may still contain dead
// tranches, which would silently under-purchase; the run must abort before any
// planning is persisted so the config is counted Errored.
func TestExecuteLadderRun_ExpiryFailure_FailsLoud(t *testing.T) {
	ctx := testutil.TestContext(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	const cfgID = "cfg-1605-expire-err"
	dbCfg := validTestDBConfig(cfgID)
	dbCfg.RampSchedule = fullyDelayedRamp(t)

	store := &ladderStatefulStore{expireErr: errors.New("connection reset by peer")}
	app := &Application{Config: store}
	capability := &fakeLadderCapability{t: t, baseline: testBaseline()}

	err := app.executeLadderRun(ctx, &dbCfg, capability, "123456789012",
		pkgladder.Term1Year, pkgladder.PaymentNoUpfront, now)
	require.Error(t, err, "a failed tranche expiry must abort the run, never plan on a stale in-flight sum")
	assert.Contains(t, err.Error(), "connection reset by peer", "the underlying cause must be surfaced")
	assert.Empty(t, store.runs, "no run may be persisted after a failed expiry")
	assert.Empty(t, store.tranches, "no tranche may be persisted after a failed expiry")
}

// TestLadderCadencePeriod pins the expiry grace to each cadence's nominal
// planning period and requires an unknown cadence to fail loud: defaulting it
// to daily would silently shorten the window that protects a due tranche from
// premature expiry.
func TestLadderCadencePeriod(t *testing.T) {
	daily, err := ladderCadencePeriod(pkgladder.CadenceDaily)
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, daily)

	weekly, err := ladderCadencePeriod(pkgladder.CadenceWeekly)
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, weekly)

	_, err = ladderCadencePeriod(pkgladder.LadderCadence("monthly"))
	require.Error(t, err, "an unrecognized cadence must not silently fall back to a default period")
	assert.Contains(t, err.Error(), "monthly")
}
