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
