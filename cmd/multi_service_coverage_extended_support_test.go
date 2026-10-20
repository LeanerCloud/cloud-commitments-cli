package main

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
)

func TestIsInExtendedSupport_EdgeCases(t *testing.T) {
	t.Parallel()
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)  // 6 months ago
	futureDate := now.AddDate(3, 0, 0) // 3 years from now

	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportStartDate: pastDate,
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
		"mysql:8.0": {
			Engine:             "mysql",
			MajorEngineVersion: "8.0",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
					LifecycleSupportStartDate: now.AddDate(-2, 0, 0),
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
	}

	tests := []struct {
		name             string
		engine           string
		fullVersion      string
		expectedExtended bool
		expectedKnown    bool
	}{
		{
			name:             "MySQL 5.7 in extended support",
			engine:           "mysql",
			fullVersion:      "5.7.44",
			expectedExtended: true,
			expectedKnown:    true,
		},
		{
			name:             "MySQL 8.0 in standard support",
			engine:           "mysql",
			fullVersion:      "8.0.35",
			expectedExtended: false,
			expectedKnown:    true,
		},
		{
			name:             "Unknown version",
			engine:           "mysql",
			fullVersion:      "9.0.0",
			expectedExtended: false,
			expectedKnown:    false,
		},
		{
			name:             "Empty version",
			engine:           "mysql",
			fullVersion:      "",
			expectedExtended: false,
			expectedKnown:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extended, known := isInExtendedSupport(tt.engine, tt.fullVersion, versionInfo)
			assert.Equal(t, tt.expectedExtended, extended)
			assert.Equal(t, tt.expectedKnown, known, "known flag mismatch")
		})
	}
}

// ==================== Tests for adjustRecommendationForExcludedVersions ====================

func TestAdjustRecommendationForExcludedVersions_AdditionalCases(t *testing.T) {
	t.Parallel()
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)
	futureDate := now.AddDate(3, 0, 0)

	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportStartDate: pastDate,
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
	}

	tests := []struct {
		instanceVersions map[string][]InstanceEngineVersion
		name             string
		rec              common.Recommendation
		expectedCount    int
	}{
		{
			name: "No running instances - no adjustment",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    10,
		},
		{
			name: "Running instances with extended support - adjust count",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{
						Engine:        "mysql",
						EngineVersion: "5.7.44",
						InstanceClass: "db.t3.small",
						Region:        "us-east-1",
					},
					{
						Engine:        "mysql",
						EngineVersion: "5.7.42",
						InstanceClass: "db.t3.small",
						Region:        "us-east-1",
					},
				},
			},
			expectedCount: 8, // 10 - 2 extended support instances
		},
		{
			name: "Running instances in different region - no adjustment",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{
						Engine:        "mysql",
						EngineVersion: "5.7.44",
						InstanceClass: "db.t3.small",
						Region:        "us-west-2", // Different region
					},
				},
			},
			expectedCount: 10, // No adjustment
		},
		{
			name: "Non-RDS recommendation - no adjustment",
			rec: common.Recommendation{
				ResourceType: "t3.small",
				Count:        5,
				Region:       "us-east-1",
				Details: common.ComputeDetails{
					Platform: "Linux",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adjustRecommendationForExcludedVersions(tt.rec, tt.instanceVersions, versionInfo)
			assert.Equal(t, tt.expectedCount, result.Count)
		})
	}
}
