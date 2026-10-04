package main

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReservationExpirySizing(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		averages                 []float64
		wantCounts               []int
		wantAverages             []float64
		demand, target, existing float64
		expiry, endDays          int
		exclude                  []string
	}{
		{"equal-shares", []float64{30, 30, 30}, []int{12, 12, 12}, []float64{30, 30, 30}, 90, 80, 40, 18, 15, nil},
		{"unequal-shares", []float64{15, 30, 45}, []int{6, 12, 18}, []float64{15, 30, 45}, 90, 80, 40, 18, 15, nil},
		{"zero-raw-shares", []float64{0, 0, 0}, []int{12, 12, 12}, []float64{30, 30, 30}, 90, 80, 40, 18, 15, nil},
		{"filtered-two", []float64{30, 30, 30}, []int{18, 18}, []float64{45, 45}, 90, 80, 40, 18, 15, []string{"expiry-account-3"}},
		{"filtered-one", []float64{30, 30, 30}, []int{36}, []float64{90}, 90, 80, 40, 18, 15, []string{"expiry-account-2", "expiry-account-3"}},
		{"outside-window", []float64{30, 30, 30}, []int{6, 6, 6}, []float64{30, 30, 30}, 90, 80, 60, 18, 180, nil},
		{"exact-boundary", []float64{15, 15}, []int{4, 4}, []float64{15, 15}, 30, 80, 160.0 / 3, 2, 15, nil},
		{"below-boundary", []float64{15, 15}, []int{3, 3}, []float64{15, 15}, 30, math.Nextafter(80, 0), 160.0 / 3, 2, 15, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := reservationExpiryRecommendations(tc.averages)
			cfg := Config{TargetCoverage: tc.target, RebuyWindowDays: 30, IncludeExtendedSupport: true, ExcludeAccounts: tc.exclude}
			filtered := applyRegionFilters(recs, engineVersionData{}, "us-east-1", cfg, nil)
			got := applyCoverageAndOverrides(filtered, cfg, recommendations.PoolCoverageMap{
				"us-east-1:m5.large": {Pct: 60, AvgInstancesPerHour: tc.demand},
			}, reservationExpiryCommitments(tc.expiry, tc.endDays), nil)
			require.Len(t, got, len(tc.wantCounts))
			for i := range got {
				assertReservationExpirySizedRow(t, got[i], tc.wantCounts[i], tc.wantAverages[i], tc.existing)
				require.NotNil(t, recs[i].RecurringMonthlyCost)
				assert.Equal(t, 300.0, *recs[i].RecurringMonthlyCost, "input monthly pointer must remain unmodified")
			}
		})
	}
}

func TestReservationExpiryMissingDemand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		coverage recommendations.PoolCoverageMap
	}{
		{"nil-map", nil},
		{"missing-key", recommendations.PoolCoverageMap{"us-west-2:m5.large": {Pct: 60, AvgInstancesPerHour: 90}}},
		{"zero", recommendations.PoolCoverageMap{"us-east-1:m5.large": {Pct: 60}}},
		{"negative", recommendations.PoolCoverageMap{"us-east-1:m5.large": {Pct: 60, AvgInstancesPerHour: -1}}},
		{"nan", recommendations.PoolCoverageMap{"us-east-1:m5.large": {Pct: 60, AvgInstancesPerHour: math.NaN()}}},
		{"infinite", recommendations.PoolCoverageMap{"us-east-1:m5.large": {Pct: 60, AvgInstancesPerHour: math.Inf(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := AppLogger.Writer()
			AppLogger.SetOutput(&output)
			t.Cleanup(func() { AppLogger.SetOutput(previous) })
			cfg := Config{TargetCoverage: 80, RebuyWindowDays: 30}
			baseline := applyCoverageAndOverrides(reservationExpiryRecommendations([]float64{10, 10, 10}), cfg, tc.coverage, nil, nil)
			output.Reset()
			got := applyCoverageAndOverrides(reservationExpiryRecommendations([]float64{10, 10, 10}), cfg, tc.coverage, reservationExpiryCommitments(18, 15), nil)
			require.Len(t, got, len(baseline))
			for i := range got {
				assert.Equal(t, baseline[i].Count, got[i].Count)
				assert.Equal(t, baseline[i].ExistingCoveragePct, got[i].ExistingCoveragePct)
				assert.Equal(t, baseline[i].CommitmentCost, got[i].CommitmentCost)
				assert.Equal(t, baseline[i].OnDemandCost, got[i].OnDemandCost)
				assert.Equal(t, baseline[i].EstimatedSavings, got[i].EstimatedSavings)
				assert.Equal(t, baseline[i].RecurringMonthlyCost, got[i].RecurringMonthlyCost)
			}
			assert.Equal(t, 1, strings.Count(output.String(), "Skipped expiry adjustment for 3 recommendations because pool demand was unavailable; coverage was left unchanged"))
		})
	}
}

func TestReservationExpiryZeroDemandRow(t *testing.T) {
	recs := reservationExpiryRecommendations([]float64{30, 0, 60})
	got := applyCoverageAndOverrides(recs, Config{TargetCoverage: 80, RebuyWindowDays: 30}, recommendations.PoolCoverageMap{
		"us-east-1:m5.large": {Pct: 60, AvgInstancesPerHour: 90},
	}, reservationExpiryCommitments(18, 15), nil)
	require.Len(t, got, 3)
	assertReservationExpirySizedRow(t, got[0], 12, 30, 40)
	assertReservationExpirySizedRow(t, got[2], 24, 60, 40)
	assert.Equal(t, 30, got[1].Count)
	assert.Equal(t, 60.0, got[1].ExistingCoveragePct)
	assert.Equal(t, 3000.0, got[1].CommitmentCost)
}

func reservationExpiryRecommendations(averages []float64) []common.Recommendation {
	recs := make([]common.Recommendation, len(averages))
	for i, average := range averages {
		monthly := 300.0
		recs[i] = common.Recommendation{
			Provider: common.ProviderAWS, Service: common.ServiceEC2, CommitmentType: common.CommitmentReservedInstance,
			ResourceType: "m5.large", Region: "us-east-1", AccountName: fmt.Sprintf("expiry-account-%d", i+1),
			Count: 30, RecommendedCount: 30, AverageInstancesUsedPerHour: average, ExistingCoveragePct: 60,
			CommitmentCost: 3000, OnDemandCost: 6000, EstimatedSavings: 3000, RecurringMonthlyCost: &monthly,
		}
	}
	return recs
}

func reservationExpiryCommitments(count, endDays int) []common.Commitment {
	return []common.Commitment{{
		Provider: common.ProviderAWS, Service: common.ServiceEC2, CommitmentType: common.CommitmentReservedInstance,
		ResourceType: "m5.large", Region: "us-east-1", Count: count, State: common.CommitmentStateActive,
		StartDate: time.Now().AddDate(-1, 0, 0), EndDate: time.Now().AddDate(0, 0, endDays),
	}}
}

func assertReservationExpirySizedRow(t *testing.T, got common.Recommendation, count int, average, existing float64) {
	t.Helper()
	assert.Equal(t, count, got.Count)
	assert.Equal(t, 30, got.RecommendedCount)
	assert.InDelta(t, average, got.AverageInstancesUsedPerHour, 1e-12)
	assert.InDelta(t, existing, got.ExistingCoveragePct, 1e-12)
	assert.InDelta(t, existing+float64(count)*100/average, got.ProjectedCoverage, 1e-10)
	assert.InDelta(t, 100, got.ProjectedUtilization, 1e-10)
	assert.InDelta(t, float64(count)*100, got.CommitmentCost, 1e-10)
	assert.InDelta(t, float64(count)*200, got.OnDemandCost, 1e-10)
	assert.InDelta(t, float64(count)*100, got.EstimatedSavings, 1e-10)
	require.NotNil(t, got.RecurringMonthlyCost)
	assert.InDelta(t, float64(count)*10, *got.RecurringMonthlyCost, 1e-10)
}
