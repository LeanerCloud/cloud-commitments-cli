package main

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetermineCSVCoverage(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		expected float64
	}{
		{
			name: "Default coverage (80) changed to 100 for CSV",
			cfg: Config{
				Coverage: 80.0,
			},
			expected: 100.0,
		},
		{
			name: "User-specified coverage preserved",
			cfg: Config{
				Coverage: 75.0,
			},
			expected: 75.0,
		},
		{
			name: "User-specified 100% coverage preserved",
			cfg: Config{
				Coverage: 100.0,
			},
			expected: 100.0,
		},
		{
			name: "User-specified 50% coverage preserved",
			cfg: Config{
				Coverage: 50.0,
			},
			expected: 50.0,
		},
		{
			name: "User-specified 0% coverage preserved",
			cfg: Config{
				Coverage: 0.0,
			},
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := determineCSVCoverage(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWriteMultiServiceCSVReport(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		results  []common.PurchaseResult
		wantErr  bool
	}{
		{
			name: "RDS results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:           common.ServiceRDS,
						Region:            "us-east-1",
						ResourceType:      "db.t3.micro",
						Count:             2,
						Term:              "3yr",
						PaymentOption:     "partial-upfront",
						EstimatedSavings:  100,
						SavingsPercentage: 30,
						Timestamp:         time.Now(),
						Details: common.DatabaseDetails{
							Engine:   "mysql",
							AZConfig: "multi-az",
						},
					},
					Success:      true,
					CommitmentID: "test-001",
					Timestamp:    time.Now(),
				},
			},
			filename: "test-rds.csv",
			wantErr:  false,
		},
		{
			name: "ElastiCache results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceElastiCache,
						Region:       "us-west-2",
						ResourceType: "cache.t3.micro",
						Count:        1,
						Term:         "1yr",
						Details: common.CacheDetails{
							Engine:   "redis",
							NodeType: "cache.t3.micro",
						},
					},
					Success:      true,
					CommitmentID: "test-002",
					Timestamp:    time.Now(),
				},
			},
			filename: "test-cache.csv",
			wantErr:  false,
		},
		{
			name: "EC2 results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceEC2,
						Region:       "eu-west-1",
						ResourceType: "t3.medium",
						Count:        5,
						Term:         "3yr",
						Details: common.ComputeDetails{
							Platform: "Linux/UNIX",
							Tenancy:  "shared",
							Scope:    "region",
						},
					},
					Success:      false,
					CommitmentID: "test-003",
					Error:        errors.New("Insufficient capacity"),
					Timestamp:    time.Now(),
				},
			},
			filename: "test-ec2.csv",
			wantErr:  false,
		},
		{
			name:     "Empty results",
			results:  []common.PurchaseResult{},
			filename: "test-empty.csv",
			wantErr:  false,
		},
		{
			name: "Unknown service type",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceType("unknown"),
						Region:       "us-east-1",
						ResourceType: "unknown.large",
						Count:        1,
						Term:         "3yr",
					},
					Success: true,
				},
			},
			filename: "test-unknown.csv",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filepath := tmpDir + "/" + tt.filename

			err := writeMultiServiceCSVReport(tt.results, filepath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWriteMultiServiceCSVReport_CoverageColumn confirms the ProjectedCoverage
// and RecommendedCount columns added for --target-coverage (#338) are emitted
// with the "blank-when-zero" formatting (matches the "0 = unknown" convention
// shared with the JSON-level omitempty tags). The sibling ProjectedUtilization
// and RecommendedUtilization fields are intentionally NOT emitted to CSV
// (both land at ~100% on every under-buy row, so they add noise without
// information).
func TestWriteMultiServiceCSVReport_CoverageColumn(t *testing.T) {
	tmpDir := t.TempDir()
	filepath := tmpDir + "/util.csv"

	results := []common.PurchaseResult{
		{
			Recommendation: common.Recommendation{
				Service:                     common.ServiceEC2,
				Region:                      "us-east-1",
				ResourceType:                "t3.medium",
				Count:                       7,   // post-sizing
				RecommendedCount:            10,  // AWS pre-sizing
				CommitmentCost:              700, // already scaled at sizing time
				Term:                        "1yr",
				ProjectedUtilization:        95.0,
				ProjectedCoverage:           87.5,
				ExistingCoveragePct:         20.0,
				ExistingCoverageKnown:       true,
				RecommendedUtilization:      80.0,
				AverageInstancesUsedPerHour: 10.0,
				// Pointer form matches the live parser (parser_services.go
				// stores &common.ComputeDetails{...}); extractEngine must
				// handle both pointer and value Details.
				Details: &common.ComputeDetails{Platform: "Linux/UNIX"},
			},
			Success: true,
		},
		{
			// All sizing-related fields zero — ProjectedCoverage and
			// RecommendedCount cells should both be blank (SP rec or a
			// pre-target rec that never went through sizing). UpfrontPayment
			// is also blank when CommitmentCost is zero. No Details either,
			// so the Engine column is blank.
			Recommendation: common.Recommendation{
				Service:      common.ServiceEC2,
				Region:       "us-east-1",
				ResourceType: "m5.large",
				Count:        5,
				Term:         "1yr",
			},
			Success: true,
		},
	}

	err := writeMultiServiceCSVReport(results, filepath)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath)
	require.NoError(t, err)
	csvText := string(content)

	// Header contains ProjectedCoverage, ExistingCoverage, RecommendedCount,
	// and UpfrontPayment but NOT the always-100% utilization siblings.
	assert.Contains(t, csvText, "ProjectedCoverage")
	assert.Contains(t, csvText, "ExistingCoverage")
	assert.Contains(t, csvText, "RecommendedCount")
	assert.Contains(t, csvText, "UpfrontPayment")
	assert.NotContains(t, csvText, "ProjectedUtilization", "column was removed; it's ~100% on every under-buy row")
	assert.NotContains(t, csvText, "RecommendedUtilization", "column was removed; it's ~99-100% on every row")

	// First data row (populated rec) has the coverage and AWS-count values.
	assert.Contains(t, csvText, "87.5", "ProjectedCoverage should render with one decimal")

	r := csv.NewReader(strings.NewReader(csvText))
	rows, err := r.ReadAll()
	require.NoError(t, err)
	// Header + 2 data rows + 1 TOTAL row.
	require.Len(t, rows, 4)
	// TOTAL row sits at the bottom with "TOTAL" in the Service column and
	// summed Count / UpfrontPayment / EstimatedSavings.
	totalRow := rows[3]
	assert.Equal(t, "TOTAL", totalRow[0], "TOTAL label lands in Service column")
	// Data rows are sorted by UpfrontPayment DESC; populated rec ($700)
	// comes before the empty rec ($0).
	header := rows[0]
	idxProjCov, idxRecCount, idxUpfront, idxExisting := -1, -1, -1, -1
	idxEngine, idxInstances, idxCovered := -1, -1, -1
	for i, h := range header {
		switch h {
		case "ProjectedCoverage":
			idxProjCov = i
		case "RecommendedCount":
			idxRecCount = i
		case "UpfrontPayment":
			idxUpfront = i
		case "ExistingCoverage":
			idxExisting = i
		case "Engine":
			idxEngine = i
		case "Instances":
			idxInstances = i
		case "CoveredInstances":
			idxCovered = i
		}
	}
	require.NotEqual(t, -1, idxProjCov, "ProjectedCoverage column not found")
	require.NotEqual(t, -1, idxRecCount, "RecommendedCount column not found")
	require.NotEqual(t, -1, idxUpfront, "UpfrontPayment column not found")
	require.NotEqual(t, -1, idxExisting, "ExistingCoverage column not found")
	require.NotEqual(t, -1, idxEngine, "Engine column not found")
	require.NotEqual(t, -1, idxInstances, "Instances column not found")
	require.NotEqual(t, -1, idxCovered, "CoveredInstances column not found")

	// Populated row: RecommendedCount=10 renders as "10", UpfrontPayment
	// emits CommitmentCost as-is (sizing already scaled it; see
	// ApplyTargetCoverage), ProjectedCoverage=87.5 renders, ExistingCoverage=20.0,
	// Engine pulled from *ComputeDetails.Platform. Instances = avg = 10.0.
	// CoveredInstances = 10.0 × 20% = 2.0.
	populatedRow := rows[1]
	assert.Equal(t, "10", populatedRow[idxRecCount], "RecommendedCount should render as decimal")
	assert.Equal(t, "700.00", populatedRow[idxUpfront], "UpfrontPayment should render rec.CommitmentCost as-is")
	assert.Equal(t, "87.5", populatedRow[idxProjCov])
	assert.Equal(t, "20.0", populatedRow[idxExisting], "ExistingCoverage should render with one decimal")
	assert.Equal(t, "Linux/UNIX", populatedRow[idxEngine], "Engine should pull from *ComputeDetails.Platform")
	assert.Equal(t, "10.0", populatedRow[idxInstances], "Instances should render avg with one decimal")
	assert.Equal(t, "2.0", populatedRow[idxCovered], "CoveredInstances = avg * existing_cov / 100")

	// Zero-fields row: optional cells blank, Engine blank when Details is nil.
	// ExistingCoverage shows "n/a" because ExistingCoverageKnown wasn't set:
	// CE had no data for this pool (distinct from "0% covered", which would
	// be ExistingCoverageKnown=true, Pct=0 rendering as "0.0").
	zeroRow := rows[2]
	assert.Equal(t, "", zeroRow[idxProjCov], "zero ProjectedCoverage should be blank")
	assert.Equal(t, "", zeroRow[idxRecCount], "zero RecommendedCount should be blank (SP rec or pre-sizing)")
	assert.Equal(t, "", zeroRow[idxUpfront], "zero CommitmentCost should leave UpfrontPayment blank")
	assert.Equal(t, "n/a", zeroRow[idxExisting], "ExistingCoverage should render n/a when CE had no signal")
	assert.Equal(t, "", zeroRow[idxEngine], "missing Details should leave Engine blank")
	assert.Equal(t, "", zeroRow[idxInstances], "zero avg should leave Instances blank")
	assert.Equal(t, "", zeroRow[idxCovered], "missing avg or existing_cov should leave CoveredInstances blank")
}

// TestWriteMultiServiceCSVReport_SortAndTotal confirms data rows are
// sorted by UpfrontPayment DESC and that a TOTAL summary row lands at
// the bottom with the column sums. Operators reading the file top-down
// want the biggest-dollar decisions surfaced first; the TOTAL row
// removes the need to copy-paste columns into a spreadsheet to add
// them up.
func TestWriteMultiServiceCSVReport_SortAndTotal(t *testing.T) {
	tmpDir := t.TempDir()
	fp := tmpDir + "/sort-total.csv"

	// Three recs: $5K, $20K, $1K upfront. After DESC sort the order
	// should be 20K, 5K, 1K.
	results := []common.PurchaseResult{
		{Recommendation: common.Recommendation{Service: "rds", ResourceType: "db.r6g.large", Count: 5, CommitmentCost: 5000, EstimatedSavings: 500}},
		{Recommendation: common.Recommendation{Service: "rds", ResourceType: "db.r6g.2xlarge", Count: 2, CommitmentCost: 20000, EstimatedSavings: 1500}},
		{Recommendation: common.Recommendation{Service: "rds", ResourceType: "db.t4g.medium", Count: 4, CommitmentCost: 1000, EstimatedSavings: 80}},
	}
	require.NoError(t, writeMultiServiceCSVReport(results, fp))
	content, err := os.ReadFile(fp)
	require.NoError(t, err)

	r := csv.NewReader(strings.NewReader(string(content)))
	rows, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 5) // header + 3 data + TOTAL

	// Find UpfrontPayment column index.
	header := rows[0]
	idxUpfront := -1
	idxCount := -1
	idxService := -1
	idxNU := -1
	idxSavings := -1
	for i, h := range header {
		switch h {
		case "UpfrontPayment":
			idxUpfront = i
		case "Count":
			idxCount = i
		case "Service":
			idxService = i
		case "NormalizedUnits":
			idxNU = i
		case "EstimatedSavings":
			idxSavings = i
		}
	}

	// Sort order: $20K, $5K, $1K.
	assert.Equal(t, "20000.00", rows[1][idxUpfront], "row 1 has the largest upfront")
	assert.Equal(t, "5000.00", rows[2][idxUpfront])
	assert.Equal(t, "1000.00", rows[3][idxUpfront])

	// TOTAL row aggregates: count=11, upfront=$26K, savings=$2,080.
	// NU = 5×4 + 2×16 + 4×2 = 20 + 32 + 8 = 60.
	totalRow := rows[4]
	assert.Equal(t, "TOTAL", totalRow[idxService])
	assert.Equal(t, "11", totalRow[idxCount])
	assert.Equal(t, "60", totalRow[idxNU])
	assert.Equal(t, "26000.00", totalRow[idxUpfront])
	assert.Equal(t, "2080.00", totalRow[idxSavings])
}

// TestFormatExistingCoverage locks the three-state rendering:
//   - ExistingCoverageKnown=false → "n/a" (CE has no data for this pool)
//   - ExistingCoverageKnown=true, Pct=0 → "0.0" (CE confirms zero coverage)
//   - ExistingCoverageKnown=true, Pct>0 → formatted with one decimal
//
// Critical for operators interpreting the column: a blank or zero cell
// previously meant either "CE was queried but returned 0%" or "CE
// returned nothing", with no way to tell which.
func TestFormatExistingCoverage(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		rec  common.Recommendation
		want string
	}{
		{"unknown (CE no data)", common.Recommendation{}, "n/a"},
		{"known zero coverage", common.Recommendation{ExistingCoverageKnown: true}, "0.0"},
		{"known partial coverage", common.Recommendation{ExistingCoverageKnown: true, ExistingCoveragePct: 37.74}, "37.7"},
		{"known full coverage", common.Recommendation{ExistingCoverageKnown: true, ExistingCoveragePct: 100.0}, "100.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatExistingCoverage(tt.rec))
		})
	}
}

// TestFormatRecurringMonthlyOrBlank locks the nil-vs-zero distinction:
// nil pointer (AWS API didn't return RecurringStandardMonthlyCost)
// renders as blank, zero value (genuinely no monthly fee, e.g.
// all-upfront RIs) renders as "0.00". Operators need to tell "we don't
// know" apart from "definitely zero" to compute total cost correctly.
func TestFormatRecurringMonthlyOrBlank(t *testing.T) {
	zero := 0.0
	twenty := 20.5
	tests := []struct {
		name string
		in   *float64
		want string
	}{
		{"nil → blank (unknown)", nil, ""},
		{"zero pointer → 0.00 (definitely zero)", &zero, "0.00"},
		{"non-zero → formatted", &twenty, "20.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRecurringMonthlyOrBlank(tt.in))
		})
	}
}

// TestExtractRDSFamily covers the family-prefix extraction used by the
// CSV writer to group rows by size-flex family.
func TestExtractRDSFamily(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		rec  common.Recommendation
		want string
	}{
		{"RDS db.r7g.large", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large"}, "db.r7g"},
		{"RDS db.t4g.medium", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.medium"}, "db.t4g"},
		{"RelationalDB alias", common.Recommendation{Service: common.ServiceRelationalDB, ResourceType: "db.m5.xlarge"}, "db.m5"},
		// Non-RDS services blank even when ResourceType looks RDS-shaped.
		{"EC2 ignored", common.Recommendation{Service: common.ServiceEC2, ResourceType: "m5.large"}, ""},
		{"ElastiCache ignored", common.Recommendation{Service: common.ServiceElastiCache, ResourceType: "cache.t3.micro"}, ""},
		// Malformed RDS type.
		{"RDS bare type", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractRDSFamily(tt.rec))
		})
	}
}

// TestFormatNormalizedUnitsOrBlank confirms NU values land for RDS rows
// with known sizes and stay blank for non-RDS / zero-count / unknown-size
// inputs, matching the "0/empty = unknown" convention used elsewhere.
func TestFormatNormalizedUnitsOrBlank(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		rec  common.Recommendation
		want string
	}{
		// 15 × db.r7g.large = 15 × 4 NU = 60 NU
		{"r7g.large × 15", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large", Count: 15}, "60"},
		// 3 × db.t4g.medium = 3 × 2 NU = 6 NU
		{"t4g.medium × 3", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.medium", Count: 3}, "6"},
		// Fractional NU survives via %g (db.t4g.micro = 0.5 NU)
		{"t4g.micro × 3", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.micro", Count: 3}, "1.5"},
		// Non-RDS service → blank
		{"EC2 row blank", common.Recommendation{Service: common.ServiceEC2, ResourceType: "m5.large", Count: 5}, ""},
		// Zero count → blank
		{"zero count blank", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large", Count: 0}, ""},
		// Unknown size → blank
		{"unknown size blank", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.bogus", Count: 5}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatNormalizedUnitsOrBlank(tt.rec))
		})
	}
}

// TestExtractDeployment covers the deployment-extraction helper used by
// the RDS row in the CSV. Single-AZ / Multi-AZ is critical context for
// pricing verification (Multi-AZ list price is ~2x Single-AZ) so the
// column should land for every RDS rec regardless of which Details form
// the upstream path used.
func TestExtractDeployment(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		rec  common.Recommendation
		want string
	}{
		{"*DatabaseDetails Single-AZ", common.Recommendation{Details: &common.DatabaseDetails{AZConfig: "single-az"}}, "single-az"},
		{"*DatabaseDetails Multi-AZ", common.Recommendation{Details: &common.DatabaseDetails{AZConfig: "multi-az"}}, "multi-az"},
		{"DatabaseDetails (value) Multi-AZ", common.Recommendation{Details: common.DatabaseDetails{AZConfig: "multi-az"}}, "multi-az"},
		{"DatabaseDetails empty AZConfig", common.Recommendation{Details: &common.DatabaseDetails{Engine: "mysql"}}, ""},
		// Non-RDS Details → blank (column is RDS-only data).
		{"CacheDetails -> empty", common.Recommendation{Details: &common.CacheDetails{Engine: "redis"}}, ""},
		{"ComputeDetails -> empty", common.Recommendation{Details: &common.ComputeDetails{Platform: "Linux/UNIX"}}, ""},
		{"nil Details -> empty", common.Recommendation{}, ""},
		{"nil *DatabaseDetails -> empty", common.Recommendation{Details: (*common.DatabaseDetails)(nil)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractDeployment(tt.rec))
		})
	}
}

// TestExtractEngine covers the four cases the helper dispatches on:
// DatabaseDetails (RDS engine), CacheDetails (ElastiCache engine),
// ComputeDetails (EC2 platform), and unset/other Details (blank).
func TestExtractEngine(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		rec  common.Recommendation
		want string
	}{
		// Pointer forms — what the live parser actually emits.
		{"*DatabaseDetails -> Engine", common.Recommendation{Details: &common.DatabaseDetails{Engine: "aurora-postgresql"}}, "aurora-postgresql"},
		{"*CacheDetails -> Engine", common.Recommendation{Details: &common.CacheDetails{Engine: "redis"}}, "redis"},
		{"*ComputeDetails -> Platform", common.Recommendation{Details: &common.ComputeDetails{Platform: "Linux/UNIX"}}, "Linux/UNIX"},
		// Value forms — what the CSV-loader path constructs.
		{"DatabaseDetails (value) -> Engine", common.Recommendation{Details: common.DatabaseDetails{Engine: "mysql"}}, "mysql"},
		{"CacheDetails (value) -> Engine", common.Recommendation{Details: common.CacheDetails{Engine: "memcached"}}, "memcached"},
		{"ComputeDetails (value) -> Platform", common.Recommendation{Details: common.ComputeDetails{Platform: "Windows"}}, "Windows"},
		// Fallbacks.
		{"nil Details -> empty", common.Recommendation{}, ""},
		{"SavingsPlanDetails -> empty", common.Recommendation{Details: &common.SavingsPlanDetails{HourlyCommitment: 1.0}}, ""},
		{"nil *DatabaseDetails -> empty", common.Recommendation{Details: (*common.DatabaseDetails)(nil)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractEngine(tt.rec))
		})
	}
}

// TestFormatCurrencyOrBlank locks the blank-when-zero behavior for the
// UpfrontPayment column. Non-zero renders with two decimals; zero renders
// as an empty cell so users can distinguish "no upfront due" from "actual
// $0 upfront", consistent with the rest of the optional CSV columns.
func TestFormatCurrencyOrBlank(t *testing.T) {
	tests := []struct { //nolint:govet // fieldalignment: reorder would break API/readability
		name string
		in   float64
		want string
	}{
		{"non-zero renders with two decimals", 1234.56, "1234.56"},
		{"integer value gets .00", 700, "700.00"},
		{"zero blanks the cell", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatCurrencyOrBlank(tt.in))
		})
	}
}

// Tests for loadRecommendationsFromCSV function.
func TestLoadRecommendationsFromCSV(t *testing.T) {
	tests := []struct {
		validate    func(t *testing.T, recs []common.Recommendation)
		name        string
		csvContent  string
		errContains string
		wantErr     bool
	}{
		{
			name: "Valid CSV with all fields",
			csvContent: `Service,Region,ResourceType,Count,Account,AccountName,Term,PaymentOption,EstimatedSavings
rds,us-east-1,db.t3.micro,5,123456789012,Production,3yr,partial-upfront,1500.50
ec2,us-west-2,t3.medium,10,123456789012,Development,1yr,all-upfront,2000.75`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 2)

				// Validate first recommendation
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "us-east-1", recs[0].Region)
				assert.Equal(t, "db.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 5, recs[0].Count)
				assert.Equal(t, "123456789012", recs[0].Account)
				assert.Equal(t, "Production", recs[0].AccountName)
				assert.Equal(t, "3yr", recs[0].Term)
				assert.Equal(t, "partial-upfront", recs[0].PaymentOption)
				assert.InDelta(t, 1500.50, recs[0].EstimatedSavings, 0.01)

				// Validate second recommendation
				assert.Equal(t, common.ServiceEC2, recs[1].Service)
				assert.Equal(t, "us-west-2", recs[1].Region)
				assert.Equal(t, "t3.medium", recs[1].ResourceType)
				assert.Equal(t, 10, recs[1].Count)
			},
		},
		{
			name: "Valid CSV with minimal fields",
			csvContent: `Service,Region,ResourceType,Count
elasticache,eu-west-1,cache.t3.micro,3`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceElastiCache, recs[0].Service)
				assert.Equal(t, "eu-west-1", recs[0].Region)
				assert.Equal(t, "cache.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 3, recs[0].Count)
			},
		},
		{
			name: "Valid CSV with empty optional Account field",
			csvContent: `Service,Region,ResourceType,Count,Account
rds,us-east-1,db.t3.micro,2,`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "us-east-1", recs[0].Region)
				assert.Equal(t, "db.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 2, recs[0].Count)
				assert.Equal(t, "", recs[0].Account)
			},
		},
		{
			name: "Engine and Deployment reconstruct service Details",
			csvContent: `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption
rds,us-east-1,db.t4g.medium,Aurora MySQL,single-az,3,3yr,partial-upfront
rds,eu-west-2,db.r7g.large,MySQL,multi-az,2,3yr,partial-upfront
elasticache,us-east-1,cache.r6g.large,redis,,4,1yr,no-upfront
ec2,us-west-2,m5.large,Linux/UNIX,,5,1yr,all-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 4)

				// RDS Aurora MySQL, single-az -> DatabaseDetails
				rds0, ok := recs[0].Details.(*common.DatabaseDetails)
				require.True(t, ok, "RDS rec should carry *DatabaseDetails")
				assert.Equal(t, "Aurora MySQL", rds0.Engine)
				assert.Equal(t, "single-az", rds0.AZConfig)
				assert.Equal(t, "db.t4g.medium", rds0.InstanceClass)

				// RDS MySQL, multi-az -> AZConfig carried through for offering lookup
				rds1, ok := recs[1].Details.(*common.DatabaseDetails)
				require.True(t, ok)
				assert.Equal(t, "MySQL", rds1.Engine)
				assert.Equal(t, "multi-az", rds1.AZConfig)

				// ElastiCache -> CacheDetails (no Deployment column)
				cache, ok := recs[2].Details.(*common.CacheDetails)
				require.True(t, ok, "ElastiCache rec should carry *CacheDetails")
				assert.Equal(t, "redis", cache.Engine)
				assert.Equal(t, "cache.r6g.large", cache.NodeType)

				// EC2 -> ComputeDetails, platform from the Engine column
				ec2, ok := recs[3].Details.(*common.ComputeDetails)
				require.True(t, ok, "EC2 rec should carry *ComputeDetails")
				assert.Equal(t, "Linux/UNIX", ec2.Platform)
				assert.Equal(t, "m5.large", ec2.InstanceType)
			},
		},
		{
			name: "No Engine column leaves Details nil (Savings Plans / minimal CSV)",
			csvContent: `Service,Region,ResourceType,Count,Term,PaymentOption
rds,us-east-1,db.t3.micro,2,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Nil(t, recs[0].Details, "Details must stay nil when Engine column is absent")
			},
		},
		{
			name: "TOTAL summary row is skipped",
			csvContent: `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption
rds,us-east-1,db.t4g.medium,Aurora MySQL,single-az,3,3yr,partial-upfront
TOTAL,,,,,3,,`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1, "the trailing TOTAL row must not become a recommendation")
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
			},
		},
		{
			name: "CSV with different column order",
			csvContent: `Count,Service,Region,ResourceType
7,rds,ap-south-1,db.r5.large`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "ap-south-1", recs[0].Region)
				assert.Equal(t, "db.r5.large", recs[0].ResourceType)
				assert.Equal(t, 7, recs[0].Count)
			},
		},
		{
			name: "Empty CSV file (only header)",
			csvContent: `Service,Region,ResourceType,Count,Account,AccountName,Term,PaymentOption,EstimatedSavings
`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				assert.Len(t, recs, 0)
			},
		},
		{
			name: "Invalid Count value - non-numeric",
			csvContent: `Service,Region,ResourceType,Count
rds,us-east-1,db.t3.micro,abc`,
			wantErr:     true,
			errContains: "invalid Count value",
		},
		{
			name: "Invalid EstimatedSavings value - non-numeric",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings
rds,us-east-1,db.t3.micro,5,invalid`,
			wantErr:     true,
			errContains: "invalid EstimatedSavings value",
		},
		{
			name: "Multiple rows with various services",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings
rds,us-east-1,db.t3.micro,5,100.00
ec2,us-west-2,t3.medium,10,200.50
elasticache,eu-west-1,cache.t3.micro,3,50.25
opensearch,ap-southeast-1,t3.small.search,2,75.00`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 4)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, common.ServiceEC2, recs[1].Service)
				assert.Equal(t, common.ServiceElastiCache, recs[2].Service)
				assert.Equal(t, common.ServiceOpenSearch, recs[3].Service)
			},
		},
		{
			name: "CSV with large Count values",
			csvContent: `Service,Region,ResourceType,Count
ec2,us-east-1,t3.large,1000`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, 1000, recs[0].Count)
			},
		},
		{
			name: "CSV with decimal savings",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings
rds,us-east-1,db.t3.micro,5,1234.5678`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.InDelta(t, 1234.5678, recs[0].EstimatedSavings, 0.0001)
			},
		},
		{
			name: "CSV with zero values",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings
rds,us-east-1,db.t3.micro,0,0`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, 0, recs[0].Count)
				assert.Equal(t, float64(0), recs[0].EstimatedSavings)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary CSV file
			tmpDir := t.TempDir()
			csvPath := filepath.Join(tmpDir, "test.csv")
			err := os.WriteFile(csvPath, []byte(tt.csvContent), 0644)
			require.NoError(t, err)

			// Call function
			recs, err := loadRecommendationsFromCSV(csvPath)

			// Validate results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, recs)
				}
			}
		})
	}
}

// Test loadRecommendationsFromCSV with file errors.
func TestLoadRecommendationsFromCSV_FileErrors(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		errContains string
	}{
		{
			name: "Non-existent file",
			setup: func(t *testing.T) string {
				return "/nonexistent/path/to/file.csv"
			},
			errContains: "failed to open CSV file",
		},
		{
			name: "Directory instead of file",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			errContains: "",
		},
		{
			name: "Empty file (no header)",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				csvPath := filepath.Join(tmpDir, "empty.csv")
				err := os.WriteFile(csvPath, []byte(""), 0644)
				require.NoError(t, err)
				return csvPath
			},
			errContains: "failed to read CSV header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			_, err := loadRecommendationsFromCSV(path)
			assert.Error(t, err)
			if tt.errContains != "" {
				assert.Contains(t, err.Error(), tt.errContains)
			}
		})
	}
}
