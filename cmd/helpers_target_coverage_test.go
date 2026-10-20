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

// TestApplyTargetCoverage_RI_ExistingCoverage covers the under-buy formula's
// existing-commitment branch: gap = (target - existing_cov)/100, then
// n_target = floor(avg * gap). Matches the worked example from the #338 design
// thread (20 instances, 10 existing RIs at 50% coverage, target 80% → buy 6).
func TestApplyTargetCoverage_RI_ExistingCoverage(t *testing.T) {
	mkRI := func(count int, avg, existingCov float64) common.Recommendation {
		return common.Recommendation{
			Service:                     common.ServiceEC2,
			Region:                      "us-east-1",
			ResourceType:                "t3.medium",
			Count:                       count,
			RecommendedCount:            count,
			CommitmentType:              common.CommitmentReservedInstance,
			CommitmentCost:              1000,
			OnDemandCost:                2000,
			EstimatedSavings:            500,
			ExistingCoverageKnown:       true,
			AverageInstancesUsedPerHour: avg,
			ExistingCoveragePct:         existingCov,
		}
	}

	tests := []struct {
		name         string
		rec          common.Recommendation
		target       float64
		wantDropped  bool
		wantCount    int
		wantTotalCov float64 // ProjectedCoverage = existing + new contribution
	}{
		{
			// User's worked example: avg=20, existing=50%, target=80%.
			// gap=30. n=ceil(20*0.30)=6. Total cov = 50 + 6/20*100 = 80.
			name:         "User example: 50% existing, 80% target on avg=20 → buy 6",
			rec:          mkRI(10, 20, 50),
			target:       80,
			wantCount:    6,
			wantTotalCov: 80,
		},
		{
			// existing=0%, target=70%, avg=10. gap=70. n=ceil(7)=7.
			name:         "Zero existing: ceil(avg*target/100)",
			rec:          mkRI(10, 10, 0),
			target:       70,
			wantCount:    7,
			wantTotalCov: 70,
		},
		{
			// existing=80% meets target=80% → drop.
			name:        "Existing meets target exactly: drop",
			rec:         mkRI(10, 10, 80),
			target:      80,
			wantDropped: true,
		},
		{
			// existing=95% exceeds target=80% → drop.
			name:        "Existing exceeds target: drop",
			rec:         mkRI(10, 10, 95),
			target:      80,
			wantDropped: true,
		},
		{
			// avg=2, existing=70%, target=80%. gap=10.
			// n = floor(2 * 10/100) = floor(0.2) = 0 → DROPPED.
			// Small pool + thin gap: no integer buy can approximate the
			// target. --min-pool-size upstream is the intended filter.
			name:        "Small gap on tiny avg drops",
			rec:         mkRI(5, 2, 70),
			target:      80,
			wantDropped: true,
		},
		{
			// avg=10, existing=60%, target=70%. gap=10.
			// n = floor(10 * 10/100) = 1. Total cov = 60 + 1/10*100 = 70.
			// Coverage-anchored: 1 RI exactly closes the 10-point gap
			// because avg=10 and each RI is worth 10% of avg demand.
			name:         "Small top-up: 1 RI exactly closes 10-pt gap on avg=10",
			rec:          mkRI(10, 10, 60),
			target:       70,
			wantCount:    1,
			wantTotalCov: 70,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ApplyTargetCoverage([]common.Recommendation{tt.rec}, tt.target, nil)
			if tt.wantDropped {
				assert.Len(t, out, 0, "expected drop")
				return
			}
			require.Len(t, out, 1)
			assert.Equal(t, tt.wantCount, out[0].Count, "Count")
			assert.InDelta(t, tt.wantTotalCov, out[0].ProjectedCoverage, 0.01, "ProjectedCoverage is TOTAL (existing + new)")
		})
	}
}

// TestApplyTargetCoverage_SP covers the SP sizing branch under under-buy
// semantics: HourlyCommitment and EstimatedSavings scale by targetPct/100
// regardless of AWS's projected utilization. CommitmentCost / OnDemandCost /
// SavingsPercentage must NOT change.
func TestApplyTargetCoverage_SP(t *testing.T) {
	mkSP := func(recUtil float64) common.Recommendation {
		return common.Recommendation{
			Service:                common.ServiceSavingsPlansCompute,
			CommitmentType:         common.CommitmentSavingsPlan,
			CommitmentCost:         1000,
			OnDemandCost:           5000,
			EstimatedSavings:       1500,
			SavingsPercentage:      30,
			RecommendedUtilization: recUtil,
			Details:                &common.SavingsPlanDetails{HourlyCommitment: 2.0},
		}
	}

	t.Run("AWS above target — still scales by target (under-buy)", func(t *testing.T) {
		// RecUtil=95, target=80. Even though AWS projects above target, the
		// flag's intent is "leave 20% headroom", so the commitment shrinks
		// to 80% of AWS rec. All cost-bearing fields scale by 0.8.
		// Projected util = 95/0.80 = 118.75 clamped to 100.
		out := ApplyTargetCoverage([]common.Recommendation{mkSP(95)}, 80, nil)
		require.Len(t, out, 1)
		assert.InDelta(t, 1.6, out[0].Details.(*common.SavingsPlanDetails).HourlyCommitment, 0.001)
		assert.InDelta(t, 800.0, out[0].CommitmentCost, 0.001, "CommitmentCost scales by target/100")
		assert.InDelta(t, 4000.0, out[0].OnDemandCost, 0.001, "OnDemandCost scales by target/100")
		assert.InDelta(t, 1200.0, out[0].EstimatedSavings, 0.001)
		assert.Equal(t, 30.0, out[0].SavingsPercentage, "SavingsPercentage is invariant")
		assert.InDelta(t, 100.0, out[0].ProjectedUtilization, 0.001, "RecUtil/ratio = 95/0.80 = 118.75 clamps to 100")
		assert.Equal(t, 0.0, out[0].ProjectedCoverage, "SPs intentionally leave ProjectedCoverage at zero")
	})

	t.Run("AWS below target — scale down by target (under-buy)", func(t *testing.T) {
		// RecUtil=50, target=80. All cost-bearing fields shrink to 80%.
		// Projected util = 50/0.80 = 62.5 (no clamp needed).
		out := ApplyTargetCoverage([]common.Recommendation{mkSP(50)}, 80, nil)
		require.Len(t, out, 1)
		details := out[0].Details.(*common.SavingsPlanDetails)
		assert.InDelta(t, 1.6, details.HourlyCommitment, 0.001)
		assert.InDelta(t, 800.0, out[0].CommitmentCost, 0.001, "CommitmentCost scales by target/100")
		assert.InDelta(t, 4000.0, out[0].OnDemandCost, 0.001, "OnDemandCost scales by target/100")
		assert.InDelta(t, 1200.0, out[0].EstimatedSavings, 0.001)
		assert.Equal(t, 30.0, out[0].SavingsPercentage, "SavingsPercentage is invariant")
		assert.InDelta(t, 62.5, out[0].ProjectedUtilization, 0.001, "RecUtil/ratio = 50/0.80 = 62.5")
		assert.Equal(t, 0.0, out[0].ProjectedCoverage)
	})

	t.Run("no signal → passed through unchanged", func(t *testing.T) {
		out := ApplyTargetCoverage([]common.Recommendation{mkSP(0)}, 80, nil)
		require.Len(t, out, 1)
		// Original recommendation values intact.
		assert.Equal(t, 2.0, out[0].Details.(*common.SavingsPlanDetails).HourlyCommitment)
		assert.Equal(t, 1500.0, out[0].EstimatedSavings)
		assert.Equal(t, 0.0, out[0].ProjectedUtilization)
	})
}
