package main

import (
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

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
