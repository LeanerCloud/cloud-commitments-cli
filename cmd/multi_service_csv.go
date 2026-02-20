package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// determineCSVCoverage determines the coverage percentage to use for CSV mode
func determineCSVCoverage(cfg Config) float64 {
	// When using CSV input, default to 100% coverage (use exact numbers from CSV)
	// unless user explicitly provided a different coverage value
	if cfg.Coverage == 80.0 {
		// User didn't override the default, so use 100% for CSV mode
		return 100.0
	}
	return cfg.Coverage
}

// loadRecommendationsFromCSV reads and returns recommendations from a CSV file
func loadRecommendationsFromCSV(csvPath string) ([]common.Recommendation, error) {
	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("Warning: failed to close CSV file %s: %v", csvPath, err)
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
	recommendations, err := parseCSVRecords(reader, colIdx)
	if err != nil {
		return nil, err
	}

	return recommendations, nil
}

// buildColumnIndexMap creates a map from column names to indices
func buildColumnIndexMap(header []string) map[string]int {
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[col] = i
	}
	return colIdx
}

// parseCSVRecords reads and parses all CSV records
func parseCSVRecords(reader *csv.Reader, colIdx map[string]int) ([]common.Recommendation, error) {
	var recommendations []common.Recommendation

	for {
		record, err := reader.Read()
		if err != nil {
			break // End of file
		}

		rec, err := parseCSVRecord(record, colIdx)
		if err != nil {
			return nil, err
		}

		recommendations = append(recommendations, rec)
	}

	return recommendations, nil
}

// parseCSVRecord parses a single CSV record into a Recommendation
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

	return rec, nil
}

// getCSVField safely retrieves a string field from a CSV record
func getCSVField(record []string, colIdx map[string]int, fieldName string) string {
	if idx, ok := colIdx[fieldName]; ok && idx < len(record) {
		return record[idx]
	}
	return ""
}

// parseCSVInt parses an integer field from a CSV record
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

// parseCSVFloat parses a float field from a CSV record
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

// writeMultiServiceCSVReport writes purchase results to a CSV file
func writeMultiServiceCSVReport(results []common.PurchaseResult, filepath string) error {
	if len(results) == 0 {
		return nil
	}

	file, err := os.Create(filepath)
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

	// Write header
	header := []string{
		"Service", "Region", "ResourceType", "Count", "Account", "AccountName",
		"Term", "PaymentOption", "EstimatedSavings", "CommitmentID",
		"Success", "Error", "Timestamp",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write data rows
	for _, r := range results {
		rec := r.Recommendation
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}

		row := []string{
			string(rec.Service),
			rec.Region,
			rec.ResourceType,
			fmt.Sprintf("%d", rec.Count),
			rec.Account,
			rec.AccountName,
			rec.Term,
			rec.PaymentOption,
			fmt.Sprintf("%.2f", rec.EstimatedSavings),
			r.CommitmentID,
			fmt.Sprintf("%t", r.Success),
			errStr,
			r.Timestamp.Format(time.RFC3339),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	return nil
}
