package main

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateTotalInstances(t *testing.T) {
	tests := []struct {
		name     string
		recs     []common.Recommendation
		expected int
	}{
		{
			name: "multiple recommendations",
			recs: []common.Recommendation{
				{Count: 5},
				{Count: 3},
				{Count: 2},
			},
			expected: 10,
		},
		{
			name:     "empty recommendations",
			recs:     []common.Recommendation{},
			expected: 0,
		},
		{
			name: "single recommendation",
			recs: []common.Recommendation{
				{Count: 7},
			},
			expected: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := CalculateTotalInstances(tt.recs)
			assert.Equal(t, tt.expected, total)
		})
	}
}

func TestApplyCountOverride(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		expectedCounts []int
		overrideCount  int32
	}{
		{
			name: "Override with positive value",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 10, ResourceType: "db.t3.medium"},
				{Count: 3, ResourceType: "db.t3.large"},
			},
			overrideCount:  2,
			expectedCounts: []int{2, 2, 2},
		},
		{
			name: "Override with zero - no change",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 10, ResourceType: "db.t3.medium"},
			},
			overrideCount:  0,
			expectedCounts: []int{5, 10},
		},
		{
			name: "Override with negative value - no change",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			overrideCount:  -1,
			expectedCounts: []int{5},
		},
		{
			name:           "Empty recommendations",
			recs:           []common.Recommendation{},
			overrideCount:  5,
			expectedCounts: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCountOverride(tt.recs, tt.overrideCount)
			assert.Equal(t, len(tt.expectedCounts), len(result))
			for i, rec := range result {
				assert.Equal(t, tt.expectedCounts[i], rec.Count)
			}
		})
	}
}

func TestApplyCoverage(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		expectedCounts []int
		coverage       float64
		expectedLen    int
	}{
		{
			name: "100% coverage - no change",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
				{Count: 5, EstimatedSavings: 50},
			},
			coverage:       100.0,
			expectedCounts: []int{10, 5},
			expectedLen:    2,
		},
		{
			name: "50% coverage",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
				{Count: 6, EstimatedSavings: 60},
			},
			coverage:       50.0,
			expectedCounts: []int{5, 3},
			expectedLen:    2,
		},
		{
			name: "0% coverage - returns empty",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       0.0,
			expectedCounts: []int{},
			expectedLen:    0,
		},
		{
			name: "Negative coverage - returns empty",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       -10.0,
			expectedCounts: []int{},
			expectedLen:    0,
		},
		{
			name: "Coverage reduces to zero - filters out",
			recs: []common.Recommendation{
				{Count: 1, EstimatedSavings: 10},
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       10.0, // 1*0.1 = 0, 10*0.1 = 1
			expectedCounts: []int{1},
			expectedLen:    1,
		},
		{
			name: "Savings Plans - reduces hourly commitment",
			recs: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlansAll,
					Count:            1,
					EstimatedSavings: 100,
					Details: &common.SavingsPlanDetails{
						HourlyCommitment: 10.0,
						PlanType:         "Compute",
					},
				},
			},
			coverage:       50.0,
			expectedCounts: []int{1}, // Count stays the same for SPs
			expectedLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCoverage(tt.recs, tt.coverage)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			// For Savings Plans, verify hourly commitment is adjusted
			if tt.name == "Savings Plans - reduces hourly commitment" && len(result) > 0 {
				details, ok := result[0].Details.(*common.SavingsPlanDetails)
				require.True(t, ok, "expected *common.SavingsPlanDetails in result Details")
				assert.Equal(t, 5.0, details.HourlyCommitment)    // 10 * 0.5
				assert.Equal(t, 50.0, result[0].EstimatedSavings) // 100 * 0.5
			}
		})
	}
}

// TestApplyCoverage_RICostScaling locks the fix for the CR finding on
// helpers.go:159-166: cost-bearing fields must scale by the DISCRETE
// count ratio (newCount / rec.Count), not the raw coverage ratio. With
// rec.Count=3 and coverage=50%, newCount=int(1.5)=1 (33% of instances)
// so costs must drop to 33% of original, not 50%, otherwise the sized
// purchase reads ~50% more expensive than what was actually bought.
func TestApplyCoverage_RICostScaling(t *testing.T) {
	monthly := 60.0
	recs := []common.Recommendation{
		{
			Service:              common.ServiceEC2,
			CommitmentType:       common.CommitmentReservedInstance,
			Count:                3,
			CommitmentCost:       900,
			OnDemandCost:         1800,
			EstimatedSavings:     300,
			RecurringMonthlyCost: &monthly,
		},
	}
	out := ApplyCoverage(recs, 50.0)
	require.Len(t, out, 1)
	// newCount = int(3 * 0.5) = 1. sizedRatio = 1/3.
	assert.Equal(t, 1, out[0].Count)
	assert.InDelta(t, 300.0, out[0].CommitmentCost, 0.01, "CommitmentCost scales by 1/3 (newCount/rec.Count), NOT 0.5 (raw ratio)")
	assert.InDelta(t, 600.0, out[0].OnDemandCost, 0.01)
	assert.InDelta(t, 100.0, out[0].EstimatedSavings, 0.01)
	require.NotNil(t, out[0].RecurringMonthlyCost)
	assert.InDelta(t, 20.0, *out[0].RecurringMonthlyCost, 0.01, "RecurringMonthlyCost scales by sized ratio too")
	// Original pointer not mutated.
	assert.Equal(t, 60.0, monthly)
}

func TestApplySizing_LegacyCoverageRecordsCountsFlooredToZero(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceEC2, Count: 1},
		{Service: common.ServiceRDS, Count: 10},
	}
	drops := common.NewDropSummary()

	out := applySizing(recs, Config{}, 10, drops)

	require.Len(t, out, 1)
	assert.Equal(t, 1, out[0].Count)
	assert.Equal(t, 1, drops.Total())
	assert.Contains(t, drops.FormatOneLine(), common.DropCoverageSizedToZero)
}

func TestAdjustRecommendationsForExisting(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		inputRecs      []common.Recommendation
		existingRIs    []common.Commitment
		expectedCounts []int
		expectedLen    int
	}{
		{
			name: "No existing RIs - all recommendations kept",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5},
				{ResourceType: "db.t3.medium", Region: "us-west-2", Count: 3},
			},
			existingRIs:    []common.Commitment{},
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Recent RI - partial adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 3, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{7}, // 10 - 3
		},
		{
			name: "Recent RI - complete coverage",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "postgresql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "postgresql", Count: 10, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    0, // All covered
			expectedCounts: []int{},
		},
		{
			name: "Old RI - not recent, no adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-48 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{5}, // No adjustment - RI is too old
		},
		{
			// Boundary: just inside the 24-hour window — should be treated as recent
			name: "RI just inside lookback threshold - adjusted",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-23*time.Hour - 59*time.Minute)},
			},
			expectedLen:    0, // Recent RI fully covers the recommendation
			expectedCounts: []int{},
		},
		{
			// Boundary: just outside the 24-hour window — should not be treated as recent
			name: "RI just outside lookback threshold - not adjusted",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-24*time.Hour - 1*time.Minute)},
			},
			expectedLen:    1, // RI is outside lookback window, no adjustment
			expectedCounts: []int{5},
		},
		{
			name: "Different engine - no adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "postgresql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{5}, // Different engine
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockServiceClient{}
			mockClient.On("GetExistingCommitments", ctx).Return(tt.existingRIs, nil)

			checker := NewDuplicateChecker(0)
			result, _, err := checker.AdjustRecommendationsForExisting(ctx, tt.inputRecs, mockClient)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestGetRecommendationDescription(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		rec      common.Recommendation
	}{
		{
			name: "RDS recommendation with database details",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.t3.small",
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			// GetDetailDescription returns "engine/AZConfig"; AZConfig is empty so trailing slash is included
			expected: "rds db.t3.small mysql/",
		},
		{
			name: "EC2 recommendation without details",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "t3.medium",
			},
			expected: "ec2 t3.medium",
		},
		{
			name: "ElastiCache recommendation with cache details",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.t3.micro",
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			// GetDetailDescription returns "engine/NodeType"; NodeType is empty so trailing slash is included
			expected: "elasticache cache.t3.micro redis/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRecommendationDescription(tt.rec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeEngineName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Aurora PostgreSQL", "aurora-postgresql"},
		{"Aurora MySQL", "aurora-mysql"},
		{"MySQL", "mysql"},
		{"PostgreSQL", "postgresql"},
		{"postgres", "postgresql"},
		{"MariaDB", "mariadb"},
		{"Oracle", "oracle"},
		{"oracle-se", "oracle"},
		{"oracle-se1", "oracle"},
		{"oracle-se2", "oracle"},
		{"oracle-ee", "oracle"},
		{"SQL Server", "sqlserver"},
		{"sqlserver-se", "sqlserver"},
		{"sqlserver-ee", "sqlserver"},
		{"sqlserver-ex", "sqlserver"},
		{"sqlserver-web", "sqlserver"},
		{"unknown-engine", "unknown-engine"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := common.NormalizeEngineName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetEngineFromRecommendation only exercises pointer-typed
// DatabaseDetails/CacheDetails: every producer (AWS, Azure, the CSV
// loader, and the JSON codec) constructs them that way, so there is no
// value-typed case to cover -- see
// pkg/common/service_details_codec.go's package doc for the invariant.
func TestGetEngineFromRecommendation(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		rec      common.Recommendation
	}{
		{
			name: "DatabaseDetails pointer type",
			rec: common.Recommendation{
				Details: &common.DatabaseDetails{Engine: "postgresql"},
			},
			expected: "postgresql",
		},
		{
			name: "CacheDetails pointer type",
			rec: common.Recommendation{
				Details: &common.CacheDetails{Engine: "valkey"},
			},
			expected: "valkey",
		},
		{
			name: "No details",
			rec: common.Recommendation{
				Details: nil,
			},
			expected: "",
		},
		{
			name: "ComputeDetails - returns empty (no engine)",
			rec: common.Recommendation{
				Details: &common.ComputeDetails{Platform: "Linux/UNIX"},
			},
			expected: "",
		},
		// Typed nils: rec.Details != nil (the interface holds a type) but the
		// pointer inside is nil, so the `rec.Details == nil` guard does not
		// catch it and the field read would panic without the per-case check.
		{
			name: "typed nil *DatabaseDetails - returns empty, no panic",
			rec: common.Recommendation{
				Details: (*common.DatabaseDetails)(nil),
			},
			expected: "",
		},
		{
			name: "typed nil *CacheDetails - returns empty, no panic",
			rec: common.Recommendation{
				Details: (*common.CacheDetails)(nil),
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := common.EngineFromDetails(tt.rec.Details)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConfirmPurchaseRejectsNonterminalInput(t *testing.T) {
	for _, input := range []string{"yes\n", "y\n", "YES\n", "no\n", ""} {
		t.Run(input, func(t *testing.T) {
			setConfirmationStdin(t, input)
			assert.False(t, ConfirmPurchase(5, 125))
		})
	}
}

func TestAdjustRecommendationsForExistingRIsEdgeCases(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		inputRecs      []common.Recommendation
		existingRIs    []common.Commitment
		expectedCounts []int
		expectedLen    int
	}{
		{
			name: "Multiple RIs same instance type different regions",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
				{ResourceType: "db.t3.small", Region: "eu-west-1", Count: 8, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 3, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
				{ResourceType: "db.t3.small", Region: "eu-west-1", Engine: "mysql", Count: 2, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    2, // Both regions should have adjusted counts
			expectedCounts: []int{7, 6},
		},
		{
			name: "Retired RI should not affect recommendations",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "retired", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1, // Retired RI should not affect
			expectedCounts: []int{5},
		},
		{
			name: "Payment pending RI should adjust",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 4, State: "payment-pending", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{6}, // 10 - 4 = 6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockServiceClient{}
			mockClient.On("GetExistingCommitments", ctx).Return(tt.existingRIs, nil)

			checker := NewDuplicateChecker(0)
			result, _, err := checker.AdjustRecommendationsForExisting(ctx, tt.inputRecs, mockClient)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestApplyInstanceLimit(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		expectedCounts []int
		expectedLen    int
		maxInstances   int32
	}{
		{
			name: "No limit - all recommendations kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 3, ResourceType: "db.t3.medium"},
			},
			maxInstances:   0,
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Limit exceeds total - all kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 3, ResourceType: "db.t3.medium"},
			},
			maxInstances:   20,
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Limit applies to first recommendation",
			recs: []common.Recommendation{
				{Count: 10, ResourceType: "db.t3.small"},
				{Count: 5, ResourceType: "db.t3.medium"},
			},
			maxInstances:   7,
			expectedLen:    1,
			expectedCounts: []int{7},
		},
		{
			name: "Limit applies across recommendations",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 5, ResourceType: "db.t3.medium"},
				{Count: 5, ResourceType: "db.t3.large"},
			},
			maxInstances:   12,
			expectedLen:    3,
			expectedCounts: []int{5, 5, 2},
		},
		{
			name: "Negative limit - all kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			maxInstances:   -1,
			expectedLen:    1,
			expectedCounts: []int{5},
		},
		{
			// math.MinInt32 should be treated the same as any negative value: no limit applied
			name: "math.MinInt32 limit - all kept (no int32 wrap)",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			maxInstances:   math.MinInt32,
			expectedLen:    1,
			expectedCounts: []int{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyInstanceLimit(tt.recs, tt.maxInstances)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}
		})
	}
}

func TestNewDuplicateChecker_CustomWindow(t *testing.T) {
	checker := NewDuplicateChecker(48)
	assert.Equal(t, 48, checker.LookbackHours)
}

func TestNewDuplicateChecker_ZeroUsesDefault(t *testing.T) {
	checker := NewDuplicateChecker(0)
	assert.Equal(t, DefaultDuplicateCheckLookbackHours, checker.LookbackHours)
}

func TestAdjustRecommendationsForExisting_WithinWindow(t *testing.T) {
	ctx := context.Background()
	rec := common.Recommendation{
		ResourceType: "db.t3.small", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}
	existing := []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql",
			Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return(existing, nil)

	checker := NewDuplicateChecker(24)
	passed, filtered, err := checker.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, mockClient)

	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
	mockClient.AssertExpectations(t)
}

func TestAdjustRecommendationsForExisting_OutsideWindow(t *testing.T) {
	ctx := context.Background()
	rec := common.Recommendation{
		ResourceType: "db.t3.small", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}
	existing := []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql",
			Count: 5, State: "active", StartDate: time.Now().Add(-30 * 24 * time.Hour)},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return(existing, nil)

	checker := NewDuplicateChecker(24)
	passed, filtered, err := checker.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, mockClient)

	require.NoError(t, err)
	assert.Len(t, passed, 1)
	assert.Equal(t, 5, passed[0].Count)
	assert.Empty(t, filtered)
	mockClient.AssertExpectations(t)
}

func TestAdjustRecommendationsForExisting_PartialCoverage(t *testing.T) {
	ctx := context.Background()
	rec := common.Recommendation{
		ResourceType: "db.t3.small", Region: "us-east-1", Count: 10,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}
	existing := []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql",
			Count: 3, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return(existing, nil)

	checker := NewDuplicateChecker(24)
	passed, filtered, err := checker.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, mockClient)

	require.NoError(t, err)
	require.Len(t, passed, 1)
	assert.Equal(t, 7, passed[0].Count) // 10 - 3 = 7
	assert.Empty(t, filtered)           // partial coverage stays in passed, not filtered
	mockClient.AssertExpectations(t)
}

func TestResolveAccountName_LookupErrorIsReturnedAndRememberedForTheRun(t *testing.T) {
	ctx := context.Background()
	org := &MockOrganizationsClient{}
	org.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{AccountId: aws.String("999888777666")}).
		Return(nil, errors.New("AccessDenied")).Once() // a second call would fail the mock

	cache := NewAccountAliasCacheWithClient(org)

	for range 3 {
		_, err := cache.ResolveAccountName(ctx, "999888777666")
		require.ErrorContains(t, err, "AccessDenied")
	}
	org.AssertExpectations(t)
}
