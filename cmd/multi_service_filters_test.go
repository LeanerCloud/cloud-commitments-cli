package main

import (
	"context"
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
