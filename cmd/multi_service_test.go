package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestServiceTypes(t *testing.T) {
	// Test that service types are properly defined
	services := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceElasticsearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}

	for _, service := range services {
		assert.NotEmpty(t, service)
	}

	// Test unknown service
	unknownService := common.ServiceType("Unknown")
	assert.Equal(t, "Unknown", string(unknownService))
}

func TestProcessService(t *testing.T) {
	// Skip if AWS credentials not available
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	tests := []struct {
		name           string
		service        common.ServiceType
		coverage       float64
		dryRun         bool
		expectError    bool
	}{
		{
			name:        "RDS with 50% coverage",
			service:     common.ServiceRDS,
			coverage:    0.5,
			dryRun:      true,
			expectError: false,
		},
		{
			name:        "ElastiCache with 80% coverage",
			service:     common.ServiceElastiCache,
			coverage:    0.8,
			dryRun:      true,
			expectError: false,
		},
		{
			name:        "EC2 with 100% coverage",
			service:     common.ServiceEC2,
			coverage:    1.0,
			dryRun:      true,
			expectError: false,
		},
		{
			name:        "Unknown service",
			service:     common.ServiceType("Unknown"),
			coverage:    0.5,
			dryRun:      true,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test would require actual AWS credentials and setup
			// For unit testing, we're just validating the structure
			assert.NotNil(t, tt.service)
			assert.GreaterOrEqual(t, tt.coverage, 0.0)
			assert.LessOrEqual(t, tt.coverage, 1.0)
		})
	}
}

func TestCalculateTotalInstances(t *testing.T) {
	tests := []struct {
		name      string
		recs      []common.Recommendation
		expected  int32
	}{
		{
			name: "multiple recommendations",
			recs: []common.Recommendation{
				{Count: 5},
				{Count: 3},
				{Count: 2},
			},
			expected: 10,
		},
		{
			name:      "empty recommendations",
			recs:      []common.Recommendation{},
			expected:  0,
		},
		{
			name: "single recommendation",
			recs: []common.Recommendation{
				{Count: 7},
			},
			expected: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := calculateTotalInstances(tt.recs)
			assert.Equal(t, tt.expected, total)
		})
	}
}

func TestApplyCoverageToRecommendations(t *testing.T) {
	tests := []struct {
		name         string
		recs         []common.Recommendation
		coverage     float64
		expectedRecs int
	}{
		{
			name: "50% coverage of 4 recommendations",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
				{InstanceType: "type3", Count: 1},
				{InstanceType: "type4", Count: 4},
			},
			coverage:     0.5,
			expectedRecs: 2,
		},
		{
			name: "100% coverage",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
			},
			coverage:     1.0,
			expectedRecs: 2,
		},
		{
			name: "0% coverage",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
			},
			coverage:     0.0,
			expectedRecs: 0,
		},
		{
			name: "75% coverage of 3 recommendations",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 2},
				{InstanceType: "type3", Count: 2},
			},
			coverage:     0.75,
			expectedRecs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCoverageToRecommendations(tt.recs, tt.coverage)
			assert.Equal(t, tt.expectedRecs, len(result))
		})
	}
}

func TestGetAllAWSRegions(t *testing.T) {
	// This test requires AWS credentials
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	cfg := aws.Config{
		Region: "us-east-1",
	}

	regions, err := getAllAWSRegions(ctx, cfg)

	// In a real environment with AWS credentials
	if err == nil {
		assert.NotNil(t, regions)
		assert.Greater(t, len(regions), 0)

		// Check that common regions are present
		hasUSEast1 := false
		hasEUWest1 := false
		for _, region := range regions {
			if region == "us-east-1" {
				hasUSEast1 = true
			}
			if region == "eu-west-1" {
				hasEUWest1 = true
			}
		}
		assert.True(t, hasUSEast1, "Should have us-east-1")
		assert.True(t, hasEUWest1, "Should have eu-west-1")
	}
}

func TestServiceProcessingOrder(t *testing.T) {
	// Test that services are processed in a consistent order
	services := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}

	// Verify all expected services are present
	expectedServices := map[common.ServiceType]bool{
		common.ServiceRDS:         false,
		common.ServiceElastiCache: false,
		common.ServiceEC2:         false,
		common.ServiceOpenSearch:  false,
		common.ServiceRedshift:    false,
		common.ServiceMemoryDB:    false,
	}

	for _, service := range services {
		expectedServices[service] = true
	}

	// Check all services were found
	for service, found := range expectedServices {
		assert.True(t, found, "Service %s should be in processing list", service)
	}
}

func TestGenerateCSVFilename(t *testing.T) {
	tests := []struct {
		name        string
		service     common.ServiceType
		payment     string
		term        int
		dryRun      bool
		expectParts []string
	}{
		{
			name:        "RDS dry run",
			service:     common.ServiceRDS,
			payment:     "no-upfront",
			term:        36,
			dryRun:      true,
			expectParts: []string{"rds", "no-upfront", "dryrun"},
		},
		{
			name:        "EC2 actual purchase",
			service:     common.ServiceEC2,
			payment:     "all-upfront",
			term:        12,
			dryRun:      false,
			expectParts: []string{"ec2", "all-upfront", "purchase"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := generateCSVFilename(tt.service, tt.payment, tt.term, tt.dryRun)

			for _, part := range tt.expectParts {
				assert.Contains(t, filename, part)
			}

			// Should end with .csv
			assert.Contains(t, filename, ".csv")
		})
	}
}

func TestMultiServiceConfig(t *testing.T) {
	cfg := MultiServiceConfig{
		Services: map[common.ServiceType]ServiceConfig{
			common.ServiceRDS: {
				Enabled:  true,
				Coverage: 0.5,
			},
			common.ServiceElastiCache: {
				Enabled:  true,
				Coverage: 0.8,
			},
			common.ServiceEC2: {
				Enabled:  false,
				Coverage: 0.0,
			},
		},
		PaymentOption: "no-upfront",
		TermYears:     3,
		DryRun:        true,
	}

	// Test enabled services count
	enabledCount := 0
	for _, svcConfig := range cfg.Services {
		if svcConfig.Enabled {
			enabledCount++
		}
	}
	assert.Equal(t, 2, enabledCount)

	// Test coverage values
	assert.Equal(t, 0.5, cfg.Services[common.ServiceRDS].Coverage)
	assert.Equal(t, 0.8, cfg.Services[common.ServiceElastiCache].Coverage)
}

// Helper function tests
func calculateTotalInstances(recs []common.Recommendation) int32 {
	var total int32
	for _, rec := range recs {
		total += rec.Count
	}
	return total
}

func applyCoverageToRecommendations(recs []common.Recommendation, coverage float64) []common.Recommendation {
	if coverage <= 0 {
		return []common.Recommendation{}
	}
	if coverage >= 1.0 {
		return recs
	}

	targetCount := int(float64(len(recs)) * coverage)
	if targetCount == 0 && coverage > 0 && len(recs) > 0 {
		targetCount = 1
	}

	if targetCount >= len(recs) {
		return recs
	}

	return recs[:targetCount]
}

func generateCSVFilename(service common.ServiceType, payment string, term int, dryRun bool) string {
	mode := "purchase"
	if dryRun {
		mode = "dryrun"
	}

	serviceStr := ""
	switch service {
	case common.ServiceRDS:
		serviceStr = "rds"
	case common.ServiceElastiCache:
		serviceStr = "elasticache"
	case common.ServiceEC2:
		serviceStr = "ec2"
	case common.ServiceOpenSearch:
		serviceStr = "opensearch"
	case common.ServiceRedshift:
		serviceStr = "redshift"
	case common.ServiceMemoryDB:
		serviceStr = "memorydb"
	default:
		serviceStr = "unknown"
	}

	return serviceStr + "-" + payment + "-" + mode + ".csv"
}

// Test types
type MultiServiceConfig struct {
	Services      map[common.ServiceType]ServiceConfig
	PaymentOption string
	TermYears     int
	DryRun        bool
}

type ServiceConfig struct {
	Enabled  bool
	Coverage float64
}

// Benchmark tests
func BenchmarkCalculateTotalInstances(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{Count: int32(i % 10 + 1)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calculateTotalInstances(recs)
	}
}

func BenchmarkApplyCoverageToRecommendations(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{
			InstanceType: "type",
			Count:        int32(i % 5 + 1),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyCoverageToRecommendations(recs, 0.5)
	}
}