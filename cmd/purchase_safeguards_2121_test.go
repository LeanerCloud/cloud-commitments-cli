package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Consumer regression tests for issue #2121 (adopt reviewed
// cloud-commitments-go purchase safeguards). Each test names the library
// issue it pins down and the CLI seam it exercises. The pin-sensitive ones
// fail with the pre-#2121 library pins and pass with
// v0.0.0-20261006205158-7ff8c1aee1bb; the proof lives in the PR body.

// TestApplyTargetCoverage_NonPositiveCountDropped_2121 covers library #221
// through the cmd ApplyTargetCoverage wrapper (helpers.go). A recommendation
// with no positive Count has no per-unit cost, so the safeguard drops it
// instead of scaling its money fields by an invented ratio. With the old
// pins a Count=0 RI rec was kept and its costs multiplied by nTarget.
func TestApplyTargetCoverage_NonPositiveCountDropped_2121(t *testing.T) {
	for _, count := range []int{0, -1} {
		rec := common.Recommendation{
			Provider:                    common.ProviderAWS,
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			CommitmentType:              common.CommitmentReservedInstance,
			Count:                       count,
			AverageInstancesUsedPerHour: 4,
			ExistingCoveragePct:         0,
			CommitmentCost:              1000,
			OnDemandCost:                2000,
			EstimatedSavings:            500,
		}
		drops := common.NewDropSummary()
		out := ApplyTargetCoverage([]common.Recommendation{rec}, 75, drops)

		assert.Empty(t, out, "count=%d must be dropped, not scaled", count)
		assert.Contains(t, drops.FormatOneLine(), "target-input-invalid=1", "count=%d", count)
	}
}

// TestApplyTargetCoverage_InvalidInputsDropped_2121 covers library #204
// through the cmd ApplyTargetCoverage wrapper. A negative existing coverage
// makes the sized count meaningless (the gap exceeds the target), so the rec
// is dropped rather than scaled up past what AWS proposed. With the old pins
// this rec was kept and scaled by gap/target arithmetic.
func TestApplyTargetCoverage_InvalidInputsDropped_2121(t *testing.T) {
	rec := common.Recommendation{
		Provider:                    common.ProviderAWS,
		Service:                     common.ServiceRDS,
		Region:                      "us-east-1",
		ResourceType:                "db.r6g.large",
		CommitmentType:              common.CommitmentReservedInstance,
		Count:                       2,
		AverageInstancesUsedPerHour: 4,
		ExistingCoveragePct:         -10,
		CommitmentCost:              1000,
		OnDemandCost:                2000,
		EstimatedSavings:            500,
	}
	drops := common.NewDropSummary()
	out := ApplyTargetCoverage([]common.Recommendation{rec}, 75, drops)

	assert.Empty(t, out, "negative existing coverage must drop the rec")
	assert.Contains(t, drops.FormatOneLine(), "target-input-invalid=1")
}

// TestDuplicateChecker_ValkeyDoesNotCoverRedis_2121 covers library #158/#208
// through cmd's NewDuplicateChecker (helpers.go) and the CLI's
// recommendation-to-purchase duplicate filter. Redis OSS reservations cover
// Valkey nodes, never the reverse, so a recent Valkey reservation must not
// suppress a Redis recommendation. With the old pins Valkey was folded into
// Redis and the Redis rec was wrongly suppressed.
func TestDuplicateChecker_ValkeyDoesNotCoverRedis_2121(t *testing.T) {
	ctx := context.Background()
	recent := time.Now().Add(-1 * time.Hour)

	commitment := func(engine string) common.Commitment {
		return common.Commitment{
			Provider:     common.ProviderAWS,
			Service:      common.ServiceCache,
			ResourceType: "cache.r6g.large",
			Region:       "us-east-1",
			Engine:       engine,
			Count:        1,
			State:        common.CommitmentStateActive,
			StartDate:    recent,
		}
	}
	rec := func(engine string) common.Recommendation {
		return common.Recommendation{
			Provider:     common.ProviderAWS,
			Service:      common.ServiceElastiCache,
			ResourceType: "cache.r6g.large",
			Region:       "us-east-1",
			Count:        1,
			Details:      &common.CacheDetails{Engine: engine},
		}
	}

	t.Run("recent valkey reservation keeps redis recommendation", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("GetExistingCommitments", ctx).
			Return([]common.Commitment{commitment("valkey")}, nil)

		passed, filtered, err := NewDuplicateChecker(0).
			AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec("redis")}, mockClient)

		require.NoError(t, err)
		require.Len(t, passed, 1, "valkey reservation must not suppress a redis recommendation")
		assert.Equal(t, 1, passed[0].Count)
		assert.Empty(t, filtered)
	})

	t.Run("recent redis reservation still suppresses valkey recommendation", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("GetExistingCommitments", ctx).
			Return([]common.Commitment{commitment("redis")}, nil)

		passed, filtered, err := NewDuplicateChecker(0).
			AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec("valkey")}, mockClient)

		require.NoError(t, err)
		assert.Empty(t, passed, "redis OSS reservations cover valkey nodes")
		require.Len(t, filtered, 1)
	})

	t.Run("recent reservation with missing engine still wildcards", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("GetExistingCommitments", ctx).
			Return([]common.Commitment{commitment("")}, nil)

		passed, filtered, err := NewDuplicateChecker(0).
			AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec("redis")}, mockClient)

		require.NoError(t, err)
		assert.Empty(t, passed, "a reservation with unknown engine matches any cache engine")
		require.Len(t, filtered, 1)
	})
}

// TestDuplicateChecker_UnknownReservationStateRemainsOwned_2121 covers the
// CLI-side half of library #156: the duplicate checker keeps commitments in
// an unrecognized state as owned (fail closed against double purchases),
// while states known to release capacity do not suppress. The matching
// provider-side change (#156, providers/aws internal reservationstate) is
// only reachable through live AWS APIs and is covered in the provider repo.
func TestDuplicateChecker_UnknownReservationStateRemainsOwned_2121(t *testing.T) {
	ctx := context.Background()
	recs := []common.Recommendation{{
		Provider:     common.ProviderAWS,
		Service:      common.ServiceRDS,
		ResourceType: "db.r6g.large",
		Region:       "us-east-1",
		Count:        1,
		Details:      &common.DatabaseDetails{Engine: "mysql"},
	}}
	commitment := func(state common.CommitmentState) common.Commitment {
		return common.Commitment{
			Provider:     common.ProviderAWS,
			Service:      common.ServiceRDS,
			ResourceType: "db.r6g.large",
			Region:       "us-east-1",
			Engine:       "mysql",
			Count:        1,
			State:        state,
			StartDate:    time.Now().Add(-1 * time.Hour),
		}
	}

	t.Run("unrecognized state suppresses", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("GetExistingCommitments", ctx).
			Return([]common.Commitment{commitment("some-future-state")}, nil)

		passed, filtered, err := NewDuplicateChecker(0).
			AdjustRecommendationsForExisting(ctx, recs, mockClient)

		require.NoError(t, err)
		assert.Empty(t, passed, "unknown states stay owned: skipping a purchase is recoverable, a duplicate is not")
		require.Len(t, filtered, 1)
	})

	t.Run("retired state does not suppress", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("GetExistingCommitments", ctx).
			Return([]common.Commitment{commitment(common.CommitmentStateRetired)}, nil)

		passed, filtered, err := NewDuplicateChecker(0).
			AdjustRecommendationsForExisting(ctx, recs, mockClient)

		require.NoError(t, err)
		require.Len(t, passed, 1)
		assert.Empty(t, filtered)
	})
}

// TestExecutePurchase_PreservesExplicitZeroVsAbsentCost_2121 covers library
// #157 at the CLI purchase dispatch (executePurchase in
// multi_service_helpers.go): PurchaseResult.Cost is the total upfront charge,
// a non-nil zero means "no upfront charge" and nil means "unknown". The CLI
// must pass both through untouched, and the two must stay distinguishable in
// the JSON form downstream consumers (audit tooling, MCP) read.
func TestExecutePurchase_PreservesExplicitZeroVsAbsentCost_2121(t *testing.T) {
	ctx := context.Background()
	rec := common.Recommendation{
		Provider:     common.ProviderAWS,
		Service:      common.ServiceEC2,
		ResourceType: "m6i.large",
		Region:       "us-east-1",
		Count:        2,
	}

	t.Run("explicit zero upfront cost stays a non-nil zero", func(t *testing.T) {
		zero := 0.0
		mockClient := &MockServiceClient{}
		mockClient.On("PurchaseCommitment", ctx, rec, mock.Anything).
			Return(common.PurchaseResult{Recommendation: rec, Success: true, Cost: &zero}, nil)

		result := executePurchase(ctx, rec, rec.Region, 1, mockClient, toolCfg)

		require.True(t, result.Success)
		require.NotNil(t, result.Cost, "explicit zero must not collapse to absent")
		assert.Equal(t, 0.0, *result.Cost)

		data, err := json.Marshal(result)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"cost":0`)
	})

	t.Run("absent upfront cost stays nil", func(t *testing.T) {
		mockClient := &MockServiceClient{}
		mockClient.On("PurchaseCommitment", ctx, rec, mock.Anything).
			Return(common.PurchaseResult{Recommendation: rec, Success: true, Cost: nil}, nil)

		result := executePurchase(ctx, rec, rec.Region, 1, mockClient, toolCfg)

		require.True(t, result.Success)
		assert.Nil(t, result.Cost, "unknown cost must not be invented as zero")

		data, err := json.Marshal(result)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"cost":null`)
	})
}

// TestWritePurchaseAuditRecord_ZeroAndUnknownCost_2121 checks the CLI output
// path (writePurchaseAuditRecord in multi_service.go): a successful purchase
// with an explicit-zero or an absent upfront cost is audited as a success,
// and the fail-closed error case is audited as an error, so a re-driven run
// reconciles against the right per-recommendation outcome.
func TestWritePurchaseAuditRecord_ZeroAndUnknownCost_2121(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	rec := common.Recommendation{
		Provider:     common.ProviderAWS,
		Service:      common.ServiceRDS,
		ResourceType: "db.r6g.large",
		Region:       "us-east-1",
		Count:        1,
	}
	zero := 0.0

	cases := []struct {
		name   string
		result common.PurchaseResult
		status string
	}{
		{"explicit zero cost success", common.PurchaseResult{Recommendation: rec, Success: true, CommitmentID: "ri-zero", Cost: &zero}, "success"},
		{"absent cost success", common.PurchaseResult{Recommendation: rec, Success: true, CommitmentID: "ri-unknown"}, "success"},
		{"provider error stays fail-closed", common.PurchaseResult{Recommendation: rec, Success: false, Error: errors.New("throttling")}, "error"},
	}

	for _, tc := range cases {
		writePurchaseAuditRecord("run-2121", rec, tc.result, tc.status, false, auditPath)
	}

	data, err := os.ReadFile(auditPath) //nolint:gosec // G304: test-controlled temp path
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, len(cases))
	for i, tc := range cases {
		var record common.AuditRecord
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &record), tc.name)
		assert.Equal(t, tc.status, record.Status, tc.name)
		assert.Equal(t, "run-2121", record.RunID, tc.name)
	}
}

// TestExecutePurchasePipeline_InterruptsFailClosed_2121 checks the
// interruption half of the fail-closed contract: once shutdown is requested,
// the purchase pipeline stops before the next recommendation instead of
// driving further purchases, and an uninterrupted dry run audits every rec.
// Dry-run mode only: no service clients are created and nothing is purchased.
func TestExecutePurchasePipeline_InterruptsFailClosed_2121(t *testing.T) {
	ctx := context.Background()
	recs := []common.Recommendation{
		{Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: "db.r6g.large", Region: "us-east-1", Count: 1},
		{Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: "db.r6g.xlarge", Region: "us-east-1", Count: 2},
	}

	t.Run("shutdown before start purchases nothing", func(t *testing.T) {
		auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
		shutdownRequested.Store(true)
		defer shutdownRequested.Store(false)

		results := executePurchasePipeline(ctx, aws.Config{}, recs, true, "run-2121-int", Config{AuditLog: auditPath})

		assert.Empty(t, results, "a requested shutdown must stop the pipeline before the first purchase")
		_, err := os.Stat(auditPath)
		assert.True(t, os.IsNotExist(err), "no audit records without purchase attempts")
	})

	t.Run("uninterrupted dry run audits every rec as skipped", func(t *testing.T) {
		auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
		results := executePurchasePipeline(ctx, aws.Config{}, recs, true, "run-2121-dry", Config{AuditLog: auditPath})

		require.Len(t, results, len(recs))
		for _, result := range results {
			assert.True(t, result.Success)
			assert.True(t, result.DryRun)
		}

		data, err := os.ReadFile(auditPath) //nolint:gosec // G304: test-controlled temp path
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		require.Len(t, lines, len(recs))
		for _, line := range lines {
			var record common.AuditRecord
			require.NoError(t, json.Unmarshal([]byte(line), &record))
			assert.Equal(t, "skipped", record.Status)
			assert.True(t, record.DryRun)
		}
	})
}
