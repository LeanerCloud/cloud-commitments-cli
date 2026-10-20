package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
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
		{
			name: "min-pool-size negative",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
				toolCfg.CoverageLookbackDays = 30
				toolCfg.MinPoolSize = -1.0
			},
			wantErr: true,
			errMsg:  "min-pool-size must be 0 (disabled) or a positive whole number",
		},
		{
			name: "min-pool-size fractional rejected",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
				toolCfg.CoverageLookbackDays = 30
				toolCfg.MinPoolSize = 1.5
			},
			wantErr: true,
			errMsg:  "min-pool-size must be a whole number",
		},
		{
			name: "min-pool-size disabled (zero)",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
				toolCfg.CoverageLookbackDays = 30
				toolCfg.MinPoolSize = 0
			},
			wantErr: false,
		},
		{
			name: "min-pool-size whole number accepted",
			setupFunc: func() {
				toolCfg.Coverage = 80.0
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
				toolCfg.CoverageLookbackDays = 30
				toolCfg.MinPoolSize = 2.0
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			t.Cleanup(saveGlobalVars().restore)
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
			t.Cleanup(saveGlobalVars().restore)
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

// TestCheckDuplicates_HonorsIdempotencyWindow reproduces the #1262 scenario:
// a re-run 30h after a purchase with --idempotency-window 72h must subtract
// that purchase. With the window ignored the lookback stayed at 24h and the
// same 5 RIs were bought again.
func TestCheckDuplicates_HonorsIdempotencyWindow(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()
	toolCfg.IdempotencyWindow = "72h"
	if err := validateIdempotencyWindow(); err != nil {
		t.Fatalf("validateIdempotencyWindow: %v", err)
	}

	ctx := context.Background()
	recs := []common.Recommendation{{
		ResourceType: "db.t3.small", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}}
	existing := []common.Commitment{{
		ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql",
		Count: 5, State: "active", StartDate: time.Now().Add(-30 * time.Hour),
	}}
	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return(existing, nil)
	t.Cleanup(func() { mockClient.AssertExpectations(t) })

	if got := checkDuplicates(ctx, recs, mockClient, false /* isDryRun */, nil); CalculateTotalInstances(got) != 0 {
		t.Errorf("checkDuplicates kept %d instance(s); the 30h-old purchase is inside the 72h window and must be subtracted", CalculateTotalInstances(got))
	}
	if got, err := adjustRecsForDuplicates(ctx, recs, mockClient); err != nil || CalculateTotalInstances(got) != 0 {
		t.Errorf("adjustRecsForDuplicates (CSV path) kept %d instance(s), err=%v; want 0", CalculateTotalInstances(got), err)
	}
}

// TestValidateNumericFlagsRejectInvalid drives the real flag set through
// rootCmd.ParseFlags (cobra/pflag parse "NaN" and "Inf" as floats) and then
// the validators, covering non-finite and out-of-range values. Nothing here
// calls runTool, so no AWS client is built.
func TestValidateNumericFlagsRejectInvalid(t *testing.T) {
	saved := toolCfg
	reset := func() {
		toolCfg = Config{}
		for _, name := range []string{"coverage", "target-coverage", "min-pool-size", "min-savings-pct"} {
			f := rootCmd.Flags().Lookup(name)
			if f == nil {
				t.Fatalf("flag %q missing", name)
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}
	t.Cleanup(func() { reset(); toolCfg = saved })

	tests := []struct {
		name     string
		args     []string
		parseErr bool
		wantErr  string
	}{
		{name: "target NaN", args: []string{"--target-coverage", "NaN"}, wantErr: "target-coverage must be a finite number, got: NaN"},
		{name: "target nan lowercase", args: []string{"--target-coverage", "nan"}, wantErr: "target-coverage must be a finite number, got: NaN"},
		{name: "target +Inf", args: []string{"--target-coverage", "+Inf"}, wantErr: "target-coverage must be a finite number, got: +Inf"},
		{name: "target -Inf", args: []string{"--target-coverage", "-Inf"}, wantErr: "target-coverage must be a finite number, got: -Inf"},
		{name: "coverage NaN", args: []string{"--coverage", "NaN"}, wantErr: "coverage must be a finite number, got: NaN"},
		{name: "coverage Inf", args: []string{"--coverage", "Inf"}, wantErr: "coverage must be a finite number, got: +Inf"},
		{name: "min-pool-size NaN", args: []string{"--min-pool-size", "NaN"}, wantErr: "min-pool-size must be a finite number, got: NaN"},
		{name: "min-savings-pct NaN", args: []string{"--min-savings-pct", "NaN"}, wantErr: "min-savings-pct must be a finite number, got: NaN"},
		{name: "min-savings-pct Inf", args: []string{"--min-savings-pct", "Inf"}, wantErr: "min-savings-pct must be a finite number, got: +Inf"},
		{name: "min-savings-pct negative", args: []string{"--min-savings-pct", "-1"}, wantErr: "min-savings-pct must be between 0 and 100, got: -1.00"},
		{name: "min-savings-pct above 100", args: []string{"--min-savings-pct", "100.5"}, wantErr: "min-savings-pct must be between 0 and 100, got: 100.50"},
		{name: "min-savings-pct bounds stay valid", args: []string{"--min-savings-pct", "100"}},
		{name: "overflow rejected by the flag parser", args: []string{"--target-coverage", "1e999"}, parseErr: true},
		{name: "explicit target 0 stays valid", args: []string{"--target-coverage", "0"}},
		{name: "valid values pass", args: []string{"--target-coverage", "95", "--coverage", "50", "--min-pool-size", "2", "--min-savings-pct", "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reset()
			toolCfg.CoverageLookbackDays = 30
			err := rootCmd.ParseFlags(tt.args)
			if tt.parseErr {
				if err == nil {
					t.Fatal("expected a flag parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			toolCfg.CoverageLookbackDays = 30
			err = validateNumericRanges(rootCmd)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
