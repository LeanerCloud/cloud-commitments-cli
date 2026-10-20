package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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
			t.Cleanup(saveGlobalVars().restore)
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

// TestValidateIdempotencyWindow covers #1262: the flag used to be accepted
// unparsed, so "banana" or "90m" ran silently with a hardcoded 24h window.
func TestValidateIdempotencyWindow(t *testing.T) {
	tests := []struct {
		window    string
		wantHours int
		wantErr   bool
	}{
		{window: "24h", wantHours: 24},
		{window: "72h", wantHours: 72},
		{window: "1h", wantHours: 1},
		{window: "", wantErr: true},
		{window: "banana", wantErr: true},
		{window: "0h", wantErr: true},
		{window: "-24h", wantErr: true},
		{window: "90m", wantErr: true},
		{window: "1h30m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			origCfg := toolCfg
			defer func() { toolCfg = origCfg }()
			toolCfg.IdempotencyWindow = tt.window
			err := validateIdempotencyWindow()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "invalid idempotency-window") {
					t.Fatalf("validateIdempotencyWindow(%q) error = %v, want invalid idempotency-window", tt.window, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateIdempotencyWindow(%q) unexpected error = %v", tt.window, err)
			}
			if toolCfg.IdempotencyWindowHours != tt.wantHours {
				t.Errorf("IdempotencyWindowHours = %d, want %d", toolCfg.IdempotencyWindowHours, tt.wantHours)
			}
		})
	}
}
