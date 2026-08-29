package main

import (
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			result := applyFilters(tt.recommendations, &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), "", nil)

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
			assert.Equal(t, tt.expected, shouldIncludePoolSize(&rec, &cfg))
		})
	}
}

// TestApplyFilters_DropMinPoolSize verifies that a recommendation whose
// AverageInstancesUsedPerHour is below --min-pool-size is recorded in a
// non-nil DropSummary under the DropMinPoolSize category. If the
// drops.Add call for that path were removed, d.Total() would stay at 0
// and the first assertion below would fail.
func TestApplyFilters_DropMinPoolSize(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	// Pool with avg=1.5 is below min-pool-size=2; it should be dropped.
	rec := common.Recommendation{
		Region:                      "us-east-1",
		ResourceType:                "db.t3.micro",
		Count:                       2,
		AverageInstancesUsedPerHour: 1.5,
	}
	toolCfg.MinPoolSize = 2.0

	d := common.NewDropSummary()
	result := applyFilters(
		[]common.Recommendation{rec},
		&toolCfg,
		make(map[string][]InstanceEngineVersion),
		make(map[string]MajorEngineVersionInfo),
		"",
		d,
	)

	assert.Empty(t, result, "below-min-pool-size rec should be filtered out")
	assert.Equal(t, 1, d.Total(), "drop summary should record 1 drop")
	assert.Contains(t, d.FormatOneLine(), common.DropMinPoolSize,
		"drop summary should name the --min-pool-size category")
}

// TestApplyFilters_DropExtendedSupport verifies that a recommendation whose
// entire instance count is on an engine version in extended support is
// recorded in a non-nil DropSummary under DropExtendedSupport when
// --include-extended-support is false. If the drops.Add call for that
// path were removed, d.Total() would stay at 0 and the assertion below
// would fail.
func TestApplyFilters_DropExtendedSupport(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.IncludeExtendedSupport = false

	// One RDS MySQL 5.7.42 instance in us-east-1; 5.7 is in extended support.
	rec := common.Recommendation{
		Region:       "us-east-1",
		ResourceType: "db.t3.micro",
		Count:        1,
		Service:      common.ServiceRDS,
		Details:      &common.DatabaseDetails{Engine: "mysql"},
	}

	instanceVersions := map[string][]InstanceEngineVersion{
		"db.t3.micro": {
			{Engine: "mysql", EngineVersion: "5.7.42", InstanceClass: "db.t3.micro", Region: "us-east-1"},
		},
	}

	// Mark mysql 5.7 as in extended support (start date well in the past).
	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-extended-support",
					LifecycleSupportStartDate: time.Now().Add(-365 * 24 * time.Hour),
				},
			},
		},
	}

	d := common.NewDropSummary()
	result := applyFilters(
		[]common.Recommendation{rec},
		&toolCfg,
		instanceVersions,
		versionInfo,
		"",
		d,
	)

	assert.Empty(t, result, "all-extended-support rec should be filtered out")
	assert.Equal(t, 1, d.Total(), "drop summary should record 1 drop")
	assert.Contains(t, d.FormatOneLine(), common.DropExtendedSupport,
		"drop summary should name the --include-extended-support category")
}

// TestApplyFilters_SavingsPlansRegionFilters covers the CLI half of #1582.
// Savings Plans recommendations never populate the top-level rec.Region
// (parser_sp.go stores the CE-supplied region in Details.Region instead), so
// filtering on the bare field dropped every SP recommendation whenever
// --include-regions was set, and leaked region-scoped EC2Instance SPs past
// --exclude-regions. The provider-side filters were fixed first; these cases
// pin the same semantics on the CLI path.
func TestApplyFilters_SavingsPlansRegionFilters(t *testing.T) {
	ec2SP := func(region string) common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderAWS,
			Service:        common.ServiceSavingsPlansEC2Instance,
			CommitmentType: common.CommitmentSavingsPlan,
			Count:          1,
			Details: &common.SavingsPlanDetails{
				PlanType:       "EC2Instance",
				InstanceFamily: "m5",
				Region:         region,
			},
		}
	}
	computeSP := func() common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderAWS,
			Service:        common.ServiceSavingsPlansCompute,
			CommitmentType: common.CommitmentSavingsPlan,
			Count:          1,
			Details:        &common.SavingsPlanDetails{PlanType: "Compute"},
		}
	}

	tests := []struct {
		name           string
		rec            common.Recommendation
		includeRegions []string
		excludeRegions []string
		wantKept       bool
	}{
		{
			name:           "EC2Instance SP in the included region survives",
			rec:            ec2SP("us-east-1"),
			includeRegions: []string{"us-east-1"},
			wantKept:       true,
		},
		{
			name:           "EC2Instance SP outside the included region is dropped",
			rec:            ec2SP("eu-west-1"),
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
		{
			name:           "region-agnostic Compute SP survives an include filter",
			rec:            computeSP(),
			includeRegions: []string{"us-east-1"},
			wantKept:       true,
		},
		{
			name:           "EC2Instance SP in an excluded region is dropped",
			rec:            ec2SP("eu-west-1"),
			excludeRegions: []string{"eu-west-1"},
			wantKept:       false,
		},
		{
			name:           "region-agnostic Compute SP survives an exclude filter",
			rec:            computeSP(),
			excludeRegions: []string{"eu-west-1"},
			wantKept:       true,
		},
		{
			// Cost Explorer omitted SavingsPlansDetails.Region. An EC2Instance
			// SP is region-scoped, so an unknown region must not be treated as
			// region-agnostic and waved past an explicit region filter.
			name:           "EC2Instance SP with an unknown region is not over-included",
			rec:            ec2SP(""),
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
		{
			name:           "reservation rec with an unknown region is still dropped",
			rec:            common.Recommendation{Service: common.ServiceRDS, CommitmentType: common.CommitmentReservedInstance, Count: 1},
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeRegions: tt.includeRegions,
				ExcludeRegions: tt.excludeRegions,
			}
			want := 0
			if tt.wantKept {
				want = 1
			}
			got := applyFilters([]common.Recommendation{tt.rec}, &cfg, nil, nil, "", common.NewDropSummary())
			assert.Len(t, got, want, "region filter kept the wrong number of recommendations")
		})
	}
}

// applyFiltersPreExtraction is the pre-refactor applyFilters implementation,
// kept verbatim (from origin/main, before the recfilter.Filters.ApplyMinPoolSize
// extraction) as a differential oracle. It applies the --min-pool-size check
// inline, in the same loop and same order as processRecommendation, rather
// than as a separate pass over the full slice.
func applyFiltersPreExtraction(recs []common.Recommendation, cfg *Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string, drops *common.DropSummary) []common.Recommendation {
	var filtered []common.Recommendation
	var poolDropCount int
	var poolDropInstances float64

	for i := range recs {
		if cfg.MinPoolSize > 0 && !shouldIncludePoolSize(&recs[i], cfg) {
			poolDropInstances += recs[i].AverageInstancesUsedPerHour
			label := fmt.Sprintf("%s/%s/%s", recs[i].Service, recs[i].Region, recs[i].ResourceType)
			log.Printf("INFO: --min-pool-size=%.1f dropped %s (avg=%.2f < threshold)", cfg.MinPoolSize, label, recs[i].AverageInstancesUsedPerHour)
			poolDropCount++
			drops.Add(common.DropMinPoolSize, 1)
			continue
		}
		adjusted, include, dropReason := processRecommendation(&recs[i], cfg, instanceVersions, versionInfo, currentRegion)
		if include {
			filtered = append(filtered, adjusted)
		} else if dropReason != "" {
			drops.Add(dropReason, 1)
		}
	}

	if poolDropCount > 0 {
		log.Printf("INFO: --min-pool-size dropped %d recommendation(s) (%.2f avg instances/hr total)", poolDropCount, poolDropInstances)
	}

	return filtered
}

// TestApplyFilters_MinPoolSizeMultiRegionMatchesPreExtractionBehaviour is a
// differential regression test against a reviewer's claim that extracting
// the --min-pool-size check into recfilter.Filters.ApplyMinPoolSize changed
// its ordering relative to the currentRegion guard in processRecommendation
// (inflating common.DropMinPoolSize in multi-region runs). It runs the
// current applyFilters and the pre-extraction oracle above side by side,
// once per region, over the same multi-region recommendation set, and
// asserts both the survivors and the drop accounting match exactly.
func TestApplyFilters_MinPoolSizeMultiRegionMatchesPreExtractionBehaviour(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg = Config{MinPoolSize: 2.0}

	regions := []string{"us-east-1", "eu-west-1", "ap-southeast-1"}
	const distinctBelowThreshold = 3 // one below-threshold rec per region below

	// Fresh copies per call: applyFilters mutates nothing in place today, but
	// the oracle and the refactored code must each see their own slice so a
	// hypothetical future in-place adjustment on one side can't leak into the
	// other's input and mask a real divergence.
	makeRecs := func() []common.Recommendation {
		return []common.Recommendation{
			{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "eu-west-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "eu-west-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "ap-southeast-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "ap-southeast-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "us-east-1", ResourceType: "db.t3.large", Count: 3, AverageInstancesUsedPerHour: 0},        // no-signal passthrough
			{
				Service:                     common.ServiceSavingsPlansCompute,
				ResourceType:                "ec2-instance",
				Count:                       10,
				AverageInstancesUsedPerHour: 5.0, // account-level: bypasses the currentRegion guard
			},
		}
	}

	var totalDropMinPoolSizeNew, totalDropMinPoolSizeOld int

	for _, region := range regions {
		t.Run(region, func(t *testing.T) {
			dropsNew := common.NewDropSummary()
			dropsOld := common.NewDropSummary()

			resultNew := applyFilters(makeRecs(), &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), region, dropsNew)
			resultOld := applyFiltersPreExtraction(makeRecs(), &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), region, dropsOld)

			assert.Equal(t, resultOld, resultNew,
				"region %s: refactored applyFilters diverged from the pre-extraction oracle -- the ApplyMinPoolSize extraction changed observable CLI filtering behavior", region)
			assert.Equal(t, dropsOld.FormatOneLine(), dropsNew.FormatOneLine(),
				"region %s: drop summaries diverged between pre- and post-extraction implementations", region)
			assert.Equal(t, dropsOld.Total(), dropsNew.Total(),
				"region %s: total drop counts diverged between pre- and post-extraction implementations", region)

			// The fixture is built so --min-pool-size is the ONLY drop reason
			// either implementation can record, which is what lets the summed
			// Total() below stand in for the min-pool count specifically.
			// Asserting the exact per-pass count keeps that guarantee honest:
			// if some other filter started dropping rows, Total() would exceed
			// the below-threshold count and this would fail.
			require.Contains(t, dropsNew.FormatOneLine(), common.DropMinPoolSize,
				"region %s: fixture must exercise the --min-pool-size drop path", region)
			require.Equal(t, distinctBelowThreshold, dropsNew.Total(),
				"region %s: --min-pool-size must be the only drop reason this fixture records", region)

			totalDropMinPoolSizeNew += dropsNew.Total()
			totalDropMinPoolSizeOld += dropsOld.Total()
		})
	}

	require.Equal(t, totalDropMinPoolSizeOld, totalDropMinPoolSizeNew,
		"old and new implementations must agree on the summed multi-region --min-pool-size drop count")

	// Pre-existing behavior, identical in both implementations (not introduced
	// by the ApplyMinPoolSize extraction): each per-region call re-scans the
	// FULL recommendation set passed to it, not a per-region subset, so a
	// below-threshold recommendation from one region is re-counted as dropped
	// during every other region's pass too. Summed across N region passes this
	// is distinctBelowThreshold * N, not distinctBelowThreshold. This assertion
	// pins today's (inflated) value rather than an aspirational deduplicated
	// one -- see the test's summary report for the actual vs. distinct counts.
	assert.Equal(t, distinctBelowThreshold*len(regions), totalDropMinPoolSizeNew,
		"summed multi-region --min-pool-size drop count should match today's pre-existing inflation factor")
}
