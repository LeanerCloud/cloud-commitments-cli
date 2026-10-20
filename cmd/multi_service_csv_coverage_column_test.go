package main

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteMultiServiceCSVReport_CoverageColumn confirms the ProjectedCoverage
// and RecommendedCount columns added for --target-coverage (#338) are emitted
// with the "blank-when-zero" formatting (matches the "0 = unknown" convention
// shared with the JSON-level omitempty tags). The sibling ProjectedUtilization
// and RecommendedUtilization fields are intentionally NOT emitted to CSV
// (both land at ~100% on every under-buy row, so they add noise without
// information).
func TestWriteMultiServiceCSVReport_CoverageColumn(t *testing.T) {
	t.Parallel()
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
