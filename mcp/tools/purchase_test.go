package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

// TestMain enables the operator-side real-purchase gate for this package's
// test binary by default. Every other test in this file that drives a
// confirmed real purchase (DryRun: false, Confirm: true) predates
// EnvEnableRealPurchases and asserts on what happens once a purchase is
// actually authorized to run (provider called, token derived, response
// shaped correctly, etc) -- none of that is what TestExecutePurchaseRealPurchaseGate
// exists to cover, so defaulting the gate on here keeps them exercising the
// behavior they were written for instead of universally failing at the gate.
// TestExecutePurchaseRealPurchaseGate is the one test that deliberately
// overrides this default, and it does so non-parallel (see its doc comment)
// so no parallel test in this package ever observes a transient override.
//
// It also pins EnvAuditLog to a path inside a run-scoped temp directory.
// Auditing is on by default (see EnvAuditLog), so without this every test in
// this file that calls ExecutePurchase would append to the developer's real
// ~/.local/state/cudly/mcp-audit.jsonl. Individual tests that need to assert
// on audit file contents override this with their own t.Setenv.
func TestMain(m *testing.M) {
	if err := os.Setenv(EnvEnableRealPurchases, "1"); err != nil {
		panic(err)
	}

	auditDir, err := os.MkdirTemp("", "cudly-mcp-audit-testmain")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv(EnvAuditLog, filepath.Join(auditDir, "mcp-audit.jsonl")); err != nil {
		panic(err)
	}

	// os.Exit skips deferred calls, so the temp dir is removed explicitly
	// before exiting rather than via defer.
	code := m.Run()
	os.RemoveAll(auditDir)
	os.Exit(code)
}

// fakeServiceClient is a minimal provider.ServiceClient test double. Only
// PurchaseCommitment is exercised by these tests; the rest of the interface
// is implemented trivially to satisfy the type.
type fakeServiceClient struct {
	purchaseCalls  int
	purchaseResult common.PurchaseResult
	purchaseErr    error
	lastOpts       common.PurchaseOptions
}

func (f *fakeServiceClient) GetServiceType() common.ServiceType { return common.ServiceEC2 }
func (f *fakeServiceClient) GetRegion() string                  { return "us-east-1" }
func (f *fakeServiceClient) GetRecommendations(_ context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetExistingCommitments(_ context.Context) ([]common.Commitment, error) {
	return nil, nil
}
func (f *fakeServiceClient) PurchaseCommitment(_ context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	f.purchaseCalls++
	f.lastOpts = opts
	f.purchaseResult.Recommendation = rec
	return f.purchaseResult, f.purchaseErr
}
func (f *fakeServiceClient) ValidateOffering(_ context.Context, _ common.Recommendation) error {
	return nil
}
func (f *fakeServiceClient) GetOfferingDetails(_ context.Context, _ common.Recommendation) (*common.OfferingDetails, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetValidResourceTypes(_ context.Context) ([]string, error) {
	return nil, nil
}

var _ provider.ServiceClient = (*fakeServiceClient)(nil)

// testRecommendation mirrors what a real purchase tool's *FromArgs
// constructor actually builds: none of them populate
// OnDemandCost/CommitmentCost/EstimatedSavings/SavingsPercentage (they build
// a fresh Recommendation from the caller's typed args, not from a priced
// search result), so this fixture leaves those fields at their zero value
// too. An earlier version of this fixture hand-set those fields, which
// masked the all-responses-report-0 finding from review -- see
// TestExecutePurchasePreviewOmitsUnknownCostFields.
func testRecommendation() common.Recommendation {
	return common.Recommendation{
		Provider:      common.ProviderAWS,
		Account:       "123456789012",
		Service:       common.ServiceEC2,
		Region:        "us-east-1",
		ResourceType:  "m5.large",
		Count:         3,
		Term:          "3yr",
		PaymentOption: "no-upfront",
	}
}

// testRecommendationWithCost extends testRecommendation with real cost
// figures, used only to prove ExecutePurchase passes a genuinely-known cost
// through to the response when one is present.
func testRecommendationWithCost() common.Recommendation {
	rec := testRecommendation()
	rec.OnDemandCost = 1000
	rec.CommitmentCost = 600
	rec.EstimatedSavings = 400
	rec.SavingsPercentage = 40
	return rec
}

func TestDecidePurchaseMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		dryRun  bool
		confirm bool
		want    purchaseMode
		wantErr bool
	}{
		{"dry run wins regardless of confirm", true, false, modePreview, false},
		{"dry run with confirm still previews", true, true, modePreview, false},
		{"confirmed real purchase executes", false, true, modeExecute, false},
		{"unconfirmed real purchase refused", false, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decidePurchaseMode(tc.dryRun, tc.confirm)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestExecutePurchaseDryRunNeverCallsProvider proves the safety rail from
// the design doc: dry_run=true must never invoke ResolveClient (and
// therefore never PurchaseCommitment), even when confirm=true. ResolveClient
// here returns an error if called at all, so any invocation fails the test.
func TestExecutePurchaseDryRunNeverCallsProvider(t *testing.T) {
	t.Parallel()
	resolveCalled := false
	resolve := func(_ context.Context) (provider.ServiceClient, error) {
		resolveCalled = true
		return nil, errors.New("ResolveClient must not be called in dry_run mode")
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendationWithCost(),
		DryRun:         true,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: resolve,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, resolveCalled, "dry_run=true must never resolve a service client")
	assert.True(t, resp.DryRun)
	assert.True(t, resp.Success)
	require.NotNil(t, resp.Cost, "a genuinely-known cost must be passed through, not dropped")
	assert.Equal(t, 600.0, *resp.Cost)
	require.NotNil(t, resp.OnDemandCost)
	assert.Equal(t, 1000.0, *resp.OnDemandCost)
	require.NotNil(t, resp.EstimatedSavings)
	assert.Equal(t, 400.0, *resp.EstimatedSavings)
	require.NotNil(t, resp.SavingsPercentage)
	assert.Equal(t, 40.0, *resp.SavingsPercentage)
}

// TestExecutePurchasePreviewOmitsUnknownCostFields proves finding 2 of the
// adversarial review: a dry-run preview built from a Recommendation that
// mirrors what real purchase tools actually construct (no cost fields set,
// since no *FromArgs constructor in this package populates them) must not
// report cost/on_demand_cost/estimated_savings/savings_percentage as a real
// 0 -- that would be indistinguishable from a confirmed $0 purchase. The
// pointer fields must be nil, and therefore omitted from the JSON payload
// entirely rather than serialized as 0.
func TestExecutePurchasePreviewOmitsUnknownCostFields(t *testing.T) {
	t.Parallel()
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         true,
		Confirm:        false,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Nil(t, resp.Cost)
	assert.Nil(t, resp.OnDemandCost)
	assert.Nil(t, resp.EstimatedSavings)
	assert.Nil(t, resp.SavingsPercentage)

	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	body := string(raw)
	assert.NotContains(t, body, `"cost"`, "unknown cost must be omitted from the JSON payload, not reported as 0")
	assert.NotContains(t, body, `"on_demand_cost"`)
	assert.NotContains(t, body, `"estimated_savings"`)
	assert.NotContains(t, body, `"savings_percentage"`)
}

// TestExecutePurchasePreviewPopulatesTermYears is the regression guard for
// the CodeRabbit finding that PurchaseResponse.TermYears was declared in the
// JSON contract but never set in either ExecutePurchase branch, so it was
// always zero/omitted even though the term is known from the recommendation.
// testRecommendation() carries Term: "3yr", the same "<N>yr" format every
// *FromArgs constructor in this package writes.
func TestExecutePurchasePreviewPopulatesTermYears(t *testing.T) {
	t.Parallel()
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         true,
		Confirm:        false,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 3, resp.TermYears, "a preview response must carry the term the caller specified")
}

// TestExecutePurchaseRealPurchasePopulatesTermYears is the real-purchase
// counterpart of TestExecutePurchasePreviewPopulatesTermYears: the term must
// be populated on the modeExecute branch too, not only the preview branch.
func TestExecutePurchaseRealPurchasePopulatesTermYears(t *testing.T) {
	t.Parallel()
	fake := &fakeServiceClient{
		purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-term-test"},
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 3, resp.TermYears, "a real-purchase response must carry the term the caller specified")
}

// TestExecutePurchaseUnconfirmedRealPurchaseRefused proves confirm=false
// refuses a real purchase (dry_run=false) with a structured error rather
// than a silent no-op, and that ResolveClient is never invoked either.
func TestExecutePurchaseUnconfirmedRealPurchaseRefused(t *testing.T) {
	t.Parallel()
	resolveCalled := false
	resolve := func(_ context.Context) (provider.ServiceClient, error) {
		resolveCalled = true
		return nil, nil
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        false,
		ResolveClient:  resolve,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.False(t, resolveCalled)
	assert.Contains(t, err.Error(), "confirm=true")
}

// setPurchaseGateEnv sets EnvEnableRealPurchases to value for the calling
// test, restoring whatever the variable held before (present or absent) on
// cleanup. value == "" removes the variable entirely rather than setting it
// to an empty string, since this suite needs to pin the real "operator never
// configured this" default, not merely an empty value that happens to behave
// the same way. Written by hand rather than via t.Setenv because t.Setenv
// cannot represent "the variable was absent".
func setPurchaseGateEnv(t *testing.T, value string) {
	t.Helper()
	prev, had := os.LookupEnv(EnvEnableRealPurchases)
	if value == "" {
		require.NoError(t, os.Unsetenv(EnvEnableRealPurchases))
	} else {
		require.NoError(t, os.Setenv(EnvEnableRealPurchases, value))
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(EnvEnableRealPurchases, prev)
		} else {
			_ = os.Unsetenv(EnvEnableRealPurchases)
		}
	})
}

// TestExecutePurchaseRealPurchaseGate is the regression guard for the
// operator authorization gap found in review: before EnvEnableRealPurchases
// existed, a confirmed real purchase (dry_run=false, confirm=true) executed
// immediately no matter what the operator running this MCP server process
// wanted -- the model's own confirm flag was the only thing standing between
// a prompt-injected or simply hallucinating model with ambient production
// credentials and a real purchase. This pins that ExecutePurchase now
// refuses a real purchase, and never resolves a client or calls the
// provider, unless the operator has explicitly set EnvEnableRealPurchases to
// "1" or "true"; and that dry runs are unaffected either way.
//
// Deliberately not parallel: every subtest mutates the process-wide
// environment variable this package's TestMain also sets a default for.
// Go's serial tests all finish before any t.Parallel() test in this binary
// begins, so running this test (and its subtests) serially guarantees no
// parallel test ever observes one of these transient overrides.
func TestExecutePurchaseRealPurchaseGate(t *testing.T) {
	rec := testRecommendation()
	realPurchaseRequest := func(fake *fakeServiceClient, dryRun bool) PurchaseRequest {
		return PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: dryRun, Confirm: true, CredentialScope: "test-scope",
			ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		}
	}

	t.Run("unset refuses a real purchase and never calls the provider", func(t *testing.T) {
		setPurchaseGateEnv(t, "")
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}

		resp, err := ExecutePurchase(context.Background(), realPurchaseRequest(fake, false))

		require.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), EnvEnableRealPurchases,
			"the refusal must name the flag the operator needs to set")
		assert.Equal(t, 0, fake.purchaseCalls, "the provider must never be called while the gate is disabled")
	})

	t.Run("\"1\" enables a real purchase", func(t *testing.T) {
		setPurchaseGateEnv(t, "1")
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-gate-1"}}

		resp, err := ExecutePurchase(context.Background(), realPurchaseRequest(fake, false))

		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.True(t, resp.Success)
		assert.Equal(t, 1, fake.purchaseCalls)
	})

	t.Run("\"true\" enables a real purchase regardless of case", func(t *testing.T) {
		setPurchaseGateEnv(t, "TrUe")
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}

		resp, err := ExecutePurchase(context.Background(), realPurchaseRequest(fake, false))

		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, 1, fake.purchaseCalls)
	})

	t.Run("0, false, and garbage all refuse a real purchase", func(t *testing.T) {
		for _, v := range []string{"0", "false", "False", "yes", "enabled", "  "} {
			setPurchaseGateEnv(t, v)
			fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}

			resp, err := ExecutePurchase(context.Background(), realPurchaseRequest(fake, false))

			require.Errorf(t, err, "%q must not enable real purchases", v)
			assert.Nil(t, resp)
			assert.Equalf(t, 0, fake.purchaseCalls, "%q must not enable real purchases", v)
		}
	})

	t.Run("dry run is unaffected by the gate either way", func(t *testing.T) {
		for _, v := range []string{"", "1", "0"} {
			setPurchaseGateEnv(t, v)
			fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}

			resp, err := ExecutePurchase(context.Background(), realPurchaseRequest(fake, true))

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.True(t, resp.DryRun)
			assert.Equal(t, 0, fake.purchaseCalls, "a preview must never call the provider regardless of the gate")
		}
	})
}

// TestExecutePurchaseRealPurchaseCallsProviderWithMCPSource proves a
// confirmed real purchase resolves the client, calls PurchaseCommitment
// exactly once, and stamps PurchaseSourceMCP + a non-empty idempotency
// token -- never a caller-suppliable source string.
func TestExecutePurchaseRealPurchaseCallsProviderWithMCPSource(t *testing.T) {
	t.Parallel()
	fake := &fakeServiceClient{
		purchaseResult: common.PurchaseResult{
			Success:      true,
			CommitmentID: "ri-12345",
			Cost:         600,
		},
	}
	resolve := func(_ context.Context) (provider.ServiceClient, error) {
		return fake, nil
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: resolve,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, fake.purchaseCalls)
	assert.Equal(t, common.PurchaseSourceMCP, fake.lastOpts.Source)
	assert.NotEmpty(t, fake.lastOpts.IdempotencyToken)
	assert.True(t, resp.Success)
	assert.Equal(t, "ri-12345", resp.CommitmentID)
	assert.False(t, resp.DryRun)
}

// TestExecutePurchaseUnsetTimestampOmitsEffectiveDate is the regression guard
// for the fabricated start date found in review. common.PurchaseResult's
// Timestamp is a plain time.Time that not every provider client populates,
// and EffectiveDate used to be a plain string set from
// result.Timestamp.Format(time.RFC3339). Formatting the zero time.Time
// yields the literal "0001-01-01T00:00:00Z" rather than "", so `omitempty`
// could never drop it and every such response advertised a real-looking
// commitment start date in the year 1 -- a value the provider never
// reported, on a field a caller may key billing or renewal reminders off.
//
// The assertion is made against the marshaled JSON, not just the Go field,
// because the JSON payload is what actually crosses the MCP boundary to the
// caller.
func TestExecutePurchaseUnsetTimestampOmitsEffectiveDate(t *testing.T) {
	t.Parallel()
	// Timestamp deliberately left unset, exactly as a provider client that
	// never populates it leaves it.
	fake := &fakeServiceClient{
		purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-no-timestamp"},
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.EffectiveDate,
		"an unset provider timestamp must stay unknown, not become a formatted zero time")

	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "0001-01-01",
		"the zero time.Time must never reach the caller as a start date: %s", payload)
	assert.NotContains(t, string(payload), "effective_date",
		"effective_date must be omitted entirely when the provider reported none: %s", payload)
}

// TestExecutePurchaseRealTimestampIsReported is the other half of the guard
// above: suppressing the zero value must not suppress a genuine one.
func TestExecutePurchaseRealTimestampIsReported(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	fake := &fakeServiceClient{
		purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-stamped", Timestamp: stamp},
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.EffectiveDate, "a populated provider timestamp must still be reported")
	assert.Equal(t, stamp.Format(time.RFC3339), *resp.EffectiveDate)
}

// TestExecutePurchaseSameRequestDerivesSameToken proves idempotencyKeyFor
// (and therefore the derived token) is deterministic for the same
// identifying fields, so a retried call with identical arguments dedupes at
// the provider rather than double-purchasing.
func TestExecutePurchaseSameRequestDerivesSameToken(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()
	fake1 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	fake2 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}

	_, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake1, nil },
	})
	require.NoError(t, err)

	_, err = ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake2, nil },
	})
	require.NoError(t, err)

	assert.Equal(t, fake1.lastOpts.IdempotencyToken, fake2.lastOpts.IdempotencyToken)

	// A materially different request (different count) must derive a
	// different token.
	rec2 := rec
	rec2.Count = 4
	fake3 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	_, err = ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec2, DryRun: false, Confirm: true, CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake3, nil },
	})
	require.NoError(t, err)
	assert.NotEqual(t, fake1.lastOpts.IdempotencyToken, fake3.lastOpts.IdempotencyToken)
}

// TestExecutePurchaseProviderErrorSurfaced proves a provider-side purchase
// failure surfaces the full underlying error text rather than being
// swallowed.
func TestExecutePurchaseProviderErrorSurfaced(t *testing.T) {
	t.Parallel()
	fake := &fakeServiceClient{purchaseErr: errors.New("AWS API: InsufficientInstanceCapacity")}
	resolve := func(_ context.Context) (provider.ServiceClient, error) { return fake, nil }

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: resolve,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "InsufficientInstanceCapacity")
}

// TestExecutePurchaseResolveClientErrorSurfaced proves a client-resolution
// failure (e.g. bad credentials) surfaces its error text too.
func TestExecutePurchaseResolveClientErrorSurfaced(t *testing.T) {
	t.Parallel()
	resolve := func(_ context.Context) (provider.ServiceClient, error) {
		return nil, errors.New("no AWS credentials found")
	}

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region:         "us-east-1",
		Recommendation: testRecommendation(),
		DryRun:         false,
		Confirm:        true, CredentialScope: "test-scope",
		ResolveClient: resolve,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "no AWS credentials found")
}

// TestIdempotencyKeyDistinguishesSavingsPlanHourlyCommitment proves finding
// 1 of the adversarial review of the purchase feature: two Savings Plans
// requests that differ only in hourly_commitment (a $5/hr vs a $50/hr
// Compute Savings Plan) must derive different idempotency tokens. Before the
// fix, idempotencyKeyFor never consulted rec.Details at all, so these two
// materially different purchases collided on the same token and AWS would
// have silently deduped the second call as a "retry" of the first instead
// of buying a second, larger plan.
func TestIdempotencyKeyDistinguishesSavingsPlanHourlyCommitment(t *testing.T) {
	t.Parallel()

	cheapArgs := validSavingsPlansArgs()
	cheapArgs.HourlyCommitment = 5
	expensiveArgs := validSavingsPlansArgs()
	expensiveArgs.HourlyCommitment = 50

	cheapRec, region, _, _, err := savingsPlanRecommendationFromArgs(cheapArgs)
	require.NoError(t, err)
	expensiveRec, _, _, _, err := savingsPlanRecommendationFromArgs(expensiveArgs)
	require.NoError(t, err)

	cheapKey := idempotencyKeyFor(region, cheapRec, "", "")
	expensiveKey := idempotencyKeyFor(region, expensiveRec, "", "")
	assert.NotEqual(t, cheapKey, expensiveKey,
		"a $5/hr and a $50/hr Compute Savings Plan must not derive the same idempotency key")
}

// TestIdempotencyKeyDistinguishesEC2Platform proves the second half of
// finding 1: an EC2 RI purchase for Linux vs Windows, with every other field
// (region/instance_type/count/term/payment_option) identical, must not
// collide on the same idempotency key -- Platform is a price- and
// product-affecting dimension carried in rec.Details, and the pre-fix key
// derivation ignored Details entirely.
func TestIdempotencyKeyDistinguishesEC2Platform(t *testing.T) {
	t.Parallel()

	linuxArgs := validEC2Args()
	linuxArgs.Platform = "Linux/UNIX"
	windowsArgs := validEC2Args()
	windowsArgs.Platform = "Windows"

	linuxRec, linuxRegion, _, _, err := ec2RecommendationFromArgs(linuxArgs)
	require.NoError(t, err)
	windowsRec, windowsRegion, _, _, err := ec2RecommendationFromArgs(windowsArgs)
	require.NoError(t, err)

	linuxKey := idempotencyKeyFor(linuxRegion, linuxRec, "", "")
	windowsKey := idempotencyKeyFor(windowsRegion, windowsRec, "", "")
	assert.NotEqual(t, linuxKey, windowsKey,
		"a Linux and a Windows EC2 RI purchase must not derive the same idempotency key")
}

// TestIdempotencyKeySameDimensionsNoNonceAlwaysMatch is the regression guard
// for the fail-safe design: identical purchase dimensions with no nonce must
// ALWAYS derive the same key, with no dependence on time at all. This is the
// inverse of, and replaces, a prior design that folded an automatic hourly
// time bucket into the key when no nonce was supplied -- under that design a
// retry that happened to straddle an hour boundary (e.g. issued at
// 12:59:58, retried four seconds later at 13:00:02) derived a DIFFERENT key,
// so the provider could treat the retry as a brand new purchase instead of
// deduping it, resulting in a double purchase. idempotencyKeyFor no longer
// reads a clock at all when nonce is empty, so this is not merely "same
// bucket" but unconditionally the same key for the life of the process.
func TestIdempotencyKeySameDimensionsNoNonceAlwaysMatch(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()
	region := "us-east-1"

	key1 := idempotencyKeyFor(region, rec, "", "")
	key2 := idempotencyKeyFor(region, rec, "", "")
	assert.Equal(t, key1, key2,
		"identical dimensions with no nonce must always derive the same key, so a retry never double-buys")
}

// TestIdempotencyKeyNonceAuthorizesDistinctRepeat proves the nonce is the
// caller's explicit opt-in to a deliberate repeat purchase: a non-empty
// nonce derives a key different from the no-nonce key and from a different
// nonce, but the SAME nonce with the SAME dimensions still dedupes (a
// nonce'd retry is still safe against double-buying).
func TestIdempotencyKeyNonceAuthorizesDistinctRepeat(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()
	region := "us-east-1"

	noNonceKey := idempotencyKeyFor(region, rec, "", "")
	nonceAKey1 := idempotencyKeyFor(region, rec, "", "nonce-a")
	nonceAKey2 := idempotencyKeyFor(region, rec, "", "nonce-a")
	nonceBKey := idempotencyKeyFor(region, rec, "", "nonce-b")

	assert.NotEqual(t, noNonceKey, nonceAKey1,
		"supplying a nonce must authorize a purchase distinct from the no-nonce default")
	assert.NotEqual(t, nonceAKey1, nonceBKey,
		"two different nonces must derive two different keys")
	assert.Equal(t, nonceAKey1, nonceAKey2,
		"the same nonce with the same dimensions must still dedupe a nonce'd retry")
}

// TestExecutePurchaseNonceThreadedThroughToToken proves PurchaseRequest.Nonce
// is actually wired end to end into ExecutePurchase's derived token, not
// just exercised at the idempotencyKeyFor level in isolation.
func TestExecutePurchaseNonceThreadedThroughToToken(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()

	fake1 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	_, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, Nonce: "call-1", CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake1, nil },
	})
	require.NoError(t, err)

	fake2 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	_, err = ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, Nonce: "call-2", CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake2, nil },
	})
	require.NoError(t, err)

	assert.NotEqual(t, fake1.lastOpts.IdempotencyToken, fake2.lastOpts.IdempotencyToken,
		"different nonces must derive different idempotency tokens")

	fake3 := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	_, err = ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, Nonce: "call-1", CredentialScope: "test-scope",
		ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake3, nil },
	})
	require.NoError(t, err)

	assert.Equal(t, fake1.lastOpts.IdempotencyToken, fake3.lastOpts.IdempotencyToken,
		"the same nonce must derive the same idempotency token")
}

// TestIdempotencyKeyDistinguishesCredentialScope is the regression guard for
// the cross-account false-dedupe found in review. The product dimensions
// folded into the key describe WHAT is bought, never WHERE it lands, so two
// identical purchases aimed at different accounts derived the SAME token.
//
// On Azure that silently skips a real purchase rather than merely being
// imprecise: reservations.FindReservationOrderByIdempotencyToken lists
// reservation orders from the TENANT-wide endpoint (ReservationOrdersListURL
// has no subscription prefix), so buying the same VM reservation for a second
// subscription in the same tenant matched the FIRST subscription's order by
// token, short-circuited, and reported success without buying anything for
// the second subscription. This test fails on the pre-fix key, which took no
// scope argument at all.
func TestIdempotencyKeyDistinguishesCredentialScope(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()
	region := "us-east-1"

	subAKey := idempotencyKeyFor(region, rec, "subscription-a", "")
	subBKey := idempotencyKeyFor(region, rec, "subscription-b", "")
	subAKeyAgain := idempotencyKeyFor(region, rec, "subscription-a", "")

	assert.NotEqual(t, subAKey, subBKey,
		"identical purchases billed to different accounts must derive different tokens")
	assert.Equal(t, subAKey, subAKeyAgain,
		"a retry against the same account must still dedupe")
}

// TestExecutePurchaseCredentialScopeThreadedThroughToToken proves
// PurchaseRequest.CredentialScope reaches the token the provider actually
// dedupes on, not just idempotencyKeyFor in isolation.
func TestExecutePurchaseCredentialScopeThreadedThroughToToken(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()

	purchaseInScope := func(scope string) *fakeServiceClient {
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
		_, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true,
			CredentialScope: scope,
			ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
		require.NoError(t, err)
		return fake
	}

	subA := purchaseInScope("subscription-a")
	subB := purchaseInScope("subscription-b")
	subARetry := purchaseInScope("subscription-a")

	assert.NotEqual(t, subA.lastOpts.IdempotencyToken, subB.lastOpts.IdempotencyToken,
		"different credential scopes must derive different idempotency tokens")
	assert.Equal(t, subA.lastOpts.IdempotencyToken, subARetry.lastOpts.IdempotencyToken,
		"the same credential scope must derive the same idempotency token")
}

// TestCredentialScopeResolution pins CredentialScope's precedence: an
// explicit caller-supplied override always wins, an ambient environment
// variable is the fallback (matching what the provider factory itself
// consults), whitespace-only input counts as absent, and "" is a legitimate
// result rather than an error (see the CredentialScope doc comment).
func TestCredentialScopeResolution(t *testing.T) {
	const envVar = "CUDLY_TEST_SUBSCRIPTION_ID"

	t.Run("explicit override wins over the environment", func(t *testing.T) {
		t.Setenv(envVar, "from-env")
		assert.Equal(t, "explicit", CredentialScope("explicit", envVar))
	})

	t.Run("falls back to the environment when no override is given", func(t *testing.T) {
		t.Setenv(envVar, "from-env")
		assert.Equal(t, "from-env", CredentialScope("", envVar))
	})

	t.Run("whitespace-only values count as absent", func(t *testing.T) {
		t.Setenv(envVar, "  ")
		assert.Empty(t, CredentialScope("   ", envVar))
	})

	t.Run("surrounding whitespace is trimmed so it cannot fork the key", func(t *testing.T) {
		assert.Equal(t, "sub-a", CredentialScope(" sub-a "))
	})

	t.Run("empty when neither override nor environment is set", func(t *testing.T) {
		assert.Empty(t, CredentialScope("", envVar))
	})
}

// TestExecutePurchaseAuditLogging pins the MCP server's only record of a
// real purchase. The CLI path emits a common.AuditRecord per purchase
// (cmd/multi_service.go) and the web path persists a purchase_executions row
// carrying the approval history; this server has neither, so before these
// log lines an operator asking "what did the assistant actually buy?" had
// nothing at all to read.
//
// It also pins that a preview stays silent (it contacts no provider and
// spends nothing, so logging every dry run would bury the real purchases)
// and that the idempotency token is masked rather than written in full.
func TestExecutePurchaseAuditLogging(t *testing.T) {
	// Not parallel: this test swaps the shared standard-logger output.
	capture := func(fn func()) string {
		var buf bytes.Buffer
		prevOut, prevFlags := log.Writer(), log.Flags()
		log.SetOutput(&buf)
		log.SetFlags(0)
		defer func() {
			log.SetOutput(prevOut)
			log.SetFlags(prevFlags)
		}()
		fn()
		return buf.String()
	}

	rec := testRecommendation()

	t.Run("a preview logs nothing", func(t *testing.T) {
		out := capture(func() {
			_, err := ExecutePurchase(context.Background(), PurchaseRequest{
				Region: "us-east-1", Recommendation: rec, DryRun: true,
			})
			require.NoError(t, err)
		})
		assert.Empty(t, out, "a dry run spends nothing and must not pollute the purchase audit trail")
	})

	t.Run("a real purchase logs the attempt and the outcome", func(t *testing.T) {
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-abc123"}}
		out := capture(func() {
			_, err := ExecutePurchase(context.Background(), PurchaseRequest{
				Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true,
				CredentialScope: "subscription-a",
				ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
			})
			require.NoError(t, err)
		})

		assert.Contains(t, out, "mcp purchase ATTEMPT")
		assert.Contains(t, out, "mcp purchase OK")
		assert.Contains(t, out, "subscription-a", "the audit line must record which account was billed")
		assert.Contains(t, out, "ri-abc123", "the audit line must record the resulting commitment ID")

		token := fake.lastOpts.IdempotencyToken
		require.NotEmpty(t, token)
		assert.NotContains(t, out, token, "the full idempotency token must never be logged")
		assert.Contains(t, out, common.MaskToken(token), "the masked token must be logged to correlate attempt with outcome")
	})

	t.Run("a failed purchase logs the failure", func(t *testing.T) {
		fake := &fakeServiceClient{purchaseErr: errors.New("insufficient capacity")}
		out := capture(func() {
			_, err := ExecutePurchase(context.Background(), PurchaseRequest{
				Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
				ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
			})
			require.Error(t, err)
		})
		assert.Contains(t, out, "mcp purchase FAILED")
		assert.Contains(t, out, "insufficient capacity")
	})

	// TestExecutePurchaseAuditLogging/a_provider-reported_failure_is_never_
	// logged_as_OK is the regression guard for the audit-line bug found in
	// review: PurchaseCommitment can return a nil Go error alongside
	// PurchaseResult{Success: false, Error: nil} (the provider ran the call
	// but reports it did not actually buy anything), and before this fix that
	// combination fell through to the "mcp purchase OK" line because
	// logPurchaseOutcome only inspected the (nil) error, never
	// result.Success.
	t.Run("a provider-reported failure is never logged as OK", func(t *testing.T) {
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: false, Error: nil}}
		out := capture(func() {
			_, err := ExecutePurchase(context.Background(), PurchaseRequest{
				Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
				ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
			})
			require.NoError(t, err, "a provider-reported failure surfaces via the response, not a Go error")
		})
		assert.Contains(t, out, "mcp purchase FAILED",
			"Success=false must be logged as a failure even with a nil result.Error")
		assert.NotContains(t, out, "mcp purchase OK")
	})
}

// TestResolveDryRunConfirm pins the shared default resolution now used by
// every purchase tool. This is the gate that decides whether real money
// moves, and it was previously hand-copied into seven files; the single
// most important property is that an OMITTED dry_run means preview, never
// execute.
func TestResolveDryRunConfirm(t *testing.T) {
	t.Parallel()
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name        string
		dryRun      *bool
		confirm     *bool
		wantDryRun  bool
		wantConfirm bool
	}{
		{"both omitted defaults to preview", nil, nil, true, false},
		// The safety-critical row: confirm=true alone must NOT execute.
		// decidePurchaseMode then sees dryRun=true and previews.
		{"omitted dry_run stays preview even when confirmed", nil, ptr(true), true, true},
		{"explicit false dry_run is honored", ptr(false), ptr(true), false, true},
		{"explicit true dry_run is honored", ptr(true), ptr(false), true, false},
		{"explicit false confirm is honored", ptr(false), ptr(false), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotDryRun, gotConfirm := ResolveDryRunConfirm(tc.dryRun, tc.confirm)
			assert.Equal(t, tc.wantDryRun, gotDryRun)
			assert.Equal(t, tc.wantConfirm, gotConfirm)
		})
	}
}

// TestArcheraOfferOnlyAfterSuccessfulRealPurchase pins when the Archera
// underutilization-insurance offer is attached, and that it always carries
// both partnership disclosures.
//
// The offer must appear ONLY after a real purchase actually succeeds: a dry
// run bought nothing and a failed purchase bought nothing, so in both cases
// there is no commitment to insure and no enrollment window running. This
// mirrors the CLI, which calls printArcheraPitch only under `riSuccess > 0`.
//
// Both disclosures are asserted because CUDly commits to surfacing the
// sponsorship AND the works-fine-without-it fact everywhere the signup link
// appears. An MCP client renders this payload through a model, so a link
// without its disclosures would let a sponsored recommendation be presented
// as a neutral one.
func TestArcheraOfferOnlyAfterSuccessfulRealPurchase(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()

	t.Run("a dry run carries no offer", func(t *testing.T) {
		t.Parallel()
		resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: true,
		})
		require.NoError(t, err)
		assert.Nil(t, resp.Archera, "nothing was bought, so there is nothing to insure")

		raw, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "archera",
			"the offer must be omitted from the payload entirely, not sent empty")
	})

	t.Run("a failed purchase carries no offer", func(t *testing.T) {
		t.Parallel()
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{
			Success: false,
			Error:   errors.New("insufficient capacity"),
		}}
		resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
			ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Nil(t, resp.Archera, "a purchase that did not happen has no enrollment window")
	})

	t.Run("a successful real purchase carries the offer and both disclosures", func(t *testing.T) {
		t.Parallel()
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{
			Success:      true,
			CommitmentID: "ri-abc123",
		}}
		resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true, CredentialScope: "test-scope",
			ResolveClient: func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
		require.NoError(t, err)
		require.NotNil(t, resp.Archera, "a completed purchase must surface the insurance option")

		assert.Equal(t, common.ArcheraSignupURL, resp.Archera.SignupURL)
		assert.Equal(t, common.ArcheraEnrollmentWindowDays, resp.Archera.EnrollmentWindowDays)
		assert.NotEmpty(t, resp.Archera.Pitch)

		// The two disclosures CUDly commits to keeping visible wherever the
		// signup link is shown.
		assert.Equal(t, common.ArcheraNonGatingDisclosure, resp.Archera.NonGatingDisclosure)
		assert.Equal(t, common.ArcheraSponsorshipDisclosure, resp.Archera.SponsorshipDisclosure)
		assert.Contains(t, resp.Archera.NonGatingDisclosure, "work fully without Archera")
		assert.Contains(t, resp.Archera.SponsorshipDisclosure, "sponsors")

		// Whatever a client renders, the link never travels without them.
		raw, err := json.Marshal(resp)
		require.NoError(t, err)
		body := string(raw)
		require.Contains(t, body, common.ArcheraSignupURL)
		assert.Contains(t, body, "non_gating_disclosure")
		assert.Contains(t, body, "sponsorship_disclosure")
	})
}

// TestExecutePurchaseAmbientScopeCannotDoubleBuy is the regression guard for
// the credential-scope aliasing double-purchase found in the independent
// re-review of #1495.
//
// idempotencyKeyFor folds CredentialScope into the token, and CredentialScope
// returns "" when neither the explicit argument nor the ambient environment
// variable is set. So the SAME target account reached two ways derived two
// DIFFERENT tokens: omitting azure_subscription_id (ambient resolves to
// sub-X) gave "", while passing azure_subscription_id="sub-X" gave "sub-X".
// Every provider's dedupe is token-keyed -- Azure's
// FindReservationOrderByIdempotencyToken, GCP's idempotentCommitmentName, and
// on AWS the EC2/Redshift tag lookups, the RDS/ElastiCache/OpenSearch/MemoryDB
// idempotencyGuard, and Savings Plans' ClientToken -- so the second call's
// lookup missed and bought a SECOND commitment.
//
// The sequence below is the realistic one, not a contrived edge: a model
// previews or purchases without naming the account, then re-calls the same
// purchase with the account explicit (self-correction, or a retry after a
// timeout). Before the fix that bought twice.
//
// The earlier review reasoned only about the opposite direction (one token
// spanning two accounts) and concluded an empty scope was safe because a
// purely-ambient provider is unambiguous for the process lifetime. That is
// true and irrelevant: the hazard is two tokens for ONE account.
func TestExecutePurchaseAmbientScopeCannotDoubleBuy(t *testing.T) {
	t.Parallel()
	rec := testRecommendation()
	realPurchase := func(scope string, fake *fakeServiceClient) (*PurchaseResponse, error) {
		return ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true,
			CredentialScope: scope,
			ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
	}

	t.Run("explicit account then the same account omitted does not buy twice", func(t *testing.T) {
		t.Parallel()
		// Call 1: the account named explicitly. This is a real purchase.
		first := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-1"}}
		resp, err := realPurchase("sub-X", first)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, 1, first.purchaseCalls)

		// Call 2: identical purchase, but the account left to ambient
		// credentials. It resolves to the SAME sub-X, so this must not
		// become a second commitment.
		second := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-2"}}
		_, err = realPurchase("", second)

		require.Error(t, err, "an undeterminable account must not reach the provider on a real purchase")
		assert.Equal(t, 0, second.purchaseCalls,
			"the second call must not purchase: it targets the same account as the first and would double-buy")
		assert.Contains(t, err.Error(), "aws_profile",
			"the refusal must name the argument the caller has to pass")
	})

	t.Run("a real purchase with no determinable account is refused before any provider call", func(t *testing.T) {
		t.Parallel()
		resolveCalled := false
		_, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true,
			CredentialScope: "",
			ResolveClient: func(_ context.Context) (provider.ServiceClient, error) {
				resolveCalled = true
				return nil, errors.New("ResolveClient must not be called when the scope is undeterminable")
			},
		})
		require.Error(t, err)
		assert.False(t, resolveCalled, "the refusal must not resolve credentials")
	})

	t.Run("whitespace-only scope counts as absent", func(t *testing.T) {
		t.Parallel()
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
		_, err := realPurchase("   ", fake)
		require.Error(t, err)
		assert.Equal(t, 0, fake.purchaseCalls)
	})

	t.Run("a dry run is unaffected and needs no account", func(t *testing.T) {
		t.Parallel()
		resolveCalled := false
		resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: true, Confirm: true,
			CredentialScope: "",
			ResolveClient: func(_ context.Context) (provider.ServiceClient, error) {
				resolveCalled = true
				return nil, errors.New("dry run must not resolve a client")
			},
		})
		require.NoError(t, err, "previewing must not require naming an account")
		require.NotNil(t, resp)
		assert.True(t, resp.DryRun)
		assert.False(t, resolveCalled)
	})
}

// TestExecutePurchaseExplicitAndAmbientAccountDedupe is the other half of the
// invariant TestExecutePurchaseAmbientScopeCannotDoubleBuy guards. Refusing an
// undeterminable account removes the aliasing only if the two ways of naming a
// KNOWN account still converge: CredentialScope falls back to the same
// environment variable the provider factory itself consults, so passing
// aws_profile="prod" and inheriting AWS_PROFILE=prod must derive ONE token.
// Without this, the fix would merely move the aliasing rather than remove it.
//
// Deliberately not parallel: t.Setenv cannot be used by a test with a parallel
// ancestor.
func TestExecutePurchaseExplicitAndAmbientAccountDedupe(t *testing.T) {
	const envVar = "CUDLY_TEST_ALIAS_PROFILE"
	t.Setenv(envVar, "prod")
	rec := testRecommendation()

	realPurchase := func(scope string, fake *fakeServiceClient) error {
		_, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: rec, DryRun: false, Confirm: true,
			CredentialScope: scope,
			ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
		return err
	}

	explicit := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	require.NoError(t, realPurchase(CredentialScope("prod", envVar), explicit))

	ambient := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true}}
	require.NoError(t, realPurchase(CredentialScope("", envVar), ambient))

	assert.Equal(t, explicit.lastOpts.IdempotencyToken, ambient.lastOpts.IdempotencyToken,
		"naming the account explicitly and inheriting it from the environment target the same account and must dedupe")
}

// TestCredentialScopeArgNamesEveryProvider pins that every provider a purchase
// tool can build a Recommendation for has a scope argument to name in the
// refusal, and that an unrecognized provider is an explicit internal error
// rather than a refusal the caller cannot act on.
func TestCredentialScopeArgNamesEveryProvider(t *testing.T) {
	t.Parallel()
	for prov, want := range map[common.ProviderType]string{
		common.ProviderAWS:   "aws_profile",
		common.ProviderAzure: "azure_subscription_id",
		common.ProviderGCP:   "gcp_project_id",
	} {
		got, err := credentialScopeSourceFor(prov)
		require.NoErrorf(t, err, "provider %q must have a scope source", prov)
		assert.Equal(t, want, got.arg)
	}

	_, err := credentialScopeSourceFor(common.ProviderType("nimbus"))
	require.Error(t, err, "an unknown provider must fail loud rather than emit an unactionable refusal")
}

// TestRequireCredentialScopeGCPMessageIsActionable pins the GCP contract found
// by review after the fail-closed gate landed.
//
// gcp_computeengine_cud.go calls CredentialScope(args.GCPProjectID) with no
// environment fallback, so omitting gcp_project_id ALWAYS yields "" and the
// gate refuses every real GCP purchase. That is the intended contract, not a
// bug -- providers/gcp reads no project environment variable, and with none
// configured it falls back to getDefaultProject, i.e. the first ACTIVE project
// in the caller's ListProjects response. Spending money in "whichever project
// happened to be listed first" is not a defensible default, and an
// env-supplied scope would be worse still: it could name project A while the
// purchase landed in project B, making the token assert an account it never
// touched.
//
// Since the refusal is the contract, the message has to be actionable. Telling
// a GCP caller their account "could not be determined" would send them looking
// for an environment variable that does not exist, so GCP gets a plain
// "required" instead.
func TestRequireCredentialScopeGCPMessageIsActionable(t *testing.T) {
	t.Parallel()

	gcpErr := requireCredentialScope(common.ProviderGCP, "")
	require.Error(t, gcpErr)
	assert.Contains(t, gcpErr.Error(), "gcp_project_id", "the refusal must name the argument to pass")
	assert.Contains(t, gcpErr.Error(), "is required",
		"GCP has no environment fallback, so the message must say the argument is required")
	assert.NotContains(t, gcpErr.Error(), "could not be determined",
		"that phrasing implies an ambient source exists, sending a GCP caller after a variable nothing reads")

	// Providers that DO have a fallback must still name it, so a caller who
	// set the environment variable is not told to pass an argument they do
	// not need.
	for _, tc := range []struct {
		provider common.ProviderType
		arg      string
		envVar   string
	}{
		{common.ProviderAWS, "aws_profile", "AWS_PROFILE"},
		{common.ProviderAzure, "azure_subscription_id", "AZURE_SUBSCRIPTION_ID"},
	} {
		err := requireCredentialScope(tc.provider, "")
		require.Errorf(t, err, "provider %q", tc.provider)
		assert.Containsf(t, err.Error(), tc.arg, "provider %q must name its argument", tc.provider)
		assert.Containsf(t, err.Error(), tc.envVar,
			"provider %q has an ambient fallback and must name it", tc.provider)
	}
}
