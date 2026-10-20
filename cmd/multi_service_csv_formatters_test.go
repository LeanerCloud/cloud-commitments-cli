package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

// TestFormatExistingCoverage locks the three-state rendering:
//   - ExistingCoverageKnown=false → "n/a" (CE has no data for this pool)
//   - ExistingCoverageKnown=true, Pct=0 → "0.0" (CE confirms zero coverage)
//   - ExistingCoverageKnown=true, Pct>0 → formatted with one decimal
//
// Critical for operators interpreting the column: a blank or zero cell
// previously meant either "CE was queried but returned 0%" or "CE
// returned nothing", with no way to tell which.
func TestFormatExistingCoverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  common.Recommendation
		want string
	}{
		{"unknown (CE no data)", common.Recommendation{}, "n/a"},
		{"known zero coverage", common.Recommendation{ExistingCoverageKnown: true}, "0.0"},
		{"known partial coverage", common.Recommendation{ExistingCoverageKnown: true, ExistingCoveragePct: 37.74}, "37.7"},
		{"known full coverage", common.Recommendation{ExistingCoverageKnown: true, ExistingCoveragePct: 100.0}, "100.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatExistingCoverage(tt.rec))
		})
	}
}

// TestFormatRecurringMonthlyOrBlank locks the nil-vs-zero distinction:
// nil pointer (AWS API didn't return RecurringStandardMonthlyCost)
// renders as blank, zero value (genuinely no monthly fee, e.g.
// all-upfront RIs) renders as "0.00". Operators need to tell "we don't
// know" apart from "definitely zero" to compute total cost correctly.
func TestFormatRecurringMonthlyOrBlank(t *testing.T) {
	t.Parallel()
	zero := 0.0
	twenty := 20.5
	tests := []struct {
		name string
		in   *float64
		want string
	}{
		{"nil → blank (unknown)", nil, ""},
		{"zero pointer → 0.00 (definitely zero)", &zero, "0.00"},
		{"non-zero → formatted", &twenty, "20.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRecurringMonthlyOrBlank(tt.in))
		})
	}
}

// TestExtractFamily covers the CSV Family column: the RDS family prefix and
// the Savings Plan instance family.
func TestExtractFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  common.Recommendation
		want string
	}{
		{"RDS db.r7g.large", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large"}, "db.r7g"},
		{"RDS db.t4g.medium", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.medium"}, "db.t4g"},
		{"RelationalDB alias", common.Recommendation{Service: common.ServiceRelationalDB, ResourceType: "db.m5.xlarge"}, "db.m5"},
		// Non-RDS services blank even when ResourceType looks RDS-shaped.
		{"EC2 ignored", common.Recommendation{Service: common.ServiceEC2, ResourceType: "m5.large"}, ""},
		{"ElastiCache ignored", common.Recommendation{Service: common.ServiceElastiCache, ResourceType: "cache.t3.micro"}, ""},
		// Malformed RDS type.
		{"RDS bare type", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g"}, ""},
		{"EC2 Instance SP", common.Recommendation{Service: common.ServiceSavingsPlansEC2Instance, Details: &common.SavingsPlanDetails{InstanceFamily: "m5"}}, "m5"},
		{"Compute SP has no family", common.Recommendation{Service: common.ServiceSavingsPlansCompute, Details: &common.SavingsPlanDetails{}}, ""},
		{"nil SP details", common.Recommendation{Service: common.ServiceSavingsPlansCompute, Details: (*common.SavingsPlanDetails)(nil)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractFamily(tt.rec))
		})
	}
}

// TestFormatNormalizedUnitsOrBlank confirms NU values land for RDS rows
// with known sizes and stay blank for non-RDS / zero-count / unknown-size
// inputs, matching the "0/empty = unknown" convention used elsewhere.
func TestFormatNormalizedUnitsOrBlank(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  common.Recommendation
		want string
	}{
		// 15 × db.r7g.large = 15 × 4 NU = 60 NU
		{"r7g.large × 15", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large", Count: 15}, "60"},
		// 3 × db.t4g.medium = 3 × 2 NU = 6 NU
		{"t4g.medium × 3", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.medium", Count: 3}, "6"},
		// Fractional NU survives via %g (db.t4g.micro = 0.5 NU)
		{"t4g.micro × 3", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.t4g.micro", Count: 3}, "1.5"},
		// Non-RDS service → blank
		{"EC2 row blank", common.Recommendation{Service: common.ServiceEC2, ResourceType: "m5.large", Count: 5}, ""},
		// Zero count → blank
		{"zero count blank", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.large", Count: 0}, ""},
		// Unknown size → blank
		{"unknown size blank", common.Recommendation{Service: common.ServiceRDS, ResourceType: "db.r7g.bogus", Count: 5}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatNormalizedUnitsOrBlank(tt.rec))
		})
	}
}

// TestExtractDeployment covers the deployment-extraction helper used by
// the RDS row in the CSV. Single-AZ / Multi-AZ is critical context for
// pricing verification (Multi-AZ list price is ~2x Single-AZ) so the
// column should land for every RDS rec. DatabaseDetails is always a
// pointer (every producer constructs it that way); no value-typed case
// is needed.
func TestExtractDeployment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  common.Recommendation
		want string
	}{
		{"*DatabaseDetails Single-AZ", common.Recommendation{Details: &common.DatabaseDetails{AZConfig: "single-az"}}, "single-az"},
		{"*DatabaseDetails Multi-AZ", common.Recommendation{Details: &common.DatabaseDetails{AZConfig: "multi-az"}}, "multi-az"},
		{"DatabaseDetails empty AZConfig", common.Recommendation{Details: &common.DatabaseDetails{Engine: "mysql"}}, ""},
		// Non-RDS Details → blank (column is RDS-only data).
		{"CacheDetails -> empty", common.Recommendation{Details: &common.CacheDetails{Engine: "redis"}}, ""},
		{"ComputeDetails -> empty", common.Recommendation{Details: &common.ComputeDetails{Platform: "Linux/UNIX"}}, ""},
		{"nil Details -> empty", common.Recommendation{}, ""},
		{"nil *DatabaseDetails -> empty", common.Recommendation{Details: (*common.DatabaseDetails)(nil)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractDeployment(tt.rec))
		})
	}
}

// TestExtractEngine covers the four cases the helper dispatches on:
// DatabaseDetails (RDS engine), CacheDetails (ElastiCache engine),
// ComputeDetails (EC2 platform), and unset/other Details (blank).
// DatabaseDetails/CacheDetails are always pointers (every producer
// constructs them that way); ComputeDetails still accepts a value because
// the Azure compute client and the GCP compute-engine client both construct
// it that way.
func TestExtractEngine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  common.Recommendation
		want string
	}{
		{"*DatabaseDetails -> Engine", common.Recommendation{Details: &common.DatabaseDetails{Engine: "aurora-postgresql"}}, "aurora-postgresql"},
		{"*CacheDetails -> Engine", common.Recommendation{Details: &common.CacheDetails{Engine: "redis"}}, "redis"},
		{"*ComputeDetails -> Platform", common.Recommendation{Details: &common.ComputeDetails{Platform: "Linux/UNIX"}}, "Linux/UNIX"},
		// The Azure compute client and GCP's compute-engine client both
		// still construct ComputeDetails as a value; keep coverage for
		// that form.
		{"ComputeDetails (value) -> Platform", common.Recommendation{Details: common.ComputeDetails{Platform: "Windows"}}, "Windows"},
		// Fallbacks.
		{"nil Details -> empty", common.Recommendation{}, ""},
		{"SavingsPlanDetails -> empty", common.Recommendation{Details: &common.SavingsPlanDetails{HourlyCommitment: 1.0}}, ""},
		{"nil *DatabaseDetails -> empty", common.Recommendation{Details: (*common.DatabaseDetails)(nil)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractEngine(tt.rec))
		})
	}
}

// TestFormatCurrencyOrBlank locks the blank-when-zero behavior for the
// UpfrontPayment column. Non-zero renders with two decimals; zero renders
// as an empty cell so users can distinguish "no upfront due" from "actual
// $0 upfront", consistent with the rest of the optional CSV columns.
func TestFormatCurrencyOrBlank(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"non-zero renders with two decimals", 1234.56, "1234.56"},
		{"integer value gets .00", 700, "700.00"},
		{"zero blanks the cell", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatCurrencyOrBlank(tt.in))
		})
	}
}
