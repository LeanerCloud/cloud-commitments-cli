package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}

func TestFetchEngineVersionData_CancellationAndDeadlineAreTerminal(t *testing.T) {
	captureAppLog(t)
	for name, ctx := range map[string]context.Context{"canceled": canceledCtx(), "deadline": expiredCtx(t)} {
		_, err := fetchEngineVersionData(ctx, Config{})
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ctx.Err(), name)
	}
}

func TestFilterAndAdjustRecommendations_CancellationAndDeadlineAreTerminal(t *testing.T) {
	for name, ctx := range map[string]context.Context{"canceled": canceledCtx(), "deadline": expiredCtx(t)} {
		recs := []common.Recommendation{{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 2}}
		got, err := filterAndAdjustRecommendations(ctx, recs, 100, Config{})
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ctx.Err(), name)
		assert.Empty(t, got, name)
	}
}

func TestPrepareCSVPurchaseRun_CanceledContextStopsBeforeConfirmationAndPurchase(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "in.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("Service,Region,ResourceType,Count,Engine\nrds,us-east-1,db.t3.small,2,mysql\n"), 0o600))
	cfg := Config{CSVInput: csvPath, AuditLog: filepath.Join(dir, "audit.jsonl")}

	for _, dry := range []bool{true, false} {
		recs, _, _, err := prepareCSVPurchaseRun(canceledCtx(), cfg, 100, dry)
		require.ErrorIs(t, err, context.Canceled, "dryRun=%v", dry)
		assert.Empty(t, recs)
	}
}

func TestFetchExistingCoverage_CanceledContextIsTerminalEvenOnDryRun(t *testing.T) {
	realClient := awsprovider.NewRecommendationsClientDirect(aws.Config{Region: "us-east-1"})
	cfg := Config{TargetCoverage: 80, Regions: []string{"us-east-1"}, ActualPurchase: false}

	got, err := fetchExistingCoverage(canceledCtx(), aws.Config{Region: "us-east-1"}, realClient, cfg)

	require.ErrorIs(t, err, context.Canceled, "a dry run used to swallow this and size against an empty map")
	assert.Nil(t, got)
}

func TestHandleRegionDiscoveryError_CanceledContextDoesNotFallBackToDiscovery(t *testing.T) {
	captureAppLog(t)
	client := &MockRecommendationsClient{} // no expectations: any call fails the test

	regions, err := handleRegionDiscoveryError(canceledCtx(), client, common.ServiceRDS, errors.New("describe regions: context canceled"))

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, regions)
	client.AssertNotCalled(t, "GetRecommendationsForService", mock.Anything, mock.Anything)
}

func TestFetchRecommendationsForRegion_InterruptIsNotReportedAsProviderFailure(t *testing.T) {
	out := captureAppLog(t)
	ctx := canceledCtx()
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.Anything).Return(nil, ctx.Err())

	got := fetchRecommendationsForRegion(ctx, client, common.ServiceRDS, "us-east-1", Config{TermYears: 1})

	assert.Nil(t, got)
	assert.NotContains(t, out.String(), "Failed to fetch recommendations")
}

func TestFetchAllRecs_InterruptStopsFanOutAndDiscardsPartialData(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Regions: []string{"us-east-1", "us-west-2", "eu-west-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
		Run(func(mock.Arguments) { cancel() }). // Ctrl-C after the first region answered
		Return([]common.Recommendation{rec}, nil).Once()

	recs, _ := fetchAllRecs(ctx, aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	assert.Empty(t, recs, "partial data from an interrupted fetch is not a result")
	client.AssertNumberOfCalls(t, "GetRecommendations", 1) // later regions are never queried
}

func TestFetchAllRecs_ActiveContextProviderErrorStaysRecoverable(t *testing.T) {
	out := captureAppLog(t)
	cfg := Config{Regions: []string{"us-east-1", "us-west-2"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
		Return(nil, errors.New("throttling")).Once()
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-west-2", ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-west-2" })).
		Return([]common.Recommendation{rec}, nil).Once()

	recs, _ := fetchAllRecs(context.Background(), aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	assert.Len(t, recs, 1, "a provider error with an active context only loses that region")
	assert.Contains(t, out.String(), "Failed to fetch recommendations")
}

func TestCheckDuplicates_InterruptedCheckIsTerminalInDryAndRealRuns(t *testing.T) {
	captureAppLog(t)
	recs := []common.Recommendation{{ResourceType: "db.t3.small", Count: 5}}
	for _, dry := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		client := &MockServiceClient{}
		client.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), context.Canceled).Run(func(mock.Arguments) { cancel() })

		drops := common.NewDropSummary()
		got := checkDuplicates(ctx, recs, client, dry, drops)

		assert.Nil(t, got, "dryRun=%v: never continue with un-deduplicated counts after an interrupt", dry)
		assert.Equal(t, 0, drops.Total(), "dryRun=%v: an interrupt is not a failed duplicate check", dry)
		cancel()
	}
}

func TestCheckDuplicatesForCSVRegion_InterruptedCheckStopsEvenOnDryRun(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &MockServiceClient{}
	client.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), context.Canceled).Run(func(mock.Arguments) { cancel() })

	got, ok := checkDuplicatesForCSVRegion(ctx, []common.Recommendation{{ResourceType: "db.t3.small", Count: 5}}, client, common.ServiceRDS, "us-east-1", true)

	assert.False(t, ok, "a dry run used to continue with un-deduplicated counts")
	assert.Nil(t, got)
}

func TestFetchAllRecs_InterruptDuringLastRegionDiscardsItsData(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Regions: []string{"us-east-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).
		Return([]common.Recommendation{rec}, nil).Once()

	recs, _ := fetchAllRecs(ctx, aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	assert.Empty(t, recs)
}
