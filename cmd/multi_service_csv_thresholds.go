package main

import (
	"fmt"
	"math"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// csvThresholdColumns carry the two figures --min-savings-pct and
// --max-break-even-months gate on, so a report replayed with --input-csv can
// be filtered the way a live run is. They are appended after csvDetailColumns,
// never reordered: readers look columns up by name.
//
// SavingsPercentage is a percent (25.5 means 25.5%), the unit of the flag and
// of common.Recommendation.SavingsPercentage. BreakEvenMonths is months.
// Both are written with the shortest exact decimal, so a write then a read
// yields the same float64.
const (
	csvSavingsPctColumn = "SavingsPercentage"
	csvBreakEvenColumn  = "BreakEvenMonths"
)

var csvThresholdColumns = []string{csvSavingsPctColumn, csvBreakEvenColumn}

// csvThresholdNeeds names the threshold figures a run reads from the CSV.
// A figure that is needed but absent fails the load: common.Recommendation
// holds both as plain float64, so an absent cell would read as 0, which the
// savings gate rejects and the break-even gate passes.
type csvThresholdNeeds struct {
	savingsPct bool
	breakEven  bool
}

func csvThresholdNeedsFor(cfg Config) csvThresholdNeeds {
	return csvThresholdNeeds{savingsPct: cfg.MinSavingsPct > 0, breakEven: cfg.MaxBreakEvenMonths > 0}
}

// thresholdCells renders rec for csvThresholdColumns, in that order. The
// break-even cell is blank when it is unknown or never pays back; "0" is a
// real value (no upfront payment).
func thresholdCells(rec common.Recommendation) []string {
	cells := []string{strconv.FormatFloat(rec.SavingsPercentage, 'f', -1, 64), ""}
	if months, known := common.BreakEven(rec); known && !math.IsInf(months, 0) {
		cells[1] = strconv.FormatFloat(months, 'f', -1, 64)
	}
	return cells
}

// requireThresholdColumns refuses a CSV whose header lacks a column a set flag
// needs (a report written before the columns existed).
func requireThresholdColumns(colIdx map[string]int, need csvThresholdNeeds) error {
	if need.savingsPct {
		if _, ok := colIdx[csvSavingsPctColumn]; !ok {
			return missingThresholdColumn(csvSavingsPctColumn, "--min-savings-pct")
		}
	}
	if need.breakEven {
		if _, ok := colIdx[csvBreakEvenColumn]; !ok {
			return missingThresholdColumn(csvBreakEvenColumn, "--max-break-even-months")
		}
	}
	return nil
}

func missingThresholdColumn(column, flag string) error {
	return fmt.Errorf("CSV has no %q column, so %s cannot be applied to it (reports written before #1819 lack it). Regenerate the report with this version, add the column, or drop %s", column, flag, flag)
}

// parseCSVThresholds reads the threshold cells of one row into rec. A cell is
// parsed whenever it holds a value, and a blank one is an error only when the
// matching flag needs it.
func parseCSVThresholds(record []string, colIdx map[string]int, need csvThresholdNeeds, rec *common.Recommendation) error {
	if err := parseThresholdCell(record, colIdx, csvSavingsPctColumn, need.savingsPct, "--min-savings-pct", &rec.SavingsPercentage); err != nil {
		return err
	}
	return parseThresholdCell(record, colIdx, csvBreakEvenColumn, need.breakEven, "--max-break-even-months", &rec.BreakEvenMonths)
}

func parseThresholdCell(record []string, colIdx map[string]int, column string, required bool, flag string, target *float64) error {
	if getCSVField(record, colIdx, column) == "" {
		if required {
			return fmt.Errorf("column %q is blank, so %s cannot be applied to this row", column, flag)
		}
		return nil
	}
	return parseCSVFloat(record, colIdx, column, target)
}
