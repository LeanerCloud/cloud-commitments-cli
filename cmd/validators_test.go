package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/spf13/cobra"
)

func TestValidateNumericRanges(t *testing.T) {
	tests := []struct {
		setupFunc func()
		name      string
		errMsg    string
		wantErr   bool
	}{
		{
			name: "valid coverage percentage",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 100
				toolCfg.OverrideCount = 10
				toolCfg.CoverageLookbackDays = 30
			},
			wantErr: false,
		},
		{
			name: "coverage below zero",
			setupFunc: func() {
				toolCfg.Coverage = -1.0
				toolCfg.CoverageLookbackDays = 30
			},
			wantErr: true,
			errMsg:  "coverage percentage must be between 0 and 100",
		},
		{
			name: "coverage above 100",
			setupFunc: func() {
				toolCfg.Coverage = 101.0
				toolCfg.CoverageLookbackDays = 30
			},
			wantErr: true,
			errMsg:  "coverage percentage must be between 0 and 100",
		},
		{
			name: "negative max instances",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = -1
				toolCfg.CoverageLookbackDays = 30
			},
			wantErr: true,
			errMsg:  "max-instances must be 0",
		},
		{
			name: "max instances exceeds limit",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = MaxReasonableInstances + 1
				toolCfg.CoverageLookbackDays = 30
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
				toolCfg.CoverageLookbackDays = 30
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
				toolCfg.CoverageLookbackDays = 30
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

			// Execute (nil cmd — these tests only exercise the numeric
			// bounds checks; the flag-source-detection branch is covered
			// by TestValidateTargetCoverage below).
			err := validateNumericRanges(nil)

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
		setupFunc func()
		name      string
		errMsg    string
		wantErr   bool
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
			name: "empty payment option",
			setupFunc: func() {
				toolCfg.PaymentOption = ""
				toolCfg.TermYears = 1
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
		service  common.ServiceType
		services []common.ServiceType
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
		setupFunc func() func()
		name      string
		errMsg    string
		wantErr   bool
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
		itemType string
		errMsg   string
		include  []string
		exclude  []string
		wantErr  bool
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
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateNoConflicts() error = %v, want error containing %v", err, tt.errMsg)
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

// TestValidateTargetCoverage covers the --target-coverage range check
// and the "both flags explicitly set" info-log gate. Range cases pass nil
// cmd (the explicit-flag detection is irrelevant for them); the "both set"
// case constructs a real cobra command and marks --coverage as explicitly
// set so the Changed("coverage") branch actually fires. The log line
// itself isn't asserted (log.Printf goes to stderr and capturing it from
// this package is more friction than value).
func TestValidateTargetCoverage(t *testing.T) {
	tests := []struct {
		name        string
		errSubstr   string
		target      float64
		coverage    float64
		wantErr     bool
		useCobraCmd bool
	}{
		{name: "disabled (zero) is valid", target: 0, coverage: 80, wantErr: false},
		{name: "min boundary valid", target: 0.0001, coverage: 80, wantErr: false},
		{name: "max boundary valid", target: 100, coverage: 80, wantErr: false},
		{name: "mid-range valid", target: 95, coverage: 80, wantErr: false},
		{
			name: "negative target rejected", target: -0.5, coverage: 80, wantErr: true,
			errSubstr: "target-coverage percentage must be between 0 and 100",
		},
		{
			name: "above 100 rejected", target: 100.01, coverage: 80, wantErr: true,
			errSubstr: "target-coverage percentage must be between 0 and 100",
		},
		{
			// Both flags explicitly set — the precedence info-log fires;
			// validation still passes. useCobraCmd builds a real command so
			// Changed("coverage") returns true; without this the branch is
			// effectively dead code in the test.
			name:        "target + coverage both set, both valid",
			target:      95,
			coverage:    50,
			wantErr:     false,
			useCobraCmd: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg = Config{Coverage: tt.coverage, TargetCoverage: tt.target, CoverageLookbackDays: 30}
			var cmd *cobra.Command
			if tt.useCobraCmd {
				cmd = &cobra.Command{Use: "test"}
				// Default doesn't matter — the Set call below marks the flag
				// as Changed, which is what validateTargetCoverage checks.
				cmd.Flags().Float64("coverage", 80, "")
				if err := cmd.Flags().Set("coverage", "50"); err != nil {
					t.Fatalf("failed to mark coverage flag as Changed: %v", err)
				}
			}
			err := validateNumericRanges(cmd)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateNumericRanges() expected error containing %q, got nil", tt.errSubstr)
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("validateNumericRanges() error = %v, want substring %q", err, tt.errSubstr)
				}
			} else if err != nil {
				t.Errorf("validateNumericRanges() unexpected error = %v", err)
			}
		})
	}
}

// TestValidateCoverageLookbackDays verifies that validateNumericRanges rejects
// non-positive --coverage-lookback-days and accepts positive values.
func TestValidateCoverageLookbackDays(t *testing.T) {
	tests := []struct {
		name      string
		errSubstr string
		days      int
		wantErr   bool
	}{
		{name: "default 30 is valid", days: 30, wantErr: false},
		{name: "1 day is valid", days: 1, wantErr: false},
		{name: "90 days is valid", days: 90, wantErr: false},
		{
			name:      "zero rejected",
			days:      0,
			wantErr:   true,
			errSubstr: "coverage-lookback-days must be >= 1",
		},
		{
			name:      "negative rejected",
			days:      -1,
			wantErr:   true,
			errSubstr: "coverage-lookback-days must be >= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCfg := toolCfg
			defer func() { toolCfg = origCfg }()
			toolCfg = Config{
				Coverage:             80,
				CoverageLookbackDays: tt.days,
			}
			err := validateNumericRanges(nil)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateNumericRanges() expected error containing %q, got nil", tt.errSubstr)
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("validateNumericRanges() error = %v, want substring %q", err, tt.errSubstr)
				}
			} else if err != nil {
				t.Errorf("validateNumericRanges() unexpected error = %v", err)
			}
		})
	}
}

// TestValidateRecLookbackPeriod verifies that validateRecLookbackPeriod accepts
// the three valid values and rejects anything else, including empty string.
func TestValidateRecLookbackPeriod(t *testing.T) {
	tests := []struct {
		name      string
		period    string
		wantErr   bool
		errSubstr string
	}{
		{name: "7d valid", period: "7d", wantErr: false},
		{name: "30d valid", period: "30d", wantErr: false},
		{name: "60d valid", period: "60d", wantErr: false},
		{name: "empty rejected", period: "", wantErr: true, errSubstr: "invalid rec-lookback-period"},
		{name: "14d rejected", period: "14d", wantErr: true, errSubstr: "invalid rec-lookback-period"},
		{name: "90d rejected", period: "90d", wantErr: true, errSubstr: "invalid rec-lookback-period"},
		{name: "SEVEN_DAYS rejected", period: "SEVEN_DAYS", wantErr: true, errSubstr: "invalid rec-lookback-period"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCfg := toolCfg
			defer func() { toolCfg = origCfg }()
			toolCfg.RecLookbackPeriod = tt.period
			err := validateRecLookbackPeriod()
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateRecLookbackPeriod() expected error containing %q, got nil", tt.errSubstr)
				} else if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("validateRecLookbackPeriod() error = %v, want substring %q", err, tt.errSubstr)
				}
			} else if err != nil {
				t.Errorf("validateRecLookbackPeriod() unexpected error = %v", err)
			}
		})
	}
}
