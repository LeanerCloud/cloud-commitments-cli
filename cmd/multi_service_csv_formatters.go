package main

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

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
