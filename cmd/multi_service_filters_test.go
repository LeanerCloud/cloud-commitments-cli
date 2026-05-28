package main

import (
	"bytes"
	"log"
	"testing"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestApplyFilters(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	// Restore after test
	defer func() {
		toolCfg = origCfg
	}()

	tests := []struct {
		name                 string
		recommendations      []common.Recommendation
		includeRegions       []string
		excludeRegions       []string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expectedCount        int
	}{
		{
			name: "No filters - all pass through",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.small", Count: 1},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Include specific regions only",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.small", Count: 1},
				{Region: "eu-west-1", ResourceType: "db.t3.medium", Count: 1},
			},
			includeRegions:       []string{"us-east-1", "eu-west-1"},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Exclude specific regions",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.small", Count: 1},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{"us-west-2"},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        1,
		},
		{
			name: "Include specific instance types",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.small", Count: 1},
				{Region: "eu-west-1", ResourceType: "db.t3.micro", Count: 1},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{"db.t3.micro"},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Combined filters",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-east-1", ResourceType: "db.t3.small", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.micro", Count: 1},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{"db.t3.micro"},
			expectedCount:        1, // Only us-east-1 with db.t3.small
		},
		{
			name: "Include and exclude same instance type - exclude takes precedence",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1},
				{Region: "us-west-2", ResourceType: "db.t3.small", Count: 1},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{"db.t3.micro", "db.t3.small"},
			excludeInstanceTypes: []string{"db.t3.micro"},
			expectedCount:        1, // db.t3.micro excluded, only db.t3.small remains
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set toolCfg fields
			toolCfg.IncludeRegions = tt.includeRegions
			toolCfg.ExcludeRegions = tt.excludeRegions
			toolCfg.IncludeInstanceTypes = tt.includeInstanceTypes
			toolCfg.ExcludeInstanceTypes = tt.excludeInstanceTypes

			// Apply filters with Config (empty currentRegion for test)
			result := applyFilters(tt.recommendations, &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), "")

			// Check count
			assert.Equal(t, tt.expectedCount, len(result))
		})
	}
}

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
			includeAccounts: []string{"123456789012", "210987654321"},
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

func TestShouldIncludePoolSize(t *testing.T) {
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
			assert.Equal(t, tt.expected, shouldIncludePoolSize(rec, cfg))
		})
	}
}

// TestApplyFilters_PoolSizeLogs verifies that applyFilters emits per-rec and
// summary log lines when --min-pool-size drops recommendations.
func TestApplyFilters_PoolSizeLogs(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	// Capture log output.
	var buf bytes.Buffer
	origFlags := log.Flags()
	origWriter := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0) // strip timestamps so the assertions are stable
	defer func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()

	recs := []common.Recommendation{
		{Service: "rds", Region: "us-east-1", ResourceType: "db.t3.micro", AverageInstancesUsedPerHour: 0.8, Count: 1},
		{Service: "rds", Region: "us-east-1", ResourceType: "db.r5.large", AverageInstancesUsedPerHour: 3.5, Count: 2},
	}
	cfg := Config{MinPoolSize: 2.0}

	result := applyFilters(recs, cfg, nil, nil, "")

	// Only the rec above threshold passes through.
	assert.Len(t, result, 1)
	assert.Equal(t, "db.r5.large", result[0].ResourceType)

	output := buf.String()
	// Per-rec line present.
	assert.Contains(t, output, "--min-pool-size=2.0 dropped rds/us-east-1/db.t3.micro (avg=0.80 < threshold)")
	// Summary line present.
	assert.Contains(t, output, "--min-pool-size dropped 1 recommendation(s)")
}
