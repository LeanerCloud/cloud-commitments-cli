package main

import (
	"fmt"
	"log"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyFilters_DropMinPoolSize verifies that a recommendation whose
// AverageInstancesUsedPerHour is below --min-pool-size is recorded in a
// non-nil DropSummary under the DropMinPoolSize category. If the
// drops.Add call for that path were removed, d.Total() would stay at 0
// and the first assertion below would fail.
func TestApplyFilters_DropMinPoolSize(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	// Pool with avg=1.5 is below min-pool-size=2; it should be dropped.
	rec := common.Recommendation{
		Region:                      "us-east-1",
		ResourceType:                "db.t3.micro",
		Count:                       2,
		AverageInstancesUsedPerHour: 1.5,
	}
	toolCfg.MinPoolSize = 2.0

	d := common.NewDropSummary()
	result := applyFilters(
		[]common.Recommendation{rec},
		&toolCfg,
		make(map[string][]InstanceEngineVersion),
		make(map[string]MajorEngineVersionInfo),
		"",
		d,
	)

	assert.Empty(t, result, "below-min-pool-size rec should be filtered out")
	assert.Equal(t, 1, d.Total(), "drop summary should record 1 drop")
	assert.Contains(t, d.FormatOneLine(), common.DropMinPoolSize,
		"drop summary should name the --min-pool-size category")
}

// applyFiltersPreExtraction is the pre-refactor applyFilters implementation,
// kept verbatim (from origin/main, before the recfilter.Filters.ApplyMinPoolSize
// extraction) as a differential oracle. It applies the --min-pool-size check
// inline, in the same loop and same order as processRecommendation, rather
// than as a separate pass over the full slice.
func applyFiltersPreExtraction(recs []common.Recommendation, cfg *Config, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo, currentRegion string, drops *common.DropSummary) []common.Recommendation {
	var filtered []common.Recommendation
	var poolDropCount int
	var poolDropInstances float64

	for i := range recs {
		if cfg.MinPoolSize > 0 && !shouldIncludePoolSize(&recs[i], cfg) {
			poolDropInstances += recs[i].AverageInstancesUsedPerHour
			label := fmt.Sprintf("%s/%s/%s", recs[i].Service, recs[i].Region, recs[i].ResourceType)
			log.Printf("INFO: --min-pool-size=%.1f dropped %s (avg=%.2f < threshold)", cfg.MinPoolSize, label, recs[i].AverageInstancesUsedPerHour)
			poolDropCount++
			drops.Add(common.DropMinPoolSize, 1)
			continue
		}
		adjusted, include, dropReason := processRecommendation(&recs[i], cfg, instanceVersions, versionInfo, currentRegion)
		if include {
			filtered = append(filtered, adjusted)
		} else if dropReason != "" {
			drops.Add(dropReason, 1)
		}
	}

	if poolDropCount > 0 {
		log.Printf("INFO: --min-pool-size dropped %d recommendation(s) (%.2f avg instances/hr total)", poolDropCount, poolDropInstances)
	}

	return filtered
}

// TestApplyFilters_MinPoolSizeMultiRegionMatchesPreExtractionBehaviour is a
// differential regression test against a reviewer's claim that extracting
// the --min-pool-size check into recfilter.Filters.ApplyMinPoolSize changed
// its ordering relative to the currentRegion guard in processRecommendation
// (inflating common.DropMinPoolSize in multi-region runs). It runs the
// current applyFilters and the pre-extraction oracle above side by side,
// once per region, over the same multi-region recommendation set, and
// asserts both the survivors and the drop accounting match exactly.
func TestApplyFilters_MinPoolSizeMultiRegionMatchesPreExtractionBehaviour(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg = Config{MinPoolSize: 2.0}

	regions := []string{"us-east-1", "eu-west-1", "ap-southeast-1"}
	const distinctBelowThreshold = 3 // one below-threshold rec per region below

	// Fresh copies per call: applyFilters mutates nothing in place today, but
	// the oracle and the refactored code must each see their own slice so a
	// hypothetical future in-place adjustment on one side can't leak into the
	// other's input and mask a real divergence.
	makeRecs := func() []common.Recommendation {
		return []common.Recommendation{
			{Region: "us-east-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "eu-west-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "eu-west-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "ap-southeast-1", ResourceType: "db.t3.micro", Count: 3, AverageInstancesUsedPerHour: 5.0},
			{Region: "ap-southeast-1", ResourceType: "db.t3.small", Count: 3, AverageInstancesUsedPerHour: 1.0}, // below threshold
			{Region: "us-east-1", ResourceType: "db.t3.large", Count: 3, AverageInstancesUsedPerHour: 0},        // no-signal passthrough
			{
				Service:                     common.ServiceSavingsPlansCompute,
				ResourceType:                "ec2-instance",
				Count:                       10,
				AverageInstancesUsedPerHour: 5.0, // account-level: bypasses the currentRegion guard
			},
		}
	}

	var totalDropMinPoolSizeNew, totalDropMinPoolSizeOld int

	for _, region := range regions {
		t.Run(region, func(t *testing.T) {
			dropsNew := common.NewDropSummary()
			dropsOld := common.NewDropSummary()

			resultNew := applyFilters(makeRecs(), &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), region, dropsNew)
			resultOld := applyFiltersPreExtraction(makeRecs(), &toolCfg, make(map[string][]InstanceEngineVersion), make(map[string]MajorEngineVersionInfo), region, dropsOld)

			assert.Equal(t, resultOld, resultNew,
				"region %s: refactored applyFilters diverged from the pre-extraction oracle -- the ApplyMinPoolSize extraction changed observable CLI filtering behavior", region)
			assert.Equal(t, dropsOld.FormatOneLine(), dropsNew.FormatOneLine(),
				"region %s: drop summaries diverged between pre- and post-extraction implementations", region)
			assert.Equal(t, dropsOld.Total(), dropsNew.Total(),
				"region %s: total drop counts diverged between pre- and post-extraction implementations", region)

			// The fixture is built so --min-pool-size is the ONLY drop reason
			// either implementation can record, which is what lets the summed
			// Total() below stand in for the min-pool count specifically.
			// Asserting the exact per-pass count keeps that guarantee honest:
			// if some other filter started dropping rows, Total() would exceed
			// the below-threshold count and this would fail.
			require.Contains(t, dropsNew.FormatOneLine(), common.DropMinPoolSize,
				"region %s: fixture must exercise the --min-pool-size drop path", region)
			require.Equal(t, distinctBelowThreshold, dropsNew.Total(),
				"region %s: --min-pool-size must be the only drop reason this fixture records", region)

			totalDropMinPoolSizeNew += dropsNew.Total()
			totalDropMinPoolSizeOld += dropsOld.Total()
		})
	}

	require.Equal(t, totalDropMinPoolSizeOld, totalDropMinPoolSizeNew,
		"old and new implementations must agree on the summed multi-region --min-pool-size drop count")

	// Pre-existing behavior, identical in both implementations (not introduced
	// by the ApplyMinPoolSize extraction): each per-region call re-scans the
	// FULL recommendation set passed to it, not a per-region subset, so a
	// below-threshold recommendation from one region is re-counted as dropped
	// during every other region's pass too. Summed across N region passes this
	// is distinctBelowThreshold * N, not distinctBelowThreshold. This assertion
	// pins today's (inflated) value rather than an aspirational deduplicated
	// one -- see the test's summary report for the actual vs. distinct counts.
	assert.Equal(t, distinctBelowThreshold*len(regions), totalDropMinPoolSizeNew,
		"summed multi-region --min-pool-size drop count should match today's pre-existing inflation factor")
}
