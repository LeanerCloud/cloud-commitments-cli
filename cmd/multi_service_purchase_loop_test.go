package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestProcessPurchaseLoopPurchaseFailure(t *testing.T) {
	useCallerAccount(t)
	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.Coverage = 80.0

	recs := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.t3.large", Count: 1, EstimatedSavings: 500},
	}

	mockClient := &MockServiceClient{}
	// Simulate a purchase failure
	failureResult := common.PurchaseResult{
		Recommendation: recs[0],
		Success:        false,
		CommitmentID:   "",
		Error:          fmt.Errorf("API error: quota exceeded"),
		Timestamp:      time.Now(),
	}
	mockClient.On("PurchaseCommitment", mock.Anything, recs[0], mock.MatchedBy(func(o common.PurchaseOptions) bool { return o.Source == common.PurchaseSourceCLI })).Return(failureResult, nil)

	t.Setenv("DISABLE_PURCHASE_DELAY", "true")

	results, _ := processPurchaseLoop(ctx, recs, "ap-south-1", false, mockClient, toolCfg, "test-run")

	assert.Len(t, results, 1)
	assert.False(t, results[0].Success)
	assert.NotNil(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "quota exceeded")

	mockClient.AssertExpectations(t)
}

func TestProcessPurchaseLoopEmptyRecommendations(t *testing.T) {
	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.Coverage = 100.0

	mockClient := &MockServiceClient{}

	results, _ := processPurchaseLoop(ctx, []common.Recommendation{}, "us-east-1", false, mockClient, toolCfg, "test-run")

	assert.Empty(t, results)
	mockClient.AssertNotCalled(t, "PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything)
}

func TestProcessServicePurchasesUserCancellation(t *testing.T) {
	useCallerAccount(t)
	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.Coverage = 85.0

	recs := []common.Recommendation{
		{Service: common.ServiceElastiCache, ResourceType: "cache.r6g.large", Count: 2, EstimatedSavings: 200},
	}

	mockClient := &MockServiceClient{}
	result := common.PurchaseResult{
		Recommendation: recs[0],
		Success:        true,
		CommitmentID:   "cache-purchase-123",
		Timestamp:      time.Now(),
	}
	mockClient.On("PurchaseCommitment", mock.Anything, recs[0], mock.MatchedBy(func(o common.PurchaseOptions) bool { return o.Source == common.PurchaseSourceCLI })).Return(result, nil)

	t.Setenv("DISABLE_PURCHASE_DELAY", "true")

	results, _ := processPurchaseLoop(ctx, recs, "us-west-1", false, mockClient, toolCfg, "test-run")

	assert.Len(t, results, 1)
	assert.True(t, results[0].Success)
	assert.Equal(t, "cache-purchase-123", results[0].CommitmentID)

	mockClient.AssertExpectations(t)
}

func TestProcessServicePurchasesDryRunMultiple(t *testing.T) {
	useCallerAccount(t)
	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	toolCfg.Coverage = 100.0

	recs := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.r5.xlarge", Count: 5, EstimatedSavings: 1000},
		{Service: common.ServiceRDS, ResourceType: "db.r5.2xlarge", Count: 3, EstimatedSavings: 800},
		{Service: common.ServiceRDS, ResourceType: "db.r5.4xlarge", Count: 1, EstimatedSavings: 500},
	}

	mockClient := &MockServiceClient{}
	// Dry run should not call PurchaseCommitment

	results, _ := processPurchaseLoop(ctx, recs, "ap-northeast-1", true, mockClient, toolCfg, "test-run")

	assert.Len(t, results, 3)
	for i, result := range results {
		assert.True(t, result.Success)
		assert.True(t, result.DryRun)
		assert.Contains(t, result.CommitmentID, "dryrun")
		assert.Nil(t, result.Error)
		assert.Equal(t, recs[i].ResourceType, result.Recommendation.ResourceType)
	}

	mockClient.AssertNotCalled(t, "PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything)
}

// ==================== New Extracted Function Tests ====================

func TestExecutePurchaseWithEmptyPurchaseID(t *testing.T) {
	ctx := context.Background()
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	toolCfg.Coverage = 85.0

	rec := common.Recommendation{
		Service:      common.ServiceElastiCache,
		ResourceType: "cache.r5.large",
		Count:        3,
	}

	mockClient := &MockServiceClient{}
	// Return result without PurchaseID
	expectedResult := common.PurchaseResult{
		Recommendation: rec,
		Success:        true,
		CommitmentID:   "", // Empty ID - should be generated
		Error:          nil,
		Timestamp:      time.Now(),
	}
	mockClient.On("PurchaseCommitment", mock.Anything, rec, mock.MatchedBy(func(o common.PurchaseOptions) bool { return o.Source == common.PurchaseSourceCLI })).Return(expectedResult, nil)

	// Logger output disabled for testing

	result := executePurchase(ctx, rec, "ap-southeast-1", 2, mockClient, toolCfg)

	assert.True(t, result.Success)
	assert.NotEmpty(t, result.CommitmentID) // Should have generated ID
	assert.Contains(t, result.CommitmentID, "ap-southeast-1")

	mockClient.AssertExpectations(t)
}
