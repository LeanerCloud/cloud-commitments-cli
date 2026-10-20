package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunToolFromCSV(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	awsHits := isolateAWSEnv(t)

	// Column names match writeMultiServiceCSVReport's header (the real
	// round-trip shape). The previous fixture used headers the parser
	// silently ignored, so every row loaded with Count=0 and the whole run
	// processed nothing while the NotPanics assertion stayed green.
	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111
elasticache,us-west-2,cache.t3.micro,redis,1,1yr,all-upfront,111111111111
`)

	tests := []struct {
		name              string
		coverage          float64
		wantResourceTypes []string
		wantCounts        []string
	}{
		{
			// 100% coverage keeps both recommendations at their CSV counts.
			name:              "Dry run writes a report row per recommendation",
			coverage:          100.0,
			wantResourceTypes: []string{"db.t3.small", "cache.t3.micro"},
			wantCounts:        []string{"2", "1"},
		},
		{
			// 50% coverage halves db.t3.small (2 -> 1) and drops
			// cache.t3.micro (1 -> 0).
			name:              "Coverage sizing is applied before the purchase loop",
			coverage:          50.0,
			wantResourceTypes: []string{"db.t3.small"},
			wantCounts:        []string{"1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "report.csv")
			toolCfg.CSVInput = csvPath
			toolCfg.CSVOutput = reportPath
			toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
			toolCfg.ActualPurchase = false
			toolCfg.Coverage = tt.coverage
			toolCfg.TargetCoverage = 0
			toolCfg.MaxInstances = 0
			toolCfg.OverrideCount = 0

			err := runToolFromCSV(context.Background(), toolCfg)
			require.NoError(t, err)

			header, rows := readPurchaseReport(t, reportPath)
			assert.ElementsMatch(t, tt.wantResourceTypes, reportColumn(t, header, rows, "ResourceType"))
			assert.ElementsMatch(t, tt.wantCounts, reportColumn(t, header, rows, "Count"))
			for _, success := range reportColumn(t, header, rows, "Success") {
				assert.Equal(t, "true", success, "dry-run purchases must be recorded as successful")
			}
		})
	}
	// Without a request the env wiring in isolateAWSEnv would be vacuous.
	assert.Positive(t, awsHits.Load(), "isolateAWSEnv's local endpoint must have served the duplicate checks")
}

// TestRunToolFromCSV_DuplicateCheckFailureRefusesToPurchase reproduces #1941:
// on a real purchase run (--input-csv ... --purchase), a failed duplicate
// check (throttling, a describe-call AccessDenied, a transient 5xx) must not
// fall back to purchasing the un-deduplicated counts. isolateAWSEnv points
// the SDK at invalid credentials so GetExistingCommitments fails the same way
// a real AWS error would, and the region must be skipped entirely rather than
// purchased with the original (potentially duplicate) counts. Before the fix,
// adjustRecsForDuplicates' error was logged as a warning and adjustedRecs was
// reset to the original recs, so the region proceeded into the purchase loop
// unchanged.
func TestRunToolFromCSV_DuplicateCheckFailureRefusesToPurchase(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	isolateAWSEnv(t)

	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111
`)
	reportPath := filepath.Join(t.TempDir(), "report.csv")
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")

	toolCfg.CSVInput = csvPath
	toolCfg.CSVOutput = reportPath
	toolCfg.AuditLog = auditPath
	toolCfg.ActualPurchase = true // the money path: a duplicate-check failure here must fail closed
	toolCfg.Coverage = 100.0
	toolCfg.TargetCoverage = 0
	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 0

	err := runToolFromCSV(context.Background(), toolCfg)
	require.NoError(t, err)

	// The region is skipped before executePurchase or processPurchaseLoop
	// ever run, so no PurchaseResult is produced and no report is written
	// (writeMultiServiceCSVReport no-ops on an empty results slice). Before
	// the fix, the un-deduplicated recommendation reached the purchase loop
	// and a report row (attempting a real AWS purchase call) was produced.
	_, statErr := os.Stat(reportPath)
	assert.True(t, os.IsNotExist(statErr), "a failed duplicate check on a purchase run must skip the region entirely, not attempt to purchase the un-deduplicated counts")
}

// TestRunToolFromCSV_NonExistentFile asserts the unreadable-input error path
// surfaces as an error (previously log.Fatalf, which made it untestable).
func TestRunToolFromCSV_NonExistentFile(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.CSVInput = filepath.Join(t.TempDir(), "does-not-exist.csv")
	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.ActualPurchase = false

	err := runToolFromCSV(context.Background(), toolCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read CSV file")
	assert.Contains(t, err.Error(), "failed to open CSV file")
}

// TestRunToolFromCSV_EmptyFile asserts a CSV without a header row is rejected
// with a parse error rather than being silently treated as zero rows.
func TestRunToolFromCSV_EmptyFile(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.CSVInput = writeTestRecommendationsCSV(t, "")
	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.ActualPurchase = false

	err := runToolFromCSV(context.Background(), toolCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read CSV file")
	assert.Contains(t, err.Error(), "failed to read CSV header")
}
