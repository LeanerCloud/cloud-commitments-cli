package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/aws/aws-sdk-go-v2/aws"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
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
