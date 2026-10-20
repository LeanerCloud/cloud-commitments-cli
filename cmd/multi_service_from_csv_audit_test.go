package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestProcessPurchaseLoop_WritesAuditRecordForRealPurchase reproduces the
// other half of #1609 at the processPurchaseLoop level: a real (non-dry-run)
// purchase attempt must be audited as "success" or "error", never silently
// dropped. createServiceClient is not injectable (see the comment on
// TestApplyMinCountFloorAfterDuplicateAdjustment), so this exercises
// processPurchaseLoop directly with a mocked provider.ServiceClient rather
// than through runToolFromCSV -- the same technique
// TestProcessPurchaseLoopActualPurchase already uses to test this loop's
// purchase behavior without live AWS credentials.
func TestProcessPurchaseLoop_WritesAuditRecordForRealPurchase(t *testing.T) {
	useCallerAccount(t)
	tests := []struct {
		result     common.PurchaseResult
		name       string
		wantStatus string
	}{
		{
			name:       "success",
			result:     common.PurchaseResult{Success: true, CommitmentID: "test-purchase-id", Timestamp: time.Now()},
			wantStatus: "success",
		},
		{
			name:       "error",
			result:     common.PurchaseResult{Success: false, Error: fmt.Errorf("API error: quota exceeded"), Timestamp: time.Now()},
			wantStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			origCfg := toolCfg
			defer func() { toolCfg = origCfg }()
			toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
			t.Setenv("DISABLE_PURCHASE_DELAY", "true")

			recs := []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 2, EstimatedSavings: 100},
			}
			result := tt.result
			result.Recommendation = recs[0]

			mockClient := &MockServiceClient{}
			mockClient.On("PurchaseCommitment", mock.Anything, recs[0], mock.MatchedBy(func(o common.PurchaseOptions) bool { return o.Source == common.PurchaseSourceCLI })).Return(result, nil)

			processPurchaseLoop(ctx, recs, "us-east-1", false /* isDryRun */, mockClient, toolCfg, "test-run-id")

			data, readErr := os.ReadFile(toolCfg.AuditLog) // #nosec G304 -- test-owned tempdir path
			require.NoError(t, readErr, "a real purchase attempt must write an audit log")
			require.NotEmpty(t, data, "the audit log must not be empty -- an empty file would make the next length check pass vacuously")

			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.Len(t, lines, 1, "one audit record per recommendation")

			var rec map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
			assert.Equal(t, "test-run-id", rec["run_id"])
			assert.Equal(t, common.PurchaseSourceCLI, rec["source"])
			assert.Equal(t, false, rec["dry_run"])
			assert.Equal(t, tt.wantStatus, rec["status"])

			mockClient.AssertExpectations(t)
		})
	}
}

// TestRunToolFromCSV_ChecksAuditLogWritability reproduces the second half of
// #1609: the --input-csv path never verified the audit log was writable
// before touching AWS, unlike the non-CSV path (CheckAuditLogWritable in
// runToolMultiService). An unwritable audit-log directory must be rejected
// before the CSV is even read, on both dry-run and purchase invocations.
func TestRunToolFromCSV_ChecksAuditLogWritability(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111
`)

	toolCfg.CSVInput = csvPath
	toolCfg.AuditLog = filepath.Join(t.TempDir(), "does-not-exist-dir", "audit.jsonl")
	toolCfg.ActualPurchase = false

	err := runToolFromCSV(context.Background(), toolCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit log")
}

// TestRunToolFromCSV_ConfirmsOnceBeforeAnyRegion reproduces #1610 through the
// public --input-csv entry point. Before the fix, confirmation lived inside
// processPurchaseLoop and was asked once per (service, region), after that
// region's AWS config, service client and duplicate check; declining only
// canceled that region. Now prepareCSVPurchaseRun asks exactly once, against
// the whole post-filter set (both regions here: 5 instances), before any AWS
// call. stdin is not a terminal in tests, so ConfirmPurchase declines without
// blocking, which is the "operator declines" and non-interactive fail-closed
// path at once.
func TestRunToolFromCSV_ConfirmsOnceBeforeAnyRegion(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	isolateAWSEnv(t)

	var appOut, stdOut bytes.Buffer
	oldLogger := AppLogger
	AppLogger = log.New(&appOut, "", 0)
	t.Cleanup(func() { AppLogger = oldLogger })
	oldWriter := log.Writer()
	log.SetOutput(&stdOut)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111
rds,us-west-2,db.t3.medium,postgres,3,1yr,all-upfront,123456789012
`)
	reportPath := filepath.Join(t.TempDir(), "report.csv")
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")

	toolCfg.CSVInput = csvPath
	toolCfg.CSVOutput = reportPath
	toolCfg.AuditLog = auditPath
	toolCfg.ActualPurchase = true
	toolCfg.Coverage = 100.0
	toolCfg.TargetCoverage = 0
	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 0

	err := runToolFromCSV(context.Background(), toolCfg)
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(stdOut.String(), "stdin is not a terminal"),
		"the purchase must be confirmed exactly once for the whole run, not per region")
	assert.Contains(t, appOut.String(), "Purchase canceled.")
	assert.NotContains(t, appOut.String(), "Region:",
		"a declined confirmation must abort before any region is processed")

	_, statErr := os.Stat(reportPath)
	assert.True(t, os.IsNotExist(statErr), "a declined run must write no purchase report")
	data, readErr := os.ReadFile(auditPath) // #nosec G304 -- test-owned tempdir path
	if readErr == nil {
		assert.Empty(t, data, "a declined run attempts no purchase, so it writes no audit record")
	} else {
		assert.True(t, os.IsNotExist(readErr), "unexpected audit log read error: %v", readErr)
	}
}
