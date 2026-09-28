package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadCSVContent(t *testing.T, content string) error {
	t.Helper()
	_, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, content))
	return err
}

// Count cells must parse whole or not at all (#1944): fmt.Sscanf truncated
// "3.7" to 3 and accepted trailing garbage, both feeding the purchase loop.
func TestLoadRecommendationsFromCSV_StrictCount(t *testing.T) {
	tests := []struct {
		name string
		cell string
	}{
		{"fractional", "3.7"},
		{"trailing garbage", "3abc"},
		{"trailing unit", "12 units"},
		{"negative", "-1"},
		{"blank", ""},
		{"whitespace only", "  "},
		{"overflows int64", "99999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count\n" +
				"rds,us-east-1,db.t3.micro,1\n" +
				"rds,us-east-1,db.t3.small,\"" + tt.cell + "\"\n"
			err := loadCSVContent(t, content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 3")
			assert.Contains(t, err.Error(), `column 4 "Count"`)
		})
	}
}

func TestLoadRecommendationsFromCSV_CountTrimmed(t *testing.T) {
	csvPath := writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count\nrds,us-east-1,db.t3.micro,\" 3 \"\n")
	recs, err := loadRecommendationsFromCSV(csvPath)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, 3, recs[0].Count)
}

func TestLoadRecommendationsFromCSV_CountColumnRequired(t *testing.T) {
	err := loadCSVContent(t, "Service,Region,ResourceType\nrds,us-east-1,db.t3.micro\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Count")
}

func TestLoadRecommendationsFromCSV_StrictEstimatedSavings(t *testing.T) {
	tests := []struct {
		name string
		cell string
	}{
		{"trailing currency", "1000 USD"},
		{"trailing garbage", "12.5abc"},
		{"NaN", "NaN"},
		{"infinity", "Inf"},
		{"overflows float64", "1e400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count,EstimatedSavings\n" +
				"rds,us-east-1,db.t3.micro,2,\"" + tt.cell + "\"\n"
			err := loadCSVContent(t, content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 2")
			assert.Contains(t, err.Error(), `column 5 "EstimatedSavings"`)
		})
	}
}

// A blank EstimatedSavings cell stays absent-as-zero: requireRankingSignal
// relies on it to refuse a binding --max-instances cap.
func TestLoadRecommendationsFromCSV_BlankSavingsStillZero(t *testing.T) {
	csvPath := writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,EstimatedSavings\nrds,us-east-1,db.t3.micro,2, \n")
	recs, err := loadRecommendationsFromCSV(csvPath)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Zero(t, recs[0].EstimatedSavings)
}

func TestRunToolFromCSV_RejectsMalformedNumbers(t *testing.T) {
	isolateAWSEnv(t)
	tests := []struct {
		name    string
		count   string
		savings string
		column  string
	}{
		{"fractional count", "3.7", "100", `column 4 "Count"`},
		{"count suffix", "12 units", "100", `column 4 "Count"`},
		{"negative count", "-5", "100", `column 4 "Count"`},
		{"count overflow", "99999999999999999999", "100", `column 4 "Count"`},
		{"savings suffix", "2", "1000 USD", `column 5 "EstimatedSavings"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				CSVInput: writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,EstimatedSavings\n"+
					"rds,us-east-1,db.t3.micro,"+tt.count+","+tt.savings+"\n"),
				CSVOutput:      filepath.Join(t.TempDir(), "report.csv"),
				AuditLog:       filepath.Join(t.TempDir(), "audit.jsonl"),
				ActualPurchase: false,
				Coverage:       100,
			}
			err := runToolFromCSV(context.Background(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to read CSV file: CSV line 2:")
			assert.Contains(t, err.Error(), tt.column)
			assert.NoFileExists(t, cfg.CSVOutput)
		})
	}
}
