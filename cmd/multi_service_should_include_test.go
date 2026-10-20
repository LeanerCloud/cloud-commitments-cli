package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestShouldIncludeRegion(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name           string
		region         string
		includeRegions []string
		excludeRegions []string
		expected       bool
	}{
		{
			name:           "No filters - should include",
			region:         "us-east-1",
			includeRegions: []string{},
			excludeRegions: []string{},
			expected:       true,
		},
		{
			name:           "In include list",
			region:         "us-east-1",
			includeRegions: []string{"us-east-1", "us-west-2"},
			excludeRegions: []string{},
			expected:       true,
		},
		{
			name:           "Not in include list",
			region:         "eu-west-1",
			includeRegions: []string{"us-east-1"},
			excludeRegions: []string{},
			expected:       false,
		},
		{
			name:           "In exclude list",
			region:         "us-east-1",
			includeRegions: []string{},
			excludeRegions: []string{"us-east-1"},
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg.IncludeRegions = tt.includeRegions
			toolCfg.ExcludeRegions = tt.excludeRegions

			result := shouldIncludeRegion(tt.region, &toolCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludeInstanceType(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name                 string
		instanceType         string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expected             bool
	}{
		{
			name:                 "No filters - should include",
			instanceType:         "db.t3.micro",
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expected:             true,
		},
		{
			name:                 "In include list",
			instanceType:         "cache.t3.micro",
			includeInstanceTypes: []string{"cache.t3.micro"},
			excludeInstanceTypes: []string{},
			expected:             true,
		},
		{
			name:                 "In exclude list",
			instanceType:         "db.t3.large",
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{"db.t3.large"},
			expected:             false,
		},
		{
			name:                 "Not in include list - excluded via whitelist",
			instanceType:         "db.r5.large",
			includeInstanceTypes: []string{"db.t3.micro"},
			excludeInstanceTypes: []string{},
			expected:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg.IncludeInstanceTypes = tt.includeInstanceTypes
			toolCfg.ExcludeInstanceTypes = tt.excludeInstanceTypes

			result := shouldIncludeInstanceType(tt.instanceType, &toolCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludeAccount(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name            string
		accountID       string
		includeAccounts []string
		excludeAccounts []string
		expected        bool
	}{
		{
			name:            "No filters - should include",
			accountID:       "123456789012",
			includeAccounts: []string{},
			excludeAccounts: []string{},
			expected:        true,
		},
		{
			name:            "In include list",
			accountID:       "123456789012",
			includeAccounts: []string{"123456789012", "111111111111"},
			excludeAccounts: []string{},
			expected:        true,
		},
		{
			name:            "Not in include list",
			accountID:       "999888777666",
			includeAccounts: []string{"123456789012"},
			excludeAccounts: []string{},
			expected:        false,
		},
		{
			name:            "In exclude list",
			accountID:       "123456789012",
			includeAccounts: []string{},
			excludeAccounts: []string{"123456789012"},
			expected:        false,
		},
		{
			name:            "Not in exclude list",
			accountID:       "999888777666",
			includeAccounts: []string{},
			excludeAccounts: []string{"123456789012"},
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg.IncludeAccounts = tt.includeAccounts
			toolCfg.ExcludeAccounts = tt.excludeAccounts

			result := shouldIncludeAccount(tt.accountID, &toolCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludeEngine(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name           string
		recommendation common.Recommendation
		includeEngines []string
		excludeEngines []string
		expected       bool
	}{
		{
			name: "ElastiCache Redis - no filters",
			recommendation: common.Recommendation{
				Service: common.ServiceElastiCache,
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			includeEngines: []string{},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "ElastiCache Redis - in include list",
			recommendation: common.Recommendation{
				Service: common.ServiceElastiCache,
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			includeEngines: []string{"redis"},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "ElastiCache Valkey - not in include list",
			recommendation: common.Recommendation{
				Service: common.ServiceElastiCache,
				Details: &common.CacheDetails{
					Engine: "valkey",
				},
			},
			includeEngines: []string{"redis"},
			excludeEngines: []string{},
			expected:       false,
		},
		{
			name: "ElastiCache Redis - in exclude list",
			recommendation: common.Recommendation{
				Service: common.ServiceElastiCache,
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			includeEngines: []string{},
			excludeEngines: []string{"redis"},
			expected:       false,
		},
		{
			name: "RDS with nil Details",
			recommendation: common.Recommendation{
				Service: common.ServiceRDS,
				Details: nil,
			},
			includeEngines: []string{"mysql"},
			excludeEngines: []string{},
			expected:       false, // nil Details with include list - exclude unknown engines
		},
		{
			name: "RDS with nil Details - no filters",
			recommendation: common.Recommendation{
				Service: common.ServiceRDS,
				Details: nil,
			},
			includeEngines: []string{},
			excludeEngines: []string{},
			expected:       true, // nil Details with no filters - include by default
		},
		{
			name: "RDS MySQL - with ServiceDetails",
			recommendation: common.Recommendation{
				Service: common.ServiceRDS,
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			includeEngines: []string{"mysql", "postgresql"},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "Case insensitive matching",
			recommendation: common.Recommendation{
				Service: common.ServiceElastiCache,
				Details: &common.CacheDetails{
					Engine: "Redis",
				},
			},
			includeEngines: []string{"REDIS"},
			excludeEngines: []string{},
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg.IncludeEngines = tt.includeEngines
			toolCfg.ExcludeEngines = tt.excludeEngines

			result := shouldIncludeEngine(&tt.recommendation, &toolCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludePoolSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		avg      float64
		minPool  float64
		expected bool
	}{
		{"filter disabled (0)", 0.5, 0, true},
		{"avg=0 passes through", 0, 2.0, true},
		{"avg below threshold", 1.5, 2.0, false},
		{"avg equal to threshold", 2.0, 2.0, true},
		{"avg above threshold", 3.0, 2.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := common.Recommendation{AverageInstancesUsedPerHour: tt.avg}
			cfg := Config{MinPoolSize: tt.minPool}
			assert.Equal(t, tt.expected, shouldIncludePoolSize(&rec, &cfg))
		})
	}
}

func TestShouldIncludeAccountIDOrNameExactMatch(t *testing.T) {
	t.Parallel()
	const id = "123456789012"
	tests := []struct {
		name        string
		accountName string
		include     []string
		exclude     []string
		want        bool
	}{
		{"include dev does not select devops-prod", "devops-prod", []string{"dev"}, nil, false},
		{"exclude dev does not drop devops-prod", "devops-prod", nil, []string{"dev"}, true},
		{"include exact name different case", "DevOps-Prod", []string{"devops-prod"}, nil, true},
		{"exclude exact name different case", "devops-prod", nil, []string{"DEVOPS-PROD"}, false},
		{"include exact ID", "devops-prod", []string{id}, nil, true},
		{"exclude exact ID", "devops-prod", nil, []string{id}, false},
		{"include name with surrounding whitespace", "dev", []string{"  dev\t"}, nil, true},
		{"exclude ID with surrounding whitespace", "dev", nil, []string{" " + id + " "}, false},
		{"include substring of ID does not match", "devops-prod", []string{"12345"}, nil, false},
		{"exclude substring of ID does not drop", "devops-prod", nil, []string{"9012"}, true},
		{"blank include never matches", "dev", []string{"  "}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{IncludeAccounts: tt.include, ExcludeAccounts: tt.exclude}
			assert.Equal(t, tt.want, shouldIncludeAccountIDOrName(id, tt.accountName, cfg))
		})
	}
}
