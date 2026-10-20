package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spHookFailing answers the Savings Plan calls like spHook, but fails the
// CreateSavingsPlan call for the given offering IDs. Every other call (the
// EC2 RI API included) gets a 400, so an RI purchase through it fails.
func spHookFailing(failOfferings ...string) *hookTransport {
	h := &hookTransport{}
	h.handle = func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CreateSavingsPlan"):
			body, _ := io.ReadAll(r.Body)
			for _, id := range failOfferings {
				if strings.Contains(string(body), `"`+id+`"`) {
					return http.StatusBadRequest, `{"__type":"ValidationException","message":"offering rejected"}`
				}
			}
			return http.StatusOK, `{"savingsPlanId":"sp-111"}`
		case strings.HasSuffix(r.URL.Path, "/DescribeSavingsPlans"):
			return http.StatusOK, `{"savingsPlans":[]}`
		}
		return http.StatusBadRequest, `{"__type":"ValidationException","message":"unexpected call"}`
	}
	return h
}

func TestExecuteAndReport_SavingsPlanFailureFailsRun(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing("o-2")
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.ErrorIs(t, err, errPurchasesFailed)
	assert.NotErrorIs(t, err, errAuditTrailLost)
	assert.Contains(t, err.Error(), "1 of 2")
	assert.Equal(t, 2, h.count("/CreateSavingsPlan"))
	assert.Contains(t, out.String(), "Purchases: 1 succeeded, 1 failed")
	assert.Contains(t, out.String(), "1 of 2 purchase(s) failed")
}

func TestExecuteAndReport_AllSavingsPlansFailFailsRun(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing("o-1", "o-2")
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.ErrorIs(t, err, errPurchasesFailed)
	assert.Contains(t, out.String(), "Purchases: 0 succeeded, 2 failed")
	assert.NotContains(t, out.String(), "Purchase operations completed")
}

func TestExecuteAndReport_SavingsPlanAndRIFailuresCountedTogether(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing("o-2")
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2), validEC2Rec()}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.ErrorIs(t, err, errPurchasesFailed)
	assert.Contains(t, err.Error(), "2 of 3")
	assert.Contains(t, out.String(), "Purchases: 1 succeeded, 2 failed")
	assert.Contains(t, out.String(), "Overall success rate: 33.3%")
}

func TestExecuteAndReport_AllSavingsPlansSucceedReturnsNil(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing()
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "Purchases: 2 succeeded, 0 failed")
	assert.Contains(t, out.String(), "Purchase operations completed")
	assert.NotContains(t, out.String(), "purchase(s) failed")
}

func TestExecuteAndReport_DryRunPreconditionFailureReturnsNil(t *testing.T) {
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing()
	bad := offeringSP("o-1", 1)
	bad.Details = nil // fails the purchase preconditions

	err := executeAndReport(context.Background(), hookedAWSConfig(h), []common.Recommendation{bad}, true, "run-1", cfg, nil)

	require.NoError(t, err, "a dry run buys nothing, so it does not fail the run")
	assert.Zero(t, h.count("/CreateSavingsPlan"))
	assert.Contains(t, out.String(), "cannot purchase", "the precondition failure is still reported")
}

func TestRunCSVPurchases_SavingsPlanFailureFailsRun(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing("o-2")
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := runCSVPurchases(context.Background(), hookedAWSConfig(h), recs, false, cfg, "run-1")

	require.ErrorIs(t, err, errPurchasesFailed)
	assert.Contains(t, err.Error(), "1 of 2")
	assert.Contains(t, out.String(), "Purchases: 1 succeeded, 1 failed")
}

func TestRunCSVPurchases_AllSucceedReturnsNil(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	h := spHookFailing()
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := runCSVPurchases(context.Background(), hookedAWSConfig(h), recs, false, cfg, "run-1")

	require.NoError(t, err)
	assert.Contains(t, out.String(), "Purchases: 2 succeeded, 0 failed")
}

func TestPrintMultiServiceSummary_CountsSavingsPlanFailures(t *testing.T) {
	stats := map[common.ServiceType]ServiceProcessingStats{
		common.ServiceEC2:                     {Service: common.ServiceEC2, RecommendationsSelected: 2, SuccessfulPurchases: 1, FailedPurchases: 1},
		common.ServiceSavingsPlansCompute:     {Service: common.ServiceSavingsPlansCompute, RecommendationsSelected: 2, FailedPurchases: 2},
		common.ServiceSavingsPlansEC2Instance: {Service: common.ServiceSavingsPlansEC2Instance, RecommendationsSelected: 1, SuccessfulPurchases: 1},
	}

	output := captureAppOutput(t, func() {
		printMultiServiceSummary(nil, nil, stats, false)
	})

	assert.Contains(t, output, "Purchases: 2 succeeded, 3 failed")
	assert.Contains(t, output, "Overall success rate: 40.0%")
	assert.Contains(t, output, "3 of 5 purchase(s) failed")
}

func TestPrintFinalMessage_SavingsPlanOnlySuccessShowsCompletion(t *testing.T) {
	output := captureAppOutput(t, func() {
		printMultiServiceSummary(nil, nil, map[common.ServiceType]ServiceProcessingStats{
			common.ServiceSavingsPlansCompute: {Service: common.ServiceSavingsPlansCompute, RecommendationsSelected: 1, SuccessfulPurchases: 1},
		}, false)
	})

	assert.Contains(t, output, "Purchase operations completed")
	assert.NotContains(t, output, "purchase(s) failed")
}

func TestPurchaseFailureError(t *testing.T) {
	t.Parallel()
	ok := common.PurchaseResult{Success: true}
	bad := common.PurchaseResult{Success: false}
	assert.NoError(t, purchaseFailureError(nil, false))
	assert.NoError(t, purchaseFailureError([]common.PurchaseResult{ok, ok}, false))
	assert.NoError(t, purchaseFailureError([]common.PurchaseResult{bad}, true), "dry run")
	err := purchaseFailureError([]common.PurchaseResult{ok, bad, bad}, false)
	require.ErrorIs(t, err, errPurchasesFailed)
	assert.Contains(t, err.Error(), "2 of 3")
}

// riHint is the RI-propagation hint; it applies only when an RI was bought.
const riHint = "Allow up to 15 minutes for RIs to appear"

func TestPrintMultiServiceSummary_RIHintOnlyAfterRISuccess(t *testing.T) {
	ri := ServiceProcessingStats{Service: common.ServiceEC2, RecommendationsSelected: 1, SuccessfulPurchases: 1}
	riFailed := ServiceProcessingStats{Service: common.ServiceEC2, RecommendationsSelected: 1, FailedPurchases: 1}
	sp := ServiceProcessingStats{Service: common.ServiceSavingsPlansCompute, RecommendationsSelected: 1, SuccessfulPurchases: 1}
	tests := []struct {
		name     string
		stats    map[common.ServiceType]ServiceProcessingStats
		isDryRun bool
		wantHint bool
	}{
		{"savings plan only", map[common.ServiceType]ServiceProcessingStats{common.ServiceSavingsPlansCompute: sp}, false, false},
		{"savings plan success with failed RI", map[common.ServiceType]ServiceProcessingStats{common.ServiceSavingsPlansCompute: sp, common.ServiceEC2: riFailed}, false, false},
		{"RI only", map[common.ServiceType]ServiceProcessingStats{common.ServiceEC2: ri}, false, true},
		{"RI and savings plan", map[common.ServiceType]ServiceProcessingStats{common.ServiceSavingsPlansCompute: sp, common.ServiceEC2: ri}, false, true},
		{"dry run", map[common.ServiceType]ServiceProcessingStats{common.ServiceEC2: ri}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureAppOutput(t, func() {
				printMultiServiceSummary(nil, nil, tt.stats, tt.isDryRun)
			})
			if tt.wantHint {
				assert.Contains(t, output, riHint)
			} else {
				assert.NotContains(t, output, riHint)
			}
			if tt.isDryRun {
				assert.Contains(t, output, "run with --purchase flag")
				assert.NotContains(t, output, "Purchase operations completed")
			} else {
				assert.Contains(t, output, "Purchase operations completed")
			}
		})
	}
}
