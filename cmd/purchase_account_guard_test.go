package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testCallerAccount = "640848491082"
	otherAccount      = "726468838177"
)

func TestCheckPurchaseAccount(t *testing.T) {
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", Account: testCallerAccount}

	require.NoError(t, checkPurchaseAccount(rec, testCallerAccount))

	rec.Account = ""
	require.NoError(t, checkPurchaseAccount(rec, testCallerAccount), "a row with no account has nothing to compare")

	rec.Account = otherAccount
	err := checkPurchaseAccount(rec, testCallerAccount)
	require.Error(t, err)
	assert.Contains(t, err.Error(), otherAccount)
	assert.Contains(t, err.Error(), testCallerAccount)

}

func TestPurchaseSingleRec_RefusesOtherAccountsRecommendation(t *testing.T) {
	captureAppLog(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl"), CallerAccountID: testCallerAccount}
	rec := validEC2Rec()
	rec.Account = otherAccount

	for _, dry := range []bool{true, false} {
		// A real run must refuse before it builds a service client: aws.Config{}
		// has no credentials, so reaching the purchase call would fail
		// differently, not with the account message.
		result, status := purchaseSingleRec(context.Background(), aws.Config{}, rec, 1, dry, cfg)
		assert.False(t, result.Success, "dryRun=%v", dry)
		require.Error(t, result.Error, "dryRun=%v", dry)
		assert.Contains(t, result.Error.Error(), "cross-account purchases are not supported", "dryRun=%v", dry)
		assert.Equal(t, "error", status)
	}
}

func TestProcessPurchaseLoop_RefusesOtherAccountsRecommendationWithoutPurchasing(t *testing.T) {
	captureAppLog(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl"), CallerAccountID: testCallerAccount}
	other := validEC2Rec()
	other.Account = otherAccount
	own := validEC2Rec()
	own.Account = testCallerAccount
	client := &MockServiceClient{}
	client.On("PurchaseCommitment", mock.Anything, mock.MatchedBy(func(r common.Recommendation) bool { return r.Account == testCallerAccount }), mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-own"}, nil).Once()

	results, err := processPurchaseLoop(context.Background(), []common.Recommendation{other, own}, "us-east-1", false, client, cfg, "run-1")

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.False(t, results[0].Success)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), otherAccount)
	assert.True(t, results[1].Success)
	client.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
}

func TestWithCallerAccount(t *testing.T) {
	orig := callerAccountFetcher
	t.Cleanup(func() { callerAccountFetcher = orig })

	callerAccountFetcher = func(context.Context, aws.Config) (string, error) { return "640848491082", nil }
	cfg, err := withCallerAccount(context.Background(), aws.Config{}, Config{})
	require.NoError(t, err)
	assert.Equal(t, "640848491082", cfg.CallerAccountID)

	callerAccountFetcher = func(context.Context, aws.Config) (string, error) { return "", errors.New("expired token") }
	_, err = withCallerAccount(context.Background(), aws.Config{}, Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired token")
}
