package main

import (
	"bytes"
	"encoding/csv"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestBuildTotalRow_NUSkipsRowsWithBlankNUCell is the regression test for
// issue #2005: the TOTAL NormalizedUnits cell must equal the sum of the
// per-row NU cells above it. ElastiCache and OpenSearch types such as
// "cache.r7g.large" have three dot-separated parts, so the NU lookup
// resolves their size suffix even though their own NU cell renders blank;
// before the fix those rows inflated the TOTAL.
func TestBuildTotalRow_NUSkipsRowsWithBlankNUCell(t *testing.T) {
	results := []common.PurchaseResult{
		// 5 × db.r6g.large = 20 NU (rendered on the row).
		{Recommendation: common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r6g.large", Count: 5}},
		// 2 × db.r6g.xlarge = 16 NU via the relational-db alias (rendered).
		{Recommendation: common.Recommendation{Service: common.ServiceRelationalDB, ResourceType: "db.r6g.xlarge", Count: 2}},
		// Non-RDS rows whose NU cell is blank: must add nothing.
		{Recommendation: common.Recommendation{Service: common.ServiceElastiCache, ResourceType: "cache.r7g.large", Count: 3}},
		{Recommendation: common.Recommendation{Service: common.ServiceOpenSearch, ResourceType: "r6g.large.search", Count: 4}},
		{Recommendation: common.Recommendation{Service: common.ServiceEC2, ResourceType: "m5.large", Count: 7}},
	}

	var buf bytes.Buffer
	require.NoError(t, writeMultiServiceCSV(&buf, results))
	rows, err := csv.NewReader(&buf).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, len(results)+2) // header + data rows + TOTAL

	idxNU := slices.Index(rows[0], "NormalizedUnits")
	require.GreaterOrEqual(t, idxNU, 0)

	var columnSum float64
	for _, row := range rows[1 : len(rows)-1] {
		if row[idxNU] == "" {
			continue
		}
		v, err := strconv.ParseFloat(row[idxNU], 64)
		require.NoError(t, err)
		columnSum += v
	}
	require.InDelta(t, 36, columnSum, 1e-9, "only the two RDS rows render NU")

	totalRow := rows[len(rows)-1]
	require.Equal(t, "TOTAL", totalRow[0])
	assert.Equal(t, "36", totalRow[idxNU], "TOTAL NU must equal the sum of the visible NU cells")
}

// TestBuildTotalRow_NUBlankWhenNoRowHasNU locks that a run with no RDS rows
// leaves the TOTAL NU cell blank instead of summing NU for blank-cell rows.
func TestBuildTotalRow_NUBlankWhenNoRowHasNU(t *testing.T) {
	results := []common.PurchaseResult{
		{Recommendation: common.Recommendation{Service: common.ServiceElastiCache, ResourceType: "cache.r7g.large", Count: 3}},
	}
	var buf bytes.Buffer
	require.NoError(t, writeMultiServiceCSV(&buf, results))
	rows, err := csv.NewReader(&buf).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 3) // header + 1 data row + TOTAL

	totalRow := rows[2]
	require.Equal(t, "TOTAL", totalRow[0])
	assert.Equal(t, "3", totalRow[slices.Index(rows[0], "Count")], "Count still sums")
	assert.Empty(t, totalRow[slices.Index(rows[0], "NormalizedUnits")], "NormalizedUnits stays blank when no row renders NU")
}
