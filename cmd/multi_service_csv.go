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
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

// determineCSVCoverage determines the coverage percentage to use for CSV mode.
func determineCSVCoverage(cfg Config) float64 {
	// When using CSV input, default to 100% coverage (use exact numbers from CSV)
	// unless user explicitly provided a different coverage value
	if cfg.Coverage == 80.0 {
		// User didn't override the default, so use 100% for CSV mode
		return 100.0
	}
	return cfg.Coverage
}

// loadRecommendationsFromCSV reads and returns recommendations from a CSV file.
func loadRecommendationsFromCSV(csvPath string) ([]common.Recommendation, error) {
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

	// Parse all records
	parsed, err := parseCSVRecords(reader, colIdx)
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
// PaymentOption values match validatePaymentAndTerm.
var (
	validCSVTerms          = []string{"1yr", "3yr"}
	validCSVPaymentOptions = []string{"all-upfront", "partial-upfront", "no-upfront"}
)

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
func parseCSVRecords(reader *csv.Reader, colIdx map[string]int) ([]common.Recommendation, error) {
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

		rec, err := parseCSVRecord(record, colIdx)
		if err != nil {
			line, _ := reader.FieldPos(0)
			return nil, fmt.Errorf("CSV line %d: %w", line, err)
		}

		recs = append(recs, rec)
	}

	return recs, nil
}

// parseCSVRecord parses a single CSV record into a Recommendation.
func parseCSVRecord(record []string, colIdx map[string]int) (common.Recommendation, error) {
	rec := common.Recommendation{Provider: common.ProviderAWS}

	// Parse string fields
	rec.Service = common.ServiceType(getCSVField(record, colIdx, "Service"))
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
	header := slices.Concat(baseHeader, csvDetailColumns)
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
		row := slices.Concat(baseRow, detailCells(rec))
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
	return slices.Concat(totals, make([]string, len(csvDetailColumns))) // detail columns do not aggregate
}

// formatIntOrBlank renders an int as its decimal string when non-zero, ""
// otherwise. SP recommendations leave RecommendedCount at zero (SPs are
// dollar-denominated, not count-denominated), so blanking matches the
// "0 = unknown / not applicable" convention used elsewhere in the CSV.
func formatIntOrBlank(v int) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}

// extractFamily returns the CSV Family value: the instance-family prefix
// (e.g. "db.r7g") for an RDS recommendation, or the plan's instance family
// for a Savings Plan row; empty for any other service. Operators use it to
// group rows by family (for RDS, the family-NU bucket).
func extractFamily(rec common.Recommendation) string {
	if sp, ok := rec.Details.(*common.SavingsPlanDetails); ok && sp != nil {
		return sp.InstanceFamily
	}
	if rec.Service != common.ServiceRDS && rec.Service != common.ServiceRelationalDB {
		return ""
	}
	return recommendations.RDSFamilyFromType(rec.ResourceType)
}

// formatNormalizedUnitsOrBlank renders the per-rec NU contribution
// (rec.Count × NU(size)) for RDS rows: e.g. 15 × db.r7g.large = 60 NU.
// Surfaces the size-flex math AWS rec API uses to bundle family demand
// into a single rec at one size — without this column, operators have
// to compute NU by hand to verify the bundling. Renders blank for
// non-RDS rows and for sizes not in the standard NU scale.
func formatNormalizedUnitsOrBlank(rec common.Recommendation) string {
	nu := rowNormalizedUnits(rec)
	if nu == 0 {
		return ""
	}
	return fmt.Sprintf("%g", nu)
}

// rowNormalizedUnits returns the NU a row contributes (rec.Count × NU(size))
// for RDS rows, and 0 for every other service. It is the single source of
// the per-row NU value, so the TOTAL row sums exactly what the NU cells
// above it show: ElastiCache types like "cache.r7g.large" would otherwise
// resolve a size suffix in the RDS NU lookup even though their own cell is
// blank (issue #2005).
func rowNormalizedUnits(rec common.Recommendation) float64 {
	if rec.Service != common.ServiceRDS && rec.Service != common.ServiceRelationalDB {
		return 0
	}
	return float64(rec.Count) * recommendations.RDSInstanceNUFromType(rec.ResourceType)
}

// extractDeployment returns the RDS deployment-option string
// ("single-az" / "multi-az") for an RDS recommendation, empty for any
// service that doesn't carry a deployment dimension. Critical for RDS
// price verification: Multi-AZ list prices are roughly 2x Single-AZ, so
// operators need to see the deployment alongside the upfront figure to
// confirm a $X upfront row is for the deployment they expect.
//
// DatabaseDetails is always a pointer: every producer (AWS, Azure, the CSV
// loader, and the JSON codec used on the purchase-execution round trip)
// constructs it that way -- see pkg/common/service_details_codec.go's
// package doc for the invariant.
func extractDeployment(rec common.Recommendation) string {
	if details, ok := rec.Details.(*common.DatabaseDetails); ok && details != nil {
		return details.AZConfig
	}
	return ""
}

// extractEngine returns the engine / platform string for a recommendation's
// polymorphic Details: Engine for RDS / ElastiCache (DatabaseDetails,
// CacheDetails), Platform for EC2 (ComputeDetails), empty for SP and other
// commitment types that don't carry an engine field.
//
// DatabaseDetails/CacheDetails are always pointers (see extractDeployment's
// godoc for the invariant). ComputeDetails is still accepted as a value
// because the Azure compute client and the GCP compute-engine client both
// construct it that way; without that case the column silently blanks every
// Azure VM and GCP compute row.
func extractEngine(rec common.Recommendation) string {
	switch details := rec.Details.(type) {
	case *common.DatabaseDetails:
		if details != nil {
			return details.Engine
		}
	case *common.CacheDetails:
		if details != nil {
			return details.Engine
		}
	case *common.ComputeDetails:
		if details != nil {
			return details.Platform
		}
	case common.ComputeDetails:
		return details.Platform
	}
	return ""
}

// formatExistingCoverage renders the existing-RI coverage cell with
// three distinct states:
//   - "n/a" when CE returned no data for the rec's pool (rec parser was
//     able to surface a recommendation from some other signal but CE's
//     coverage view doesn't see the pool yet — e.g. recently-launched
//     instances within CUDly's run window but outside CE's lookback)
//   - "0.0" when CE confirms the pool exists but has zero RI coverage
//     (the legitimate "buy for uncovered demand" case)
//   - "X.X" with one decimal for any non-zero coverage percentage
//
// Previously both the no-data and the genuine-zero cases rendered as a
// blank cell, conflating "we don't know" with "definitely zero" and
// making it impossible to spot pools where the CE signal was missing.
func formatExistingCoverage(rec common.Recommendation) string {
	if !rec.ExistingCoverageKnown {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", rec.ExistingCoveragePct)
}

// formatRecurringMonthlyOrBlank renders rec.RecurringMonthlyCost (the
// per-month fee on top of any upfront payment, populated by the AWS
// parser when CE returns RecurringStandardMonthlyCost). Distinguishes
// "no recurring fee" (all-upfront RIs, where the pointer is set to
// zero) from "unknown" (pointer is nil because CE didn't return the
// field): zero renders as "0.00", nil renders as blank.
//
// Operators on partial-upfront / no-upfront plans need this to compute
// total cost (upfront + monthly × 36); without it the CSV only shows
// the upfront portion and over-states ROI.
func formatRecurringMonthlyOrBlank(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *p)
}

// formatCurrencyOrBlank renders a currency value as "%.2f" when non-zero,
// "" otherwise. Used for UpfrontPayment so a no-upfront / unknown-upfront
// rec renders as a blank cell rather than "$0.00".
func formatCurrencyOrBlank(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%.2f", v)
}

// formatAvgInstancesOrBlank renders the average instances-per-hour signal
// (AverageInstancesUsedPerHour from CE) with one decimal so operators can
// see the pool's running demand without losing the fractional precision
// CE returns. Blank when zero, matching the "0 = no signal" convention.
func formatAvgInstancesOrBlank(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%.1f", v)
}

// formatCoveredInstancesOrBlank renders the instances in the pool already
// covered by existing commitments: avg × existing_coverage / 100. Useful
// next to Instances so operators can read "you have X running, Y are
// already covered, this rec adds N more" without doing the arithmetic.
// Blank when either signal is zero (we can't compute a meaningful value).
func formatCoveredInstancesOrBlank(rec common.Recommendation) string {
	if rec.AverageInstancesUsedPerHour <= 0 || rec.ExistingCoveragePct <= 0 {
		return ""
	}
	covered := rec.AverageInstancesUsedPerHour * rec.ExistingCoveragePct / 100.0
	return fmt.Sprintf("%.1f", covered)
}

// formatPercentOrBlank renders a % value as "%.1f" when non-zero, "" otherwise.
// Zero means "unknown / not applicable" — we don't want "0.0" in cells where
// the metric simply wasn't computed (e.g. ProjectedCoverage for SP rows, or
// any utilization field when --target-coverage wasn't used).
func formatPercentOrBlank(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%.1f", v)
}
