package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestRequireRankingSignal_IgnoresZeroSavingsSavingsPlan(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.micro", Count: 5, EstimatedSavings: 100},
		{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5, EstimatedSavings: 50},
		{Service: common.ServiceCompute, Region: "us-east-1", ResourceType: "compute", Count: 1,
			CommitmentType: common.CommitmentSavingsPlan, EstimatedSavings: 0},
	}
	require.NoError(t, requireRankingSignal(recs, Config{MaxInstances: 6}))

	recs[1].EstimatedSavings = 0
	require.Error(t, requireRankingSignal(recs, Config{MaxInstances: 6}))
}
