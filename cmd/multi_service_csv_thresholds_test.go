package main

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	csvThresholdArgsEnv = "CUDLY_CSV_THRESHOLD_TEST_ARGS"
	csvThresholdCSVEnv  = "CUDLY_CSV_THRESHOLD_TEST_CSV"
	csvThresholdWantEnv = "CUDLY_CSV_THRESHOLD_TEST_WANT"
)

const thresholdCSVHeader = "Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account,EstimatedSavings,SavingsPercentage,BreakEvenMonths\n"

var thresholdCSVBodies = map[string]string{
	"full": thresholdCSVHeader +
		"rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111,100,10,3\n" +
		"rds,us-east-1,db.t3.medium,postgres,2,1yr,all-upfront,111111111111,200,30,14\n" +
		"rds,us-east-1,db.t3.large,postgres,2,1yr,all-upfront,111111111111,300,50,8\n",
	"old": "Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account,EstimatedSavings\n" +
		"rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111,100\n",
	"blank": thresholdCSVHeader +
		"rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111,100,,\n",
}

// TestCSVThresholdFlagsThroughRootCommand drives --min-savings-pct and
// --max-break-even-months through the real command (cobra, validateFlags,
// runTool, the --input-csv dry run) for #1819. Each case runs in a child
// process so cobra's flag state never leaks between cases.
func TestCSVThresholdFlagsThroughRootCommand(t *testing.T) {
	if args, ok := os.LookupEnv(csvThresholdArgsEnv); ok {
		runCSVThresholdChild(t, args, os.Getenv(csvThresholdCSVEnv), os.Getenv(csvThresholdWantEnv))
		return
	}
	tests := []struct {
		name string
		csv  string
		args []string
		want string
	}{
		{"no thresholds keeps every row", "full", nil, "ok:db.t3.large,db.t3.medium,db.t3.small"},
		{"min-savings-pct filters rows below it", "full", []string{"--min-savings-pct", "25"}, "ok:db.t3.large,db.t3.medium"},
		{"max-break-even-months filters rows above it", "full", []string{"--max-break-even-months", "10"}, "ok:db.t3.large,db.t3.small"},
		{"both thresholds combine", "full", []string{"--min-savings-pct", "25", "--max-break-even-months", "10"}, "ok:db.t3.large"},
		{"older CSV without the columns runs when no flag needs them", "old", nil, "ok:db.t3.small"},
		{"older CSV with --min-savings-pct is refused", "old", []string{"--min-savings-pct", "25"}, `err:no "SavingsPercentage" column`},
		{"older CSV with --max-break-even-months is refused", "old", []string{"--max-break-even-months", "10"}, `err:no "BreakEvenMonths" column`},
		{"blank cell with --min-savings-pct is refused", "blank", []string{"--min-savings-pct", "25"}, `err:column "SavingsPercentage" is blank`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCSVThresholdFlagsThroughRootCommand$", "-test.v")
			child.Env = []string{
				csvThresholdArgsEnv + "=" + strings.Join(tt.args, "\x1f"),
				csvThresholdCSVEnv + "=" + tt.csv,
				csvThresholdWantEnv + "=" + tt.want,
			}
			output, err := child.CombinedOutput()
			if substr, isErr := strings.CutPrefix(tt.want, "err:"); isErr {
				// runTool reports a refused load with log.Fatalf, so the run
				// ends the process non-zero before any report is written.
				require.Error(t, err, "%s", output)
				assert.Contains(t, string(output), substr)
				return
			}
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), "--- PASS: TestCSVThresholdFlagsThroughRootCommand")
		})
	}
}

func runCSVThresholdChild(t *testing.T, joinedArgs, csvKey, want string) {
	isolateAWSEnv(t)
	dir := t.TempDir()
	csvPath := writeTestRecommendationsCSV(t, thresholdCSVBodies[csvKey])
	reportPath := filepath.Join(dir, "report.csv")
	args := []string{"--input-csv", csvPath, "--output", reportPath, "--audit-log", filepath.Join(dir, "audit.jsonl")}
	if joinedArgs != "" {
		args = append(args, strings.Split(joinedArgs, "\x1f")...)
	}
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	require.NoError(t, err)
	header, rows := readPurchaseReport(t, reportPath)
	got := reportColumn(t, header, rows, "ResourceType")
	sort.Strings(got)
	assert.Equal(t, strings.Split(strings.TrimPrefix(want, "ok:"), ","), got)
}

// TestCSVThresholdColumnsRoundTrip pins that the writer emits both columns and
// the parser reads back exactly what was written.
func TestCSVThresholdColumnsRoundTrip(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 2, Term: "1yr", PaymentOption: "all-upfront",
			CommitmentCost: 1200, EstimatedSavings: 50, SavingsPercentage: 33.333333333333336},
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.medium", Count: 1, Term: "1yr", PaymentOption: "no-upfront",
			EstimatedSavings: 20, SavingsPercentage: 12.5},
	}
	results := make([]common.PurchaseResult, len(recs))
	for i, r := range recs {
		results[i] = common.PurchaseResult{Recommendation: r, Success: true, Timestamp: time.Unix(0, 0).UTC()}
	}
	var buf bytes.Buffer
	require.NoError(t, writeMultiServiceCSV(&buf, results))

	path := writeTestRecommendationsCSV(t, buf.String())
	loaded, err := loadCSVRecommendations(path, csvThresholdNeeds{savingsPct: true, breakEven: true})
	require.NoError(t, err)
	byType := map[string]common.Recommendation{}
	for _, r := range loaded {
		byType[r.ResourceType] = r
	}
	assert.Equal(t, 33.333333333333336, byType["db.t3.small"].SavingsPercentage, "exact decimal round trip")
	assert.Equal(t, 8.0, byType["db.t3.small"].BreakEvenMonths)
	assert.Equal(t, 12.5, byType["db.t3.medium"].SavingsPercentage)
	assert.Zero(t, byType["db.t3.medium"].BreakEvenMonths, "no upfront breaks even at 0 months")

	header := strings.Split(strings.SplitN(buf.String(), "\n", 2)[0], ",")
	assert.Equal(t, csvThresholdColumns, header[len(header)-len(csvThresholdColumns):], "threshold columns are appended last")
}

// TestThresholdCellsBlankWhenBreakEvenUnknownOrInfinite pins that a break-even
// that cannot be stated as a finite number is written blank (absent), never as
// "+Inf" or a fabricated 0.
func TestThresholdCellsBlankWhenBreakEvenUnknownOrInfinite(t *testing.T) {
	never := common.Recommendation{Term: "1yr", CommitmentCost: 1200, EstimatedSavings: -100, SavingsPercentage: 1}
	months, known := common.BreakEven(never)
	require.True(t, known)
	require.True(t, math.IsInf(months, 1))
	assert.Equal(t, []string{"1", ""}, thresholdCells(never))

	badTerm := common.Recommendation{Term: "5yr", CommitmentCost: 1200, EstimatedSavings: 50}
	assert.Equal(t, []string{"0", ""}, thresholdCells(badTerm))
}

// TestCSVThresholdHeaderStaysStrict pins that carrying the new columns did not
// loosen the required-column check.
func TestCSVThresholdHeaderStaysStrict(t *testing.T) {
	_, err := loadCSVRecommendations(writeTestRecommendationsCSV(t,
		"Service,Region,ResourceType,Term,PaymentOption,SavingsPercentage,BreakEvenMonths\nrds,us-east-1,db.t3.small,1yr,all-upfront,10,3\n"),
		csvThresholdNeeds{savingsPct: true, breakEven: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CSV header missing required columns: Count")
}

// TestCSVThresholdWhitespaceCellIsBlank pins that a whitespace-only cell is
// blank: refused when its flag is set, ignored (left 0) when it is not.
func TestCSVThresholdWhitespaceCellIsBlank(t *testing.T) {
	const row = "rds,us-east-1,db.t3.small,postgres,2,1yr,all-upfront,111111111111,100,%s,%s\n"
	for _, blank := range []string{" ", "\t"} {
		for _, col := range []struct {
			name, flag string
			cells      [2]string
			need       csvThresholdNeeds
		}{
			{csvSavingsPctColumn, "--min-savings-pct", [2]string{blank, "3"}, csvThresholdNeeds{savingsPct: true}},
			{csvBreakEvenColumn, "--max-break-even-months", [2]string{"10", blank}, csvThresholdNeeds{breakEven: true}},
		} {
			path := writeTestRecommendationsCSV(t, thresholdCSVHeader+strings.Replace(strings.Replace(row, "%s", col.cells[0], 1), "%s", col.cells[1], 1))

			_, err := loadCSVRecommendations(path, col.need)
			require.Error(t, err, "%q in %s", blank, col.name)
			assert.Contains(t, err.Error(), col.name)
			assert.Contains(t, err.Error(), col.flag)
			assert.Contains(t, err.Error(), "CSV line 2")

			recs, err := loadCSVRecommendations(path, csvThresholdNeeds{})
			require.NoError(t, err, "%q in %s with no flag", blank, col.name)
			require.Len(t, recs, 1)
		}
	}
}

// TestCSVThresholdOrderingStaysOnSavingsPerInstance pins that populating
// SavingsPercentage does not re-rank --max-instances: the cap still buys the
// best savings-per-instance row, not the highest percentage.
func TestCSVThresholdOrderingStaysOnSavingsPerInstance(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "high-pct", Count: 4, EstimatedSavings: 40, SavingsPercentage: 90},
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "high-rate", Count: 4, EstimatedSavings: 400, SavingsPercentage: 20},
	}
	got, err := scoreAndLimitCSVRecs(recs, Config{MaxInstances: 4, MinSavingsPct: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "high-rate", got[0].ResourceType)
}
