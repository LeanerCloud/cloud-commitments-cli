package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOverrideStore is a small in-memory stand-in for the scheduler's
// StoreInterface, exercising only the methods that ListRecommendations'
// override path touches. Mirrors the pattern used by mockSuppressionStore
// in scheduler_suppressions_test.go to keep tests self-contained.
type mockOverrideStore struct {
	MockConfigStore
	getGlobalErr   error
	getOverrideErr error
	globals        map[string]*config.ServiceConfig
	overrides      map[string]*config.AccountServiceOverride
	recs           []config.RecommendationRecord
}

func (m *mockOverrideStore) ListStoredRecommendations(_ context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	if filter.ID == "" {
		return m.recs, nil
	}
	for _, r := range m.recs {
		if r.ID == filter.ID {
			return []config.RecommendationRecord{r}, nil
		}
	}
	return nil, nil
}
func (m *mockOverrideStore) ListActiveSuppressions(_ context.Context) ([]config.PurchaseSuppression, error) {
	return nil, nil
}
func (m *mockOverrideStore) GetRecommendationsFreshness(_ context.Context) (*config.RecommendationsFreshness, error) {
	now := time.Now()
	return &config.RecommendationsFreshness{LastCollectedAt: &now}, nil
}
func (m *mockOverrideStore) MarkCollectionStarted(_ context.Context) (string, bool, error) {
	return "mock-owner-token", true, nil
}
func (m *mockOverrideStore) ClearCollectionStarted(_ context.Context, _ string) error {
	return nil
}
func (m *mockOverrideStore) GetServiceConfig(_ context.Context, provider, service string) (*config.ServiceConfig, error) {
	if m.getGlobalErr != nil {
		return nil, m.getGlobalErr
	}
	return m.globals[provider+"|"+service], nil
}
func (m *mockOverrideStore) GetAccountServiceOverride(_ context.Context, accountID, provider, service string) (*config.AccountServiceOverride, error) {
	if m.getOverrideErr != nil {
		return nil, m.getOverrideErr
	}
	return m.overrides[accountID+"|"+provider+"|"+service], nil
}

// GetGlobalConfig returns a default config so ListRecommendations can resolve
// the effective stale TTL without panicking on the embedded MockConfigStore.
// The returned RecommendationsCacheStaleHours of 24 means ListRecommendations
// will use the DB-configured value (24h); the tests in this file exercise
// override/suppression logic, not TTL behavior.
func (m *mockOverrideStore) GetGlobalConfig(_ context.Context) (*config.GlobalConfig, error) {
	return &config.GlobalConfig{
		RecommendationsCacheStaleHours: config.DefaultRecommendationsCacheStaleHours,
		RecommendationsLookbackDays:    config.DefaultRecommendationsLookbackDays,
	}, nil
}
func (m *mockOverrideStore) UpsertNotificationMute(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockOverrideStore) IsNotificationMuted(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func boolPtr(b bool) *bool { return &b }

// rdsRec returns a rec for the given account/region/engine with sensible defaults.
//
//nolint:unparam // test helper: region fixed by design across current callers
func rdsRec(account, region, engine string) config.RecommendationRecord {
	a := account
	return config.RecommendationRecord{
		ID:             account + "/" + region + "/" + engine,
		Provider:       "aws",
		Service:        "rds",
		Region:         region,
		Engine:         engine,
		ResourceType:   "db.t3.medium",
		Count:          1,
		CloudAccountID: &a,
	}
}

func TestApplyAccountOverrides_DisabledOverride_DropsAccountSvcRecs(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			rdsRec("acct-A", "us-east-1", "mysql"),
			rdsRec("acct-B", "us-east-1", "mysql"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {Enabled: boolPtr(false)},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "acct-A's rec dropped; acct-B's kept")
	assert.Equal(t, "acct-B", *recs[0].CloudAccountID)
}

func TestApplyAccountOverrides_GlobalDisabled_DropsAllRecsForService(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			rdsRec("acct-A", "us-east-1", "mysql"),
			rdsRec("acct-B", "us-east-1", "mysql"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: false},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	assert.Empty(t, recs, "global Enabled=false drops all per-account recs for the service")
}

func TestApplyAccountOverrides_NoGlobalConfig_RecsPassThrough(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{rdsRec("acct-A", "us-east-1", "mysql")},
		// globals empty — GetServiceConfig returns nil
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	assert.Len(t, recs, 1, "no global config -> no per-account policy applies -> rec passes through")
}

// ambientRec returns an ec2 rec with no cloud account, the shape the AWS
// ambient-credentials collector writes when the executing identity matches no
// registered cloud_accounts row.
func ambientRec(id string, count int) config.RecommendationRecord {
	return config.RecommendationRecord{
		ID: id, Provider: "aws", Service: "ec2",
		Region: "us-east-1", ResourceType: "t4g.nano", Count: count,
		CloudAccountID: nil,
	}
}

// TestApplyAccountOverrides_NilCloudAccountID_GlobalDisabled_DropsRec is the
// regression test for issue #1604. Pre-fix the ambient rec was skipped by the
// resolver and passed through by the filter, so a service the operator had
// switched off in Settings stayed listed and purchasable.
func TestApplyAccountOverrides_NilCloudAccountID_GlobalDisabled_DropsRec(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient", 1)},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {Provider: "aws", Service: "ec2", Enabled: false},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	assert.Empty(t, recs, "global Enabled=false applies to recs with no cloud account")
}

// TestApplyAccountOverrides_NilCloudAccountID_GlobalEnabled_KeepsRec is the
// opposite direction: the fix must not blank the ambient deployment's page for
// services the operator left enabled.
func TestApplyAccountOverrides_NilCloudAccountID_GlobalEnabled_KeepsRec(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient", 1)},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {Provider: "aws", Service: "ec2", Enabled: true},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "an enabled global config keeps account-less recs visible")
	assert.Equal(t, "ambient", recs[0].ID)
}

// TestApplyAccountOverrides_NilCloudAccountID_NoGlobalConfig_PassesThrough
// keeps the unconfigured deployment's behaviour unchanged: with no
// service_configs row there is no policy, so nothing is filtered.
func TestApplyAccountOverrides_NilCloudAccountID_NoGlobalConfig_PassesThrough(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient", 1)},
		// globals empty — GetServiceConfig returns nil
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "no global config -> no policy applies -> rec passes through")
}

// TestApplyAccountOverrides_NilCloudAccountID_MinCountAndAccountRecs exercises
// both directions in one list: the ambient rec below the global floor is
// dropped, the ambient rec above it is kept, and an account-scoped rec keeps
// resolving through its own (account, provider, service) entry.
func TestApplyAccountOverrides_NilCloudAccountID_MinCountAndAccountRecs(t *testing.T) {
	ctx := context.Background()
	acctLow := rdsRec("acct-A", "us-east-1", "mysql") // Count 1, below the rds floor
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			ambientRec("ambient-below", 1),
			ambientRec("ambient-above", 5),
			acctLow,
		},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {Provider: "aws", Service: "ec2", Enabled: true, MinCount: 2},
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true, MinCount: 2},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "both the below-floor ambient rec and the below-floor account rec are dropped")
	assert.Equal(t, "ambient-above", recs[0].ID)
}

// TestApplyAccountOverrides_NilCloudAccountID_GlobalExcludeRegion covers the
// include/exclude half of the global policy on account-less recs.
func TestApplyAccountOverrides_NilCloudAccountID_GlobalExcludeRegion(t *testing.T) {
	ctx := context.Background()
	other := ambientRec("ambient-eu", 1)
	other.Region = "eu-west-1"
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient-us", 1), other},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {
				Provider: "aws", Service: "ec2", Enabled: true,
				ExcludeRegions: []string{"eu-west-1"},
			},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "excluded region dropped, the other region kept")
	assert.Equal(t, "ambient-us", recs[0].ID)
}

func TestApplyAccountOverrides_IncludeEngineMatch(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			rdsRec("acct-A", "us-east-1", "mysql"),
			rdsRec("acct-A", "us-east-1", "postgres"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {IncludeEngines: []string{"mysql"}},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "mysql", recs[0].Engine, "non-matching engine filtered out")
}

func TestApplyAccountOverrides_ExcludeEngine(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			rdsRec("acct-A", "us-east-1", "mysql"),
			rdsRec("acct-A", "us-east-1", "postgres"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {ExcludeEngines: []string{"postgres"}},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "mysql", recs[0].Engine)
}

func TestApplyAccountOverrides_RegionAndTypeFilters(t *testing.T) {
	ctx := context.Background()
	mkRec := func(account, region, rtype string) config.RecommendationRecord {
		a := account
		return config.RecommendationRecord{
			ID: account + "/" + region + "/" + rtype, Provider: "aws", Service: "rds",
			Region: region, ResourceType: rtype, Count: 1, CloudAccountID: &a,
		}
	}
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			mkRec("acct-A", "us-east-1", "db.t3.small"),
			mkRec("acct-A", "us-east-1", "db.t3.large"),
			mkRec("acct-A", "eu-west-1", "db.t3.small"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {
				IncludeRegions: []string{"us-east-1"},
				ExcludeTypes:   []string{"db.t3.small"},
			},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "us-east-1", recs[0].Region)
	assert.Equal(t, "db.t3.large", recs[0].ResourceType)
}

func TestApplyAccountOverrides_MinCount_DropsRecsBelowFloor(t *testing.T) {
	// The per-service MinCount filter (GUI/CLI --min-count) drops recs whose
	// persisted Count is below the configured floor. MinCount=0 disables it.
	ctx := context.Background()
	mkRec := func(account string, count int) config.RecommendationRecord {
		a := account
		return config.RecommendationRecord{
			ID: account + "/" + string(rune('a'+count)), Provider: "aws", Service: "rds",
			Region: "us-east-1", ResourceType: "db.t3.medium", Engine: "mysql",
			Count: count, CloudAccountID: &a,
		}
	}
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			mkRec("acct-A", 1),
			mkRec("acct-A", 2),
			mkRec("acct-A", 5),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true, MinCount: 2},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 2, "count=1 must be dropped; count>=2 kept")
	counts := []int{recs[0].Count, recs[1].Count}
	assert.ElementsMatch(t, []int{2, 5}, counts)
}

func TestApplyAccountOverrides_MinCountZero_KeepsAll(t *testing.T) {
	ctx := context.Background()
	a := "acct-A"
	rec := config.RecommendationRecord{
		ID: "acct-A/1", Provider: "aws", Service: "rds", Region: "us-east-1",
		ResourceType: "db.t3.medium", Engine: "mysql", Count: 1, CloudAccountID: &a,
	}
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{rec},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true, MinCount: 0},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "MinCount=0 disables the floor")
}

func TestApplyAccountOverrides_EmptyEngine_NotFilteredByIncludeEngines(t *testing.T) {
	// Savings Plans / Compute recs carry no engine; an IncludeEngines list
	// added on another service for the same account must NOT silently drop
	// engine-less recs.
	ctx := context.Background()
	mkSP := func(account string) config.RecommendationRecord {
		a := account
		return config.RecommendationRecord{
			ID: account + "/sp", Provider: "aws", Service: "savings-plans",
			Region: "us-east-1", ResourceType: "Compute", Count: 1, CloudAccountID: &a,
		}
	}
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{mkSP("acct-A")},
		globals: map[string]*config.ServiceConfig{
			"aws|savings-plans": {Provider: "aws", Service: "savings-plans", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|savings-plans": {
				IncludeEngines: []string{"mysql"}, // misconfigured but must not blank the page
			},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	assert.Len(t, recs, 1, "engine-less rec not filtered by IncludeEngines")
}

func TestApplyAccountOverrides_LookupError_PassesThrough(t *testing.T) {
	// Mirrors the applySuppressions over-show-vs-under-show trade-off.
	// A store error must not blank the recommendations page.
	ctx := context.Background()
	store := &mockOverrideStore{
		recs:         []config.RecommendationRecord{rdsRec("acct-A", "us-east-1", "mysql")},
		getGlobalErr: errors.New("postgres timeout"),
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err, "ListRecommendations swallows the override-resolver error")
	assert.Len(t, recs, 1, "un-filtered list returned on lookup failure")
}

// TestFilterRecsByResolvedConfigs_DoesNotMutateCallerSlice asserts that
// filterRecsByResolvedConfigs allocates a fresh output slice instead of
// aliasing the caller's backing array via recs[:0] (05-M1).
//
// Pre-fix: out := recs[:0] shared the backing array, so any append during
// filtering overwrote elements the caller still expected at their original
// positions. The fix allocates make([]T, 0, len(recs)).
func TestFilterRecsByResolvedConfigs_DoesNotMutateCallerSlice(t *testing.T) {
	// Resolve a ServiceConfig that disables the second rec.
	enabled := &config.ServiceConfig{Provider: "aws", Service: "rds", Enabled: true}
	disabled := &config.ServiceConfig{Provider: "aws", Service: "rds", Enabled: false}
	accA := "acct-A"
	accB := "acct-B"
	recs := []config.RecommendationRecord{
		{ID: "keep", Provider: "aws", Service: "rds", Region: "us-east-1",
			ResourceType: "db.t3.medium", Count: 1, CloudAccountID: &accA},
		{ID: "drop", Provider: "aws", Service: "rds", Region: "us-east-1",
			ResourceType: "db.t3.medium", Count: 1, CloudAccountID: &accB},
	}
	// Keep a snapshot of the original IDs.
	originalIDs := []string{recs[0].ID, recs[1].ID}

	resolved := map[string]*config.ServiceConfig{
		config.AccountConfigKey(accA, "aws", "rds"): enabled,
		config.AccountConfigKey(accB, "aws", "rds"): disabled,
	}
	out := filterRecsByResolvedConfigs(recs, resolved)

	// Only the enabled rec survives.
	require.Len(t, out, 1)
	assert.Equal(t, "keep", out[0].ID)

	// The original slice must not be mutated.
	require.Len(t, recs, 2, "original slice length unchanged")
	assert.Equal(t, originalIDs[0], recs[0].ID, "recs[0] must not be overwritten")
	assert.Equal(t, originalIDs[1], recs[1].ID, "recs[1] must not be overwritten")
}

func TestApplyAccountOverrides_OverrideLookupError_PassesThrough(t *testing.T) {
	// Mirrors the global-config lookup failure contract: if the per-account
	// override lookup fails, the page should over-show rather than blank.
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{rdsRec("acct-A", "us-east-1", "mysql")},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		getOverrideErr: errors.New("override lookup timeout"),
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err, "ListRecommendations swallows the override-resolver error")
	assert.Len(t, recs, 1, "un-filtered list returned on override lookup failure")
}

// Issue #196 acceptance criterion (mirrored from issue #111):
//
// Seed a global ServiceConfig + a per-account override that disables one
// account; confirm the listed recs reflect the override.
func TestApplyAccountOverrides_AcceptanceCriterion_Issue196(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{
			rdsRec("acct-A", "us-east-1", "mysql"),
			rdsRec("acct-B", "us-east-1", "mysql"),
		},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 3, Payment: "all_upfront"},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {Enabled: boolPtr(false)},
		},
	}
	s := &Scheduler{config: store}

	recs, err := s.ListRecommendations(ctx, config.RecommendationFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1, "acct-A's recs hidden by the override")
	assert.Equal(t, "acct-B", *recs[0].CloudAccountID)
}

// TestGetRecommendationByID exercises the three acceptance cases from issue #214:
//
//	(a) rec visible: returns rec, nil hiddenBy
//	(b) rec hidden by override: returns rec + hiddenBy reasons (no 404)
//	(c) rec genuinely absent: returns nil, nil, nil
func TestGetRecommendationByID_VisibleRec(t *testing.T) {
	ctx := context.Background()
	rec := rdsRec("acct-A", "us-east-1", "mysql")
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{rec},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
	}
	s := &Scheduler{config: store}

	got, hiddenBy, err := s.GetRecommendationByID(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "visible rec must be returned")
	assert.Equal(t, rec.ID, got.ID)
	assert.Empty(t, hiddenBy, "visible rec must carry no hidden_by reasons")
}

func TestGetRecommendationByID_HiddenByOverride(t *testing.T) {
	// Issue #214: detail endpoint must return the rec with hidden_by reasons
	// instead of a 404 when an account-service override is filtering it out.
	ctx := context.Background()
	rec := rdsRec("acct-A", "us-east-1", "mysql")
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{rec},
		globals: map[string]*config.ServiceConfig{
			"aws|rds": {Provider: "aws", Service: "rds", Enabled: true},
		},
		overrides: map[string]*config.AccountServiceOverride{
			"acct-A|aws|rds": {Enabled: boolPtr(false)},
		},
	}
	s := &Scheduler{config: store}

	got, hiddenBy, err := s.GetRecommendationByID(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "override-hidden rec must still be returned")
	assert.Equal(t, rec.ID, got.ID)
	assert.Equal(t, []string{"enabled=false"}, hiddenBy,
		"hidden_by must report the override dimension that caused the filter")
}

// TestGetRecommendationByID_NilCloudAccountID_HiddenByGlobal is the detail-page
// half of issue #1604: once the list filter hides an account-less rec, the
// deep link must explain why instead of rendering it as visible.
func TestGetRecommendationByID_NilCloudAccountID_HiddenByGlobal(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient", 1)},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {Provider: "aws", Service: "ec2", Enabled: false},
		},
	}
	s := &Scheduler{config: store}

	got, hiddenBy, err := s.GetRecommendationByID(ctx, "ambient")
	require.NoError(t, err)
	require.NotNil(t, got, "hidden rec must still be returned")
	assert.Equal(t, []string{"enabled=false"}, hiddenBy)
}

// TestGetRecommendationByID_NilCloudAccountID_VisibleRec is the other
// direction: an account-less rec its global config permits carries no
// hidden_by marker.
func TestGetRecommendationByID_NilCloudAccountID_VisibleRec(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{
		recs: []config.RecommendationRecord{ambientRec("ambient", 1)},
		globals: map[string]*config.ServiceConfig{
			"aws|ec2": {Provider: "aws", Service: "ec2", Enabled: true},
		},
	}
	s := &Scheduler{config: store}

	got, hiddenBy, err := s.GetRecommendationByID(ctx, "ambient")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, hiddenBy, "permitted account-less rec carries no hidden_by reasons")
}

func TestGetRecommendationByID_AbsentRec(t *testing.T) {
	ctx := context.Background()
	store := &mockOverrideStore{recs: nil}
	s := &Scheduler{config: store}

	got, hiddenBy, err := s.GetRecommendationByID(ctx, "no-such-rec")
	require.NoError(t, err)
	assert.Nil(t, got, "absent rec must return nil")
	assert.Nil(t, hiddenBy)
}
