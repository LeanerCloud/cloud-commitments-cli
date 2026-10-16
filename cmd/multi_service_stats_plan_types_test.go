package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func spRec(service common.ServiceType, planType string, savings float64) common.Recommendation {
	rec := common.Recommendation{Service: service, EstimatedSavings: savings}
	if planType != "" {
		rec.Details = &common.SavingsPlanDetails{PlanType: planType}
	}
	return rec
}

func TestSPSummaryCountsEveryPlanType(t *testing.T) {
	tests := []struct {
		name string
		rec  common.Recommendation
		want SPTypeBreakdown
	}{
		{"parser compute", spRec(common.ServiceSavingsPlansCompute, "Compute", 10), SPTypeBreakdown{ComputeCount: 1, ComputeSavings: 10}},
		{"parser ec2instance", spRec(common.ServiceSavingsPlansEC2Instance, "EC2Instance", 10), SPTypeBreakdown{EC2InstanceCount: 1, EC2InstanceSavings: 10}},
		{"parser sagemaker", spRec(common.ServiceSavingsPlansSageMaker, "SageMaker", 10), SPTypeBreakdown{SageMakerCount: 1, SageMakerSavings: 10}},
		{"parser database", spRec(common.ServiceSavingsPlansDatabase, "Database", 10), SPTypeBreakdown{DatabaseCount: 1, DatabaseSavings: 10}},
		{"umbrella slug compute", spRec(common.ServiceSavingsPlansAll, "Compute", 10), SPTypeBreakdown{ComputeCount: 1, ComputeSavings: 10}},
		{"umbrella slug sagemaker", spRec(common.ServiceSavingsPlansAll, "SageMaker", 10), SPTypeBreakdown{SageMakerCount: 1, SageMakerSavings: 10}},
		{"umbrella slug database", spRec(common.ServiceSavingsPlansAll, "Database", 10), SPTypeBreakdown{DatabaseCount: 1, DatabaseSavings: 10}},
		{"csv lowercase plan type", spRec(common.ServiceSavingsPlansAll, "sagemaker", 10), SPTypeBreakdown{SageMakerCount: 1, SageMakerSavings: 10}},
		{"canonical slug without details", spRec(common.ServiceSavingsPlansDatabase, "", 10), SPTypeBreakdown{DatabaseCount: 1, DatabaseSavings: 10}},
		{"unknown plan type", spRec(common.ServiceSavingsPlansAll, "Quantum", 10), SPTypeBreakdown{OtherCount: 1, OtherSavings: 10}},
		{"umbrella without details", spRec(common.ServiceSavingsPlansAll, "", 10), SPTypeBreakdown{OtherCount: 1, OtherSavings: 10}},
		{"non-SP service ignored", spRec(common.ServiceRDS, "", 10), SPTypeBreakdown{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := []common.Recommendation{tt.rec}
			assert.Equal(t, tt.want, categorizeSPRecommendations(recs))

			b := tt.want
			assert.Equal(t, SPSavingsByType{
				EC2SPSavings: b.EC2InstanceSavings, ComputeSPSavings: b.ComputeSavings,
				DatabaseSPSavings: b.DatabaseSavings, SageMakerSPSavings: b.SageMakerSavings,
				OtherSPSavings: b.OtherSavings,
			}, collectSPSavings(recs))
		})
	}
}

func TestSPSummaryTotalsAddUp(t *testing.T) {
	recs := []common.Recommendation{
		spRec(common.ServiceSavingsPlansCompute, "Compute", 1),
		spRec(common.ServiceSavingsPlansEC2Instance, "EC2Instance", 2),
		spRec(common.ServiceSavingsPlansSageMaker, "SageMaker", 4),
		spRec(common.ServiceSavingsPlansDatabase, "Database", 8),
		spRec(common.ServiceSavingsPlansAll, "Mystery", 16),
		spRec(common.ServiceRDS, "", 100),
	}
	b := categorizeSPRecommendations(recs)
	assert.Equal(t, 5, b.ComputeCount+b.EC2InstanceCount+b.SageMakerCount+b.DatabaseCount+b.OtherCount)
	assert.InDelta(t, 31, b.ComputeSavings+b.EC2InstanceSavings+b.SageMakerSavings+b.DatabaseSavings+b.OtherSavings, 1e-9)

	s := collectSPSavings(recs)
	assert.InDelta(t, 31, s.ComputeSPSavings+s.EC2SPSavings+s.SageMakerSPSavings+s.DatabaseSPSavings+s.OtherSPSavings, 1e-9)
}

func TestPrintSPTypeSummariesShowsOther(t *testing.T) {
	out := captureAppOutput(t, func() {
		printSPTypeSummaries(SPTypeBreakdown{OtherCount: 2, OtherSavings: 12.5})
	})
	assert.Contains(t, out, "Other SP")
	assert.Contains(t, out, "12.50")
}

func TestCollectRISavingsLegacyAndCanonical(t *testing.T) {
	tests := []struct {
		service common.ServiceType
		wantEC2 float64
		wantDB  float64
	}{
		{common.ServiceEC2, 5, 0},
		{common.ServiceCompute, 5, 0},
		{common.ServiceRDS, 0, 5},
		{common.ServiceRelationalDB, 0, 5},
		{common.ServiceElastiCache, 0, 5},
		{common.ServiceCache, 0, 5},
		{common.ServiceMemoryDB, 0, 5},
		{common.ServiceRedshift, 0, 5},
		{common.ServiceDataWarehouse, 0, 5},
		{common.ServiceOpenSearch, 0, 0},
	}
	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			got := collectRISavings(map[common.ServiceType]ServiceProcessingStats{
				tt.service: {TotalEstimatedSavings: 5},
			})
			assert.Equal(t, RISavingsByService{EC2RISavings: tt.wantEC2, DBRISavings: tt.wantDB}, got)
		})
	}
}
