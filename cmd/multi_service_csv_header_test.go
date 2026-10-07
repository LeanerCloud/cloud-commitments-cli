package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Header validation (#1327): before this, a header the parser did not
// recognize silently decoded every row to an empty Service/Region/
// ResourceType, and only the Count cell check (added in #1944) caught
// anything at all, with a misleading "missing Count column" message when
// the real problem was a wrong header. The run must fail loudly up front
// and name the missing columns.
func TestLoadRecommendationsFromCSV_HeaderValidation_1327(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		errContains string
	}{
		{"missing Service", "Region,ResourceType,Count\n", "Service"},
		{"missing Region", "Service,ResourceType,Count\n", "Region"},
		{"missing ResourceType", "Service,Region,Count\n", "ResourceType"},
		{"missing Count", "Service,Region,ResourceType\n", "Count"},
		{"multiple missing", "Service,Count\n", "Region, ResourceType"},
		{
			"legacy TEST-02 headers rejected",
			"Service,Region,Instance Type,Instance Count\n",
			"ResourceType, Count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadCSVContent(t, tt.header+"rds,us-east-1,db.t3.micro,2\n")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "CSV header missing required columns")
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// A UTF-8 BOM is stripped before header matching so Excel-exported CSVs
// parse instead of failing the required-column check on the BOM-prefixed
// first header cell.
func TestLoadRecommendationsFromCSV_BOMPrefixed_1327(t *testing.T) {
	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
		"\ufeffService,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro,2\n"))
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "us-east-1", recs[0].Region)
	assert.Equal(t, 2, recs[0].Count)
}

// Lock in encoding/csv behaviors the purchase path relies on.
func TestLoadRecommendationsFromCSV_EdgeCases_1327(t *testing.T) {
	t.Run("CRLF line endings", func(t *testing.T) {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
			"Service,Region,ResourceType,Count\r\nrds,us-east-1,db.t3.micro,2\r\n"))
		require.NoError(t, err)
		require.Len(t, recs, 1)
		assert.Equal(t, 2, recs[0].Count)
	})

	t.Run("quoted commas and newlines inside fields", func(t *testing.T) {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
			"Service,Region,ResourceType,Count,AccountName\n"+
				"rds,us-east-1,\"db.t3.micro, burstable\",2,\"prod\naccount\"\n"))
		require.NoError(t, err)
		require.Len(t, recs, 1)
		assert.Equal(t, "db.t3.micro, burstable", recs[0].ResourceType)
		assert.Equal(t, "prod\naccount", recs[0].AccountName)
	})

	t.Run("headers only loads zero recs", func(t *testing.T) {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
			"Service,Region,ResourceType,Count\n"))
		require.NoError(t, err)
		assert.Empty(t, recs)
	})

	t.Run("wrong field count on a data row", func(t *testing.T) {
		err := loadCSVContent(t,
			"Service,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read CSV record")
	})

	t.Run("TOTAL row still skipped with full header", func(t *testing.T) {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
			"Service,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro,2\nTOTAL,,,2\n"))
		require.NoError(t, err)
		require.Len(t, recs, 1)
	})
}
