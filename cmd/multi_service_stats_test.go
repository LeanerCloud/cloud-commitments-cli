package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
)

// captureAppOutput captures output from AppLogger and returns the captured string.
// Usage: output := captureAppOutput(t, func() { printSomething() })
func captureAppOutput(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	oldLogger := AppLogger
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w
	AppLogger = log.New(w, "", 0)

	fn()

	_ = w.Close()
	os.Stdout = old
	AppLogger = oldLogger

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestCalculateServiceStats(t *testing.T) {
	tests := []struct {
		name     string
		service  common.ServiceType
		recs     []common.Recommendation
		results  []common.PurchaseResult
		expected ServiceProcessingStats
	}{
		{
			name:    "Empty inputs",
			service: common.ServiceRDS,
			recs:    []common.Recommendation{},
			results: []common.PurchaseResult{},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceRDS,
				RegionsProcessed:        0,
				RecommendationsFound:    0,
				RecommendationsSelected: 0,
				InstancesProcessed:      0,
				SuccessfulPurchases:     0,
				FailedPurchases:         0,
				TotalEstimatedSavings:   0,
			},
		},
		{
			name:    "Multiple regions with mixed results",
			service: common.ServiceEC2,
			recs: []common.Recommendation{
				{Region: "us-east-1", Count: 2, EstimatedSavings: 100},
				{Region: "us-west-2", Count: 3, EstimatedSavings: 200},
				{Region: "eu-west-1", Count: 1, EstimatedSavings: 50},
			},
			results: []common.PurchaseResult{
				{Success: true},
				{Success: true},
				{Success: false},
			},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceEC2,
				RegionsProcessed:        3,
				RecommendationsFound:    3,
				RecommendationsSelected: 3,
				InstancesProcessed:      6,
				SuccessfulPurchases:     2,
				FailedPurchases:         1,
				TotalEstimatedSavings:   350,
			},
		},
		{
			name:    "Same region multiple recommendations",
			service: common.ServiceElastiCache,
			recs: []common.Recommendation{
				{Region: "us-east-1", Count: 1, EstimatedSavings: 100},
				{Region: "us-east-1", Count: 2, EstimatedSavings: 200},
				{Region: "us-east-1", Count: 3, EstimatedSavings: 300},
			},
			results: []common.PurchaseResult{
				{Success: true},
				{Success: true},
				{Success: true},
			},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceElastiCache,
				RegionsProcessed:        1,
				RecommendationsFound:    3,
				RecommendationsSelected: 3,
				InstancesProcessed:      6,
				SuccessfulPurchases:     3,
				FailedPurchases:         0,
				TotalEstimatedSavings:   600,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateServiceStats(tt.service, tt.recs, tt.results)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPrintServiceSummary(t *testing.T) {
	tests := []struct {
		name    string
		service common.ServiceType
		stats   ServiceProcessingStats
	}{
		{
			name:    "With savings",
			service: common.ServiceRDS,
			stats: ServiceProcessingStats{
				Service:                 common.ServiceRDS,
				RegionsProcessed:        2,
				RecommendationsSelected: 5,
				InstancesProcessed:      10,
				SuccessfulPurchases:     4,
				FailedPurchases:         1,
				TotalEstimatedSavings:   1500.50,
			},
		},
		{
			name:    "Without savings",
			service: common.ServiceEC2,
			stats: ServiceProcessingStats{
				Service:                 common.ServiceEC2,
				RegionsProcessed:        1,
				RecommendationsSelected: 0,
				InstancesProcessed:      0,
				SuccessfulPurchases:     0,
				FailedPurchases:         0,
				TotalEstimatedSavings:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureAppOutput(t, func() {
				printServiceSummary(tt.service, tt.stats)
			})

			// Verify output contains expected information
			assert.Contains(t, output, getServiceDisplayName(tt.service))
			assert.Contains(t, output, fmt.Sprintf("Regions processed: %d", tt.stats.RegionsProcessed))
			assert.Contains(t, output, fmt.Sprintf("Recommendations: %d", tt.stats.RecommendationsSelected))
			assert.Contains(t, output, fmt.Sprintf("Instances: %d", tt.stats.InstancesProcessed))

			if tt.stats.TotalEstimatedSavings > 0 {
				assert.Contains(t, output, fmt.Sprintf("$%.2f", tt.stats.TotalEstimatedSavings))
			}
		})
	}
}

func TestPrintMultiServiceSummary(t *testing.T) {
	tests := []struct {
		name     string
		recs     []common.Recommendation
		results  []common.PurchaseResult
		stats    map[common.ServiceType]ServiceProcessingStats
		isDryRun bool
	}{
		{
			name: "Dry run with multiple services",
			recs: []common.Recommendation{
				{Service: common.ServiceRDS, Count: 2},
				{Service: common.ServiceEC2, Count: 3},
			},
			results: []common.PurchaseResult{
				{Success: true, Recommendation: common.Recommendation{Count: 2}},
				{Success: false, Recommendation: common.Recommendation{Count: 3}},
			},
			stats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceRDS: {
					Service:                 common.ServiceRDS,
					RecommendationsSelected: 1,
					InstancesProcessed:      2,
					SuccessfulPurchases:     1,
					TotalEstimatedSavings:   500.0,
				},
				common.ServiceEC2: {
					Service:                 common.ServiceEC2,
					RecommendationsSelected: 1,
					InstancesProcessed:      3,
					FailedPurchases:         1,
					TotalEstimatedSavings:   300.0,
				},
			},
			isDryRun: true,
		},
		{
			name: "Actual purchase with success",
			recs: []common.Recommendation{
				{Service: common.ServiceElastiCache, Count: 5},
			},
			results: []common.PurchaseResult{
				{Success: true, Recommendation: common.Recommendation{Count: 5}},
			},
			stats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceElastiCache: {
					Service:                 common.ServiceElastiCache,
					RecommendationsSelected: 1,
					InstancesProcessed:      5,
					SuccessfulPurchases:     1,
					TotalEstimatedSavings:   1000.0,
				},
			},
			isDryRun: false,
		},
		{
			name:     "Empty results",
			recs:     []common.Recommendation{},
			results:  []common.PurchaseResult{},
			stats:    map[common.ServiceType]ServiceProcessingStats{},
			isDryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureAppOutput(t, func() {
				printMultiServiceSummary(tt.recs, tt.results, tt.stats, tt.isDryRun)
			})

			// Verify output contains expected information
			assert.Contains(t, output, "Final Summary")
			if tt.isDryRun {
				assert.Contains(t, output, "DRY RUN")
			} else {
				assert.Contains(t, output, "ACTUAL PURCHASE")
			}

			if len(tt.stats) > 0 {
				assert.Contains(t, output, "RESERVED INSTANCES:")
			}

			if len(tt.results) > 0 {
				assert.Contains(t, output, "success rate")
			}
		})
	}
}

func TestPrintSavingsPlansSection(t *testing.T) {
	tests := []struct {
		name            string
		recommendations []common.Recommendation
		stats           ServiceProcessingStats
		checkOutput     func(t *testing.T, output string)
	}{
		{
			name: "Prints Compute Savings Plans",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 500.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "Compute",
						HourlyCommitment: 1.5,
					},
				},
			},
			stats: ServiceProcessingStats{
				Service:                 common.ServiceSavingsPlans,
				RecommendationsSelected: 1,
				TotalEstimatedSavings:   500.0,
			},
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "SAVINGS PLANS:")
				assert.Contains(t, output, "Compute SP")
				assert.Contains(t, output, "500.00")
			},
		},
		{
			name: "Prints EC2 Instance Savings Plans",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 300.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "EC2Instance",
						HourlyCommitment: 1.0,
					},
				},
			},
			stats: ServiceProcessingStats{
				Service:                 common.ServiceSavingsPlans,
				RecommendationsSelected: 1,
				TotalEstimatedSavings:   300.0,
			},
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "EC2 Inst SP")
				assert.Contains(t, output, "300.00")
			},
		},
		{
			name: "Prints Database Savings Plans",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 400.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "Database",
						HourlyCommitment: 1.2,
					},
				},
			},
			stats: ServiceProcessingStats{
				Service:                 common.ServiceSavingsPlans,
				RecommendationsSelected: 1,
				TotalEstimatedSavings:   400.0,
			},
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "Database SP")
				assert.Contains(t, output, "400.00")
			},
		},
		{
			name: "Prints SageMaker Savings Plans",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 250.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "SageMaker",
						HourlyCommitment: 0.8,
					},
				},
			},
			stats: ServiceProcessingStats{
				Service:                 common.ServiceSavingsPlans,
				RecommendationsSelected: 1,
				TotalEstimatedSavings:   250.0,
			},
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "SageMaker SP")
				assert.Contains(t, output, "250.00")
			},
		},
		{
			name: "Prints multiple SP types with recommendations",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 500.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "Compute",
						HourlyCommitment: 1.5,
					},
				},
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 600.0,
					Details: common.SavingsPlanDetails{
						PlanType:         "EC2Instance",
						HourlyCommitment: 1.8,
					},
				},
			},
			stats: ServiceProcessingStats{
				Service:                 common.ServiceSavingsPlans,
				RecommendationsSelected: 2,
				TotalEstimatedSavings:   1100.0,
			},
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "Compute SP")
				assert.Contains(t, output, "EC2 Inst SP")
				assert.Contains(t, output, "500.00")
				assert.Contains(t, output, "600.00")
				assert.Contains(t, output, "Best for EC2")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureAppOutput(t, func() {
				printSavingsPlansSection(tt.recommendations, tt.stats)
			})

			tt.checkOutput(t, output)
		})
	}
}

func TestPrintComparisonSection(t *testing.T) {
	tests := []struct {
		name            string
		recommendations []common.Recommendation
		riStats         map[common.ServiceType]ServiceProcessingStats
		riSavings       float64
		checkOutput     func(t *testing.T, output string)
	}{
		{
			name: "Comparison with EC2 RIs and EC2 Instance SP",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 600.0,
					Details: common.SavingsPlanDetails{
						PlanType: "EC2Instance",
					},
				},
			},
			riStats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceEC2: {
					TotalEstimatedSavings: 500.0,
				},
			},
			riSavings: 500.0,
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "COMPARISON:")
				assert.Contains(t, output, "Option 1 (All RIs)")
				assert.Contains(t, output, "500.00")
				assert.Contains(t, output, "Option 2")
			},
		},
		{
			name: "Comparison with Database RIs and Database SP",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 800.0,
					Details: common.SavingsPlanDetails{
						PlanType: "Database",
					},
				},
			},
			riStats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceRDS: {
					TotalEstimatedSavings: 700.0,
				},
				common.ServiceElastiCache: {
					TotalEstimatedSavings: 200.0,
				},
			},
			riSavings: 900.0,
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "COMPARISON:")
				assert.Contains(t, output, "Option 3")
				assert.Contains(t, output, "Database SP")
			},
		},
		{
			name: "Compute SP better than EC2 Instance SP",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 500.0,
					Details: common.SavingsPlanDetails{
						PlanType: "EC2Instance",
					},
				},
				{
					Service:          common.ServiceSavingsPlans,
					EstimatedSavings: 700.0,
					Details: common.SavingsPlanDetails{
						PlanType: "Compute",
					},
				},
			},
			riStats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceEC2: {
					TotalEstimatedSavings: 600.0,
				},
			},
			riSavings: 600.0,
			checkOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "Compute SP")
				assert.Contains(t, output, "700.00")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			output := captureAppOutput(t, func() {
				printComparisonSection(tt.recommendations, tt.riStats, tt.riSavings)
			})

			tt.checkOutput(t, output)
		})
	}
}
