package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestCreateServiceClient(t *testing.T) {
	t.Parallel()
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
			name:      "Compute Savings Plans service",
			service:   common.ServiceSavingsPlansCompute,
			expectNil: false,
		},
		{
			name:      "EC2 Instance Savings Plans service",
			service:   common.ServiceSavingsPlansEC2Instance,
			expectNil: false,
		},
		{
			name:      "SageMaker Savings Plans service",
			service:   common.ServiceSavingsPlansSageMaker,
			expectNil: false,
		},
		{
			name:      "Database Savings Plans service",
			service:   common.ServiceSavingsPlansDatabase,
			expectNil: false,
		},
		{
			// Umbrella sentinel is no longer dispatched to a client — the
			// per-plan-type slugs above carry the actual work. Keep the case
			// to lock the contract: createServiceClient returns nil for the
			// umbrella so the caller knows to fan out.
			name:      "Savings Plans umbrella sentinel (returns nil)",
			service:   common.ServiceSavingsPlansAll,
			expectNil: true,
		},
		{
			name:      "Unknown service",
			service:   common.ServiceType("unknown"),
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := createServiceClient(tt.service, cfg)
			if tt.expectNil {
				assert.Nil(t, client)
			} else {
				assert.NotNil(t, client)
			}
		})
	}
}

func TestCreateServiceClientAllServices(t *testing.T) {
	t.Parallel()
	cfg := aws.Config{
		Region: "eu-central-1",
	}

	// Test that all services return non-nil clients now
	services := getAllServices()
	for _, service := range services {
		client := createServiceClient(service, cfg)
		assert.NotNil(t, client, "Service %s should have a client", service)
	}
}

func TestSanitizeAccountName(t *testing.T) {
	t.Parallel()
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

func TestValidateInstanceTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		errorContains string
		instanceTypes []string
		expectError   bool
	}{
		{
			name:          "Empty slice is valid",
			instanceTypes: []string{},
			expectError:   false,
		},
		{
			name:          "Valid instance types",
			instanceTypes: []string{"db.t3.micro", "cache.r5.large", "t3.medium"},
			expectError:   false,
		},
		{
			name:          "Invalid - no dot",
			instanceTypes: []string{"t3micro"},
			expectError:   true,
			errorContains: "invalid instance type format",
		},
		{
			name:          "Invalid - empty string",
			instanceTypes: []string{"db.t3.micro", "", "cache.r5.large"},
			expectError:   true,
			errorContains: "empty instance type",
		},
		{
			name:          "Valid with multiple dots",
			instanceTypes: []string{"r5.xlarge.search", "db.r6g.2xlarge"},
			expectError:   false,
		},
		{
			name:          "Single valid instance type",
			instanceTypes: []string{"m5.large"},
			expectError:   false,
		},
		{
			name:          "Mix of valid and invalid",
			instanceTypes: []string{"db.t3.small", "invalidtype"},
			expectError:   true,
			errorContains: "invalid instance type format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInstanceTypes(tt.instanceTypes)
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
