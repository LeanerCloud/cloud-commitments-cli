package main

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
