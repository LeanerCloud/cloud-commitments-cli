package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func riRec(count int, avg, existing, cov, util float64) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceEC2, CommitmentType: common.CommitmentReservedInstance, Region: "us-east-1", ResourceType: "m5.large",
		Count: count, AverageInstancesUsedPerHour: avg, ExistingCoveragePct: existing,
		ExistingCoverageKnown: true, ProjectedCoverage: cov, ProjectedUtilization: util,
		EstimatedSavings: 1000, CommitmentCost: 2000, OnDemandCost: 3000,
	}
}

func TestRederiveProjections(t *testing.T) {
	rds := riRec(10, 125, 10, 55, 100)
	rds.Service = common.ServiceRDS
	noKnown := riRec(10, 125, 0, 80, 100)
	noKnown.ExistingCoverageKnown = false
	sp := riRec(10, 125, 0, 0, 90)
	sp.Service = common.ServiceSavingsPlansCompute

	tests := []struct {
		name      string
		rec       common.Recommendation
		cov, util float64
	}{
		{"issue scenario: cap 100 -> 10", riRec(10, 125, 0, 80, 100), 8.0, 100},
		{"unclamped util, clamped cov (hand-computed)", riRec(10, 4, 0, 50, 100), 100, 40},
		{"unclamped cov with existing (hand-computed)", riRec(10, 125, 10, 80, 100), 18.0, 100},
		{"zero projections stay zero", riRec(10, 125, 10, 0, 0), 0, 0},
		{"only util computed stays so", riRec(10, 4, 0, 0, 100), 0, 40},
		{"avg zero untouched", riRec(10, 0, 0, 80, 100), 80, 100},
		{"count zero untouched", riRec(0, 125, 0, 80, 100), 80, 100},
		{"existing coverage unknown: cov untouched", noKnown, 80, 100},
		{"RDS: cov untouched (family aggregate), util refreshed", rds, 55, 100},
		{"SP untouched", sp, 0, 90},
		{"NaN avg untouched", riRec(10, math.NaN(), 0, 80, 100), 80, 100},
		{"+Inf avg untouched", riRec(10, math.Inf(1), 0, 80, 100), 80, 100},
		{"negative avg untouched", riRec(10, -3, 0, 80, 100), 80, 100},
		{"negative count untouched", riRec(-5, 125, 0, 80, 100), 80, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rederiveProjections(tt.rec)
			assert.InDelta(t, tt.cov, got.ProjectedCoverage, 1e-9)
			assert.InDelta(t, tt.util, got.ProjectedUtilization, 1e-9)
		})
	}
}

func TestSyncDataWarehouseNodes(t *testing.T) {
	ptr := &common.DataWarehouseDetails{NodeType: "ra3.xlplus", NumberOfNodes: 10, ClusterType: "multi-node"}
	rec := common.Recommendation{Service: common.ServiceRedshift, Count: 3, Details: ptr}

	got := syncDataWarehouseNodes(rec)
	assert.Equal(t, 3, got.Details.(*common.DataWarehouseDetails).NumberOfNodes)
	assert.Equal(t, 10, ptr.NumberOfNodes, "the shared pointer must not be written through")

	rec.Count = 1
	assert.Equal(t, "single-node", syncDataWarehouseNodes(rec).Details.(*common.DataWarehouseDetails).ClusterType)

	val := common.Recommendation{Service: common.ServiceRedshift, Count: 2, Details: common.DataWarehouseDetails{NumberOfNodes: 1, ClusterType: "single-node"}}
	d := syncDataWarehouseNodes(val).Details.(common.DataWarehouseDetails)
	assert.Equal(t, 2, d.NumberOfNodes)
	assert.Equal(t, "multi-node", d.ClusterType)

	azure := common.Recommendation{Service: common.ServiceDataWarehouse, Count: 4, Details: common.DataWarehouseDetails{ClusterType: "dedicated-sql-pool"}}
	assert.Equal(t, azure, syncDataWarehouseNodes(azure), "non-Redshift recs are never touched")
	custom := common.Recommendation{Service: common.ServiceDataWarehouse, Count: 4, Details: common.DataWarehouseDetails{NumberOfNodes: 9, ClusterType: "single-node"}}
	assert.Equal(t, custom, syncDataWarehouseNodes(custom), "non-Redshift NumberOfNodes stays 9")

	same := common.Recommendation{Count: 10, Details: ptr}
	assert.Same(t, ptr, syncDataWarehouseNodes(same).Details, "equal values keep pointer identity")
	var nilPtr *common.DataWarehouseDetails
	assert.NotPanics(t, func() { syncDataWarehouseNodes(common.Recommendation{Count: 2, Details: nilPtr}) })
	zero := common.Recommendation{Count: 0, Details: ptr}
	assert.Same(t, ptr, syncDataWarehouseNodes(zero).Details)
	other := common.Recommendation{Count: 2, Details: &common.DatabaseDetails{Engine: "mysql"}}
	assert.Equal(t, other, syncDataWarehouseNodes(other))
}

// The coverage mirror must match what the shared sizer writes.
func TestRefreshCountLinearMatchesSizerOutput(t *testing.T) {
	in := make([]common.Recommendation, 0, 4)
	for _, p := range []struct{ avg, existing float64 }{{125, 0}, {40.5, 30}, {400, 79.5}, {33.3, 10}} {
		in = append(in, riRec(50, p.avg, p.existing, 0, 0))
	}
	out := ApplyTargetCoverage(in, 80, nil)
	require.Len(t, out, len(in))
	for i := range out {
		require.NotZero(t, out[i].ProjectedCoverage)
		assert.Equal(t, out[i], refreshCountLinear(out[i]), "pool %d drifted", i)
	}
}

// Issue #1845: the cap changes Count, so the projections must follow.
func TestApplyInstanceLimitRederivesProjections(t *testing.T) {
	capped := riRec(100, 125, 0, 80, 100)
	untouched := riRec(5, 10, 0, 40, 100)
	untouched.ResourceType = "c5.large"
	zero := riRec(100, 125, 0, 0, 0)
	zero.ResourceType = "r5.large"
	got := ApplyInstanceLimit([]common.Recommendation{untouched, capped, zero}, 10)
	require.Len(t, got, 2)
	assert.Equal(t, untouched, got[0], "a row the cap does not truncate is byte-identical")
	assert.Equal(t, 5, got[1].Count)
	// remaining budget 5 after the first row: cov = 5/125*100 = 4.0
	assert.InDelta(t, 4.0, got[1].ProjectedCoverage, 1e-9)

	got = ApplyInstanceLimit([]common.Recommendation{capped}, 10)
	assert.InDelta(t, 8.0, got[0].ProjectedCoverage, 1e-9)
	assert.InDelta(t, 100, got[0].ProjectedUtilization, 1e-9)

	got = ApplyInstanceLimit([]common.Recommendation{zero}, 10)
	assert.Zero(t, got[0].ProjectedCoverage)
	assert.Zero(t, got[0].ProjectedUtilization)

	noSignal := riRec(100, 0, 0, 80, 100)
	assert.Equal(t, 80.0, ApplyInstanceLimit([]common.Recommendation{noSignal}, 10)[0].ProjectedCoverage)
}

func TestApplyInstanceLimitSyncsRedshiftNodes(t *testing.T) {
	src := &common.DataWarehouseDetails{NodeType: "ra3.xlplus", NumberOfNodes: 10, ClusterType: "multi-node"}
	rec := common.Recommendation{Service: common.ServiceRedshift, Count: 10, Details: src}
	got := ApplyInstanceLimit([]common.Recommendation{rec}, 3)
	assert.Equal(t, 3, got[0].Details.(*common.DataWarehouseDetails).NumberOfNodes)
	assert.Equal(t, 10, src.NumberOfNodes)
}

func TestApplyCountOverrideRederivesProjections(t *testing.T) {
	got := ApplyCountOverride([]common.Recommendation{riRec(100, 125, 0, 80, 100)}, 200)
	assert.InDelta(t, 100, got[0].ProjectedCoverage, 1e-9) // 200/125*100 clamps
	assert.InDelta(t, 62.5, got[0].ProjectedUtilization, 1e-9)

	rds := riRec(100, 125, 0, 55, 100)
	rds.Service = common.ServiceRDS
	got = ApplyCountOverride([]common.Recommendation{rds}, 10)
	assert.Equal(t, 55.0, got[0].ProjectedCoverage, "RDS family coverage survives")
}

func TestAdjustRecsForDuplicatesRederivesProjections(t *testing.T) {
	ctx := context.Background()
	client := &MockServiceClient{}
	client.On("GetExistingCommitments", ctx).Return([]common.Commitment{
		{ResourceType: "m5.large", Region: "us-east-1", Count: 60, State: "active", StartDate: time.Now().Add(-time.Hour)},
	}, nil)
	got, err := adjustRecsForDuplicates(ctx, []common.Recommendation{riRec(100, 125, 0, 80, 100)}, client)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 40, got[0].Count)
	assert.InDelta(t, 32.0, got[0].ProjectedCoverage, 1e-9) // 40/125*100
}

// Fixture-based: sizer output flows through the run-wide cap unchanged in shape.
func TestTargetCoverageThenGlobalCapAgree(t *testing.T) {
	sized := ApplyTargetCoverage([]common.Recommendation{riRec(300, 125, 0, 0, 0)}, 80, nil)
	require.Len(t, sized, 1)
	require.Equal(t, 100, sized[0].Count)
	require.InDelta(t, 80.0, sized[0].ProjectedCoverage, 1e-9)
	got := applyGlobalInstanceLimit(sized, Config{MaxInstances: 10}, rankBySavingsPercentage, common.NewDropSummary())
	require.Len(t, got, 1)
	assert.Equal(t, 10, got[0].Count)
	assert.InDelta(t, 8.0, got[0].ProjectedCoverage, 1e-9)
}

func TestTargetCoverageSyncsRedshiftNodes(t *testing.T) {
	src := &common.DataWarehouseDetails{NodeType: "ra3.xlplus", NumberOfNodes: 10, ClusterType: "multi-node"}
	rec := riRec(10, 5, 0, 0, 0)
	rec.Service = common.ServiceRedshift
	rec.Details = src
	got := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil)
	require.Len(t, got, 1)
	assert.Equal(t, 4, got[0].Count)
	assert.Equal(t, 4, got[0].Details.(*common.DataWarehouseDetails).NumberOfNodes)
	assert.Equal(t, 10, src.NumberOfNodes)
}

func rdsFamilyRec(count int, avg, cov, util float64) common.Recommendation {
	r := riRec(count, avg, 0, cov, util)
	r.Service = common.ServiceRDS
	return r
}

func TestRDSCoverageSurvivesCapAndDedupe(t *testing.T) {
	got := ApplyInstanceLimit([]common.Recommendation{rdsFamilyRec(100, 40, 55, 40)}, 10)
	assert.Equal(t, 55.0, got[0].ProjectedCoverage, "family coverage untouched by the cap")
	assert.InDelta(t, 100, got[0].ProjectedUtilization, 1e-9) // 40/10 clamps

	ctx := context.Background()
	client := &MockServiceClient{}
	client.On("GetExistingCommitments", ctx).Return([]common.Commitment{
		{ResourceType: "m5.large", Region: "us-east-1", Count: 60, State: "active", StartDate: time.Now().Add(-time.Hour)},
	}, nil)
	dd := checkDuplicates(ctx, []common.Recommendation{rdsFamilyRec(100, 20, 55, 20)}, client, false, common.NewDropSummary())
	require.Len(t, dd, 1)
	require.Equal(t, 40, dd[0].Count)
	assert.Equal(t, 55.0, dd[0].ProjectedCoverage)
	assert.InDelta(t, 50, dd[0].ProjectedUtilization, 1e-9) // 20/40
}

func TestCheckDuplicatesRederivesProjections(t *testing.T) {
	ctx := context.Background()
	client := &MockServiceClient{}
	client.On("GetExistingCommitments", ctx).Return([]common.Commitment{
		{ResourceType: "m5.large", Region: "us-east-1", Count: 60, State: "active", StartDate: time.Now().Add(-time.Hour)},
	}, nil)
	got := checkDuplicates(ctx, []common.Recommendation{riRec(100, 125, 0, 80, 100)}, client, false, common.NewDropSummary())
	require.Len(t, got, 1)
	require.Equal(t, 40, got[0].Count)
	assert.InDelta(t, 32.0, got[0].ProjectedCoverage, 1e-9)
}

func TestApplyCoverageSyncsRedshiftNodes(t *testing.T) {
	src := &common.DataWarehouseDetails{NodeType: "ra3.xlplus", NumberOfNodes: 10, ClusterType: "multi-node"}
	rec := riRec(10, 5, 0, 0, 0)
	rec.Service = common.ServiceRedshift
	rec.Details = src
	for name, got := range map[string][]common.Recommendation{
		"ApplyCoverage": ApplyCoverage([]common.Recommendation{rec}, 40),
		"applyCoverage": applyCoverage([]common.Recommendation{rec}, 40, common.NewDropSummary()),
	} {
		require.Len(t, got, 1, name)
		assert.Equal(t, 4, got[0].Count, name)
		assert.Equal(t, 4, got[0].Details.(*common.DataWarehouseDetails).NumberOfNodes, name)
	}
	assert.Equal(t, 10, src.NumberOfNodes)
	assert.Nil(t, mapRecs(nil, syncDataWarehouseNodes), "nil stays nil")
}
