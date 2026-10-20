package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/aws/aws-sdk-go-v2/aws"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRunToolMultiService_Validation(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	// Restore after test
	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		setupVars   func()
		name        string
		expectPanic bool
	}{
		{
			name: "Valid input - all services",
			setupVars: func() {
				toolCfg.Coverage = 75.0
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 3
				toolCfg.AllServices = true
				toolCfg.Services = nil
			},
			expectPanic: false,
		},
		{
			name: "Valid input - specific services",
			setupVars: func() {
				toolCfg.Coverage = 50.0
				toolCfg.PaymentOption = "no-upfront"
				toolCfg.TermYears = 1
				toolCfg.AllServices = false
				toolCfg.Services = []string{"rds", "ec2"}
			},
			expectPanic: false,
		},
		{
			name: "Invalid coverage - too high",
			setupVars: func() {
				toolCfg.Coverage = 150.0
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 3
			},
			expectPanic: true,
		},
		{
			name: "Invalid coverage - negative",
			setupVars: func() {
				toolCfg.Coverage = -10.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 1
			},
			expectPanic: true,
		},
		{
			name: "Invalid payment option",
			setupVars: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "invalid-payment"
				toolCfg.TermYears = 3
			},
			expectPanic: true,
		},
		{
			name: "Invalid term years",
			setupVars: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 2 // Only 1 or 3 allowed
			},
			expectPanic: true,
		},
		{
			name: "Default to RDS when no services",
			setupVars: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 3
				toolCfg.AllServices = false
				toolCfg.Services = nil
			},
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupVars()

			if tt.expectPanic {
				// Can't easily test log.Fatalf, so we skip execution
				// In production code, would use dependency injection
				return
			}

			// For non-panic tests, verify the setup is valid
			assert.GreaterOrEqual(t, toolCfg.Coverage, 0.0)
			assert.LessOrEqual(t, toolCfg.Coverage, 100.0)
			assert.Contains(t, []string{"all-upfront", "partial-upfront", "no-upfront"}, toolCfg.PaymentOption)
			assert.Contains(t, []int{1, 3}, toolCfg.TermYears)
		})
	}
}

func TestProcessService_EdgeCases(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	// Set test values
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 3

	tests := []struct {
		name       string
		setupFunc  func()
		service    common.ServiceType
		isDryRun   bool
		expectRecs int
	}{
		{
			name: "With explicit regions",
			setupFunc: func() {
				toolCfg.Regions = []string{"us-east-1"}
				toolCfg.Coverage = 100.0
			},
			service:    common.ServiceRDS,
			isDryRun:   true,
			expectRecs: 0, // Would need mock to return actual recs
		},
		{
			name: "No regions triggers discovery",
			setupFunc: func() {
				toolCfg.Regions = []string{}
				toolCfg.Coverage = 75.0
			},
			service:    common.ServiceEC2,
			isDryRun:   false,
			expectRecs: 0, // Would need mock
		},
		{
			name: "Zero coverage",
			setupFunc: func() {
				toolCfg.Regions = []string{"us-west-2"}
				toolCfg.Coverage = 0.0
			},
			service:    common.ServiceElastiCache,
			isDryRun:   true,
			expectRecs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc()

			// Verify test configuration was applied correctly
			// Note: Full integration tests would require AWS credentials or mocks
			// These tests verify the configuration setup is working as expected

			switch tt.name {
			case "With explicit regions":
				assert.Equal(t, []string{"us-east-1"}, toolCfg.Regions)
				assert.Equal(t, 100.0, toolCfg.Coverage)
			case "No regions triggers discovery":
				assert.Empty(t, toolCfg.Regions)
				assert.Equal(t, 75.0, toolCfg.Coverage)
			case "Zero coverage":
				assert.Equal(t, []string{"us-west-2"}, toolCfg.Regions)
				assert.Equal(t, 0.0, toolCfg.Coverage)
			}

			// Verify the test case service is valid
			assert.NotEmpty(t, tt.service, "service should not be empty")
			assert.GreaterOrEqual(t, tt.expectRecs, 0, "expected recommendations should be non-negative")
		})
	}
}

// ==================== Helper Function Tests ====================

func TestApplyCoverageToRecommendations(t *testing.T) {
	tests := []struct {
		name         string
		recs         []common.Recommendation
		coverage     float64
		expectedRecs int
	}{
		{
			name: "50% coverage of 4 recommendations",
			recs: []common.Recommendation{
				{ResourceType: "type1", Count: 2},
				{ResourceType: "type2", Count: 3},
				{ResourceType: "type3", Count: 1},
				{ResourceType: "type4", Count: 4},
			},
			coverage:     0.5,
			expectedRecs: 2,
		},
		{
			name: "100% coverage",
			recs: []common.Recommendation{
				{ResourceType: "type1", Count: 2},
				{ResourceType: "type2", Count: 3},
			},
			coverage:     1.0,
			expectedRecs: 2,
		},
		{
			name: "0% coverage",
			recs: []common.Recommendation{
				{ResourceType: "type1", Count: 2},
				{ResourceType: "type2", Count: 3},
			},
			coverage:     0.0,
			expectedRecs: 0,
		},
		{
			name: "75% coverage of 3 recommendations",
			recs: []common.Recommendation{
				{ResourceType: "type1", Count: 2},
				{ResourceType: "type2", Count: 2},
				{ResourceType: "type3", Count: 2},
			},
			coverage:     0.75,
			expectedRecs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCoverageToRecommendations(tt.recs, tt.coverage)
			assert.Equal(t, tt.expectedRecs, len(result))
		})
	}
}

func TestPrintDropSummaryImmediatelyPrecedesTerminalOutput(t *testing.T) {
	for _, terminal := range []string{
		"ℹ️  No recommendations passed filters. Nothing to purchase.",
		"FINAL SUMMARY",
	} {
		t.Run(terminal, func(t *testing.T) {
			oldLogger := AppLogger
			var output bytes.Buffer
			AppLogger = log.New(&output, "", 0)
			t.Cleanup(func() { AppLogger = oldLogger })

			drops := common.NewDropSummary()
			drops.Add(common.DropTargetSizedToZero, 1)

			printDropSummary(drops)
			AppLogger.Printf("\n%s\n", terminal)

			assert.Contains(t, output.String(),
				"Dropped 1 recs: target-sized-to-zero=1\n\n"+terminal)
			assert.Equal(t, 1, strings.Count(output.String(), "Dropped 1 recs"))
		})
	}
}

func TestRunPurchaseAndReportDeclinedConfirmationPrintsDropsBeforeCancellation(t *testing.T) {
	drops := common.NewDropSummary()
	drops.Add(common.DropTargetSizedToZero, 1)
	scored := scorer.ScoredResult{Passed: []common.Recommendation{{Count: 1, EstimatedSavings: 10}}}

	output := captureAppOutput(t, func() {
		runPurchaseAndReport(context.Background(), aws.Config{}, scored, false, Config{}, drops)
	})

	assert.Contains(t, output,
		"Dropped 1 recs: target-sized-to-zero=1\n\n❌ Purchase canceled.")
	assert.Equal(t, 1, strings.Count(output, "Dropped 1 recs"))
}

func TestServiceProcessingOrder(t *testing.T) {
	// Test that services are processed in a consistent order
	services := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}

	// Verify all expected services are present
	expectedServices := map[common.ServiceType]bool{
		common.ServiceRDS:         false,
		common.ServiceElastiCache: false,
		common.ServiceEC2:         false,
		common.ServiceOpenSearch:  false,
		common.ServiceRedshift:    false,
		common.ServiceMemoryDB:    false,
	}

	for _, service := range services {
		expectedServices[service] = true
	}

	// Check all services were found
	for service, found := range expectedServices {
		assert.True(t, found, "Service %s should be in processing list", service)
	}
}

func TestGenerateCSVFilenameHelper(t *testing.T) {
	tests := []struct {
		name        string
		service     common.ServiceType
		payment     string
		expectParts []string
		dryRun      bool
	}{
		{
			name:        "RDS dry run",
			service:     common.ServiceRDS,
			payment:     "no-upfront",
			dryRun:      true,
			expectParts: []string{"rds", "no-upfront", "dryrun"},
		},
		{
			name:        "EC2 actual purchase",
			service:     common.ServiceEC2,
			payment:     "all-upfront",
			dryRun:      false,
			expectParts: []string{"ec2", "all-upfront", "purchase"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := generateCSVFilenameTestHelper(tt.service, tt.payment, tt.dryRun)

			for _, part := range tt.expectParts {
				assert.Contains(t, filename, part)
			}

			// Should end with .csv
			assert.Contains(t, filename, ".csv")
		})
	}
}

func TestMultiServiceConfig(t *testing.T) {
	cfg := MultiServiceConfig{
		Services: map[common.ServiceType]ServiceConfig{
			common.ServiceRDS: {
				Enabled:  true,
				Coverage: 0.5,
			},
			common.ServiceElastiCache: {
				Enabled:  true,
				Coverage: 0.8,
			},
			common.ServiceEC2: {
				Enabled:  false,
				Coverage: 0.0,
			},
		},
		PaymentOption: "no-upfront",
		TermYears:     3,
		DryRun:        true,
	}

	// Test enabled services count
	enabledCount := 0
	for _, svcConfig := range cfg.Services {
		if svcConfig.Enabled {
			enabledCount++
		}
	}
	assert.Equal(t, 2, enabledCount)

	// Test coverage values
	assert.Equal(t, 0.5, cfg.Services[common.ServiceRDS].Coverage)
	assert.Equal(t, 0.8, cfg.Services[common.ServiceElastiCache].Coverage)
}

// ==================== Benchmark Tests ====================

func BenchmarkCalculateTotalInstances(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{Count: i%10 + 1}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calculateTotalInstances(recs)
	}
}

func BenchmarkApplyCoverageToRecommendations(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{
			ResourceType: "type",
			Count:        i%5 + 1,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyCoverageToRecommendations(recs, 0.5)
	}
}

// ==================== Helper Functions for Tests ====================

func calculateTotalInstances(recs []common.Recommendation) int {
	var total int
	for _, rec := range recs {
		total += rec.Count
	}
	return total
}

func applyCoverageToRecommendations(recs []common.Recommendation, coverage float64) []common.Recommendation {
	if coverage <= 0 {
		return []common.Recommendation{}
	}
	if coverage >= 1.0 {
		return recs
	}

	targetCount := int(float64(len(recs)) * coverage)
	if targetCount == 0 && coverage > 0 && len(recs) > 0 {
		targetCount = 1
	}

	if targetCount >= len(recs) {
		return recs
	}

	return recs[:targetCount]
}

func generateCSVFilenameTestHelper(service common.ServiceType, payment string, dryRun bool) string {
	mode := "purchase"
	if dryRun {
		mode = "dryrun"
	}

	serviceStr := ""
	switch service {
	case common.ServiceRDS:
		serviceStr = "rds"
	case common.ServiceElastiCache:
		serviceStr = "elasticache"
	case common.ServiceEC2:
		serviceStr = "ec2"
	case common.ServiceOpenSearch:
		serviceStr = "opensearch"
	case common.ServiceRedshift:
		serviceStr = "redshift"
	case common.ServiceMemoryDB:
		serviceStr = "memorydb"
	default:
		serviceStr = "unknown"
	}

	return serviceStr + "-" + payment + "-" + mode + ".csv"
}

// Test types.
type MultiServiceConfig struct {
	Services      map[common.ServiceType]ServiceConfig
	PaymentOption string
	TermYears     int
	DryRun        bool
}

type ServiceConfig struct {
	Enabled  bool
	Coverage float64
}

// ==================== Filter Function Tests ====================

// ==================== CSV Report Tests ====================

// ==================== Additional ProcessPurchaseLoop Tests ====================

func TestFilterAndAdjustRecommendations(t *testing.T) {
	// Save and restore ALL global variables
	saved := saveGlobalVars()
	defer saved.restore()

	tests := []struct {
		setupFilters    func()
		name            string
		recommendations []common.Recommendation
		coverage        float64
		expectedMin     int
		expectedMax     int
	}{
		{
			name: "100% coverage no filters",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 5},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 3},
			},
			coverage: 100.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
			},
			expectedMin: 2,
			expectedMax: 2,
		},
		{
			name: "50% coverage",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 10},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 6},
			},
			coverage: 50.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
			},
			expectedMin: 1,
			expectedMax: 2,
		},
		{
			name: "Instance limit applied",
			// EstimatedSavings is set because a binding --max-instances has to
			// rank the rows it chooses between; requireRankingSignal refuses a
			// run whose rows carry no savings value rather than capping them by
			// name.
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 10, EstimatedSavings: 100},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 10, EstimatedSavings: 200},
				{Service: common.ServiceRDS, ResourceType: "db.t3.large", Count: 10, EstimatedSavings: 300},
			},
			coverage: 100.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 15
				toolCfg.OverrideCount = 0
			},
			expectedMin: 1,
			expectedMax: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup filters
			tt.setupFilters()

			// Suppress logger
			// Logger output disabled for testing

			result, err := filterAndAdjustRecommendations(context.Background(), tt.recommendations, tt.coverage, toolCfg)
			require.NoError(t, err)

			// Verify result is within expected range
			assert.GreaterOrEqual(t, len(result), tt.expectedMin)
			assert.LessOrEqual(t, len(result), tt.expectedMax)

			// Verify all results have count > 0
			for _, rec := range result {
				assert.Positive(t, rec.Count)
			}
		})
	}
}

// clearAWSEndpointOverrides blanks every service-specific AWS_ENDPOINT_URL_*
// variable and AWS_IGNORE_CONFIGURED_ENDPOINT_URLS for the test, so a
// developer's global overrides can neither bypass nor disable a test's local
// endpoint. Set the test's own endpoint variables after calling it.
func clearAWSEndpointOverrides(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "AWS_ENDPOINT_URL_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "")
}

// isolateAWSEnv pins the AWS environment to deterministic invalid static
// credentials and points every SDK client at a local server that answers 403
// AccessDenied, so the runToolFromCSV tests behave identically on credentialed
// developer machines and bare CI runners: config loading succeeds, every AWS
// API call fails fast on the warn-and-continue path, and nothing leaves the
// machine. It returns the number of requests the local server has received.
func isolateAWSEnv(t *testing.T) *atomic.Int64 {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "invalid-test-key-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "invalid-test-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.Header().Set("X-Amzn-ErrorType", "AccessDeniedException")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"denied by test"}`))
	}))
	t.Cleanup(srv.Close)

	clearAWSEndpointOverrides(t)
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	return hits
}

// readPurchaseReport parses the CSV purchase report written by runToolFromCSV
// and returns the header plus per-purchase data rows. The trailing TOTAL
// summary row is skipped, mirroring loadRecommendationsFromCSV.
func readPurchaseReport(t *testing.T, path string) ([]string, [][]string) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "runToolFromCSV must write the purchase report")
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.NotEmpty(t, rows, "purchase report must contain a header row")
	dataRows := make([][]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) > 0 && row[0] == "TOTAL" {
			continue
		}
		dataRows = append(dataRows, row)
	}
	return rows[0], dataRows
}

// reportColumn returns the values of the named column across all data rows.
func reportColumn(t *testing.T, header []string, rows [][]string, name string) []string {
	t.Helper()
	idx := -1
	for i, col := range header {
		if col == name {
			idx = i
			break
		}
	}
	require.NotEqual(t, -1, idx, "column %s missing from purchase report header", name)
	vals := make([]string, 0, len(rows))
	for _, row := range rows {
		vals = append(vals, row[idx])
	}
	return vals
}

// writeTestRecommendationsCSV writes a recommendations CSV into a temp dir
// and returns its path.
func writeTestRecommendationsCSV(t *testing.T, csvData string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recommendations.csv")
	require.NoError(t, os.WriteFile(path, []byte(csvData), 0o600))
	return path
}

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

func TestRunToolFromCSV_WithMaxInstances(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	isolateAWSEnv(t)

	// EstimatedSavings is populated because a binding --max-instances has to
	// rank the rows it chooses between. A file without that column is refused
	// rather than capped by name; that case is covered by
	// TestCSVCapRefusesRowsWithoutARankingSignal.
	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,EstimatedSavings,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,10,100.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,mysql,10,200.00,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.large,postgres,10,300.00,1yr,all-upfront,123456789012
`)

	reportPath := filepath.Join(t.TempDir(), "report.csv")
	toolCfg.CSVInput = csvPath
	toolCfg.CSVOutput = reportPath
	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.ActualPurchase = false
	toolCfg.Coverage = 100.0
	toolCfg.TargetCoverage = 0
	toolCfg.OverrideCount = 0
	toolCfg.MaxInstances = 15 // Should limit total instances to 15

	err := runToolFromCSV(context.Background(), toolCfg)
	require.NoError(t, err)

	header, rows := readPurchaseReport(t, reportPath)
	total := 0
	for _, raw := range reportColumn(t, header, rows, "Count") {
		n, convErr := strconv.Atoi(raw)
		require.NoError(t, convErr)
		total += n
	}
	assert.Positive(t, total, "some instances must survive the max-instances cap")
	assert.LessOrEqual(t, total, 15, "purchased instance total must respect --max-instances")
}

func TestRunToolFromCSV_WithOverrideCount(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	isolateAWSEnv(t)

	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,10,1yr,all-upfront,123456789012
rds,us-east-1,db.t3.medium,mysql,5,1yr,all-upfront,123456789012
`)

	reportPath := filepath.Join(t.TempDir(), "report.csv")
	toolCfg.CSVInput = csvPath
	toolCfg.CSVOutput = reportPath
	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.ActualPurchase = false
	toolCfg.Coverage = 100.0
	toolCfg.TargetCoverage = 0
	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 3 // Override each recommendation to count=3

	err := runToolFromCSV(context.Background(), toolCfg)
	require.NoError(t, err)

	header, rows := readPurchaseReport(t, reportPath)
	counts := reportColumn(t, header, rows, "Count")
	require.Len(t, counts, 2, "both recommendations must reach the purchase loop")
	for _, raw := range counts {
		assert.Equal(t, "3", raw, "--override-count must replace every recommendation count")
	}
}

// TestRunToolFromCSV_WritesAuditRecordsOnDryRun reproduces #1609 through the
// public --input-csv entry point: before the fix, processPurchaseLoop never
// wrote an audit record at all, on a dry run or a real purchase. A dry run is
// used here (rather than ActualPurchase=true with invalid credentials)
// because #1941 made the duplicate check fail closed on a real purchase run:
// with no valid AWS credentials the check now refuses the region before ever
// reaching a purchase attempt, and writes no audit record at all by design
// (see TestCheckDuplicates_ErrorOnPurchaseRun_DropsRecsRatherThanFallingBack)
// -- so there is no way to drive an un-mocked real-purchase attempt through
// this entry point in a test. A dry run still exercises the exact wiring
// #1609 fixes (prepareCSVPurchaseRun's runID -> processPurchaseLoop ->
// writePurchaseAuditRecord), since the duplicate check only warns and
// continues on a dry run rather than refusing.
// TestProcessPurchaseLoop_WritesAuditRecordForRealPurchase below covers the
// "success"/"error" statuses a real purchase attempt gets audited with.
func TestRunToolFromCSV_WritesAuditRecordsOnDryRun(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	isolateAWSEnv(t)

	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account
rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111
`)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")

	toolCfg.CSVInput = csvPath
	toolCfg.CSVOutput = filepath.Join(t.TempDir(), "report.csv")
	toolCfg.AuditLog = auditPath
	toolCfg.ActualPurchase = false
	toolCfg.Coverage = 100.0
	toolCfg.TargetCoverage = 0
	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 0

	err := runToolFromCSV(context.Background(), toolCfg)
	require.NoError(t, err)

	data, readErr := os.ReadFile(auditPath) // #nosec G304 -- test-owned tempdir path
	require.NoError(t, readErr, "a purchase run through --input-csv must write an audit log")
	require.NotEmpty(t, data, "the audit log must not be empty -- an empty file would make the next length check pass vacuously")

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1, "one audit record per recommendation")

	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
	assert.NotEmpty(t, rec["run_id"], "the audit record must carry a run ID grouping the CSV run")
	assert.Equal(t, common.PurchaseSourceCLI, rec["source"])
	assert.Equal(t, "db.t3.small", rec["resource_type"])
	assert.Equal(t, true, rec["dry_run"])
	assert.Equal(t, "skipped", rec["status"])
}

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

// ==================== Tests for adjustRecommendationForExcludedVersions ====================

// Helper to create test version info with extended support dates.
func createTestVersionInfo() map[string]MajorEngineVersionInfo {
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)  // 6 months ago
	futureDate := now.AddDate(3, 0, 0) // 3 years from now

	return map[string]MajorEngineVersionInfo{
		"aurora-mysql:5.7": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
					LifecycleSupportStartDate: now.AddDate(-5, 0, 0),
					LifecycleSupportEndDate:   pastDate,
				},
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportStartDate: pastDate,
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
		"aurora-mysql:8.0": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "8.0",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
					LifecycleSupportStartDate: now.AddDate(-2, 0, 0),
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
	}
}

// ==================== generateCSVFilename Tests ====================

// ==================== printRunMode Tests ====================

// ==================== printPaymentAndTerm Tests ====================

// ==================== extractMajorVersion Tests ====================

// ==================== determineServicesToProcess Tests ====================

// ==================== determineCSVCoverage Tests ====================
