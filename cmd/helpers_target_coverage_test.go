package main

import (
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyTargetCoverage covers the RI sizing branch of issue #338's
// --target-coverage flag, now under-buy semantics: n = floor(avg*target).
// Confirms: floor (not ceil) selection so coverage stays at-most target,
// drop-when-target-too-low (avg*target < 1), no-signal pass-through, and
// projected utilization (typically 100% since we under-buy) / coverage
// (tracks target%) outputs.
func TestApplyTargetCoverage_RI(t *testing.T) {
	mkRI := func(count int, avg, recUtil float64) common.Recommendation {
		return common.Recommendation{
			Service:                     common.ServiceEC2,
			Region:                      "us-east-1",
			ResourceType:                "t3.medium",
			Count:                       count,
			CommitmentType:              common.CommitmentReservedInstance,
			CommitmentCost:              1000,
			OnDemandCost:                2000,
			EstimatedSavings:            500,
			ExistingCoverageKnown:       true,
			AverageInstancesUsedPerHour: avg,
			RecommendedUtilization:      recUtil,
		}
	}

	tests := []struct {
		name           string
		rec            common.Recommendation
		target         float64
		wantDropped    bool
		wantCount      int
		wantProjUtil   float64 // 0 means "don't assert"
		wantProjCovGTE float64 // we assert coverage >= this (handles the float clamping)
	}{
		{
			// avg=8.5, target=95%, existing=0%.
			// gap=95. n = floor(8.5 * 95/100) = floor(8.075) = 8.
			// Projected util = 8.5/8 = 106.25 → clamped to 100.
			// Projected cov  = 0 + 8/8.5*100 = 94.117…%
			name:           "RI: target 95 buys 8 (floor of avg*0.95)",
			rec:            mkRI(10, 8.5, 0),
			target:         95,
			wantCount:      8,
			wantProjUtil:   100,
			wantProjCovGTE: 94.0,
		},
		{
			// avg=10, target=50%, existing=0%. n=floor(10*0.5)=5.
			// Projected cov = 5/10*100 = 50.0%.
			name:           "RI: target 50 buys half of avg demand",
			rec:            mkRI(10, 10, 0),
			target:         50,
			wantCount:      5,
			wantProjUtil:   100, // 10/5 clamped
			wantProjCovGTE: 50.0,
		},
		{
			// avg=0.4, target=50%, existing=0%.
			// n = floor(0.4 * 50/100) = floor(0.2) = 0 → DROPPED.
			// Tiny pools where avg×gap%<100 produce 0 RIs under the
			// coverage-anchored formula. --min-pool-size upstream is the
			// intended filter for these; the drop here is the fallback
			// when the upstream filter wasn't applied.
			name:        "RI: tiny avg below 1-RI threshold drops",
			rec:         mkRI(5, 0.4, 0),
			target:      50,
			wantDropped: true,
		},
		{
			// avg=0 (no signal) → passed through unchanged, counted in skip summary.
			// Projection metrics never set on the pass-through path.
			name:         "RI: no signal → passed through unmodified",
			rec:          mkRI(5, 0, 0),
			target:       80,
			wantCount:    5,
			wantProjUtil: 0, // never set in pass-through
		},
		{
			// avg=4, target=80%, existing=0%. n=floor(4*0.8)=3.
			// Projected util = 4/3 = 133% clamped to 100.
			// Projected cov  = 3/4*100 = 75.0%.
			name:           "RI: target 80 buys floor(avg*0.8)",
			rec:            mkRI(5, 4, 0),
			target:         80,
			wantCount:      3,
			wantProjUtil:   100,
			wantProjCovGTE: 75.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := []common.Recommendation{tt.rec}
			out := ApplyTargetCoverage(recs, tt.target, nil)
			if tt.wantDropped {
				if len(out) != 0 {
					t.Fatalf("expected drop; got %d recs", len(out))
				}
				return
			}
			if len(out) != 1 {
				t.Fatalf("expected 1 rec; got %d", len(out))
			}
			if out[0].Count != tt.wantCount {
				t.Errorf("Count: got %d, want %d", out[0].Count, tt.wantCount)
			}
			if tt.wantProjUtil > 0 {
				if math.Abs(out[0].ProjectedUtilization-tt.wantProjUtil) > 0.01 {
					t.Errorf("ProjectedUtilization: got %.4f, want %.4f",
						out[0].ProjectedUtilization, tt.wantProjUtil)
				}
			}
			// Zero means "don't assert" (matches the wantProjUtil convention)
			// since the pass-through path leaves ProjectedCoverage at zero.
			if tt.wantProjCovGTE > 0 {
				if out[0].ProjectedCoverage < tt.wantProjCovGTE-0.01 {
					t.Errorf("ProjectedCoverage: got %.4f, want >= %.4f",
						out[0].ProjectedCoverage, tt.wantProjCovGTE)
				}
			}
		})
	}
}

// TestApplyTargetCoverage_RI_CostScaling verifies RI cost-bearing fields
// scale by the sized-to-original count ratio. SavingsPercentage is invariant.
// The scaled values let downstream consumers (CSV writer, reporter, audit
// log) trust rec.CommitmentCost / rec.EstimatedSavings as the sized purchase
// rather than AWS's pre-sized proposal.
func TestApplyTargetCoverage_RI_CostScaling(t *testing.T) {
	rec := common.Recommendation{
		Service:                     common.ServiceEC2,
		Count:                       10,
		CommitmentType:              common.CommitmentReservedInstance,
		CommitmentCost:              1000,
		OnDemandCost:                2000,
		EstimatedSavings:            500,
		SavingsPercentage:           25,
		ExistingCoverageKnown:       true,
		AverageInstancesUsedPerHour: 8,
	}
	// target=80, existing=0, avg=8 → gap=80.
	// n = floor(8 * 80/100) = 6. Ratio = 6/10 = 0.6 (cost scaling still
	// uses rec.Count to convert AWS's quoted cost-for-rec.Count into
	// cost-for-nTarget).
	out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
	require.Len(t, out, 1)
	assert.Equal(t, 6, out[0].Count)
	assert.InDelta(t, 600.0, out[0].CommitmentCost, 0.001, "CommitmentCost scales by nTarget/rec.Count")
	assert.InDelta(t, 1200.0, out[0].OnDemandCost, 0.001, "OnDemandCost scales by nTarget/rec.Count")
	assert.InDelta(t, 300.0, out[0].EstimatedSavings, 0.001, "EstimatedSavings scales by nTarget/rec.Count")
	assert.Equal(t, 25.0, out[0].SavingsPercentage, "SavingsPercentage is invariant under count scaling")

	t.Run("RecurringMonthlyCost scales by ratio when populated", func(t *testing.T) {
		// AWS populated RecurringStandardMonthlyCost for partial/no-upfront
		// recs; sized purchase must scale this monthly fee by the same
		// nTarget/rec.Count ratio so total cost (upfront + monthly × term)
		// reflects what the user actually buys.
		monthly := 50.0
		recWithMonthly := rec
		recWithMonthly.RecurringMonthlyCost = &monthly
		out := ApplyTargetCoverage([]common.Recommendation{recWithMonthly}, 80, nil)
		require.Len(t, out, 1)
		require.NotNil(t, out[0].RecurringMonthlyCost, "scaled pointer should be non-nil")
		assert.InDelta(t, 30.0, *out[0].RecurringMonthlyCost, 0.001, "monthly cost scales by 6/10")
		// Original pointer untouched (we allocated a new one).
		assert.Equal(t, 50.0, monthly, "original RecurringMonthlyCost target should not be mutated")
	})

	t.Run("RecurringMonthlyCost stays nil when not populated", func(t *testing.T) {
		// AWS API didn't return RecurringStandardMonthlyCost (all-upfront,
		// or field missing). The sized rec should also have nil so
		// downstream renders "unknown" rather than zero.
		out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
		require.Len(t, out, 1)
		assert.Nil(t, out[0].RecurringMonthlyCost, "nil input → nil output")
	})
}
