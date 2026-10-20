package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterAndAdjustRecommendations(t *testing.T) {
	// Save and restore ALL global variables
	saved := saveGlobalVars()
	defer saved.restore()

	tests := []struct {
		setupFilters    func()
		name            string
		recommendations []common.Recommendation
		coverage        float64
		expectedMin     int
		expectedMax     int
	}{
		{
			name: "100% coverage no filters",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 5},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 3},
			},
			coverage: 100.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
			},
			expectedMin: 2,
			expectedMax: 2,
		},
		{
			name: "50% coverage",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 10},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 6},
			},
			coverage: 50.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = 0
			},
			expectedMin: 1,
			expectedMax: 2,
		},
		{
			name: "Instance limit applied",
			// EstimatedSavings is set because a binding --max-instances has to
			// rank the rows it chooses between; requireRankingSignal refuses a
			// run whose rows carry no savings value rather than capping them by
			// name.
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 10, EstimatedSavings: 100},
				{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 10, EstimatedSavings: 200},
				{Service: common.ServiceRDS, ResourceType: "db.t3.large", Count: 10, EstimatedSavings: 300},
			},
			coverage: 100.0,
			setupFilters: func() {
				toolCfg.MaxInstances = 15
				toolCfg.OverrideCount = 0
			},
			expectedMin: 1,
			expectedMax: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup filters
			tt.setupFilters()

			// Suppress logger
			// Logger output disabled for testing

			result, err := filterAndAdjustRecommendations(context.Background(), tt.recommendations, tt.coverage, toolCfg)
			require.NoError(t, err)

			// Verify result is within expected range
			assert.GreaterOrEqual(t, len(result), tt.expectedMin)
			assert.LessOrEqual(t, len(result), tt.expectedMax)

			// Verify all results have count > 0
			for _, rec := range result {
				assert.Positive(t, rec.Count)
			}
		})
	}
}
