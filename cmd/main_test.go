package main

import (
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestParseServices(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []common.ServiceType
	}{
		{
			name:  "Valid services",
			input: []string{"rds", "elasticache", "ec2"},
			expected: []common.ServiceType{
				common.ServiceRDS,
				common.ServiceElastiCache,
				common.ServiceEC2,
			},
		},
		{
			name:  "Mixed case services",
			input: []string{"RDS", "ElastiCache", "EC2"},
			expected: []common.ServiceType{
				common.ServiceRDS,
				common.ServiceElastiCache,
				common.ServiceEC2,
			},
		},
		{
			name:     "Invalid services",
			input:    []string{"invalid", "unknown"},
			expected: nil,
		},
		{
			name:  "Mix of valid and invalid",
			input: []string{"rds", "invalid", "ec2"},
			expected: []common.ServiceType{
				common.ServiceRDS,
				common.ServiceEC2,
			},
		},
		{
			name:  "All supported services",
			input: []string{"rds", "elasticache", "ec2", "opensearch", "redshift", "memorydb"},
			expected: []common.ServiceType{
				common.ServiceRDS,
				common.ServiceElastiCache,
				common.ServiceEC2,
				common.ServiceOpenSearch,
				common.ServiceRedshift,
				common.ServiceMemoryDB,
			},
		},
		{
			name:  "Legacy elasticsearch alias",
			input: []string{"elasticsearch"},
			expected: []common.ServiceType{
				common.ServiceElasticsearch,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseServices(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetAllServices(t *testing.T) {
	services := getAllServices()

	expected := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}

	assert.Equal(t, expected, services)
}

func TestGeneratePurchaseID(t *testing.T) {
	tests := []struct {
		name           string
		rec            any
		region         string
		index          int
		isDryRun       bool
		coverage       float64
		expectedPrefix string
	}{
		{
			name: "Common Recommendation - dry run",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t3.micro",
				Count:        2,
			},
			region:         "us-east-1",
			index:          1,
			isDryRun:       true,
			coverage:       80.0,
			expectedPrefix: "dryrun-rds-us-east-1-db-t3-micro-2x",
		},
		{
			name: "Common Recommendation - actual purchase",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				InstanceType: "t3.large",
				Count:        5,
			},
			region:         "eu-west-1",
			index:          3,
			isDryRun:       false,
			coverage:       80.0,
			expectedPrefix: "ri-ec2-eu-west-1-t3-large-5x",
		},
		{
			name: "Legacy Recommendation - dry run",
			rec: recommendations.Recommendation{
				Engine:       "mysql",
				InstanceType: "db.r5.large",
				Count:        1,
				AZConfig:     "single",
			},
			region:         "us-west-2",
			index:          2,
			isDryRun:       true,
			coverage:       80.0,
			expectedPrefix: "dryrun-mysql-r5-large-1x-80pct-saz-us-west-2",
		},
		{
			name: "Legacy Recommendation - multi-AZ",
			rec: recommendations.Recommendation{
				Engine:       "postgres",
				InstanceType: "db.m5.xlarge",
				Count:        3,
				AZConfig:     "multi-az",  // GetMultiAZ() checks for "multi-az"
			},
			region:         "ap-southeast-1",
			index:          5,
			isDryRun:       false,
			coverage:       80.0,
			expectedPrefix: "ri-postgres-m5-xlarge-3x-80pct-maz-ap-southeast-1",
		},
		{
			name:           "Unknown type",
			rec:            "invalid",
			region:         "us-east-1",
			index:          1,
			isDryRun:       true,
			coverage:       80.0,
			expectedPrefix: "dryrun-unknown-us-east-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun, tt.coverage)
			assert.Contains(t, result, tt.expectedPrefix)
			// Should contain timestamp (YYYYMMDD-HHMMSS) and UUID suffix (8 chars)
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, result)
		})
	}
}

func TestCreatePurchaseClient(t *testing.T) {
	cfg := aws.Config{
		Region: "us-east-1",
	}

	tests := []struct {
		name      string
		service   common.ServiceType
		expectNil bool
	}{
		{
			name:      "RDS service",
			service:   common.ServiceRDS,
			expectNil: false,
		},
		{
			name:      "ElastiCache service",
			service:   common.ServiceElastiCache,
			expectNil: false,
		},
		{
			name:      "EC2 service",
			service:   common.ServiceEC2,
			expectNil: false,
		},
		{
			name:      "OpenSearch service",
			service:   common.ServiceOpenSearch,
			expectNil: false,
		},
		{
			name:      "Elasticsearch service",
			service:   common.ServiceElasticsearch,
			expectNil: false,
		},
		{
			name:      "Redshift service",
			service:   common.ServiceRedshift,
			expectNil: false,
		},
		{
			name:      "MemoryDB service",
			service:   common.ServiceMemoryDB,
			expectNil: false,
		},
		{
			name:      "Unknown service",
			service:   common.ServiceType("unknown"),
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := createPurchaseClient(tt.service, cfg)
			if tt.expectNil {
				assert.Nil(t, client)
			} else {
				assert.NotNil(t, client)
			}
		})
	}
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

func TestGeneratePurchaseIDEdgeCases(t *testing.T) {
	testCoverage := 80.0

	// Test with recommendations that have special characters
	rec := recommendations.Recommendation{
		Engine:       "MySQL 8.0",
		InstanceType: "db.r5b.2xlarge",
		Count:        10,
		AZConfig:     "single",
	}

	id := generatePurchaseID(rec, "us-east-1", 999, false, testCoverage)
	assert.Contains(t, id, "mysql-8.0")  // Engine keeps dots, only replaces spaces and underscores
	assert.Contains(t, id, "r5b-2xlarge")
	assert.Contains(t, id, "10x")
	// Index is no longer included due to UUID replacement

	// Test with empty region
	id = generatePurchaseID(rec, "", 1, true, testCoverage)
	assert.Contains(t, id, "dryrun")

	// Test with very long instance type
	rec.InstanceType = "db.x2gd.metal.16xlarge"
	id = generatePurchaseID(rec, "ap-south-1", 1, false, testCoverage)
	assert.Contains(t, id, "x2gd-metal")
}

func TestGeneratePurchaseIDComprehensive(t *testing.T) {
	// Use a test coverage value
	testCoverage := 75.0

	tests := []struct {
		name                 string
		rec                  any
		region               string
		isDryRun             bool
		expectedContains     []string
		expectedNotContains  []string
	}{
		{
			name: "RDS with account name and engine",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.r5.large",
				Count:        3,
				AccountName:  "Production Account",
				ServiceDetails: &common.RDSDetails{
					Engine: "PostgreSQL",
				},
			},
			region:   "eu-west-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "production-account", "rds", "postgresql", "eu-west-1",
				"db-r5-large", "3x", "75pct",
			},
		},
		{
			name: "ElastiCache with Redis engine",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				InstanceType: "cache.r5.xlarge",
				Count:        5,
				ServiceDetails: &common.ElastiCacheDetails{
					Engine: "Redis",
				},
			},
			region:   "us-west-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "elasticache", "redis", "us-west-2",
				"cache-r5-xlarge", "5x", "75pct",
			},
		},
		{
			name: "EC2 with platform",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				InstanceType: "m5.2xlarge",
				Count:        10,
				ServiceDetails: &common.EC2Details{
					Platform: "Linux/UNIX",
				},
			},
			region:   "ap-southeast-1",
			isDryRun: true,
			expectedContains: []string{
				"dryrun-", "ec2", "linux-unix", "ap-southeast-1",
				"m5-2xlarge", "10x", "75pct",
			},
		},
		{
			name: "MemoryDB recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceMemoryDB,
				InstanceType: "db.r6g.large",
				Count:        2,
				ServiceDetails: &common.MemoryDBDetails{},
			},
			region:   "us-east-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "memorydb", "memorydb", "us-east-1",
				"db-r6g-large", "2x", "75pct",
			},
		},
		{
			name: "OpenSearch without engine",
			rec: common.Recommendation{
				Service:      common.ServiceOpenSearch,
				InstanceType: "r5.large.search",
				Count:        4,
			},
			region:   "eu-central-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "opensearch", "eu-central-1",
				"r5-large-search", "4x", "75pct",
			},
		},
		{
			name: "Redshift without engine",
			rec: common.Recommendation{
				Service:      common.ServiceRedshift,
				InstanceType: "dc2.large",
				Count:        8,
			},
			region:   "us-east-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "redshift", "us-east-2",
				"dc2-large", "8x", "75pct",
			},
		},
		{
			name: "Legacy recommendation with account",
			rec: recommendations.Recommendation{
				Engine:       "aurora-mysql",
				InstanceType: "db.r6g.xlarge",
				Count:        15,
				AZConfig:     "multi-az",
				AccountName:  "Staging",
			},
			region:   "ca-central-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "staging", "aurora-mysql", "r6g-xlarge",
				"15x", "75pct", "maz", "ca-central-1",
			},
		},
		{
			name: "Legacy single-AZ recommendation",
			rec: recommendations.Recommendation{
				Engine:       "redis",
				InstanceType: "cache.m5.large",
				Count:        1,
				AZConfig:     "single",
			},
			region:   "ap-northeast-1",
			isDryRun: true,
			expectedContains: []string{
				"dryrun-", "redis", "m5-large",
				"1x", "75pct", "saz", "ap-northeast-1",
			},
		},
		{
			name: "Recommendation with special characters in engine",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t3.micro",
				Count:        20,
				ServiceDetails: &common.RDSDetails{
					Engine: "MySQL_8.0_Community",
				},
			},
			region:   "us-west-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "rds", "mysql-8.0-community",
				"db-t3-micro", "20x", "75pct",
			},
		},
		{
			name: "Large count recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				InstanceType: "t3.nano",
				Count:        999,
			},
			region:   "eu-west-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "ec2", "eu-west-2",
				"t3-nano", "999x", "75pct",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, 1, tt.isDryRun, testCoverage)

			// Check expected contains
			for _, expected := range tt.expectedContains {
				assert.Contains(t, result, expected, "Expected ID to contain '%s'", expected)
			}

			// Check expected not contains
			for _, notExpected := range tt.expectedNotContains {
				assert.NotContains(t, result, notExpected, "Expected ID not to contain '%s'", notExpected)
			}

			// Should always contain timestamp and UUID
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, result)
		})
	}
}

func TestGeneratePurchaseIDCoverageVariations(t *testing.T) {
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		InstanceType: "db.t3.small",
		Count:        1,
		ServiceDetails: &common.RDSDetails{
			Engine: "mysql",
		},
	}

	tests := []struct {
		name             string
		coverage         float64
		expectedCoverage string
	}{
		{"Coverage 0%", 0.0, "0pct"},
		{"Coverage 50%", 50.0, "50pct"},
		{"Coverage 75.5%", 75.5, "76pct"}, // Rounds to nearest integer
		{"Coverage 99%", 99.0, "99pct"},
		{"Coverage 100%", 100.0, "100pct"},
		{"Coverage 33.3%", 33.3, "33pct"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(rec, "us-east-1", 1, false, tt.coverage)
			assert.Contains(t, result, tt.expectedCoverage)
		})
	}
}

func TestParseServicesWithEmptyAndNil(t *testing.T) {
	// Empty slice
	result := parseServices([]string{})
	assert.Empty(t, result)

	// Slice with empty strings
	result = parseServices([]string{"", "rds", ""})
	assert.Len(t, result, 1)
	assert.Equal(t, common.ServiceRDS, result[0])

	// All invalid
	result = parseServices([]string{"foo", "bar", "baz"})
	assert.Empty(t, result)
}

func TestFilterFlagValidation(t *testing.T) {
	// Save original toolCfg values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name                 string
		includeRegions       []string
		excludeRegions       []string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expectError          bool
		errorContains        string
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
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCreatePurchaseClientAllServices(t *testing.T) {
	cfg := aws.Config{
		Region: "eu-central-1",
	}

	// Test that all services return non-nil clients now
	services := getAllServices()
	for _, service := range services {
		client := createPurchaseClient(service, cfg)
		assert.NotNil(t, client, "Service %s should have a client", service)
	}
}

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name        string
		setCoverage float64
		setTerm     int
		setPayment  string
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

func TestValidateFlagsExtended(t *testing.T) {
	// Save original toolCfg
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name                 string
		setCoverage          float64
		setTerm              int
		setPayment           string
		setMaxInstances      int32
		setCSVOutput         string
		setCSVInput          string
		setIncludeEngines    []string
		setExcludeEngines    []string
		setIncludeAccounts   []string
		setExcludeAccounts   []string
		setIncludeTypes      []string
		setExcludeTypes      []string
		expectError          bool
		errorContains        string
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
			setIncludeTypes: []string{"invalid.type"},
			expectError:     true,
			errorContains:   "invalid include-instance-types",
		},
		{
			name:            "Invalid exclude instance type",
			setCoverage:     80.0,
			setTerm:         1,
			setPayment:      "no-upfront",
			setExcludeTypes: []string{"bad.instance.type"},
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
			name:        "All valid flags combined",
			setCoverage: 85.5,
			setTerm:     3,
			setPayment:  "partial-upfront",
			setMaxInstances: 50,
			setIncludeTypes: []string{"db.t3.small"},
			setExcludeTypes: []string{"db.m5.large"},
			setIncludeEngines: []string{"mysql"},
			setExcludeEngines: []string{"postgres"},
			expectError: false,
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
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSanitizeAccountName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Simple name", "production", "production"},
		{"Spaces to hyphens", "my account", "my-account"},
		{"Underscores to hyphens", "my_account", "my-account"},
		{"Uppercase to lowercase", "PRODUCTION", "production"},
		{"Special chars removed", "my@account#123", "myaccount123"},
		{"Dots to hyphens", "my.account.com", "my-account-com"},
		{"Long name preserved", "very-long-production-environment-name", "very-long-production-environment-name"},
		{"Empty string", "", ""},
		{"Only special chars", "@#$%", ""},
		{"Multiple hyphens collapsed", "my---account", "my-account"},
		{"Leading/trailing hyphens removed", "-account-", "account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeAccountName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}