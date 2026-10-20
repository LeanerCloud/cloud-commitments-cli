package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
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

// TestApplyFilters_SavingsPlansRegionsFlagCSVModeUnchanged pins that
// --regions does not narrow Savings Plan rows loaded from --input-csv: CSV
// rows carry no Provider or CommitmentType, so they are used as supplied.
func TestApplyFilters_SavingsPlansRegionsFlagCSVModeUnchanged(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "sp.csv")
	content := `Service,Region,ResourceType,Count,Term,PaymentOption,PlanType,HourlyCommitment,InstanceFamily,DetailsRegion
savings-plans-compute,,,1,1yr,no-upfront,Compute,1.5,,
savings-plans-ec2instance,,,1,1yr,no-upfront,EC2Instance,1.5,m5,eu-west-2
savings-plans-ec2instance,,,1,1yr,no-upfront,EC2Instance,1.5,m5,us-east-1
`
	require.NoError(t, os.WriteFile(csvPath, []byte(content), 0o600))
	recs, err := loadRecommendationsFromCSV(csvPath)
	require.NoError(t, err)
	require.Len(t, recs, 3)

	cfg := Config{CSVInput: csvPath, Regions: []string{"eu-west-2"}}
	got := applyFilters(recs, &cfg, nil, nil, "", common.NewDropSummary())
	assert.Len(t, got, 3)
}

// TestApplyFilters_SavingsPlansIncludeRegionsCSVMode pins that a CSV Savings
// Plan row with an empty Region is matched by --include/--exclude-regions on
// its plan region (Details.Region), as on the main path, while --regions alone
// still leaves it alone (#2228).
func TestApplyFilters_SavingsPlansIncludeRegionsCSVMode(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "sp.csv")
	content := `Service,Region,ResourceType,Count,Term,PaymentOption,PlanType,HourlyCommitment,InstanceFamily,DetailsRegion
savings-plans-ec2instance,,,1,1yr,no-upfront,EC2Instance,1.5,m5,us-east-1
`
	require.NoError(t, os.WriteFile(csvPath, []byte(content), 0o600))
	recs, err := loadRecommendationsFromCSV(csvPath)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Empty(t, recs[0].Region)

	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{"include another region drops it", Config{IncludeRegions: []string{"eu-west-1"}}, 0},
		{"include its region keeps it", Config{IncludeRegions: []string{"us-east-1"}}, 1},
		{"exclude its region drops it", Config{ExcludeRegions: []string{"us-east-1"}}, 0},
		{"--regions alone does not drop it", Config{Regions: []string{"eu-west-1"}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.CSVInput = csvPath
			got := applyFilters(recs, &tt.cfg, nil, nil, "", common.NewDropSummary())
			assert.Len(t, got, tt.want)
		})
	}
}

func TestRequireExactExcludes(t *testing.T) {
	recs := []common.Recommendation{
		{
			Account:     "123456789012",
			AccountName: "dev-1",
		},
		{
			Account:     "111222333444",
			AccountName: "devops",
		},
		{
			Account:     "999888777666",
			AccountName: "prod",
		},
	}
	tests := []struct {
		name    string
		exclude []string
		include []string
		wantErr []string
	}{
		{"partial exclude errors and names accounts", []string{"dev"}, nil, []string{`"dev-1", "devops"`, `"dev"`}},
		{"partial exclude with whitespace and case", []string{" DEV "}, nil, []string{`"dev-1", "devops"`}},
		{"exact name exclude works", []string{"DevOps"}, nil, nil},
		{"exact ID exclude works", []string{"123456789012"}, nil, nil},
		{"exclude matching nothing is a no-op", []string{"zzz"}, nil, nil},
		{"include stays exact and does not error", nil, []string{"dev"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireResolvedAccountNames(recs, &Config{ExcludeAccounts: tt.exclude, IncludeAccounts: tt.include})
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				for _, w := range tt.wantErr {
					assert.Contains(t, err.Error(), w)
				}
			}
		})
	}
}

const (
	testUnidentifiedAcct = "123456789012"
	testOtherAcct        = "999888777666"
)

func TestApplyFilters_UnidentifiedAccountUnderInclude(t *testing.T) {
	unidentified := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1}
	named := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1, Account: testUnidentifiedAcct, AccountName: "prod"}
	tests := []struct {
		name      string
		cfg       Config
		recs      []common.Recommendation
		wantKept  int
		wantDrops int
	}{
		{"include drops it and counts it", Config{IncludeExtendedSupport: true, IncludeAccounts: []string{"prod"}}, []common.Recommendation{unidentified, named}, 1, 1},
		{"include with other region mismatch is not counted", Config{IncludeExtendedSupport: true, IncludeAccounts: []string{"prod"}, IncludeRegions: []string{"eu-west-1"}}, []common.Recommendation{unidentified}, 0, 0},
		{"no account filter keeps it", Config{IncludeExtendedSupport: true}, []common.Recommendation{unidentified}, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drops := common.NewDropSummary()
			got := applyFilters(tt.recs, &tt.cfg, nil, nil, "", drops)
			assert.Len(t, got, tt.wantKept)
			assert.Equal(t, tt.wantDrops, drops.Total())
			if tt.wantDrops > 0 {
				assert.Contains(t, drops.FormatOneLine(), "unidentified-account")
			}
		})
	}
}

func TestApplyFilters_UnidentifiedAccountUnderExclude(t *testing.T) {
	unidentified := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1}
	named := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1, Account: testUnidentifiedAcct, AccountName: "prod"}
	other := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1, Account: testOtherAcct, AccountName: "dev"}
	tests := []struct {
		name       string
		cfg        Config
		recs       []common.Recommendation
		wantKept   int
		wantDrops  int
		wantReason string
	}{
		{"exclude-only drops and counts it, keeps others", Config{IncludeExtendedSupport: true, ExcludeAccounts: []string{testUnidentifiedAcct}}, []common.Recommendation{unidentified, named, other}, 1, 1, "unidentified-account (--exclude-accounts)"},
		{"exclude-only with other region mismatch is not counted", Config{IncludeExtendedSupport: true, ExcludeAccounts: []string{testUnidentifiedAcct}, IncludeRegions: []string{"eu-west-1"}}, []common.Recommendation{unidentified}, 0, 0, ""},
		{"include and exclude count it once under the include reason", Config{IncludeExtendedSupport: true, IncludeAccounts: []string{"dev"}, ExcludeAccounts: []string{testUnidentifiedAcct}}, []common.Recommendation{unidentified, other}, 1, 1, "unidentified-account (--include-accounts)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drops := common.NewDropSummary()
			got := applyFilters(tt.recs, &tt.cfg, nil, nil, "", drops)
			assert.Len(t, got, tt.wantKept)
			assert.Equal(t, tt.wantDrops, drops.Total())
			if tt.wantReason != "" {
				assert.Contains(t, drops.FormatOneLine(), tt.wantReason)
			}
		})
	}
}

func TestCSVExcludeOnly_UnidentifiedRowFilteredOutWithoutError(t *testing.T) {
	unidentified := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1}
	other := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 1, Account: testOtherAcct, AccountName: "dev"}
	cfg := Config{IncludeExtendedSupport: true, ExcludeAccounts: []string{testUnidentifiedAcct}}
	recs := []common.Recommendation{unidentified, other}

	assert.NoError(t, requireResolvedAccountNames(recs, &cfg))
	got := applyFilters(recs, &cfg, nil, nil, "", common.NewDropSummary())
	if assert.Len(t, got, 1) {
		assert.Equal(t, testOtherAcct, got[0].Account)
	}
}

func TestPopulateRecommendationAccountNames_UnidentifiedRecUnderExcludeOnly(t *testing.T) {
	cache := NewAccountAliasCacheWithClient(&MockOrganizationsClient{})
	recs := []common.Recommendation{{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro"}}

	assert.NoError(t, populateRecommendationAccountNames(context.Background(), recs, cache, Config{ExcludeAccounts: []string{testUnidentifiedAcct}}))
}
