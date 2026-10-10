package main

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRecommendationsFromCSV_HeaderValidation_1327(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		row         string
		errContains string
	}{
		{"missing Service", "Region,ResourceType,Count\n", "us-east-1,db.t3.micro,2\n", "Service"},
		{"missing Region", "Service,ResourceType,Count\n", "rds,db.t3.micro,2\n", "Region"},
		{"missing ResourceType", "Service,Region,Count\n", "rds,us-east-1,2\n", "ResourceType"},
		{"missing Count", "Service,Region,ResourceType\n", "rds,us-east-1,db.t3.micro\n", "Count"},
		{"multiple missing", "Service,Count\n", "rds,2\n", "Region, ResourceType"},
		{
			"legacy TEST-02 headers rejected",
			"Service,Region,Instance Type,Instance Count\n",
			"rds,us-east-1,db.t3.micro,2\n",
			"ResourceType, Count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadCSVContent(t, tt.header+tt.row)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "CSV header missing required columns")
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestLoadRecommendationsFromCSV_BOMPrefixed_1327(t *testing.T) {
	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
		"\ufeffService,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro,2\n"))
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, common.ServiceRDS, recs[0].Service)
	assert.Equal(t, "us-east-1", recs[0].Region)
	assert.Equal(t, 2, recs[0].Count)
}

func TestLoadRecommendationsFromCSV_Encoding_1327(t *testing.T) {
	t.Run("UTF-16", func(t *testing.T) {
		units := utf16.Encode([]rune("Service,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro,2\n"))
		for _, tt := range []struct {
			name  string
			bom   []byte
			order binary.ByteOrder
		}{
			{"little endian", []byte{0xff, 0xfe}, binary.LittleEndian},
			{"big endian", []byte{0xfe, 0xff}, binary.BigEndian},
		} {
			t.Run(tt.name, func(t *testing.T) {
				encoded := make([]byte, 2+2*len(units))
				copy(encoded, tt.bom)
				for i, unit := range units {
					tt.order.PutUint16(encoded[2+2*i:], unit)
				}
				err := loadCSVContent(t, string(encoded))
				require.Error(t, err)
				assert.EqualError(t, err, "CSV header: column 1: invalid UTF-8 encoding")
			})
		}
	})

	t.Run("Latin-1 optional header", func(t *testing.T) {
		err := loadCSVContent(t, "Service,Region,ResourceType,Count,Caf\xe9\n"+
			"rds,us-east-1,db.t3.micro,2,prod\n")
		require.Error(t, err)
		assert.EqualError(t, err, "CSV header: column 5: invalid UTF-8 encoding")
	})

	t.Run("Latin-1 AccountName", func(t *testing.T) {
		err := loadCSVContent(t, "Service,Region,ResourceType,Count,AccountName\n"+
			"rds,us-east-1,db.t3.micro,2,Caf\xe9\n")
		require.Error(t, err)
		assert.EqualError(t, err, "CSV line 2: column 5: invalid UTF-8 encoding")
	})

	t.Run("invalid ignored field in TOTAL after multiline record", func(t *testing.T) {
		err := loadCSVContent(t, "Service,Region,ResourceType,Count,Ignored\n"+
			"rds,us-east-1,db.t3.micro,2,\"prod\naccount\"\n"+
			"TOTAL,,,2,\xff\n")
		require.Error(t, err)
		assert.EqualError(t, err, "CSV line 4: column 5: invalid UTF-8 encoding")
	})

	t.Run("valid non-ASCII UTF-8", func(t *testing.T) {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
			"Service,Region,ResourceType,Count,AccountName\n"+
				"rds,us-east-1,db.t3.micro,2,Café 東京\n"))
		require.NoError(t, err)
		require.Len(t, recs, 1)
		assert.Equal(t, "Café 東京", recs[0].AccountName)
	})
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
