package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterFlagValidation(t *testing.T) {
	// Save original toolCfg values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name                 string
		errorContains        string
		includeRegions       []string
		excludeRegions       []string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expectError          bool
	}{
		{
			name:                 "No conflicts",
			includeRegions:       []string{"us-east-1"},
			excludeRegions:       []string{"us-west-2"},
			includeInstanceTypes: []string{"db.t3.micro"},
			excludeInstanceTypes: []string{"db.t3.large"},
			expectError:          false,
		},
		{
			name:                 "Region conflict",
			includeRegions:       []string{"us-east-1", "us-west-2"},
			excludeRegions:       []string{"us-west-2"},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectError:          true,
			errorContains:        "region 'us-west-2' cannot be both included and excluded",
		},
		{
			name:                 "Instance type conflict",
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{"db.t3.small"},
			excludeInstanceTypes: []string{"db.t3.small"},
			expectError:          true,
			errorContains:        "instance type 'db.t3.small' cannot be both included and excluded",
		},
		{
			name:                 "Empty filters valid",
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectError:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test values in toolCfg
			toolCfg.IncludeRegions = tt.includeRegions
			toolCfg.ExcludeRegions = tt.excludeRegions
			toolCfg.IncludeInstanceTypes = tt.includeInstanceTypes
			toolCfg.ExcludeInstanceTypes = tt.excludeInstanceTypes
			toolCfg.Coverage = 80.0
			toolCfg.PaymentOption = "no-upfront"
			toolCfg.TermYears = 3

			// Call validateFlags
			err := validateFlags(nil, nil)

			if tt.expectError {
				assert.Error(t, err)
				if err != nil && tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name        string
		setPayment  string
		setCoverage float64
		setTerm     int
		expectError bool
	}{
		{
			name:        "Valid flags",
			setCoverage: 80.0,
			setTerm:     1,
			setPayment:  "partial-upfront",
			expectError: false,
		},
		{
			name:        "Coverage too high",
			setCoverage: 150.0,
			setTerm:     1,
			setPayment:  "partial-upfront",
			expectError: true,
		},
		{
			name:        "Coverage negative",
			setCoverage: -10.0,
			setTerm:     1,
			setPayment:  "partial-upfront",
			expectError: true,
		},
		{
			name:        "Invalid term",
			setCoverage: 80.0,
			setTerm:     2,
			setPayment:  "partial-upfront",
			expectError: true,
		},
		{
			name:        "Invalid payment option",
			setCoverage: 80.0,
			setTerm:     1,
			setPayment:  "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original values
			origCfg := toolCfg

			// Set test values
			toolCfg.Coverage = tt.setCoverage
			toolCfg.TermYears = tt.setTerm
			toolCfg.PaymentOption = tt.setPayment

			// Call validateFlags
			err := validateFlags(nil, []string{})

			// Restore original values
			toolCfg = origCfg

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
