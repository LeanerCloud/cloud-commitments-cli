package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/computeengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Probe the library interface at runtime so this test also compiles with the old pins.
type gcpRecentCommitmentFilter interface {
	FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error)
}

type fakeGCPServiceClient struct {
	libraryClient any
	commitments   []common.Commitment
}

func (f *fakeGCPServiceClient) GetServiceType() common.ServiceType { return common.ServiceCompute }
func (f *fakeGCPServiceClient) GetRegion() string                  { return "us-central1" }

func (f *fakeGCPServiceClient) GetRecommendations(context.Context, *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, errors.New("not used by this test")
}

func (f *fakeGCPServiceClient) GetExistingCommitments(context.Context) ([]common.Commitment, error) {
	return f.commitments, nil
}

func (f *fakeGCPServiceClient) PurchaseCommitment(context.Context, common.Recommendation, common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{}, errors.New("purchase must never be attempted by the duplicate checker")
}

func (f *fakeGCPServiceClient) ValidateOffering(context.Context, common.Recommendation) error {
	return errors.New("not used by this test")
}

func (f *fakeGCPServiceClient) GetOfferingDetails(context.Context, common.Recommendation) (*common.OfferingDetails, error) {
	return nil, errors.New("not used by this test")
}

func (f *fakeGCPServiceClient) GetValidResourceTypes(context.Context) ([]string, error) {
	return nil, errors.New("not used by this test")
}

func (f *fakeGCPServiceClient) FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) ([]common.Recommendation, []common.Recommendation, error) {
	filter, ok := f.libraryClient.(gcpRecentCommitmentFilter)
	if !ok {
		return nil, nil, errors.New("pinned gcp provider client has no recent-commitment filter (pre-#155 library)")
	}
	return filter.FilterRecommendationsForRecentCommitments(recs, existing)
}

var _ provider.ServiceClient = (*fakeGCPServiceClient)(nil)

func TestDuplicateChecker_RecentGCPCUDSuppressesFamilyRetry_2121(t *testing.T) {
	ctx := context.Background()
	previousWindow := toolCfg.IdempotencyWindowHours
	toolCfg.IdempotencyWindowHours = 0
	t.Cleanup(func() { toolCfg.IdempotencyWindowHours = previousWindow })

	recentCUD := common.Commitment{
		Provider:       common.ProviderGCP,
		Account:        "proj-1",
		CommitmentID:   "cud-recent-1",
		CommitmentType: common.CommitmentCUD,
		Service:        common.ServiceCompute,
		Region:         "us-central1",
		ResourceType:   "GENERAL_PURPOSE_N2", // what GetExistingCommitments reports: the commitment Type
		Count:          8,
		State:          common.CommitmentStateActive,
		StartDate:      time.Now().Add(-2 * time.Hour),
	}
	cudRec := func(machineType string) common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderGCP,
			Account:        "proj-1",
			Service:        common.ServiceCompute,
			CommitmentType: common.CommitmentCUD,
			Region:         "us-central1",
			ResourceType:   machineType,
			Count:          2,
		}
	}
	client := &fakeGCPServiceClient{
		libraryClient: &computeengine.Client{}, // zero value: the filter uses only its arguments
		commitments:   []common.Commitment{recentCUD},
	}
	recs := []common.Recommendation{cudRec("n2-standard-4"), cudRec("n4-standard-4")}
	drops := common.NewDropSummary()
	adjusted := checkDuplicates(ctx, recs, client, false, drops)
	assert.Equal(t, "Dropped 1 recs: duplicate-dedup=1", drops.FormatOneLine())
	require.Len(t, adjusted, 1, "a different commitment family must not be suppressed")
	assert.Equal(t, "n4-standard-4", adjusted[0].ResourceType)
}
