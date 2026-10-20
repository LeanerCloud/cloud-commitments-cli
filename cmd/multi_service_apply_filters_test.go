package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestApplyFilters_RegionFiltering(t *testing.T) {
	tests := []struct {
		name           string
		currentRegion  string
		recs           []common.Recommendation
		includeRegions []string
		excludeRegions []string
		expectedCount  int
	}{
		{
			name: "No region filters - all pass",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-west-2", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
				{Region: "eu-west-1", ResourceType: "db.t3.large", Count: 3, Service: common.ServiceRDS},
			},
			includeRegions: []string{},
			excludeRegions: []string{},
			currentRegion:  "",
			expectedCount:  3,
		},
		{
			name: "Include specific regions",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-west-2", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
				{Region: "eu-west-1", ResourceType: "db.t3.large", Count: 3, Service: common.ServiceRDS},
			},
			includeRegions: []string{"us-east-1", "us-west-2"},
			excludeRegions: []string{},
			currentRegion:  "",
			expectedCount:  2,
		},
		{
			name: "Exclude specific regions",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-west-2", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
				{Region: "eu-west-1", ResourceType: "db.t3.large", Count: 3, Service: common.ServiceRDS},
			},
			includeRegions: []string{},
			excludeRegions: []string{"us-west-2"},
			currentRegion:  "",
			expectedCount:  2,
		},
		{
			name: "Current region filter with RDS",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-west-2", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
			},
			includeRegions: []string{},
			excludeRegions: []string{},
			currentRegion:  "us-east-1",
			expectedCount:  1,
		},
		{
			name: "Savings Plans bypass region filter",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "ComputeSP", Count: 1, Service: common.ServiceSavingsPlansAll},
				{Region: "us-west-2", ResourceType: "ComputeSP", Count: 2, Service: common.ServiceSavingsPlansAll},
			},
			includeRegions: []string{},
			excludeRegions: []string{},
			currentRegion:  "us-east-1",
			expectedCount:  2, // Both should pass, SP is account-level
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeRegions: tt.includeRegions,
				ExcludeRegions: tt.excludeRegions,
			}

			result := applyFilters(tt.recs, &cfg, map[string][]InstanceEngineVersion{}, map[string]MajorEngineVersionInfo{}, tt.currentRegion, nil)
			assert.Equal(t, tt.expectedCount, len(result), "Expected %d recommendations, got %d", tt.expectedCount, len(result))
		})
	}
}

func TestApplyFilters_InstanceTypeFiltering(t *testing.T) {
	tests := []struct {
		name                 string
		recs                 []common.Recommendation
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expectedCount        int
	}{
		{
			name: "Include specific instance types",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.r5.large", Count: 3, Service: common.ServiceRDS},
			},
			includeInstanceTypes: []string{"db.t3.small", "db.t3.medium"},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Exclude specific instance types",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.r5.large", Count: 3, Service: common.ServiceRDS},
			},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{"db.r5.large"},
			expectedCount:        2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeInstanceTypes: tt.includeInstanceTypes,
				ExcludeInstanceTypes: tt.excludeInstanceTypes,
			}

			result := applyFilters(tt.recs, &cfg, map[string][]InstanceEngineVersion{}, map[string]MajorEngineVersionInfo{}, "", nil)
			assert.Equal(t, tt.expectedCount, len(result))
		})
	}
}

func TestApplyFilters_EngineFiltering(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		includeEngines []string
		excludeEngines []string
		expectedCount  int
	}{
		{
			name: "Include specific engines",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "postgresql"}},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "mysql"}},
				{Region: "us-east-1", ResourceType: "cache.t3.small", Count: 3, Service: common.ServiceElastiCache, Details: &common.CacheDetails{Engine: "redis"}},
			},
			includeEngines: []string{"postgresql", "mysql"},
			excludeEngines: []string{},
			expectedCount:  2,
		},
		{
			name: "Exclude specific engines",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "postgresql"}},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "mysql"}},
				{Region: "us-east-1", ResourceType: "cache.t3.small", Count: 3, Service: common.ServiceElastiCache, Details: &common.CacheDetails{Engine: "redis"}},
			},
			includeEngines: []string{},
			excludeEngines: []string{"redis"},
			expectedCount:  2,
		},
		{
			name: "Case insensitive engine matching",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "PostgreSQL"}},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "MySQL"}},
			},
			includeEngines: []string{"postgresql", "mysql"},
			excludeEngines: []string{},
			expectedCount:  2,
		},
		{
			name: "No engine details - with include list",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			includeEngines: []string{"mysql"},
			excludeEngines: []string{},
			expectedCount:  1, // Only the one with engine details
		},
		{
			name: "No engine details - no include list",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS},
			},
			includeEngines: []string{},
			excludeEngines: []string{},
			expectedCount:  2, // All pass
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeEngines: tt.includeEngines,
				ExcludeEngines: tt.excludeEngines,
			}

			result := applyFilters(tt.recs, &cfg, map[string][]InstanceEngineVersion{}, map[string]MajorEngineVersionInfo{}, "", nil)
			assert.Equal(t, tt.expectedCount, len(result))
		})
	}
}

func TestApplyFilters_AccountFiltering(t *testing.T) {
	tests := []struct {
		name            string
		recs            []common.Recommendation
		includeAccounts []string
		excludeAccounts []string
		expectedCount   int
	}{
		{
			name: "Include specific accounts",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, AccountName: "prod-account"},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, AccountName: "dev-account"},
				{Region: "us-east-1", ResourceType: "db.t3.large", Count: 3, Service: common.ServiceRDS, AccountName: "staging-account"},
			},
			includeAccounts: []string{"prod-account", "DEV-account"},
			excludeAccounts: []string{},
			expectedCount:   2, // Exact, case-insensitive match
		},
		{
			name: "Exclude specific accounts",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, AccountName: "prod-account"},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, AccountName: "dev-account"},
			},
			includeAccounts: []string{},
			excludeAccounts: []string{"dev-account"},
			expectedCount:   1,
		},
		{
			name: "Empty account name with filters",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, AccountName: ""},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, AccountName: "prod"},
			},
			includeAccounts: []string{"prod"},
			excludeAccounts: []string{},
			expectedCount:   1, // Empty account name is filtered out
		},
		{
			name: "Empty account name without filters",
			recs: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, AccountName: ""},
				{Region: "us-east-1", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, AccountName: "prod"},
			},
			includeAccounts: []string{},
			excludeAccounts: []string{},
			expectedCount:   2, // All pass
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeAccounts: tt.includeAccounts,
				ExcludeAccounts: tt.excludeAccounts,
			}

			result := applyFilters(tt.recs, &cfg, map[string][]InstanceEngineVersion{}, map[string]MajorEngineVersionInfo{}, "", nil)
			assert.Equal(t, tt.expectedCount, len(result))
		})
	}
}

func TestApplyFilters_CombinedFilters(t *testing.T) {
	recs := []common.Recommendation{
		{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "postgresql"}, AccountName: "prod-account"},
		{Region: "us-west-2", ResourceType: "db.t3.medium", Count: 2, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "mysql"}, AccountName: "dev-account"},
		{Region: "us-east-1", ResourceType: "db.r5.large", Count: 3, Service: common.ServiceRDS, Details: &common.DatabaseDetails{Engine: "postgresql"}, AccountName: "staging-account"},
		{Region: "eu-west-1", ResourceType: "cache.t3.small", Count: 4, Service: common.ServiceElastiCache, Details: &common.CacheDetails{Engine: "redis"}, AccountName: "prod-account"},
	}

	cfg := Config{
		IncludeRegions:       []string{"us-east-1", "us-west-2"},
		IncludeEngines:       []string{"postgresql", "mysql"},
		ExcludeInstanceTypes: []string{"db.r5.large"},
		IncludeAccounts:      []string{"prod-account", "dev-account"},
	}

	result := applyFilters(recs, &cfg, map[string][]InstanceEngineVersion{}, map[string]MajorEngineVersionInfo{}, "", nil)

	// Only the first two should pass all filters
	assert.Equal(t, 2, len(result))
	if len(result) >= 2 {
		assert.Equal(t, "db.t3.small", result[0].ResourceType)
		assert.Equal(t, "db.t3.medium", result[1].ResourceType)
	}
}
