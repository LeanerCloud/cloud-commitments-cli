package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestPrintComparisonSection(t *testing.T) {
	tests := []struct {
		riStats         map[common.ServiceType]ServiceProcessingStats
		checkOutput     func(t *testing.T, output string)
		name            string
		recommendations []common.Recommendation
		riSavings       float64
	}{
		{
			name: "Comparison with EC2 RIs and EC2 Instance SP",
			recommendations: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlansAll,
					EstimatedSavings: 600.0,
					Details: &common.SavingsPlanDetails{
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
					Service:          common.ServiceSavingsPlansAll,
					EstimatedSavings: 800.0,
					Details: &common.SavingsPlanDetails{
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
					Service:          common.ServiceSavingsPlansAll,
					EstimatedSavings: 500.0,
					Details: &common.SavingsPlanDetails{
						PlanType: "EC2Instance",
					},
				},
				{
					Service:          common.ServiceSavingsPlansAll,
					EstimatedSavings: 700.0,
					Details: &common.SavingsPlanDetails{
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
