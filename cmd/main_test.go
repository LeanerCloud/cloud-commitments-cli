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
			expectedPrefix: "dryrun-mysql-r5-large-1x-saz-us-west-2",
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
			expectedPrefix: "ri-postgres-m5-xlarge-3x-maz-ap-southeast-1",
		},
		{
			name:           "Unknown type",
			rec:            "invalid",
			region:         "us-east-1",
			index:          1,
			isDryRun:       true,
			expectedPrefix: "dryrun-unknown-us-east-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun)
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
	// Test with recommendations that have special characters
	rec := recommendations.Recommendation{
		Engine:       "MySQL 8.0",
		InstanceType: "db.r5b.2xlarge",
		Count:        10,
		AZConfig:     "single",
	}

	id := generatePurchaseID(rec, "us-east-1", 999, false)
	assert.Contains(t, id, "mysql-8.0")  // Engine keeps dots, only replaces spaces and underscores
	assert.Contains(t, id, "r5b-2xlarge")
	assert.Contains(t, id, "10x")
	// Index is no longer included due to UUID replacement

	// Test with empty region
	id = generatePurchaseID(rec, "", 1, true)
	assert.Contains(t, id, "dryrun")

	// Test with very long instance type
	rec.InstanceType = "db.x2gd.metal.16xlarge"
	id = generatePurchaseID(rec, "ap-south-1", 1, false)
	assert.Contains(t, id, "x2gd-metal")
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
	// Save original values
	origIncludeRegions := includeRegions
	origExcludeRegions := excludeRegions
	origIncludeTypes := includeInstanceTypes
	origExcludeTypes := excludeInstanceTypes
	origCoverage := coverage
	origPaymentOption := paymentOption
	origTermYears := termYears

	defer func() {
		includeRegions = origIncludeRegions
		excludeRegions = origExcludeRegions
		includeInstanceTypes = origIncludeTypes
		excludeInstanceTypes = origExcludeTypes
		coverage = origCoverage
		paymentOption = origPaymentOption
		termYears = origTermYears
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
			// Set test values
			includeRegions = tt.includeRegions
			excludeRegions = tt.excludeRegions
			includeInstanceTypes = tt.includeInstanceTypes
			excludeInstanceTypes = tt.excludeInstanceTypes
			coverage = 80.0
			paymentOption = "no-upfront"
			termYears = 3

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