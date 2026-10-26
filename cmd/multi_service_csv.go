package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// determineCSVCoverage determines the coverage percentage to use for CSV mode.
// Without --coverage on the command line a CSV run buys each row's full Count
// (100%). An explicit --coverage is always honored, including a value equal to
// the flag's default (80): the check uses CoverageFromDefault, which runTool
// sets from cobra's Changed(), never a comparison against the default value.
func determineCSVCoverage(cfg Config) float64 {
	if cfg.CoverageFromDefault {
		return 100.0
	}
	return cfg.Coverage
}

// loadRecommendationsFromCSV reads and returns recommendations from a CSV file.
func loadRecommendationsFromCSV(csvPath string) ([]common.Recommendation, error) {
	return loadCSVRecommendations(csvPath, csvThresholdNeeds{})
}

// loadCSVRecommendations is loadRecommendationsFromCSV for a run whose flags
// need threshold figures from the file (see csvThresholdNeeds).
func loadCSVRecommendations(csvPath string, need csvThresholdNeeds) ([]common.Recommendation, error) {
	file, err := os.Open(csvPath) // #nosec G304 -- CLI tool: csvPath is an operator-supplied command-line argument
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("Warning: failed to close CSV file %s: %v", csvPath, closeErr)
		}
	}()

	reader := csv.NewReader(file)

	// Read header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}
	if err = validateCSVUTF8(header); err != nil {
		return nil, fmt.Errorf("CSV header: %w", err)
	}

	colIdx, err := buildColumnIndexMap(header)
	if err != nil {
		return nil, err
	}
	if colErr := requireThresholdColumns(colIdx, need); colErr != nil {
		return nil, colErr
	}

	// Parse all records
	parsed, err := parseCSVRecords(reader, colIdx, need)
	if err != nil {
		return nil, err
	}

	return parsed, nil
}

// Minimal CSVs require these columns; service-specific columns stay optional.
// Term and PaymentOption are required because the purchase path reads them per
// row and must never guess a commitment's term or payment option.
var requiredCSVColumns = []string{"Service", "Region", "ResourceType", "Count", "Term", "PaymentOption"}

// Accepted Term values match fetchRecommendationsForRegion; accepted
// PaymentOption values match validatePaymentAndTerm. Accepted Service values
// are the canonical slugs writeMultiServiceCSV emits and createServiceClient
// builds a client for; --services flag aliases (elasticsearch, savingsplans,
// savingsplans-compute, ...) are not accepted, since a row the purchase loop
// cannot route would otherwise be skipped mid-run instead of failing the load.
var (
	validCSVTerms          = []string{"1yr", "3yr"}
	validCSVPaymentOptions = []string{"all-upfront", "partial-upfront", "no-upfront"}
	validCSVServices       = csvServiceNames()
)

func csvServiceNames() []string {
	services := getAllServices()
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = string(s)
	}
	return names
}

// parseCSVEnum requires the cell to be exactly one of allowed.
func parseCSVEnum(record []string, colIdx map[string]int, fieldName string, allowed []string) (string, error) {
	col := colIdx[fieldName] + 1
	value := getCSVField(record, colIdx, fieldName)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("column %d %q: value is required (one of: %s)", col, fieldName, strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if value == a {
			return value, nil
		}
	}
	return "", fmt.Errorf("column %d %q: invalid value %q (must be one of: %s)", col, fieldName, value, strings.Join(allowed, ", "))
}

func validateCSVUTF8(fields []string) error {
	for i, field := range fields {
		if !utf8.ValidString(field) {
			return fmt.Errorf("column %d: invalid UTF-8 encoding", i+1)
		}
	}
	return nil
}

func buildColumnIndexMap(header []string) (map[string]int, error) {
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[col] = i
	}
	var missing []string
	for _, req := range requiredCSVColumns {
		if _, ok := colIdx[req]; !ok {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("CSV header missing required columns: %s", strings.Join(missing, ", "))
	}
	return colIdx, nil
}

// parseCSVRecords reads and parses all CSV records.
func parseCSVRecords(reader *csv.Reader, colIdx map[string]int, need csvThresholdNeeds) ([]common.Recommendation, error) {
	var recs []common.Recommendation

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV record: %w", err)
		}
		if err = validateCSVUTF8(record); err != nil {
			line, _ := reader.FieldPos(0)
			return nil, fmt.Errorf("CSV line %d: %w", line, err)
		}

		// Skip the trailing TOTAL summary row that writeMultiServiceCSVReport
		// emits (label in the Service column). Without this, feeding the tool
		// its own output parses TOTAL as a bogus recommendation with an
		// unknown service type.
		if getCSVField(record, colIdx, "Service") == "TOTAL" {
			continue
		}

		rec, err := parseCSVRecord(record, colIdx, need)
		if err != nil {
			line, _ := reader.FieldPos(0)
			return nil, fmt.Errorf("CSV line %d: %w", line, err)
		}

		recs = append(recs, rec)
	}

	return recs, nil
}

// parseCSVRecord parses a single CSV record into a Recommendation.
func parseCSVRecord(record []string, colIdx map[string]int, need csvThresholdNeeds) (common.Recommendation, error) {
	rec := common.Recommendation{Provider: common.ProviderAWS}

	service, err := parseCSVEnum(record, colIdx, "Service", validCSVServices)
	if err != nil {
		return rec, err
	}
	rec.Service = common.ServiceType(service)

	// Parse string fields
	rec.Region = getCSVField(record, colIdx, "Region")
	rec.ResourceType = getCSVField(record, colIdx, "ResourceType")
	rec.Account = getCSVField(record, colIdx, "Account")
	rec.AccountName = getCSVField(record, colIdx, "AccountName")

	term, err := parseCSVEnum(record, colIdx, "Term", validCSVTerms)
	if err != nil {
		return rec, err
	}
	rec.Term = term
	payment, err := parseCSVEnum(record, colIdx, "PaymentOption", validCSVPaymentOptions)
	if err != nil {
		return rec, err
	}
	rec.PaymentOption = payment

	err = parseCSVCount(record, colIdx, &rec.Count)
	if err != nil {
		return rec, err
	}

	err = parseCSVFloat(record, colIdx, "EstimatedSavings", &rec.EstimatedSavings)
	if err != nil {
		return rec, err
	}

	if cellErr := parseCSVThresholds(record, colIdx, need, &rec); cellErr != nil {
		return rec, cellErr
	}

	// Details come from the Engine/Deployment columns and the appended detail
	// columns (see csvDetails).
	details, err := csvDetails(rec, record, colIdx)
	if err != nil {
		return rec, err
	}
	rec.Details = details

	return rec, nil
}

// getCSVField safely retrieves a string field from a CSV record.
func getCSVField(record []string, colIdx map[string]int, fieldName string) string {
	if idx, ok := colIdx[fieldName]; ok && idx < len(record) {
		return record[idx]
	}
	return ""
}

// Savings Plans exports use Count=1, preserving positive-count round trips.
func parseCSVCount(record []string, colIdx map[string]int, target *int) error {
	const fieldName = "Count"
	idx, ok := colIdx[fieldName]
	if !ok {
		return fmt.Errorf("missing required %s column", fieldName)
	}
	value := strings.TrimSpace(getCSVField(record, colIdx, fieldName))
	if value == "" {
		return fmt.Errorf("column %d %q: value is required", idx+1, fieldName)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("column %d %q: invalid integer: %w", idx+1, fieldName, err)
	}
	if n < 1 {
		return fmt.Errorf("column %d %q: must be at least 1, got %d", idx+1, fieldName, n)
	}
	// Same ceiling validateFlags puts on --max-instances and --override-count.
	if n > MaxReasonableInstances {
		return fmt.Errorf("column %d %q: must be at most %d, got %d", idx+1, fieldName, MaxReasonableInstances, n)
	}
	*target = n
	return nil
}

// Blank savings stay absent-as-zero for requireRankingSignal.
func parseCSVFloat(record []string, colIdx map[string]int, fieldName string, target *float64) error {
	value := strings.TrimSpace(getCSVField(record, colIdx, fieldName))
	if value == "" {
		return nil
	}
	col := colIdx[fieldName] + 1
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("column %d %q: invalid number: %w", col, fieldName, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("column %d %q: invalid number %q: must be finite", col, fieldName, value)
	}
	if f < 0 {
		return fmt.Errorf("column %d %q: must not be negative, got %g", col, fieldName, f)
	}
	*target = f
	return nil
}

// writeCSVReport writes the purchase report and tells the operator whether a
// file exists: "written to" is printed only after a complete, flushed and
// closed write. A run with no results writes no file, so it says so instead.
func writeCSVReport(results []common.PurchaseResult, path string) {
	if len(results) == 0 {
		AppLogger.Printf("\n📋 No purchase results; CSV report not written.\n")
		return
	}
	if err := writeMultiServiceCSVReport(results, path); err != nil {
		log.Printf("Warning: Failed to write CSV output: %v", err)
		return
	}
	AppLogger.Printf("\n📋 CSV report written to: %s\n", path)
}

// writeMultiServiceCSVReport writes purchase results to a CSV file. It is a
// no-op returning nil when results is empty (no file is created).
func writeMultiServiceCSVReport(results []common.PurchaseResult, filepath string) (err error) {
	if len(results) == 0 {
		return nil
	}

	file, err := os.Create(filepath) // #nosec G304 -- CLI tool: filepath is an operator-supplied output path argument
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer func() {
		// A failed close can mean the data never reached the disk (e.g. on
		// a network mount), so it fails the write rather than a warning.
		if cerr := file.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close CSV file: %w", cerr)
		}
	}()

	return writeMultiServiceCSV(file, results)
}

// writeMultiServiceCSV renders the purchase report to w, flushing before it
// returns so a failed flush is returned rather than lost in a defer.
func writeMultiServiceCSV(w io.Writer, results []common.PurchaseResult) error {
	writer := csv.NewWriter(w)

	// Write header. RecommendedCount shows AWS's pre-sizing count alongside
	// Count (the post-sizing value); UpfrontPayment is rec.CommitmentCost,
	// which ApplyCoverage / ApplyTargetCoverage now scale at sizing time so
	// the value already reflects the sized purchase. ExistingCoverage shows
	// the % of demand already covered by commitments in the same pool (from
	// CE GetReservationCoverage); ProjectedCoverage shows where the purchase
	// landed (total coverage after adding the new RIs). All four optional
	// columns render blank when zero so users on the straight --coverage
	// path don't see noise. ProjectedUtilization and RecommendedUtilization
	// are NOT emitted because under under-buy sizing both land at ~100% on
	// every row, which adds noise without information; the underlying fields
	// stay on the Recommendation struct for internal use (SP no-signal
	// guard, etc.).
	baseHeader := []string{
		"Service", "Region", "ResourceType", "Family", "Engine", "Deployment",
		"Instances", "CoveredInstances",
		"Count", "NormalizedUnits", "RecommendedCount",
		"Account", "AccountName", "Term", "PaymentOption",
		"UpfrontPayment", "RecurringMonthlyCost", "EstimatedSavings",
		"CommitmentID", "Success", "Error", "Timestamp",
		"ExistingCoverage", "ProjectedCoverage",
	}
	// Appended, never reordered: existing readers look columns up by name.
	header := slices.Concat(baseHeader, csvDetailColumns, csvThresholdColumns)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Sort by upfront DESC so the biggest-dollar decisions surface at
	// the top of the file rather than wherever AWS rec API happened to
	// return them. Operators reading top-down see the rows that matter
	// most for budget review first. Copy the slice so the caller's
	// ordering isn't mutated (some callers iterate results twice).
	sorted := make([]common.PurchaseResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Recommendation.CommitmentCost > sorted[j].Recommendation.CommitmentCost
	})

	for i := range sorted {
		r := sorted[i]
		rec := r.Recommendation
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}

		baseRow := []string{
			string(rec.Service),
			rec.Region,
			rec.ResourceType,
			extractFamily(rec),
			extractEngine(rec),
			extractDeployment(rec),
			formatAvgInstancesOrBlank(rec.AverageInstancesUsedPerHour),
			formatCoveredInstancesOrBlank(rec),
			fmt.Sprintf("%d", rec.Count),
			formatNormalizedUnitsOrBlank(rec),
			formatIntOrBlank(rec.RecommendedCount),
			rec.Account,
			rec.AccountName,
			rec.Term,
			rec.PaymentOption,
			formatCurrencyOrBlank(rec.CommitmentCost),
			formatRecurringMonthlyOrBlank(rec.RecurringMonthlyCost),
			fmt.Sprintf("%.2f", rec.EstimatedSavings),
			r.CommitmentID,
			fmt.Sprintf("%t", r.Success),
			errStr,
			r.Timestamp.Format(time.RFC3339),
			formatExistingCoverage(rec),
			formatPercentOrBlank(rec.ProjectedCoverage),
		}
		row := slices.Concat(baseRow, detailCells(rec), thresholdCells(rec))
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	// TOTAL row aggregates the sum-able fields (Count, NormalizedUnits,
	// UpfrontPayment, RecurringMonthlyCost, EstimatedSavings) so operators
	// don't have to recompute in a spreadsheet. The "TOTAL" label lands in
	// the Service column for easy spotting; columns that don't aggregate
	// meaningfully (per-rec identifiers, timestamps, %) stay blank.
	if len(sorted) > 0 {
		totalRow := buildTotalRow(sorted)
		if err := writer.Write(totalRow); err != nil {
			return fmt.Errorf("failed to write CSV total row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("failed to flush CSV file: %w", err)
	}
	return nil
}

// buildTotalRow sums the count + currency columns across results and
// returns a row aligned to the same header order as writeCSVRowsOrdered.
// Non-summable cells (per-rec identifiers, percentages, timestamps) are
// blank; the "TOTAL" label lands in Service so the row reads as a
// summary at first glance.
func buildTotalRow(results []common.PurchaseResult) []string {
	var totalCount int
	var totalNU, totalUpfront, totalRecurring, totalSavings float64
	hasRecurring := false
	for i := range results {
		r := results[i]
		totalCount += r.Recommendation.Count
		totalNU += rowNormalizedUnits(r.Recommendation)
		totalUpfront += r.Recommendation.CommitmentCost
		totalSavings += r.Recommendation.EstimatedSavings
		if r.Recommendation.RecurringMonthlyCost != nil {
			totalRecurring += *r.Recommendation.RecurringMonthlyCost
			hasRecurring = true
		}
	}
	recurringCell := ""
	if hasRecurring {
		recurringCell = fmt.Sprintf("%.2f", totalRecurring)
	}
	nuCell := ""
	if totalNU > 0 {
		nuCell = fmt.Sprintf("%g", totalNU)
	}
	totals := []string{
		"TOTAL", "", "", "", "", "", // Service through Deployment
		"", "", // Instances, CoveredInstances
		fmt.Sprintf("%d", totalCount), nuCell, "", // Count, NormalizedUnits, RecommendedCount
		"", "", "", "", // Account, AccountName, Term, PaymentOption
		fmt.Sprintf("%.2f", totalUpfront), recurringCell, fmt.Sprintf("%.2f", totalSavings),
		"", "", "", "", // CommitmentID, Success, Error, Timestamp
		"", "", // ExistingCoverage, ProjectedCoverage
	}
	return slices.Concat(totals, make([]string, len(csvDetailColumns)+len(csvThresholdColumns))) // detail and threshold columns do not aggregate
}
