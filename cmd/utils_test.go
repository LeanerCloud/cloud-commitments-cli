package main

import (
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestGetAllServices(t *testing.T) {
	services := getAllServices()

	// Should return all 6 services
	assert.Len(t, services, 6)

	// Verify all expected services are present
	expectedServices := map[common.ServiceType]bool{
		common.ServiceRDS:         false,
		common.ServiceElastiCache: false,
		common.ServiceEC2:         false,
		common.ServiceOpenSearch:  false,
		common.ServiceRedshift:    false,
		common.ServiceMemoryDB:    false,
	}

	for _, svc := range services {
		expectedServices[svc] = true
	}

	for svc, found := range expectedServices {
		assert.True(t, found, "Service %s should be in the list", svc)
	}
}

func TestParseServices(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []common.ServiceType
	}{
		{
			name:     "single service",
			input:    []string{"rds"},
			expected: []common.ServiceType{common.ServiceRDS},
		},
		{
			name:     "multiple services",
			input:    []string{"rds", "ec2", "elasticache"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
		},
		{
			name:     "case insensitive",
			input:    []string{"RDS", "EC2", "ElastiCache"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
		},
		{
			name:     "with spaces",
			input:    []string{" rds ", " ec2 "},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceEC2},
		},
		{
			name:     "invalid service ignored",
			input:    []string{"rds", "invalid", "ec2"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceEC2},
		},
		{
			name:     "all services",
			input:    []string{"rds", "elasticache", "ec2", "opensearch", "redshift", "memorydb"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceElastiCache, common.ServiceEC2, common.ServiceOpenSearch, common.ServiceRedshift, common.ServiceMemoryDB},
		},
		{
			name:     "elasticsearch alias",
			input:    []string{"elasticsearch"},
			expected: []common.ServiceType{common.ServiceOpenSearch},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseServices(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetServiceDisplayName(t *testing.T) {
	tests := []struct {
		service  common.ServiceType
		expected string
	}{
		{common.ServiceRDS, "Amazon RDS"},
		{common.ServiceElastiCache, "Amazon ElastiCache"},
		{common.ServiceEC2, "Amazon EC2"},
		{common.ServiceOpenSearch, "Amazon OpenSearch"},
		{common.ServiceRedshift, "Amazon Redshift"},
		{common.ServiceMemoryDB, "Amazon MemoryDB"},
		{common.ServiceType("Unknown"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			result := getServiceDisplayName(tt.service)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatServices(t *testing.T) {
	tests := []struct {
		name     string
		services []common.ServiceType
		expected string
	}{
		{
			name:     "single service",
			services: []common.ServiceType{common.ServiceRDS},
			expected: "RDS",
		},
		{
			name:     "two services",
			services: []common.ServiceType{common.ServiceRDS, common.ServiceEC2},
			expected: "RDS, EC2",
		},
		{
			name:     "multiple services",
			services: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
			expected: "RDS, EC2, ElastiCache",
		},
		{
			name:     "empty list",
			services: []common.ServiceType{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatServices(tt.services)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestApplyCommonCoverage(t *testing.T) {
	tests := []struct {
		name               string
		recommendations    []common.Recommendation
		coveragePercentage float64
		expectedCount      int
	}{
		{
			name: "50% coverage of 4 recommendations",
			recommendations: []common.Recommendation{
				{InstanceType: "type1"},
				{InstanceType: "type2"},
				{InstanceType: "type3"},
				{InstanceType: "type4"},
			},
			coveragePercentage: 0.5,
			expectedCount:      2,
		},
		{
			name: "100% coverage",
			recommendations: []common.Recommendation{
				{InstanceType: "type1"},
				{InstanceType: "type2"},
			},
			coveragePercentage: 1.0,
			expectedCount:      2,
		},
		{
			name:               "empty recommendations",
			recommendations:    []common.Recommendation{},
			coveragePercentage: 0.5,
			expectedCount:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCommonCoverage(tt.recommendations, tt.coveragePercentage)
			assert.Equal(t, tt.expectedCount, len(result))

			// Verify counts are adjusted correctly
			if tt.coveragePercentage < 100.0 && len(result) > 0 {
				for i, res := range result {
					expectedCount := int32(float64(tt.recommendations[i].Count) * (tt.coveragePercentage / 100.0))
					assert.Equal(t, expectedCount, res.Count, "Count should be adjusted by coverage percentage")
				}
			}
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
			name:      "Unknown service",
			service:   common.ServiceType("Unknown"),
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

func TestCalculateServiceStats(t *testing.T) {
	recs := []common.Recommendation{
		{InstanceType: "type1", Count: 2, EstimatedCost: 100},
		{InstanceType: "type2", Count: 3, EstimatedCost: 200},
	}

	results := []common.PurchaseResult{
		{Success: true},
		{Success: false},
	}

	stats := calculateServiceStats(common.ServiceRDS, recs, results)

	assert.Equal(t, common.ServiceRDS, stats.Service)
	assert.Equal(t, 2, stats.RecommendationsFound)
	assert.Equal(t, 1, stats.SuccessfulPurchases)
	assert.Equal(t, 1, stats.FailedPurchases)
}

func TestServiceProcessingStats(t *testing.T) {
	stats := ServiceProcessingStats{
		Service:                 common.ServiceRDS,
		RegionsProcessed:        5,
		RecommendationsFound:    20,
		RecommendationsSelected: 10,
		InstancesProcessed:      25,
		SuccessfulPurchases:     8,
		FailedPurchases:         2,
		TotalEstimatedSavings:   5000.50,
	}

	assert.Equal(t, common.ServiceRDS, stats.Service)
	assert.Equal(t, 5, stats.RegionsProcessed)
	assert.Equal(t, 20, stats.RecommendationsFound)
	assert.Equal(t, 10, stats.RecommendationsSelected)
	assert.Equal(t, int32(25), stats.InstancesProcessed)
	assert.Equal(t, 8, stats.SuccessfulPurchases)
	assert.Equal(t, 2, stats.FailedPurchases)
	assert.Equal(t, 5000.50, stats.TotalEstimatedSavings)
}

// Benchmark tests
func BenchmarkParseServices(b *testing.B) {
	services := []string{"rds", "ec2", "elasticache", "opensearch", "redshift", "memorydb"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseServices(services)
	}
}

func BenchmarkApplyCommonCoverage(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{
			InstanceType:  "type",
			EstimatedCost: float64(i * 100),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyCommonCoverage(recs, 0.5)
	}
}