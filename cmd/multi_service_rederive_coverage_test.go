package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rederiveRDSRec(count int) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceRDS, CommitmentType: common.CommitmentReservedInstance,
		Region: "us-east-1", ResourceType: "db.r7g.large", Count: count,
		CommitmentCost: float64(count) * 100, EstimatedSavings: 100,
		AverageInstancesUsedPerHour: 3,
		Details:                     &common.DatabaseDetails{Engine: "aurora-mysql", AZConfig: "single-az"},
	}
}

var rederiveCoverage = recommendations.PoolCoverageMap{
	"us-east-1:db.r7g.large:auroramysql:singleaz": {Pct: 20, AvgInstancesPerHour: 40},
}

// sizedRederiveRec returns one family-NU sized RDS rec (the --target-coverage
// output) and its ProjectedCoverage.
func sizedRederiveRec(t *testing.T) common.Recommendation {
	t.Helper()
	sized, _, _ := recommendations.ApplyFamilyNUSizingRDS([]common.Recommendation{rederiveRDSRec(30)}, rederiveCoverage, 90)
	require.Len(t, sized, 1)
	require.Greater(t, sized[0].Count, 5)
	return sized[0]
}

func TestScoreLimitAndDisplay_TargetCoverageRederivesRDSCoverageAfterCap(t *testing.T) {
	sized := sizedRederiveRec(t)
	uncapped := sized.ProjectedCoverage

	got := scoreLimitAndDisplay([]common.Recommendation{sized}, Config{TargetCoverage: 90, MaxInstances: 5}, nil, rederiveCoverage)

	require.Len(t, got.Passed, 1)
	assert.Equal(t, 5, got.Passed[0].Count)
	// existing 20% + 5 instances of 40 = 12.5%
	assert.InDelta(t, 32.5, got.Passed[0].ProjectedCoverage, 1e-6)
	assert.Less(t, got.Passed[0].ProjectedCoverage, uncapped)
}

func TestScoreLimitAndDisplay_LegacyCoverageLeavesProjectedCoverage(t *testing.T) {
	rec := rederiveRDSRec(30)
	rec.ExistingCoverageKnown, rec.ExistingCoveragePct, rec.ProjectedCoverage = true, 20, 77

	got := scoreLimitAndDisplay([]common.Recommendation{rec}, Config{Coverage: 80, MaxInstances: 5}, nil, rederiveCoverage)

	require.Len(t, got.Passed, 1)
	assert.Equal(t, 77.0, got.Passed[0].ProjectedCoverage)
}

func TestScoreLimitAndDisplay_CSVPathNilCoverageUnchanged(t *testing.T) {
	sized := sizedRederiveRec(t)

	got := scoreLimitAndDisplay([]common.Recommendation{sized}, Config{TargetCoverage: 90}, nil, nil)

	require.Len(t, got.Passed, 1)
	assert.Equal(t, sized.ProjectedCoverage, got.Passed[0].ProjectedCoverage)
}
