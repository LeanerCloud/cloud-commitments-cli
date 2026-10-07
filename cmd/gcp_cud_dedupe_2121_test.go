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

// Consumer regression test for issue #2121, library #155: a recently
// purchased GCP committed-use discount suppresses retries for the same
// commitment family. The CLI reaches this through its duplicate-purchase
// filter (NewDuplicateChecker in helpers.go), which dispatches to the
// provider client's recent-commitment filter when the pinned library
// provides one.
//
// The test holds the pinned library's real *computeengine.Client behind an
// interface probe instead of naming the method statically: with the pre-#155
// pins the type does not implement the filter, the probe fails at runtime,
// and this test fails; with the new pins the real family-level filter runs.
// Either way the file compiles, so the sibling pin-sensitive tests keep
// running under both pin sets.

// gcpRecentCommitmentFilter mirrors the unexported recfilter dispatch
// interface so the probe can detect whether the pinned library client
// implements the family-level suppression.
type gcpRecentCommitmentFilter interface {
	FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error)
}

// fakeGCPServiceClient is a provider.ServiceClient whose existing
// commitments are fixed and whose recent-commitment filter delegates to the
// pinned library's real compute engine client, so the family-matching logic
// under test is the library's, not a reimplementation.
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

// TestDuplicateChecker_RecentGPCCUDSuppressesFamilyRetry_2121 drives the
// CLI's duplicate-purchase seam with a recent GCP CUD record: an n2
// recommendation for the same account and region as the recent
// GENERAL_PURPOSE_N2 commitment is suppressed (a retry of the purchase that
// already happened), while an n4 recommendation in the same region belongs
// to a different commitment family and still passes. With the old pins the
// gcp client implements no such filter and the generic per-resource-type
// matching lets the n2 retry through.
func TestDuplicateChecker_RecentGPCCUDSuppressesFamilyRetry_2121(t *testing.T) {
	ctx := context.Background()

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
	passed, filtered, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(ctx, recs, client)

	require.NoError(t, err)
	require.Len(t, filtered, 1, "recent GENERAL_PURPOSE_N2 purchase must suppress the n2 retry")
	assert.Equal(t, "n2-standard-4", filtered[0].ResourceType)
	require.Len(t, passed, 1, "a different commitment family must not be suppressed")
	assert.Equal(t, "n4-standard-4", passed[0].ResourceType)
}
