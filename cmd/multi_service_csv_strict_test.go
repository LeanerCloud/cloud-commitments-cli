package main

import (
	"context"
	"path/filepath"
	"strconv"
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
	t.Parallel()
	tests := []struct {
		name string
		cell string
	}{
		{"fractional", "3.7"},
		{"trailing garbage", "3abc"},
		{"trailing unit", "12 units"},
		{"negative", "-1"},
		{"zero", "0"},
		{"blank", ""},
		{"whitespace only", "  "},
		{"overflows int64", "99999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count,Term,PaymentOption\n" +
				"rds,us-east-1,db.t3.micro,1,3yr,partial-upfront\n" +
				"rds,us-east-1,db.t3.small,\"" + tt.cell + "\",3yr,partial-upfront\n"
			err := loadCSVContent(t, content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 3")
			assert.Contains(t, err.Error(), `column 4 "Count"`)
		})
	}
}

func TestLoadRecommendationsFromCSV_CountTrimmed(t *testing.T) {
	t.Parallel()
	csvPath := writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,Term,PaymentOption\nrds,us-east-1,db.t3.micro,\" 3 \",3yr,partial-upfront\n")
	recs, err := loadRecommendationsFromCSV(csvPath)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, 3, recs[0].Count)
}

func TestLoadRecommendationsFromCSV_CountColumnRequired(t *testing.T) {
	t.Parallel()
	err := loadCSVContent(t, "Service,Region,ResourceType,Term,PaymentOption\nrds,us-east-1,db.t3.micro,3yr,partial-upfront\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Count")
}

func TestLoadRecommendationsFromCSV_StrictEstimatedSavings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cell string
	}{
		{"negative", "-100"},
		{"trailing currency", "1000 USD"},
		{"trailing garbage", "12.5abc"},
		{"NaN", "NaN"},
		{"infinity", "Inf"},
		{"overflows float64", "1e400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption\n" +
				"rds,us-east-1,db.t3.micro,2,\"" + tt.cell + "\",3yr,partial-upfront\n"
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
	t.Parallel()
	csvPath := writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption\nrds,us-east-1,db.t3.micro,2, ,3yr,partial-upfront\n")
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
				CSVInput: writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption\n"+
					"rds,us-east-1,db.t3.micro,"+tt.count+","+tt.savings+",3yr,partial-upfront\n"),
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

// Term and PaymentOption cells must be exactly what the purchase path accepts
// (1yr/3yr, all-upfront/partial-upfront/no-upfront); a blank or unknown value
// fails the load with the row number instead of reaching the purchase loop.
func TestLoadRecommendationsFromCSV_TermAndPaymentValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		term    string
		payment string
		column  string
	}{
		{"empty Term", "", "no-upfront", `column 5 "Term"`},
		{"whitespace Term", " ", "no-upfront", `column 5 "Term"`},
		{"unknown Term", "5yr", "no-upfront", `column 5 "Term"`},
		{"numeric Term", "3", "no-upfront", `column 5 "Term"`},
		{"uppercase Term", "3YR", "no-upfront", `column 5 "Term"`},
		{"empty PaymentOption", "1yr", "", `column 6 "PaymentOption"`},
		{"unknown PaymentOption", "1yr", "monthly", `column 6 "PaymentOption"`},
		{"display-form PaymentOption", "1yr", "All Upfront", `column 6 "PaymentOption"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count,Term,PaymentOption\n" +
				"rds,us-east-1,db.t3.micro,1,1yr,all-upfront\n" +
				"rds,us-east-1,db.t3.small,2," + tt.term + "," + tt.payment + "\n"
			err := loadCSVContent(t, content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 3")
			assert.Contains(t, err.Error(), tt.column)
		})
	}
}

func TestLoadRecommendationsFromCSV_TermAndPaymentAccepted(t *testing.T) {
	t.Parallel()
	for _, term := range []string{"1yr", "3yr"} {
		for _, payment := range []string{"all-upfront", "partial-upfront", "no-upfront"} {
			t.Run(term+"/"+payment, func(t *testing.T) {
				recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
					"Service,Region,ResourceType,Count,Term,PaymentOption\n"+
						"rds,us-east-1,db.t3.micro,2,"+term+","+payment+"\n"))
				require.NoError(t, err)
				require.Len(t, recs, 1)
				assert.Equal(t, term, recs[0].Term)
				assert.Equal(t, payment, recs[0].PaymentOption)
			})
		}
	}
}

// Service cells must be one of the canonical service types the purchase loop
// builds a client for (#379, audit A10-007). A flag alias such as
// "elasticsearch", a padded or capitalized slug, or the "savingsplans"
// umbrella used to load as an unknown ServiceType, and the row was then
// skipped with "Service client not yet implemented" instead of failing.
func TestLoadRecommendationsFromCSV_ServiceValues(t *testing.T) {
	tests := []struct {
		name    string
		service string
	}{
		{"empty", ""},
		{"whitespace only", " "},
		{"flag alias", "elasticsearch"},
		{"trailing space", "rds "},
		{"uppercase", "RDS"},
		{"savings plans umbrella", "savingsplans"},
		{"flag-only savings plans spelling", "savingsplans-compute"},
		{"unknown", "dynamodb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Service,Region,ResourceType,Count,Term,PaymentOption\n" +
				"rds,us-east-1,db.t3.micro,1,1yr,all-upfront\n" +
				"\"" + tt.service + "\",us-east-1,db.t3.small,2,1yr,all-upfront\n"
			err := loadCSVContent(t, content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 3")
			assert.Contains(t, err.Error(), `column 1 "Service"`)
		})
	}
}

// Every service the tool supports still loads, so the report a run writes can
// be fed back through --input-csv.
func TestLoadRecommendationsFromCSV_ServiceAccepted(t *testing.T) {
	for _, service := range getAllServices() {
		t.Run(string(service), func(t *testing.T) {
			recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t,
				"Service,Region,ResourceType,Count,Term,PaymentOption\n"+
					string(service)+",us-east-1,m5.large,1,1yr,no-upfront\n"))
			require.NoError(t, err)
			require.Len(t, recs, 1)
			assert.Equal(t, service, recs[0].Service)
		})
	}
}

// Count is bounded by MaxReasonableInstances, the same ceiling validateFlags
// puts on --max-instances and --override-count (#379).
func TestLoadRecommendationsFromCSV_CountUpperBound(t *testing.T) {
	row := func(count int) string {
		return "Service,Region,ResourceType,Count,Term,PaymentOption\n" +
			"rds,us-east-1,db.t3.micro," + strconv.Itoa(count) + ",3yr,partial-upfront\n"
	}

	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, row(MaxReasonableInstances)))
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, MaxReasonableInstances, recs[0].Count)

	err = loadCSVContent(t, row(MaxReasonableInstances+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
	assert.Contains(t, err.Error(), `column 4 "Count"`)
	assert.Contains(t, err.Error(), strconv.Itoa(MaxReasonableInstances))
}

// End to end: a row with a bad Service cell stops the run before any AWS call
// and before a report is written, rather than being dropped mid-run.
func TestRunToolFromCSV_RejectsUnknownService(t *testing.T) {
	awsHits := isolateAWSEnv(t)
	cfg := Config{
		CSVInput: writeTestRecommendationsCSV(t, "Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption\n"+
			"rds,us-east-1,db.t3.micro,1,100,1yr,all-upfront\n"+
			"elasticsearch,us-east-1,m5.large.search,1,100,1yr,all-upfront\n"),
		CSVOutput:      filepath.Join(t.TempDir(), "report.csv"),
		AuditLog:       filepath.Join(t.TempDir(), "audit.jsonl"),
		ActualPurchase: false,
		Coverage:       100,
	}
	err := runToolFromCSV(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read CSV file: CSV line 3:")
	assert.Contains(t, err.Error(), `column 1 "Service"`)
	assert.Contains(t, err.Error(), `"elasticsearch"`)
	assert.NoFileExists(t, cfg.CSVOutput)
	assert.Zero(t, awsHits.Load())
}
