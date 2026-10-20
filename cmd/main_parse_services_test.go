package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestParseServices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		input         []string
		expected      []common.ServiceType
		errorContains string
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
			name:          "Invalid services",
			errorContains: "invalid",
			input:         []string{"invalid", "unknown"},
			expected:      nil,
		},
		{
			name:          "Mix of valid and invalid",
			input:         []string{"rds", "invalid", "ec2"},
			errorContains: "invalid",
			expected:      nil,
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
			result, err := parseServices(tt.input)
			if tt.errorContains != "" {
				assert.ErrorContains(t, err, tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetAllServices(t *testing.T) {
	t.Parallel()
	services := getAllServices()

	expected := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
		common.ServiceSavingsPlansCompute,
		common.ServiceSavingsPlansEC2Instance,
		common.ServiceSavingsPlansSageMaker,
		common.ServiceSavingsPlansDatabase,
	}

	assert.Equal(t, expected, services)
}

func TestParseServicesWithEmptyAndNil(t *testing.T) {
	t.Parallel()
	for _, input := range [][]string{nil, {}} {
		result, err := parseServices(input)
		assert.NoError(t, err)
		assert.Nil(t, result)
	}
	for _, input := range [][]string{{"", "rds", ""}, {"rds", ""}, {"foo", "bar", "baz"}, {" rds"}} {
		result, err := parseServices(input)
		assert.Error(t, err)
		assert.Nil(t, result)
	}
}

func TestParseServicesSavingsPlanAliases(t *testing.T) {
	t.Parallel()
	expected := []common.ServiceType{common.ServiceSavingsPlansCompute, common.ServiceSavingsPlansEC2Instance, common.ServiceSavingsPlansSageMaker, common.ServiceSavingsPlansDatabase}
	for _, alias := range []string{"savingsplans", "savings-plans", "SP"} {
		result, err := parseServices([]string{"ec2", alias, "savingsplans-compute", "rds", "EC2"})
		assert.NoError(t, err)
		assert.Equal(t, append(append([]common.ServiceType{common.ServiceEC2}, expected...), common.ServiceRDS), result)
	}
	for i, name := range []string{"compute", "ec2instance", "sagemaker", "database"} {
		result, err := parseServices([]string{"savingsplans-" + name, "savings-plans-" + name})
		assert.NoError(t, err)
		assert.Equal(t, []common.ServiceType{expected[i]}, result)
	}
}
