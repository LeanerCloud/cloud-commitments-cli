package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/providers/aws/recommendations"
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

	// Build column index map
	colIdx := buildColumnIndexMap(header)

	// Parse all records
	parsed, err := parseCSVRecords(reader, colIdx)
	if err != nil {
		return nil, err
	}

	return parsed, nil
}

// buildColumnIndexMap creates a map from column names to indices.
func buildColumnIndexMap(header []string) map[string]int {
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[col] = i
	}
	return colIdx
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

		// Skip the trailing TOTAL summary row that writeMultiServiceCSVReport
		// emits (label in the Service column). Without this, feeding the tool
		// its own output parses TOTAL as a bogus recommendation with an
		// unknown service type.
		if getCSVField(record, colIdx, "Service") == "TOTAL" {
			continue
		}

		rec, err := parseCSVRecord(record, colIdx)
		if err != nil {
			return nil, err
		}

		recs = append(recs, rec)
	}

	return recs, nil
}

// parseCSVRecord parses a single CSV record into a Recommendation.
func parseCSVRecord(record []string, colIdx map[string]int) (common.Recommendation, error) {
	rec := common.Recommendation{}

	// Parse string fields
	rec.Service = common.ServiceType(getCSVField(record, colIdx, "Service"))
	rec.Region = getCSVField(record, colIdx, "Region")
	rec.ResourceType = getCSVField(record, colIdx, "ResourceType")
	rec.Account = getCSVField(record, colIdx, "Account")
	rec.AccountName = getCSVField(record, colIdx, "AccountName")
	rec.Term = getCSVField(record, colIdx, "Term")
	rec.PaymentOption = getCSVField(record, colIdx, "PaymentOption")

	// Parse integer fields
	if err := parseCSVInt(record, colIdx, "Count", &rec.Count); err != nil {
		return rec, err
	}

	// Parse float fields
	if err := parseCSVFloat(record, colIdx, "EstimatedSavings", &rec.EstimatedSavings); err != nil {
		return rec, err
	}

	// Reconstruct the service Details from the Engine/Deployment columns. The
	// purchase path needs them: RDS findOfferingID rejects a rec with nil
	// Details ("invalid service details for RDS"), and RI offerings are keyed
	// by engine and Multi-AZ. This mirrors the writer side (extractEngine /
	// extractDeployment emit DatabaseDetails / CacheDetails / ComputeDetails),
	// so a CSV the tool wrote round-trips losslessly. Engine is stored in Cost
	// Explorer format ("Aurora MySQL"); findOfferingID normalizes it. Guarded
	// on a non-empty Engine so minimal CSVs and Savings Plans rows (no Engine
	// column) keep their previous nil-Details behavior.
	if engine := getCSVField(record, colIdx, "Engine"); engine != "" {
		deployment := getCSVField(record, colIdx, "Deployment")
		switch rec.Service {
		case common.ServiceRDS, common.ServiceRelationalDB:
			rec.Details = &common.DatabaseDetails{
				Engine:        engine,
				AZConfig:      deployment,
				InstanceClass: rec.ResourceType,
			}
		case common.ServiceElastiCache, common.ServiceCache:
			rec.Details = &common.CacheDetails{
				Engine:   engine,
				NodeType: rec.ResourceType,
			}
		case common.ServiceEC2, common.ServiceCompute:
			rec.Details = &common.ComputeDetails{
				InstanceType: rec.ResourceType,
				Platform:     engine,
			}
		}
	}

	return rec, nil
}

// getCSVField safely retrieves a string field from a CSV record.
func getCSVField(record []string, colIdx map[string]int, fieldName string) string {
	if idx, ok := colIdx[fieldName]; ok && idx < len(record) {
		return record[idx]
	}
	return ""
}

// parseCSVInt parses an integer field from a CSV record.
func parseCSVInt(record []string, colIdx map[string]int, fieldName string, target *int) error {
	value := getCSVField(record, colIdx, fieldName)
	if value == "" {
		return nil
	}

	if _, err := fmt.Sscanf(value, "%d", target); err != nil {
		return fmt.Errorf("invalid %s value '%s': %w", fieldName, value, err)
	}
	return nil
}

// parseCSVFloat parses a float field from a CSV record.
func parseCSVFloat(record []string, colIdx map[string]int, fieldName string, target *float64) error {
	value := getCSVField(record, colIdx, fieldName)
	if value == "" {
		return nil
	}

	if _, err := fmt.Sscanf(value, "%f", target); err != nil {
		return fmt.Errorf("invalid %s value '%s': %w", fieldName, value, err)
	}
	return nil
}

// writeMultiServiceCSVReport writes purchase results to a CSV file.
func writeMultiServiceCSVReport(results []common.PurchaseResult, filepath string) error {
	if len(results) == 0 {
		return nil
	}

	file, err := os.Create(filepath) // #nosec G304 -- CLI tool: filepath is an operator-supplied output path argument
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("Warning: failed to close CSV file %s: %v", filepath, err)
		}
	}()

	writer := csv.NewWriter(file)
	defer writer.Flush()

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
	header := []string{
		"Service", "Region", "ResourceType", "Family", "Engine", "Deployment",
		"Instances", "CoveredInstances",
		"Count", "NormalizedUnits", "RecommendedCount",
		"Account", "AccountName", "Term", "PaymentOption",
		"UpfrontPayment", "RecurringMonthlyCost", "EstimatedSavings",
		"CommitmentID", "Success", "Error", "Timestamp",
		"ExistingCoverage", "ProjectedCoverage",
	}
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

	for _, r := range sorted { //nolint:gocritic // rangeValCopy: acceptable value copy
		rec := r.Recommendation
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}

		row := []string{
			string(rec.Service),
			rec.Region,
			rec.ResourceType,
			extractRDSFamily(rec),
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
	for _, r := range results { //nolint:gocritic // rangeValCopy: acceptable value copy
		totalCount += r.Recommendation.Count
		totalNU += float64(r.Recommendation.Count) * recommendations.RDSInstanceNUFromType(r.Recommendation.ResourceType)
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
	return []string{
		"TOTAL", "", "", "", "", "", // Service through Deployment
		"", "", // Instances, CoveredInstances
		fmt.Sprintf("%d", totalCount), nuCell, "", // Count, NormalizedUnits, RecommendedCount
		"", "", "", "", // Account, AccountName, Term, PaymentOption
		fmt.Sprintf("%.2f", totalUpfront), recurringCell, fmt.Sprintf("%.2f", totalSavings),
		"", "", "", "", // CommitmentID, Success, Error, Timestamp
		"", "", // ExistingCoverage, ProjectedCoverage
	}
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

// extractRDSFamily returns the RDS instance-family prefix (e.g.
// "db.r7g") for an RDS recommendation, empty for any service whose
// instance type doesn't follow the RDS three-part naming. Useful for
// grouping rows in the CSV by family-NU bucket so operators can see at
// a glance which recs belong to the same size-flex family.
func extractRDSFamily(rec common.Recommendation) string {
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
	if rec.Service != common.ServiceRDS && rec.Service != common.ServiceRelationalDB {
		return ""
	}
	nu := recommendations.RDSInstanceNUFromType(rec.ResourceType)
	if nu == 0 || rec.Count == 0 {
		return ""
	}
	return fmt.Sprintf("%g", float64(rec.Count)*nu)
}

// extractDeployment returns the RDS deployment-option string
// ("single-az" / "multi-az") for an RDS recommendation, empty for any
// service that doesn't carry a deployment dimension. Critical for RDS
// price verification: Multi-AZ list prices are roughly 2x Single-AZ, so
// operators need to see the deployment alongside the upfront figure to
// confirm a $X upfront row is for the deployment they expect.
//
// Both value and pointer Details are accepted to mirror extractEngine
// (parser path stores pointers; CSV-loader path constructs values).
func extractDeployment(rec common.Recommendation) string {
	switch details := rec.Details.(type) {
	case *common.DatabaseDetails:
		if details != nil {
			return details.AZConfig
		}
	case common.DatabaseDetails:
		return details.AZConfig
	}
	return ""
}

// extractEngine returns the engine / platform string for a recommendation's
// polymorphic Details: Engine for RDS / ElastiCache (DatabaseDetails,
// CacheDetails), Platform for EC2 (ComputeDetails), empty for SP and other
// commitment types that don't carry an engine field.
//
// Both value and pointer Details are accepted because the parser stores
// *DatabaseDetails / *CacheDetails / *ComputeDetails while the CSV-loader
// path constructs the value forms; the dispatch in generatePurchaseID does
// the same trick. Without the pointer cases the column silently blanks
// every row coming from the live parser path.
func extractEngine(rec common.Recommendation) string {
	switch details := rec.Details.(type) {
	case *common.DatabaseDetails:
		if details != nil {
			return details.Engine
		}
	case common.DatabaseDetails:
		return details.Engine
	case *common.CacheDetails:
		if details != nil {
			return details.Engine
		}
	case common.CacheDetails:
		return details.Engine
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
