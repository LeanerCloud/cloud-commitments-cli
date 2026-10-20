package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplySizing checks the routing helper picks the right sizer based
// on cfg.TargetCoverage being >0 vs ==0.
func TestApplySizing(t *testing.T) {
	ri := common.Recommendation{
		Service:                     common.ServiceEC2,
		Count:                       10,
		CommitmentType:              common.CommitmentReservedInstance,
		ExistingCoverageKnown:       true,
		AverageInstancesUsedPerHour: 8,
	}

	t.Run("TargetCoverage > 0 → ApplyTargetCoverage", func(t *testing.T) {
		cfg := Config{TargetCoverage: 80, Coverage: 100}
		out := applySizing([]common.Recommendation{ri}, cfg, cfg.Coverage, nil)
		require.Len(t, out, 1)
		// avg=8, target=80%, existing=0%. gap=80.
		// n = floor(8 * 80/100) = floor(6.4) = 6. ProjUtil = 8/6 = 133% → 100.
		assert.Equal(t, 6, out[0].Count)
		assert.Equal(t, 100.0, out[0].ProjectedUtilization)
	})

	t.Run("TargetCoverage == 0 → ApplyCoverage", func(t *testing.T) {
		cfg := Config{TargetCoverage: 0, Coverage: 50}
		out := applySizing([]common.Recommendation{ri}, cfg, cfg.Coverage, nil)
		require.Len(t, out, 1)
		// ApplyCoverage(50) on count=10 → 5. ProjectedUtilization NOT set
		// (zero) because we took the coverage branch.
		assert.Equal(t, 5, out[0].Count)
		assert.Equal(t, 0.0, out[0].ProjectedUtilization)
	})
}

// TestApplyTargetCoverage_RI_Target100 covers the target == 100 boundary.
// With the coverage-anchored formula, target=100 (existing=0) yields
// n = floor(avg * 100/100) = floor(avg) — operators get a buy sized to
// the pool's average concurrent demand, not AWS's rec.Count (which may
// be sized to peak/ROI-curated). Pools where avg<1 drop; --min-pool-size
// is the intended upstream filter.
func TestApplyTargetCoverage_RI_Target100(t *testing.T) {
	mkRI := func(count int, avg float64) common.Recommendation {
		return common.Recommendation{
			Service:                     common.ServiceEC2,
			Region:                      "us-east-1",
			ResourceType:                "t3.medium",
			Count:                       count,
			CommitmentType:              common.CommitmentReservedInstance,
			ExistingCoverageKnown:       true,
			AverageInstancesUsedPerHour: avg,
		}
	}

	tests := []struct {
		name        string
		rec         common.Recommendation
		wantDropped bool
		wantCount   int
	}{
		// avg=0.999 → floor(0.999)=0 → drop.
		{name: "target 100, avg=0.999 → drop (avg<1)", rec: mkRI(5, 0.999), wantDropped: true},
		// avg=1.0 → buy 1 (matches avg demand).
		{name: "target 100, avg=1 → buy 1", rec: mkRI(5, 1.0), wantCount: 1},
		// avg=8.7 → floor(8.7) = 8.
		{name: "target 100, avg=8.7 → buy floor(avg)=8", rec: mkRI(10, 8.7), wantCount: 8},
		// avg=10 → buy 10 (avg=10 means 10 concurrent instances on average).
		{name: "target 100, avg=10 → buy 10", rec: mkRI(10, 10.0), wantCount: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ApplyTargetCoverage([]common.Recommendation{tt.rec}, 100, nil)
			if tt.wantDropped {
				assert.Len(t, out, 0, "expected drop at target=100 for avg=%.3f", tt.rec.AverageInstancesUsedPerHour)
				return
			}
			require.Len(t, out, 1)
			assert.Equal(t, tt.wantCount, out[0].Count)
		})
	}
}

// TestApplyTargetCoverage_SP_NoSignalGuards covers the two SP no-signal
// branches: RecommendedUtilization <= 0 (already covered by other tests) and
// the new HourlyCommitment <= 0 guard (CE occasionally returns $0
// placeholder recs).
func TestApplyTargetCoverage_SP_NoSignalGuards(t *testing.T) {
	t.Run("HourlyCommitment=0 with positive RecommendedUtilization → pass through unscaled", func(t *testing.T) {
		rec := common.Recommendation{
			Service:                common.ServiceSavingsPlansCompute,
			CommitmentType:         common.CommitmentSavingsPlan,
			EstimatedSavings:       1500,
			RecommendedUtilization: 50,
			Details:                &common.SavingsPlanDetails{HourlyCommitment: 0},
		}
		out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
		require.Len(t, out, 1, "$0 SP rec should still be in output (pass-through)")
		// Pass-through — projection fields must NOT be set, savings unchanged.
		assert.Equal(t, 0.0, out[0].ProjectedUtilization, "ProjectedUtilization must NOT be set for $0-commitment pass-through")
		assert.Equal(t, 1500.0, out[0].EstimatedSavings, "EstimatedSavings unchanged on pass-through")
		assert.Equal(t, 0.0, out[0].Details.(*common.SavingsPlanDetails).HourlyCommitment, "HourlyCommitment unchanged")
	})

	t.Run("Details is wrong type → pass through unscaled, no projection metric set", func(t *testing.T) {
		// Defensive case: SP rec with non-SP Details (a parser bug). The
		// scaling can't proceed, and we MUST NOT set ProjectedUtilization
		// to target% because the underlying cost fields aren't scaled —
		// that would mislead the operator into thinking the rec was sized
		// to the target when in fact it's the original unscaled commitment.
		rec := common.Recommendation{
			Service:                common.ServiceSavingsPlansCompute,
			CommitmentType:         common.CommitmentSavingsPlan,
			EstimatedSavings:       1500,
			RecommendedUtilization: 50,
			Details:                common.ComputeDetails{Platform: "Linux/UNIX"}, // wrong type
		}
		out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
		require.Len(t, out, 1)
		assert.Equal(t, 0.0, out[0].ProjectedUtilization, "must NOT set projection when scaling failed")
		assert.Equal(t, 1500.0, out[0].EstimatedSavings, "EstimatedSavings must remain unscaled when scaling failed")
	})
}

// TestApplyTargetCoverage_DropTargetAlreadyMet verifies that when existing
// coverage already meets or exceeds the target, the recommendation is
// dropped and the drop is recorded in a non-nil DropSummary under the
// DropTargetAlreadyMet category. If the drops.Add call for that branch
// were removed, d.Total() would stay at 0 and the assertion below would fail.
func TestApplyTargetCoverage_DropTargetAlreadyMet(t *testing.T) {
	// ExistingCoveragePct=90 >= target=80: gapPct=80-90=-10 <= 0 -> drop.
	rec := common.Recommendation{
		Service:                     common.ServiceEC2,
		Region:                      "us-east-1",
		ResourceType:                "t3.medium",
		Count:                       5,
		CommitmentType:              common.CommitmentReservedInstance,
		ExistingCoverageKnown:       true,
		AverageInstancesUsedPerHour: 8.0,
		ExistingCoveragePct:         90.0,
	}

	d := common.NewDropSummary()
	out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, d)

	assert.Empty(t, out, "already-covered rec should be dropped")
	assert.Equal(t, 1, d.Total(), "drop summary should record 1 drop")
	assert.Contains(t, d.FormatOneLine(), common.DropTargetAlreadyMet,
		"drop summary should name the target-already-met category")
}

// TestApplyTargetCoverage_DropTargetSizedToZero verifies that when the
// floor(avg * gapPct / 100) formula produces 0, the recommendation is
// dropped and the drop is recorded in a non-nil DropSummary under the
// DropTargetSizedToZero category. If the drops.Add call for that branch
// were removed, d.Total() would stay at 0 and the assertion below would fail.
func TestApplyTargetCoverage_DropTargetSizedToZero(t *testing.T) {
	// avg=0.4, target=80%, existing=0%: floor(0.4 * 80/100) = floor(0.32) = 0 -> drop.
	rec := common.Recommendation{
		Service:                     common.ServiceEC2,
		Region:                      "us-east-1",
		ResourceType:                "t3.micro",
		Count:                       1,
		CommitmentType:              common.CommitmentReservedInstance,
		ExistingCoverageKnown:       true,
		AverageInstancesUsedPerHour: 0.4,
		ExistingCoveragePct:         0.0,
	}

	d := common.NewDropSummary()
	out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, d)

	assert.Empty(t, out, "floor-to-zero rec should be dropped")
	assert.Equal(t, 1, d.Total(), "drop summary should record 1 drop")
	assert.Contains(t, d.FormatOneLine(), common.DropTargetSizedToZero,
		"drop summary should name the target-sized-to-zero category")
}
