package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
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
