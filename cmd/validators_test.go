package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

func TestValidateNumericRanges(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func()
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid coverage percentage",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 100
				toolCfg.OverrideCount = 10
			},
			wantErr: false,
		},
		{
			name: "coverage below zero",
			setupFunc: func() {
				toolCfg.Coverage = -1.0
			},
			wantErr: true,
			errMsg:  "coverage percentage must be between 0 and 100",
		},
		{
			name: "coverage above 100",
			setupFunc: func() {
				toolCfg.Coverage = 101.0
			},
			wantErr: true,
			errMsg:  "coverage percentage must be between 0 and 100",
		},
		{
			name: "negative max instances",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = -1
			},
			wantErr: true,
			errMsg:  "max-instances must be 0",
		},
		{
			name: "max instances exceeds limit",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = MaxReasonableInstances + 1
			},
			wantErr: true,
			errMsg:  "max-instances",
		},
		{
			name: "negative override count",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 100
				toolCfg.OverrideCount = -1
			},
			wantErr: true,
			errMsg:  "override-count must be 0",
		},
		{
			name: "override count exceeds limit",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 100
				toolCfg.OverrideCount = MaxReasonableInstances + 1
			},
			wantErr: true,
			errMsg:  "override-count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			toolCfg = Config{}
			tt.setupFunc()

			// Execute
			err := validateNumericRanges()

			// Verify
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateNumericRanges() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateNumericRanges() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateNumericRanges() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestValidatePaymentAndTerm(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func()
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid payment option - no-upfront",
			setupFunc: func() {
				toolCfg.PaymentOption = "no-upfront"
				toolCfg.TermYears = 3
				toolCfg.Services = []string{"elasticache"}
			},
			wantErr: false,
		},
		{
			name: "valid payment option - all-upfront",
			setupFunc: func() {
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 1
			},
			wantErr: false,
		},
		{
			name: "valid payment option - partial-upfront",
			setupFunc: func() {
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 3
			},
			wantErr: false,
		},
		{
			name: "invalid payment option",
			setupFunc: func() {
				toolCfg.PaymentOption = "invalid-option"
				toolCfg.TermYears = 3
			},
			wantErr: true,
			errMsg:  "invalid payment option",
		},
		{
			name: "invalid term - 2 years",
			setupFunc: func() {
				toolCfg.PaymentOption = "no-upfront"
				toolCfg.TermYears = 2
			},
			wantErr: true,
			errMsg:  "invalid term",
		},
		{
			name: "invalid term - 0 years",
			setupFunc: func() {
				toolCfg.PaymentOption = "no-upfront"
				toolCfg.TermYears = 0
			},
			wantErr: true,
			errMsg:  "invalid term",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			toolCfg = Config{}
			tt.setupFunc()

			// Execute
			err := validatePaymentAndTerm()

			// Verify
			if tt.wantErr {
				if err == nil {
					t.Errorf("validatePaymentAndTerm() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validatePaymentAndTerm() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validatePaymentAndTerm() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestContainsService(t *testing.T) {
	tests := []struct {
		name     string
		services []common.ServiceType
		service  common.ServiceType
		want     bool
	}{
		{
			name:     "service found in list",
			services: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
			service:  common.ServiceRDS,
			want:     true,
		},
		{
			name:     "service not found in list",
			services: []common.ServiceType{common.ServiceRDS, common.ServiceEC2},
			service:  common.ServiceElastiCache,
			want:     false,
		},
		{
			name:     "empty list",
			services: []common.ServiceType{},
			service:  common.ServiceRDS,
			want:     false,
		},
		{
			name:     "single service list - match",
			services: []common.ServiceType{common.ServiceRDS},
			service:  common.ServiceRDS,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsService(tt.services, tt.service)
			if got != tt.want {
				t.Errorf("containsService() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateFilePaths(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		setupFunc func() func()
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid CSV output path",
			setupFunc: func() func() {
				toolCfg.CSVOutput = filepath.Join(tmpDir, "output.csv")
				toolCfg.CSVInput = ""
				return func() {}
			},
			wantErr: false,
		},
		{
			name: "valid CSV input path",
			setupFunc: func() func() {
				// Create a test CSV file
				inputPath := filepath.Join(tmpDir, "input.csv")
				if err := os.WriteFile(inputPath, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVInput = inputPath
				toolCfg.CSVOutput = ""
				return func() {
					os.Remove(inputPath)
				}
			},
			wantErr: false,
		},
		{
			name: "CSV output directory does not exist",
			setupFunc: func() func() {
				toolCfg.CSVOutput = "/nonexistent/directory/output.csv"
				toolCfg.CSVInput = ""
				return func() {}
			},
			wantErr: true,
			errMsg:  "output directory does not exist",
		},
		{
			name: "CSV input file does not exist",
			setupFunc: func() func() {
				toolCfg.CSVInput = filepath.Join(tmpDir, "nonexistent.csv")
				toolCfg.CSVOutput = ""
				return func() {}
			},
			wantErr: true,
			errMsg:  "input CSV file does not exist",
		},
		{
			name: "CSV input file wrong extension",
			setupFunc: func() func() {
				// Create a test file with wrong extension
				inputPath := filepath.Join(tmpDir, "input.txt")
				if err := os.WriteFile(inputPath, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVInput = inputPath
				toolCfg.CSVOutput = ""
				return func() {
					os.Remove(inputPath)
				}
			},
			wantErr: true,
			errMsg:  "input file must have .csv extension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			toolCfg = Config{}
			cleanup := tt.setupFunc()
			defer cleanup()

			// Execute
			err := validateFilePaths()

			// Verify
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateFilePaths() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateFilePaths() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateFilePaths() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestValidateNoConflicts(t *testing.T) {
	tests := []struct {
		name     string
		include  []string
		exclude  []string
		itemType string
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "no conflicts",
			include:  []string{"us-east-1", "us-west-2"},
			exclude:  []string{"eu-west-1", "ap-south-1"},
			itemType: "region",
			wantErr:  false,
		},
		{
			name:     "conflict found",
			include:  []string{"us-east-1", "us-west-2"},
			exclude:  []string{"us-west-2", "eu-west-1"},
			itemType: "region",
			wantErr:  true,
			errMsg:   "region 'us-west-2' cannot be both included and excluded",
		},
		{
			name:     "empty include list",
			include:  []string{},
			exclude:  []string{"us-west-2"},
			itemType: "region",
			wantErr:  false,
		},
		{
			name:     "empty exclude list",
			include:  []string{"us-east-1"},
			exclude:  []string{},
			itemType: "region",
			wantErr:  false,
		},
		{
			name:     "both lists empty",
			include:  []string{},
			exclude:  []string{},
			itemType: "region",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoConflicts(tt.include, tt.exclude, tt.itemType)

			if tt.wantErr {
				if err == nil {
					t.Errorf("validateNoConflicts() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("validateNoConflicts() error = %v, want %v", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateNoConflicts() unexpected error = %v", err)
				}
			}
		})
	}
}

// Note: TestValidateInstanceTypes and TestValidateFlags already exist in main_test.go
