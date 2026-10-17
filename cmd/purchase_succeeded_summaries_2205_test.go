package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func succeededWithError() common.PurchaseResult {
	return common.PurchaseResult{
		Success:      true,
		Error:        errors.New("x"),
		CommitmentID: "id-1",
		Recommendation: common.Recommendation{
			Service:      common.ServiceEC2,
			ResourceType: "m5.large",
		},
	}
}

func TestPrintInterruptedSummary_SuccessWithErrorIsListed(t *testing.T) {
	ok := common.PurchaseResult{Success: true}
	bad := common.PurchaseResult{Success: false, Error: errors.New("boom")}
	tests := []struct {
		name    string
		results []common.PurchaseResult
		want    bool
	}{
		{"success true with error", []common.PurchaseResult{succeededWithError()}, true},
		{"ordinary success", []common.PurchaseResult{ok}, false},
		{"success false with error", []common.PurchaseResult{bad}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureAppOutput(t, func() { printInterruptedSummary(1, 2, tt.results, false) })
			assert.Equal(t, tt.want, containsVerify(out), out)
		})
	}
}

func containsVerify(s string) bool {
	return strings.Contains(s, "verify in AWS before re-running")
}

func TestAuditStopMessage_SuccessWithErrorReportsError(t *testing.T) {
	msg := auditStopMessage(1, 2, []common.PurchaseResult{succeededWithError()}, errors.New("cause"))
	assert.Contains(t, msg, "(errored: x)")
	assert.NotContains(t, msg, "(commitment")

	ok := common.PurchaseResult{Success: true, CommitmentID: "id-2"}
	assert.Contains(t, auditStopMessage(1, 2, []common.PurchaseResult{ok}, errors.New("c")), "(commitment id-2)")
	bad := common.PurchaseResult{Success: false, Error: errors.New("y")}
	assert.Contains(t, auditStopMessage(1, 2, []common.PurchaseResult{bad}, errors.New("c")), "(errored: y)")
}

func TestCalculateServiceStats_SuccessWithErrorCountsFailed(t *testing.T) {
	results := []common.PurchaseResult{
		{Success: true},
		succeededWithError(),
		{Success: false},
		{Success: false, Error: errors.New("z")},
	}
	stats := calculateServiceStats(common.ServiceEC2, nil, results)
	assert.Equal(t, 1, stats.SuccessfulPurchases)
	assert.Equal(t, 3, stats.FailedPurchases)
}

func TestPurchaseFailureError_SuccessWithErrorIsFailure(t *testing.T) {
	err := purchaseFailureError([]common.PurchaseResult{{Success: true}, succeededWithError()}, false)
	assert.ErrorIs(t, err, errPurchasesFailed)
	assert.Contains(t, err.Error(), "1 of 2")
}
