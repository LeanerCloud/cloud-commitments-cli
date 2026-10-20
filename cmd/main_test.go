package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// defaultTestAccount is the account the stubbed credentials belong to.
const defaultTestAccount = "111111111111"

// wantPurchaseOutcome is the Success and Error report columns for a dry-run
// row owned by account: the stubbed credentials belong to defaultTestAccount,
// and rows of any other account are refused.
func wantPurchaseOutcome(account string) (success, errMsg string) {
	if account == defaultTestAccount {
		return "true", ""
	}
	return "false", fmt.Sprintf("recommendation is for account %s but the credentials are for account %s; cross-account purchases are not supported, so run with credentials for account %s", account, defaultTestAccount, account)
}

// useCallerAccount gives toolCfg the stubbed credentials' account for one test,
// for tests that call the purchase loops directly instead of through an entry
// point (which resolves the account itself).
func useCallerAccount(t *testing.T) {
	t.Helper()
	prev := toolCfg.CallerAccountID
	toolCfg.CallerAccountID = defaultTestAccount
	t.Cleanup(func() { toolCfg.CallerAccountID = prev })
}

// TestMain overrides toolCfg.AuditLog's cobra-registered default
// ("./cudly-audit.jsonl", relative to the process working directory) before
// any test in this package runs. Since #1609, processPurchaseLoop (shared by
// the --input-csv path and the legacy per-region purchase path) writes a real
// audit record via cfg.AuditLog on every dry-run and real purchase attempt.
// Without this override, any test that reaches that loop without setting its
// own AuditLog would silently create/append to a stray cmd/cudly-audit.jsonl
// file in the repo working directory on every `go test` run. Tests that need
// to assert on audit-log contents still set their own t.TempDir()-scoped
// AuditLog, which takes precedence within that test.
func TestMain(m *testing.M) {
	toolCfg.AuditLog = filepath.Join(os.TempDir(), fmt.Sprintf("cudly-test-audit-%d.jsonl", os.Getpid()))
	// Keep tests off AWS: the real fetcher is exercised explicitly against a
	// local stub (useRealEngineVersionFetcher).
	engineVersionFetcher = func(context.Context, Config, bool) (engineVersionData, error) {
		return engineVersionData{}, nil
	}
	callerAccountFetcher = func(context.Context, aws.Config) (string, error) {
		return defaultTestAccount, nil
	}
	finishGuard, err := startOffhostGuard()
	if err != nil {
		fmt.Fprintln(os.Stderr, "off-host guard:", err)
		os.Exit(1)
	}
	code := m.Run()
	if reportOffhost(finishGuard()) {
		code = 1
	}
	_ = os.Remove(toolCfg.AuditLog)
	os.Exit(code)
}

func TestRunTool(t *testing.T) {
	// Skip this integration test that requires AWS credentials
	t.Skip("Skipping integration test that requires AWS credentials - functionality tested in TestProcessServiceWithMocks")

	// This test would validate that runTool correctly delegates to runToolMultiService
	// but it requires actual AWS credentials to run.
	// The actual functionality is tested in TestProcessServiceWithMocks in multi_service_test.go
	// which uses mocked AWS clients and doesn't require credentials.
}

func TestInit(t *testing.T) {
	// Test that init properly sets up command flags
	// This is called automatically, so we just verify the flags exist

	assert.NotNil(t, rootCmd)
	assert.Equal(t, "ri-helper", rootCmd.Use)

	// Check that flags are defined
	flag := rootCmd.Flags().Lookup("regions")
	assert.NotNil(t, flag)
	assert.Equal(t, "r", flag.Shorthand)

	flag = rootCmd.Flags().Lookup("services")
	assert.NotNil(t, flag)
	assert.Equal(t, "s", flag.Shorthand)

	flag = rootCmd.Flags().Lookup("all-services")
	assert.NotNil(t, flag)

	flag = rootCmd.Flags().Lookup("coverage")
	assert.NotNil(t, flag)
	assert.Equal(t, "c", flag.Shorthand)

	flag = rootCmd.Flags().Lookup("purchase")
	assert.NotNil(t, flag)

	flag = rootCmd.Flags().Lookup("output")
	assert.NotNil(t, flag)
	assert.Equal(t, "o", flag.Shorthand)

	flag = rootCmd.Flags().Lookup("payment")
	assert.NotNil(t, flag)
	assert.Equal(t, "p", flag.Shorthand)

	flag = rootCmd.Flags().Lookup("term")
	assert.NotNil(t, flag)
	assert.Equal(t, "t", flag.Shorthand)
}

func TestMainFunction(t *testing.T) {
	// Save original args
	origArgs := rootCmd.Args

	// Test with help flag to avoid actual execution
	rootCmd.SetArgs([]string{"--help"})

	// Run main should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("main() panicked: %v", r)
		}
		// Restore
		rootCmd.Args = origArgs
	}()

	// We can't easily test main() directly due to log.Fatalf
	// but we can test the command structure
	assert.NotNil(t, rootCmd)
}

func TestCommandFlags(t *testing.T) {
	tests := []struct {
		name         string
		flagName     string
		shorthand    string
		defaultValue string
	}{
		{"regions flag", "regions", "r", "[]"},
		{"services flag", "services", "s", "[rds]"},
		{"coverage flag", "coverage", "c", "80"},
		{"payment flag", "payment", "p", "no-upfront"},
		{"term flag", "term", "t", "3"},
		{"output flag", "output", "o", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := rootCmd.Flags().Lookup(tt.flagName)
			assert.NotNil(t, flag)
			if tt.shorthand != "" {
				assert.Equal(t, tt.shorthand, flag.Shorthand)
			}
			assert.Equal(t, tt.defaultValue, flag.DefValue)
		})
	}
}

// TestEffectiveSizingPct locks the rule that target-coverage wins over
// coverage when both are configured. Without this, dry-run purchase IDs (and
// any other audit label that calls effectiveSizingPct) print the unused
// default Coverage=80 instead of the actual target value the user passed.
// Regression guard for the Aurora-target / RDS-target CLI dry-run runs.
func TestEffectiveSizingPct(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want float64
	}{
		{"target unset, coverage default", Config{Coverage: 80}, 80},
		{"target unset, coverage custom", Config{Coverage: 50}, 50},
		{"target set, coverage default ignored", Config{TargetCoverage: 70, Coverage: 80}, 70},
		{"target set, coverage explicit ignored", Config{TargetCoverage: 95, Coverage: 30}, 95},
		{"target zero falls back to coverage", Config{TargetCoverage: 0, Coverage: 60}, 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, effectiveSizingPct(tt.cfg))
		})
	}
}

func TestValidateFlagsExtended(t *testing.T) {
	// Save original toolCfg
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		setCSVInput        string
		setCSVOutput       string
		errorContains      string
		setPayment         string
		name               string
		setIncludeEngines  []string
		setExcludeAccounts []string
		setIncludeAccounts []string
		setExcludeEngines  []string
		setIncludeTypes    []string
		setExcludeTypes    []string
		setCoverage        float64
		setTerm            int
		setMaxInstances    int32
		expectError        bool
	}{
		// Coverage boundary tests
		{
			name:        "Coverage at minimum boundary (0)",
			setCoverage: 0.0,
			setTerm:     1,
			setPayment:  "no-upfront",
			expectError: false,
		},
		{
			name:        "Coverage at maximum boundary (100)",
			setCoverage: 100.0,
			setTerm:     1,
			setPayment:  "no-upfront",
			expectError: false,
		},
		{
			name:          "Coverage below minimum",
			setCoverage:   -0.001,
			setTerm:       1,
			setPayment:    "no-upfront",
			expectError:   true,
			errorContains: "coverage percentage must be between 0 and 100",
		},
		{
			name:          "Coverage above maximum",
			setCoverage:   100.001,
			setTerm:       1,
			setPayment:    "no-upfront",
			expectError:   true,
			errorContains: "coverage percentage must be between 0 and 100",
		},
		{
			name:        "Coverage with decimals",
			setCoverage: 75.5,
			setTerm:     3,
			setPayment:  "partial-upfront",
			expectError: false,
		},

		// Max instances tests
		{
			name:            "Max instances zero (no limit)",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setMaxInstances: 0,
			expectError:     false,
		},
		{
			name:            "Max instances positive",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setMaxInstances: 100,
			expectError:     false,
		},
		{
			name:            "Max instances negative",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setMaxInstances: -5,
			expectError:     true,
			errorContains:   "max-instances must be 0",
		},

		// Payment option tests
		{
			name:        "Payment all-upfront",
			setCoverage: 80.0,
			setTerm:     3,
			setPayment:  "all-upfront",
			expectError: false,
		},
		{
			name:          "Payment invalid mixed case",
			setCoverage:   80.0,
			setTerm:       1,
			setPayment:    "All-Upfront",
			expectError:   true,
			errorContains: "invalid payment option",
		},
		{
			name:          "Payment empty string",
			setCoverage:   80.0,
			setTerm:       1,
			setPayment:    "",
			expectError:   true,
			errorContains: "invalid payment option",
		},

		// Term tests
		{
			name:          "Term zero",
			setCoverage:   80.0,
			setTerm:       0,
			setPayment:    "no-upfront",
			expectError:   true,
			errorContains: "invalid term",
		},
		{
			name:          "Term negative",
			setCoverage:   80.0,
			setTerm:       -1,
			setPayment:    "no-upfront",
			expectError:   true,
			errorContains: "invalid term",
		},
		{
			name:          "Term five years",
			setCoverage:   80.0,
			setTerm:       5,
			setPayment:    "no-upfront",
			expectError:   true,
			errorContains: "invalid term",
		},

		// Engine conflict tests
		{
			name:              "Engine conflict",
			setCoverage:       80.0,
			setTerm:           1,
			setPayment:        "no-upfront",
			setIncludeEngines: []string{"mysql", "postgres"},
			setExcludeEngines: []string{"postgres", "redis"},
			expectError:       true,
			errorContains:     "engine 'postgres' cannot be both included and excluded",
		},
		{
			name:              "No engine conflict",
			setCoverage:       80.0,
			setTerm:           1,
			setPayment:        "no-upfront",
			setIncludeEngines: []string{"mysql"},
			setExcludeEngines: []string{"postgres"},
			expectError:       false,
		},

		// Instance type validation tests
		{
			name:            "Invalid include instance type",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setIncludeTypes: []string{"invalidtype"}, // No dot, should fail validation
			expectError:     true,
			errorContains:   "invalid include-instance-types",
		},
		{
			name:            "Invalid exclude instance type",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setExcludeTypes: []string{"badinstance"}, // No dot, should fail validation
			expectError:     true,
			errorContains:   "invalid exclude-instance-types",
		},
		{
			name:            "Valid instance types",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setIncludeTypes: []string{"db.t3.small", "cache.t3.small"},
			setExcludeTypes: []string{"db.m5.large"},
			expectError:     false,
		},

		// Combined validations
		{
			name:              "All valid flags combined",
			setCoverage:       85.5,
			setTerm:           3,
			setPayment:        "partial-upfront",
			setMaxInstances:   50,
			setIncludeTypes:   []string{"db.t3.small"},
			setExcludeTypes:   []string{"db.m5.large"},
			setIncludeEngines: []string{"mysql"},
			setExcludeEngines: []string{"postgres"},
			expectError:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test values
			toolCfg.Coverage = tt.setCoverage
			toolCfg.TermYears = tt.setTerm
			toolCfg.PaymentOption = tt.setPayment
			toolCfg.MaxInstances = tt.setMaxInstances
			toolCfg.CSVOutput = tt.setCSVOutput
			toolCfg.CSVInput = tt.setCSVInput
			toolCfg.IncludeEngines = tt.setIncludeEngines
			toolCfg.ExcludeEngines = tt.setExcludeEngines
			toolCfg.IncludeAccounts = tt.setIncludeAccounts
			toolCfg.ExcludeAccounts = tt.setExcludeAccounts
			toolCfg.IncludeInstanceTypes = tt.setIncludeTypes
			toolCfg.ExcludeInstanceTypes = tt.setExcludeTypes

			// Call validateFlags
			err := validateFlags(nil, []string{})

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

func TestServicesCommandRejectsUnknownBeforeRun(t *testing.T) {
	original := toolCfg
	t.Cleanup(func() { toolCfg = original })
	tests := []struct {
		name          string
		args          []string
		errorContains string
	}{
		{"mixed", []string{"--services=rds,elasticahe"}, "elasticahe"},
		{"unknown", []string{"--services=elasticahe"}, "elasticahe"},
		{"empty", []string{"--services="}, "--services must contain"},
		{"empty element", []string{"--services=rds,"}, "unknown service \"\""},
		{"all-services", []string{"--all-services", "--services=rds,elasticahe"}, "elasticahe"},
		{"CSV", []string{"--input-csv=missing.csv", "--services=rds,elasticahe"}, "elasticahe"},
		{"empty all-services", []string{"--all-services", "--services="}, "--services must contain"},
		{"empty CSV", []string{"--input-csv=missing.csv", "--services="}, "--services must contain"},
		{"default", nil, ""},
		{"valid", []string{"--services=RDS,SP,elasticsearch"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg = Config{Coverage: 80, CoverageLookbackDays: 30, PaymentOption: "partial-upfront", TermYears: 1, RecLookbackPeriod: "30d", IdempotencyWindow: "24h"}
			ran := false
			cmd := &cobra.Command{Use: "test", PreRunE: validateFlags, Run: func(*cobra.Command, []string) { ran = true }, SilenceUsage: true, SilenceErrors: true}
			cmd.Flags().StringSliceVar(&toolCfg.Services, "services", []string{"rds"}, "")
			cmd.Flags().BoolVar(&toolCfg.AllServices, "all-services", false, "")
			cmd.Flags().StringVar(&toolCfg.CSVInput, "input-csv", "", "")
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if tt.errorContains != "" {
				assert.ErrorContains(t, err, tt.errorContains)
				assert.False(t, ran, "invalid services must stop before Run")
			} else {
				assert.NoError(t, err)
				assert.True(t, ran)
			}
		})
	}
}
