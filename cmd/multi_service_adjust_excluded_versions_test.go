package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
)

func TestAdjustRecommendationForExcludedVersions_MultipleVersionsInExtendedSupport(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceRDS,
		Region:       "us-east-1",
		ResourceType: "db.r5.large",
		Count:        10,
		Details: &common.DatabaseDetails{
			Engine: "Aurora MySQL",
		},
	}

	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r5.large": {
			{Engine: "aurora-mysql", EngineVersion: "5.6.mysql_aurora.1.22.5", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
		},
	}

	// Version info with both 5.6 and 5.7 in extended support
	versionInfo := map[string]MajorEngineVersionInfo{
		"aurora-mysql:5.6": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "5.6",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportStartDate: time.Now().AddDate(0, -12, 0),
					LifecycleSupportEndDate:   time.Now().AddDate(2, 0, 0),
				},
			},
		},
		"aurora-mysql:5.7": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportStartDate: time.Now().AddDate(0, -6, 0),
					LifecycleSupportEndDate:   time.Now().AddDate(3, 0, 0),
				},
			},
		},
	}

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 8, result.Count, "Should exclude 2 instances (5.6 and 5.7 both in extended support)")
}

func TestAdjustRecommendationForExcludedVersions_NonRDSService(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceEC2,
		Region:       "us-east-1",
		ResourceType: "m5.large",
		Count:        5,
		Details:      nil, // Not RDS
	}

	instanceVersions := map[string][]InstanceEngineVersion{}
	versionInfo := createTestVersionInfo()

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 5, result.Count, "Non-RDS services should not be adjusted")
}

// TestAdjustRecommendationForExcludedVersions_TypedNilDetails pins the typed-nil
// guard on the *common.DatabaseDetails case. A nil interface is caught by the
// caller's own nil checks, but a (*common.DatabaseDetails)(nil) stored in the
// interface reaches the type switch, and reading details.Engine there panics.
// instanceVersions must contain an entry for the rec's ResourceType so the
// function gets past its early "no running instances" return and actually
// reaches the switch.
func TestAdjustRecommendationForExcludedVersions_TypedNilDetails(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceRDS,
		Region:       "us-east-1",
		ResourceType: "db.r5.large",
		Count:        7,
		Details:      (*common.DatabaseDetails)(nil),
	}

	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r5.large": {
			{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
		},
	}

	assert.NotPanics(t, func() {
		result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, createTestVersionInfo())
		assert.Equal(t, 7, result.Count, "typed-nil Details must pass through unadjusted")
	})
}

// TestAdjustRecommendationForExcludedVersions_UnknownLifecycle pins the
// issue #1313 contract: when an instance's engine/major version is missing
// from versionInfo, the instance must NOT be silently treated as "not on
// extended support". The count stays untouched (conservative skip) and a
// warning naming the engine, version, and region is logged, once per
// engine:version pair.
func TestAdjustRecommendationForExcludedVersions_UnknownLifecycle(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceRDS,
		Region:       "us-east-1",
		ResourceType: "db.r5.large",
		Count:        4,
		Details: &common.DatabaseDetails{
			Engine: "MySQL",
		},
	}

	// Two instances of the same unknown version plus one of another: the
	// warning must be deduplicated per engine:version, not per instance.
	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r5.large": {
			{Engine: "mysql", EngineVersion: "9.0.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "mysql", EngineVersion: "9.0.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "mysql", EngineVersion: "10.1.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
		},
	}

	// versionInfo has data for mysql:8.0 only, so 9.x and 10.x lookups miss.
	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:8.0": {
			Engine:             "mysql",
			MajorEngineVersion: "8.0",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
					LifecycleSupportStartDate: time.Now().AddDate(-2, 0, 0),
					LifecycleSupportEndDate:   time.Now().AddDate(3, 0, 0),
				},
			},
		},
	}

	var logBuf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 4, result.Count, "unknown lifecycle status must leave the count untouched")

	logged := logBuf.String()
	assert.Contains(t, logged, "mysql 9.0.1", "warning must name the unknown engine and version")
	assert.Contains(t, logged, "mysql 10.1.0", "warning must name the unknown engine and version")
	assert.Contains(t, logged, "us-east-1", "warning must name the region")
	assert.Equal(t, 2, strings.Count(logged, "No lifecycle data for"), "warning must be logged once per engine:version, not per instance")
}

// TestAdjustRecommendationForExcludedVersions_ScalesMoneyWithCount: cutting the
// count must scale the money fields with it, or the report keeps the full
// recommendation's cost for fewer instances.
func TestAdjustRecommendationForExcludedVersions_ScalesMoneyWithCount(t *testing.T) {
	monthly := 30.0
	recommendation := common.Recommendation{
		Service:              common.ServiceRDS,
		Region:               "us-west-2",
		ResourceType:         "db.r6g.large",
		Count:                3,
		CommitmentCost:       900,
		OnDemandCost:         600,
		EstimatedSavings:     150,
		RecurringMonthlyCost: &monthly,
		Details:              &common.DatabaseDetails{Engine: "aurora-mysql"},
	}
	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r6g.large": {
			{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.12.0", InstanceClass: "db.r6g.large", Region: "us-west-2"},
		},
	}

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, createTestVersionInfo())

	assert.Equal(t, 2, result.Count)
	assert.InDelta(t, 600.0, result.CommitmentCost, 0.01, "2/3 of 900")
	assert.InDelta(t, 400.0, result.OnDemandCost, 0.01)
	assert.InDelta(t, 100.0, result.EstimatedSavings, 0.01)
	if assert.NotNil(t, result.RecurringMonthlyCost) {
		assert.InDelta(t, 20.0, *result.RecurringMonthlyCost, 0.01)
	}
	assert.InDelta(t, 30.0, monthly, 0.001, "the input's pointer is not mutated")
}

func TestAdjustRecommendationForExcludedVersions(t *testing.T) {
	tests := []struct {
		versionInfo      map[string]MajorEngineVersionInfo
		instanceVersions map[string][]InstanceEngineVersion
		name             string
		recommendation   common.Recommendation
		expectedCount    int
		expectedAdjusted bool
	}{
		{
			name: "No running instances - recommendation unchanged",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        10,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo:      createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    10,
			expectedAdjusted: false,
		},
		{
			name: "Exclude 1 MySQL 5.7 instance in extended support",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        10,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
					{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
					{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
				},
			},
			expectedCount:    9, // 10 - 1 MySQL 5.7 instance in extended support
			expectedAdjusted: true,
		},
		{
			name: "Exclude all MySQL 5.7 instances in extended support",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "eu-west-2",
				ResourceType: "db.t3.small",
				Count:        2,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.2", InstanceClass: "db.t3.small", Region: "eu-west-2"},
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.2", InstanceClass: "db.t3.small", Region: "eu-west-2"},
				},
			},
			expectedCount:    0, // All excluded (both in extended support)
			expectedAdjusted: true,
		},
		{
			name: "Different engine - no adjustment",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        5,
				Details: &common.DatabaseDetails{
					Engine: "Aurora PostgreSQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
				},
			},
			expectedCount:    5, // Different engine, no adjustment
			expectedAdjusted: false,
		},
		{
			name: "Different region - no adjustment",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        5,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "eu-west-2"},
				},
			},
			expectedCount:    5, // Different region, no adjustment
			expectedAdjusted: false,
		},
		{
			name: "MySQL (not Aurora) with standard mysql engine name",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "eu-west-2",
				ResourceType: "db.r5.4xlarge",
				Count:        8,
				Details: &common.DatabaseDetails{
					Engine: "MySQL",
				},
			},
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: time.Now().AddDate(0, -6, 0),
							LifecycleSupportEndDate:   time.Now().AddDate(3, 0, 0),
						},
					},
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.4xlarge": {
					{Engine: "mysql", EngineVersion: "5.7.44", InstanceClass: "db.r5.4xlarge", Region: "eu-west-2"},
					{Engine: "mysql", EngineVersion: "8.0.35", InstanceClass: "db.r5.4xlarge", Region: "eu-west-2"},
				},
			},
			expectedCount:    7, // 8 - 1 MySQL 5.7 instance in extended support
			expectedAdjusted: true,
		},
		{
			name: "Engine name normalization - spaces vs hyphens",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-west-2",
				ResourceType: "db.r6g.large",
				Count:        3,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL", // Space in name
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r6g.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.12.0", InstanceClass: "db.r6g.large", Region: "us-west-2"}, // Hyphen in name
				},
			},
			expectedCount:    2, // Should match despite space vs hyphen
			expectedAdjusted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adjustRecommendationForExcludedVersions(tt.recommendation, tt.instanceVersions, tt.versionInfo)

			assert.Equal(t, tt.expectedCount, result.Count, "Instance count mismatch")

			if tt.expectedAdjusted {
				assert.NotEqual(t, tt.recommendation.Count, result.Count, "Count should have been adjusted")
			} else {
				assert.Equal(t, tt.recommendation.Count, result.Count, "Count should not have been adjusted")
			}
		})
	}
}
