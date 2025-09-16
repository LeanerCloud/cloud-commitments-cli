package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test generatePurchaseID function
func TestGeneratePurchaseID(t *testing.T) {
	tests := []struct {
		name      string
		rec       recommendations.Recommendation
		region    string
		index     int
		isDryRun  bool
		expectRun string
		expectRI  string
	}{
		{
			name: "Aurora MySQL single-AZ dry run",
			rec: recommendations.Recommendation{
				Engine:       "Aurora MySQL",
				InstanceType: "db.t4g.medium",
				Count:        5,
				AZConfig:     "single-az",
			},
			region:    "us-east-1",
			index:     1,
			isDryRun:  true,
			expectRun: "dryrun-aurora-mysql-t4g-medium-5x-saz-us-east-1-",
		},
		{
			name: "PostgreSQL multi-AZ actual purchase",
			rec: recommendations.Recommendation{
				Engine:       "postgres",
				InstanceType: "db.r6g.large",
				Count:        10,
				AZConfig:     "multi-az",
			},
			region:   "eu-west-1",
			index:    2,
			isDryRun: false,
			expectRI: "ri-postgres-r6g-large-10x-eu-west-1-002",
		},
		{
			name: "Engine with spaces and underscores",
			rec: recommendations.Recommendation{
				Engine:       "Aurora_PostgreSQL Server",
				InstanceType: "db.m5.xlarge",
				Count:        1,
				AZConfig:     "single-az",
			},
			region:    "ap-southeast-1",
			index:     3,
			isDryRun:  true,
			expectRun: "dryrun-aurora-postgresql-server-m5-xlarge-1x-saz-ap-southeast-1-",
		},
		{
			name: "Invalid instance type",
			rec: recommendations.Recommendation{
				Engine:       "mysql",
				InstanceType: "invalid-format",
				Count:        2,
				AZConfig:     "single-az",
			},
			region:   "us-west-2",
			index:    4,
			isDryRun: false,
			expectRI: "ri-mysql-unknown-2x-us-west-2-004",
		},
		{
			name: "Empty instance type",
			rec: recommendations.Recommendation{
				Engine:       "mysql",
				InstanceType: "",
				Count:        1,
				AZConfig:     "multi-az",
			},
			region:   "us-west-2",
			index:    5,
			isDryRun: false,
			expectRI: "ri-mysql-unknown-1x-us-west-2-005",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun)

			if tt.isDryRun {
				// For dry runs, check that it starts with the expected prefix and has timestamp
				assert.True(t, strings.HasPrefix(result, tt.expectRun),
					"Expected dry run ID to start with '%s', got: %s", tt.expectRun, result)
				// Should end with the index
				assert.True(t, strings.HasSuffix(result, fmt.Sprintf("-%03d", tt.index)),
					"Expected dry run ID to end with '-%03d', got: %s", tt.index, result)
			} else {
				// For actual purchases, exact match
				assert.Equal(t, tt.expectRI, result)
			}
		})
	}
}

func TestGeneratePurchaseIDMultiAZ(t *testing.T) {
	rec := recommendations.Recommendation{
		Engine:       "mysql",
		InstanceType: "db.t4g.medium",
		Count:        1,
		AZConfig:     "multi-az",
	}

	result := generatePurchaseID(rec, "us-east-1", 1, false)
	expected := "ri-mysql-t4g-medium-1x-us-east-1-001"
	assert.Equal(t, expected, result)
}

// Test applyCoverage function (expanded from existing tests)
func TestApplyCoverage(t *testing.T) {
	tests := []struct {
		name            string
		recommendations []recommendations.Recommendation
		coverage        float64
		expectedCount   int
		expectedCounts  []int32
	}{
		{
			name: "100% coverage",
			recommendations: []recommendations.Recommendation{
				{Count: 10},
				{Count: 5},
				{Count: 2},
			},
			coverage:       100.0,
			expectedCount:  3,
			expectedCounts: []int32{10, 5, 2},
		},
		{
			name: "50% coverage",
			recommendations: []recommendations.Recommendation{
				{Count: 10},
				{Count: 5},
				{Count: 2},
			},
			coverage:       50.0,
			expectedCount:  3,
			expectedCounts: []int32{5, 2, 1},
		},
		{
			name: "20% coverage with filtering",
			recommendations: []recommendations.Recommendation{
				{Count: 10},
				{Count: 5},
				{Count: 2},
				{Count: 1}, // This should be filtered out (20% of 1 = 0.2 -> 0)
			},
			coverage:       20.0,
			expectedCount:  2,             // Only first two items survive
			expectedCounts: []int32{2, 1}, // 20% of 10=2, 20% of 5=1, others filtered
		},
		{
			name: "0% coverage",
			recommendations: []recommendations.Recommendation{
				{Count: 10},
				{Count: 5},
			},
			coverage:      0.0,
			expectedCount: 0,
		},
		{
			name: "coverage above 100%",
			recommendations: []recommendations.Recommendation{
				{Count: 10},
				{Count: 5},
			},
			coverage:       150.0,
			expectedCount:  2,
			expectedCounts: []int32{10, 5}, // Should be same as 100%
		},
		{
			name:            "empty recommendations",
			recommendations: []recommendations.Recommendation{},
			coverage:        50.0,
			expectedCount:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCoverage(tt.recommendations, tt.coverage)
			assert.Len(t, result, tt.expectedCount)

			// Check individual counts for non-empty results
			if len(tt.expectedCounts) > 0 && len(result) > 0 {
				actualCounts := make([]int32, len(result))
				for i, rec := range result {
					actualCounts[i] = rec.Count
				}
				assert.Equal(t, tt.expectedCounts, actualCounts)
			}
		})
	}
}

// Test printRegionalSummary function
func TestPrintRegionalSummary(t *testing.T) {
	tests := []struct {
		name            string
		region          string
		recommendations []recommendations.Recommendation
		expectedOutput  []string
	}{
		{
			name:            "empty recommendations",
			region:          "us-east-1",
			recommendations: []recommendations.Recommendation{},
			expectedOutput:  []string{}, // Should print nothing
		},
		{
			name:   "single recommendation",
			region: "us-east-1",
			recommendations: []recommendations.Recommendation{
				{
					Engine:       "mysql",
					InstanceType: "db.t4g.medium",
					Count:        5,
				},
			},
			expectedOutput: []string{
				"📊 us-east-1 Purchase Summary:",
				"mysql",
				"db.t4g.medium",
				"5 instances",
				"Region us-east-1 total instances: 5",
				"mysql: 5 instances",
			},
		},
		{
			name:   "multiple recommendations",
			region: "eu-west-1",
			recommendations: []recommendations.Recommendation{
				{
					Engine:       "aurora-mysql",
					InstanceType: "db.t4g.medium",
					Count:        10,
				},
				{
					Engine:       "postgres",
					InstanceType: "db.r6g.large",
					Count:        3,
				},
				{
					Engine:       "aurora-mysql",
					InstanceType: "db.t4g.large",
					Count:        2,
				},
			},
			expectedOutput: []string{
				"📊 eu-west-1 Purchase Summary:",
				"aurora-mysql",
				"postgres",
				"Region eu-west-1 total instances: 15",
				"aurora-mysql: 12 instances",
				"postgres: 3 instances",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			printRegionalSummary(tt.region, tt.recommendations)

			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			if len(tt.expectedOutput) == 0 {
				assert.Empty(t, output)
			} else {
				for _, expected := range tt.expectedOutput {
					assert.Contains(t, output, expected)
				}
			}
		})
	}
}

// Test printComprehensiveSummary function
func TestPrintComprehensiveSummary(t *testing.T) {
	allRecommendations := []recommendations.Recommendation{
		{Engine: "mysql", Count: 5},
		{Engine: "postgres", Count: 3},
		{Engine: "mysql", Count: 2},
	}

	allResults := []purchase.Result{
		{Success: true, Config: recommendations.Recommendation{Count: 5}},
		{Success: false, Config: recommendations.Recommendation{Count: 3}},
		{Success: true, Config: recommendations.Recommendation{Count: 2}},
	}

	regionStats := map[string]RegionProcessingStats{
		"us-east-1": {
			Region:                  "us-east-1",
			Success:                 true,
			RecommendationsFound:    2,
			RecommendationsSelected: 2,
			InstancesProcessed:      7,
			SuccessfulPurchases:     2,
			FailedPurchases:         0,
		},
		"us-west-2": {
			Region:       "us-west-2",
			Success:      false,
			ErrorMessage: "connection timeout",
		},
	}

	// Set global regions for the test
	regions = []string{"us-east-1", "us-west-2"}

	tests := []struct {
		name           string
		isDryRun       bool
		expectedOutput []string
	}{
		{
			name:     "dry run mode",
			isDryRun: true,
			expectedOutput: []string{
				"🎯 Comprehensive Summary:",
				"Mode: DRY RUN",
				"Total recommendations: 3",
				"Successful operations: 2",
				"Failed operations: 1",
				"Total instances processed: 7",
				"By Engine (All Regions):",
				"mysql",
				"postgres",
				"Overall success rate: 66.7%",
				"💡 To actually purchase these RIs, run with --purchase flag",
			},
		},
		{
			name:     "actual purchase mode",
			isDryRun: false,
			expectedOutput: []string{
				"🎯 Comprehensive Summary:",
				"Mode: ACTUAL PURCHASE",
				"Total recommendations: 3",
				"🎉 Purchase operations completed!",
				"⏰ Allow up to 15 minutes for RIs to appear in your account",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			printComprehensiveSummary(allRecommendations, allResults, regionStats, tt.isDryRun)

			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			for _, expected := range tt.expectedOutput {
				assert.Contains(t, output, expected, "Expected output to contain: %s", expected)
			}
		})
	}
}

// Test RegionProcessingStats struct
func TestRegionProcessingStats(t *testing.T) {
	stats := RegionProcessingStats{
		Region:                  "us-east-1",
		Success:                 true,
		RecommendationsFound:    10,
		RecommendationsSelected: 8,
		InstancesProcessed:      25,
		SuccessfulPurchases:     6,
		FailedPurchases:         2,
	}

	assert.Equal(t, "us-east-1", stats.Region)
	assert.True(t, stats.Success)
	assert.Equal(t, 10, stats.RecommendationsFound)
	assert.Equal(t, 8, stats.RecommendationsSelected)
	assert.Equal(t, int32(25), stats.InstancesProcessed)
	assert.Equal(t, 6, stats.SuccessfulPurchases)
	assert.Equal(t, 2, stats.FailedPurchases)
}

// Test command line argument validation
func TestValidateCommandLineArgs(t *testing.T) {
	tests := []struct {
		name        string
		coverage    float64
		expectValid bool
	}{
		{
			name:        "valid coverage 50%",
			coverage:    50.0,
			expectValid: true,
		},
		{
			name:        "valid coverage 0%",
			coverage:    0.0,
			expectValid: true,
		},
		{
			name:        "valid coverage 100%",
			coverage:    100.0,
			expectValid: true,
		},
		{
			name:        "invalid coverage -10%",
			coverage:    -10.0,
			expectValid: false,
		},
		{
			name:        "invalid coverage 150%",
			coverage:    150.0,
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate command line validation logic from runTool
			isValid := tt.coverage >= 0 && tt.coverage <= 100
			assert.Equal(t, tt.expectValid, isValid)
		})
	}
}

// Test CSV filename generation logic
func TestCSVFilenameGeneration(t *testing.T) {
	tests := []struct {
		name        string
		isDryRun    bool
		expectStart string
	}{
		{
			name:        "dry run filename",
			isDryRun:    true,
			expectStart: "rds-ri-dryrun-",
		},
		{
			name:        "purchase filename",
			isDryRun:    false,
			expectStart: "rds-ri-purchase-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate filename generation logic from runTool
			timestamp := time.Now().Format("20060102-150405")
			var mode string
			if tt.isDryRun {
				mode = "dryrun"
			} else {
				mode = "purchase"
			}
			filename := fmt.Sprintf("rds-ri-%s-%s.csv", mode, timestamp)

			assert.True(t, strings.HasPrefix(filename, tt.expectStart))
			assert.True(t, strings.HasSuffix(filename, ".csv"))
			assert.Contains(t, filename, timestamp)
		})
	}
}

// Test region validation logic
func TestRegionValidation(t *testing.T) {
	validRegions := []string{
		"us-east-1",
		"us-west-2",
		"eu-central-1",
		"eu-west-1",
		"ap-southeast-1",
		"ap-northeast-1",
	}

	invalidRegions := []string{
		"",
		"invalid-region",
		"us-east-99",
		"europe-central-1",
	}

	// Test valid regions
	for _, region := range validRegions {
		t.Run("valid_"+region, func(t *testing.T) {
			// Basic validation - non-empty and follows AWS region pattern
			assert.NotEmpty(t, region)
			assert.Contains(t, region, "-")
		})
	}

	// Test invalid regions
	for _, region := range invalidRegions {
		t.Run("invalid_"+region, func(t *testing.T) {
			if region == "" {
				assert.Empty(t, region)
			} else {
				// For this test, we just check they don't match expected patterns
				assert.NotContains(t, validRegions, region)
			}
		})
	}
}

// Test dry run vs actual purchase logic
func TestDryRunVsActualPurchase(t *testing.T) {
	tests := []struct {
		name           string
		actualPurchase bool
		expectedMode   string
	}{
		{
			name:           "dry run mode",
			actualPurchase: false,
			expectedMode:   "dry-run",
		},
		{
			name:           "actual purchase mode",
			actualPurchase: true,
			expectedMode:   "actual",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from runTool function
			isDryRun := !tt.actualPurchase

			var mode string
			if isDryRun {
				mode = "dry-run"
			} else {
				mode = "actual"
			}

			assert.Equal(t, tt.expectedMode, mode)
		})
	}
}

// Test error handling scenarios
func TestErrorHandlingScenarios(t *testing.T) {
	tests := []struct {
		name            string
		coverage        float64
		recommendations []recommendations.Recommendation
		expectError     bool
	}{
		{
			name:            "negative coverage",
			coverage:        -10.0,
			recommendations: createMockRecommendations(),
			expectError:     true,
		},
		{
			name:            "coverage over 100",
			coverage:        150.0,
			recommendations: createMockRecommendations(),
			expectError:     true,
		},
		{
			name:            "valid coverage with empty recommendations",
			coverage:        50.0,
			recommendations: []recommendations.Recommendation{},
			expectError:     false,
		},
		{
			name:            "valid coverage with valid recommendations",
			coverage:        75.0,
			recommendations: createMockRecommendations(),
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate validation logic from runTool
			hasError := tt.coverage < 0 || tt.coverage > 100
			assert.Equal(t, tt.expectError, hasError)

			if !hasError {
				// Test that applyCoverage works with valid input
				result := applyCoverage(tt.recommendations, tt.coverage)
				// Should not panic and should return valid result
				assert.NotNil(t, result)
			}
		})
	}
}

// Test regional statistics calculation
func TestRegionalStatisticsCalculation(t *testing.T) {
	results := []purchase.Result{
		{Success: true, Config: recommendations.Recommendation{Count: 5}},
		{Success: false, Config: recommendations.Recommendation{Count: 3}},
		{Success: true, Config: recommendations.Recommendation{Count: 2}},
		{Success: true, Config: recommendations.Recommendation{Count: 1}},
	}

	// Simulate the statistics calculation logic from runTool
	successCount := 0
	totalInstances := int32(0)
	for _, result := range results {
		if result.Success {
			successCount++
			totalInstances += result.Config.Count
		}
	}

	assert.Equal(t, 3, successCount)
	assert.Equal(t, int32(8), totalInstances) // 5 + 2 + 1 (only successful)
}

// Test engine aggregation logic
func TestEngineAggregationLogic(t *testing.T) {
	recs := []recommendations.Recommendation{
		{Engine: "mysql", Count: 5},
		{Engine: "postgres", Count: 3},
		{Engine: "mysql", Count: 2},
		{Engine: "aurora-mysql", Count: 1},
	}

	// Simulate the engine aggregation logic used in printRegionalSummary
	engineCounts := make(map[string]int32)
	totalInstances := int32(0)

	for _, rec := range recs {
		engineCounts[rec.Engine] += rec.Count
		totalInstances += rec.Count
	}

	assert.Equal(t, int32(11), totalInstances)              // 5 + 3 + 2 + 1
	assert.Equal(t, int32(7), engineCounts["mysql"])        // 5 + 2
	assert.Equal(t, int32(3), engineCounts["postgres"])     // 3
	assert.Equal(t, int32(1), engineCounts["aurora-mysql"]) // 1
}

// Test success rate calculation
func TestSuccessRateCalculation(t *testing.T) {
	tests := []struct {
		name          string
		totalOps      int
		successfulOps int
		expectedRate  float64
	}{
		{
			name:          "100% success rate",
			totalOps:      10,
			successfulOps: 10,
			expectedRate:  100.0,
		},
		{
			name:          "50% success rate",
			totalOps:      10,
			successfulOps: 5,
			expectedRate:  50.0,
		},
		{
			name:          "0% success rate",
			totalOps:      10,
			successfulOps: 0,
			expectedRate:  0.0,
		},
		{
			name:          "no operations",
			totalOps:      0,
			successfulOps: 0,
			expectedRate:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate success rate calculation from printComprehensiveSummary
			var successRate float64
			if tt.totalOps > 0 {
				successRate = (float64(tt.successfulOps) / float64(tt.totalOps)) * 100
			}
			assert.Equal(t, tt.expectedRate, successRate)
		})
	}
}

// Test command execution without AWS dependencies (unit test for cobra command structure)
func TestRootCommandStructure(t *testing.T) {
	// Test that the root command is properly configured
	assert.Equal(t, "rds-ri-tool", rootCmd.Use)
	assert.NotEmpty(t, rootCmd.Short)
	assert.NotEmpty(t, rootCmd.Long)

	// Test flags exist
	flags := rootCmd.Flags()
	assert.NotNil(t, flags.Lookup("regions"), "regions flag should exist")
	assert.NotNil(t, flags.Lookup("coverage"), "coverage flag should exist")
	assert.NotNil(t, flags.Lookup("purchase"), "purchase flag should exist")
	assert.NotNil(t, flags.Lookup("output"), "output flag should exist")

}

// Test flag parsing
func TestFlagParsing(t *testing.T) {
	// Reset global variables
	regions = []string{}
	coverage = 80.0
	actualPurchase = false
	csvOutput = ""

	// Create a new command for testing
	testCmd := &cobra.Command{
		Use: "test",
		Run: func(cmd *cobra.Command, args []string) {
			// This would be called if we executed the command
		},
	}

	testCmd.Flags().StringSliceVarP(&regions, "regions", "r", []string{}, "Test regions")
	testCmd.Flags().Float64VarP(&coverage, "coverage", "c", 80.0, "Test coverage")
	testCmd.Flags().BoolVar(&actualPurchase, "purchase", false, "Test purchase")
	testCmd.Flags().StringVarP(&csvOutput, "output", "o", "", "Test output")

	// Test flag parsing
	testCmd.SetArgs([]string{"--regions", "us-east-1,us-west-2", "--coverage", "50", "--purchase", "--output", "test.csv"})
	err := testCmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"us-east-1", "us-west-2"}, regions)
	assert.Equal(t, 50.0, coverage)
	assert.True(t, actualPurchase)
	assert.Equal(t, "test.csv", csvOutput)
}

// Helper function for creating mock recommendations
func createMockRecommendations() []recommendations.Recommendation {
	return []recommendations.Recommendation{
		{
			Region:         "us-east-1",
			Engine:         "mysql",
			InstanceType:   "db.t4g.medium",
			AZConfig:       "single-az",
			PaymentOption:  "partial-upfront",
			Term:           36,
			Count:          5,
			EstimatedCost:  100.0,
			SavingsPercent: 20.0,
			Description:    "MySQL t4g.medium Single-AZ",
		},
		{
			Region:         "us-east-1",
			Engine:         "postgres",
			InstanceType:   "db.r6g.large",
			AZConfig:       "multi-az",
			PaymentOption:  "partial-upfront",
			Term:           36,
			Count:          3,
			EstimatedCost:  200.0,
			SavingsPercent: 30.0,
			Description:    "PostgreSQL r6g.large Multi-AZ",
		},
	}
}

// Test applyCoverage edge cases
func TestApplyCoverageEdgeCases(t *testing.T) {
	// Test with very small counts that might round to zero
	recs := []recommendations.Recommendation{
		{Count: 1},
		{Count: 1},
		{Count: 1},
	}

	// 10% of 1 = 0.1, which should round down to 0 and be filtered
	result := applyCoverage(recs, 10.0)
	assert.Empty(t, result)

	// 50% of 1 = 0.5, which should round down to 0 and be filtered
	result = applyCoverage(recs, 50.0)
	assert.Empty(t, result)

	// 100% of 1 = 1.0, which should remain as 1
	result = applyCoverage(recs, 100.0)
	assert.Len(t, result, 3)
	for _, rec := range result {
		assert.Equal(t, int32(1), rec.Count)
	}
}

// Test applyCoverage with large numbers
func TestApplyCoverageWithLargeNumbers(t *testing.T) {
	recs := []recommendations.Recommendation{
		{Count: 1000},
		{Count: 500},
		{Count: 100},
	}

	result := applyCoverage(recs, 25.0)
	require.Len(t, result, 3)

	expectedCounts := []int32{250, 125, 25}
	for i, expected := range expectedCounts {
		assert.Equal(t, expected, result[i].Count)
	}
}

// Test applyCoverage preserves other fields
func TestApplyCoveragePreservesOtherFields(t *testing.T) {
	recs := []recommendations.Recommendation{
		{
			Count:          10,
			Engine:         "mysql",
			InstanceType:   "db.t4g.medium",
			Region:         "us-east-1",
			EstimatedCost:  100.0,
			SavingsPercent: 25.0,
			Description:    "MySQL instance",
		},
	}

	result := applyCoverage(recs, 50.0)
	require.Len(t, result, 1)

	// Count should be modified
	assert.Equal(t, int32(5), result[0].Count)

	// Other fields should be preserved
	assert.Equal(t, "mysql", result[0].Engine)
	assert.Equal(t, "db.t4g.medium", result[0].InstanceType)
	assert.Equal(t, "us-east-1", result[0].Region)
	assert.Equal(t, 100.0, result[0].EstimatedCost)
	assert.Equal(t, 25.0, result[0].SavingsPercent)
	assert.Equal(t, "MySQL instance", result[0].Description)
}

// Benchmark tests for main function components
func BenchmarkApplyCoverage(b *testing.B) {
	recs := make([]recommendations.Recommendation, 1000)
	for i := 0; i < 1000; i++ {
		recs[i] = recommendations.Recommendation{
			Count:          int32(i%100 + 1),
			Engine:         "mysql",
			InstanceType:   "db.t4g.medium",
			Region:         "us-east-1",
			EstimatedCost:  100.0,
			SavingsPercent: 25.0,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyCoverage(recs, 75.0)
	}
}

func BenchmarkGeneratePurchaseID(b *testing.B) {
	rec := recommendations.Recommendation{
		Engine:       "aurora-mysql",
		InstanceType: "db.t4g.medium",
		Count:        5,
		AZConfig:     "single-az",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generatePurchaseID(rec, "us-east-1", 1, true)
	}
}
