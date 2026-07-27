package api

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/pkg/exchange"
	"github.com/LeanerCloud/CUDly/providers/aws/recommendations"
	ec2svc "github.com/LeanerCloud/CUDly/providers/aws/services/ec2"
	azurecompute "github.com/LeanerCloud/CUDly/providers/azure/services/compute"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestListConvertibleRIs_RequiresAdmin(t *testing.T) {
	h := &Handler{} // no auth configured
	_, err := h.listConvertibleRIs(context.Background(), &events.LambdaFunctionURLRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

// issue #871: the AWS convertible-RI list must honor the Main Header global
// account filter. When the ?account_id= chip selects an account other than the
// running (ambient) AWS account, none of these RIs belong to it, so the
// handler returns an empty list without touching AWS config.
func TestListConvertibleRIs_AccountScopeMismatchReturnsEmpty(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{
		auth: mockAuth,
		// Running account differs from the requested account_id.
		riInstancesAccountResolver: func(_ context.Context) (string, error) {
			return "999999999999", nil
		},
	}

	resp, err := h.listConvertibleRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"account_id": "123456789012"},
	})
	require.NoError(t, err)
	typed, ok := resp.(*ConvertibleRIsResponse)
	require.True(t, ok, "expected *ConvertibleRIsResponse, got %T", resp)
	assert.Empty(t, typed.Instances, "RIs from a different account must not leak under an account_id filter")
}

// issue #871: a real STS resolution failure must fail closed (propagate the
// error) rather than silently falling through to the unscoped fleet.
func TestListConvertibleRIs_AccountResolveErrorFailsClosed(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{
		auth: mockAuth,
		riInstancesAccountResolver: func(_ context.Context) (string, error) {
			return "", fmt.Errorf("sts get-caller-identity denied")
		},
	}

	_, err := h.listConvertibleRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"account_id": "123456789012"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve running AWS account")
}

func TestGetRIUtilization_RequiresAdmin(t *testing.T) {
	h := &Handler{}
	_, err := h.getRIUtilization(context.Background(), &events.LambdaFunctionURLRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestGetReshapeRecommendations_RequiresAdmin(t *testing.T) {
	h := &Handler{}
	_, err := h.getReshapeRecommendations(context.Background(), &events.LambdaFunctionURLRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestGetExchangeQuote_RequiresAdmin(t *testing.T) {
	h := &Handler{}
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestExecuteExchange_RequiresAdmin(t *testing.T) {
	h := &Handler{}
	_, err := h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestGetExchangeQuote_Validation(t *testing.T) {
	// Test with a mock auth that always succeeds
	mockAuth := &mockAuthForExchange{}
	h := &Handler{auth: mockAuth}

	// Missing body
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    "{}",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ri_ids is required")

	// Missing both target_offering_id and targets[]
	_, err = h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"]}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "targets[] or target_offering_id is required")

	// Empty offering_id inside targets[] is rejected per-entry so a
	// caller can't sneak through with a zero-valued target.
	_, err = h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"targets":[{"offering_id":"","count":1}]}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "targets[0].offering_id is required")
}

func TestGetRIUtilization_LookbackValidation(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{auth: mockAuth}

	tests := []struct {
		name    string
		days    string
		wantErr string
	}{
		{"negative", "-1", "lookback_days must be between 1 and 365"},
		{"zero", "0", "lookback_days must be between 1 and 365"},
		{"too large", "999", "lookback_days must be between 1 and 365"},
		{"not a number", "abc", "lookback_days must be between 1 and 365"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.getRIUtilization(context.Background(), &events.LambdaFunctionURLRequest{
				Headers:               map[string]string{"authorization": "Bearer test-token"},
				QueryStringParameters: map[string]string{"lookback_days": tt.days},
			})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestGetReshapeRecommendations_LookbackValidation(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{auth: mockAuth}

	tests := []struct {
		name    string
		days    string
		wantErr string
	}{
		{"negative", "-1", "lookback_days must be between 1 and 365"},
		{"zero", "0", "lookback_days must be between 1 and 365"},
		{"too large", "999", "lookback_days must be between 1 and 365"},
		{"not a number", "abc", "lookback_days must be between 1 and 365"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.getReshapeRecommendations(context.Background(), &events.LambdaFunctionURLRequest{
				Headers:               map[string]string{"authorization": "Bearer test-token"},
				QueryStringParameters: map[string]string{"lookback_days": tt.days},
			})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestGetReshapeRecommendations_ThresholdValidation(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{auth: mockAuth}

	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{"negative", "-1", "threshold must be a number between 0 and 100"},
		{"over 100", "101", "threshold must be a number between 0 and 100"},
		{"not a number", "abc", "threshold must be a number between 0 and 100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.getReshapeRecommendations(context.Background(), &events.LambdaFunctionURLRequest{
				Headers:               map[string]string{"authorization": "Bearer test-token"},
				QueryStringParameters: map[string]string{"threshold": tt.value},
			})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestExecuteExchange_Validation(t *testing.T) {
	mockAuth := &mockAuthForExchange{}
	h := &Handler{auth: mockAuth}

	// Missing max_payment_due_usd
	_, err := h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"offering-1","target_count":1}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max_payment_due_usd is required")

	// Invalid max_payment_due_usd -- region provided so validation reaches the ParseDecimalRat check.
	_, err = h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"offering-1","target_count":1,"max_payment_due_usd":"abc","region":"us-east-1"}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid max_payment_due_usd")

	// Missing both target_offering_id and targets[]
	_, err = h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"max_payment_due_usd":"10"}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "targets[] or target_offering_id is required")

	// Empty offering_id inside targets[] rejected.
	_, err = h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"targets":[{"offering_id":"","count":1}],"max_payment_due_usd":"10"}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "targets[0].offering_id is required")
}

// --- Approve/Reject edge case tests ---

func TestRejectRIExchange_AlreadyCompleted(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440000"
	token := "valid-token-123"

	// Record exists but is already completed
	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: token,
		Status:        "completed",
		ExchangeID:    "exch-already-done",
	}, nil)

	// Transition from pending->canceled fails (record is not pending).
	// Uses canonical spelling; see config.StatusCanceled.
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", config.StatusCanceled, mock.Anything).
		Return((*config.RIExchangeRecord)(nil), nil)

	_, err := h.rejectRIExchange(ctx, id, token)
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, err.Error(), "already processed")

	mockStore.AssertExpectations(t)
}

func TestApproveRIExchange_AlreadyCancelled(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440001"
	token := "valid-token-456"

	// Record exists but was canceled by a newer analysis run
	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: token,
		Status:        "cancelled",
	}, nil)

	// Transition from pending→processing fails (record is canceled)
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything).
		Return((*config.RIExchangeRecord)(nil), nil)

	_, err := h.approveRIExchange(ctx, &events.LambdaFunctionURLRequest{}, id, token)
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, err.Error(), "already processed")

	mockStore.AssertExpectations(t)
}

func TestApproveRIExchange_DoubleApprove(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440002"
	token := "valid-token-789"

	// Record exists and is already being processed (first approve succeeded)
	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: token,
		Status:        "processing",
		SourceRIIDs:   []string{"ri-123"},
		PaymentDue:    "5.00",
	}, nil)

	// Transition from pending→processing fails (already processing)
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything).
		Return((*config.RIExchangeRecord)(nil), nil)

	_, err := h.approveRIExchange(ctx, &events.LambdaFunctionURLRequest{}, id, token)
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, err.Error(), "already processed")

	mockStore.AssertExpectations(t)
}

func TestApproveRIExchange_InvalidToken(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440003"

	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: "correct-token",
		Status:        "pending",
	}, nil)

	_, err := h.approveRIExchange(ctx, &events.LambdaFunctionURLRequest{}, id, "wrong-token")
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 403, ce.code)
	assert.Contains(t, err.Error(), "invalid approval token")
}

// TestApproveRIExchange_SessionAdmin verifies that an admin session can approve
// a pending RI exchange without an email token (issue #300).
func TestApproveRIExchange_SessionAdmin(t *testing.T) {
	mockStore := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	h := &Handler{config: mockStore, auth: mockAuth}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440010"
	creatorID := "creator-uuid"

	adminSession := &Session{UserID: "admin-uuid", Email: "admin@example.com"}
	mockAuth.On("ValidateSession", ctx, "admin-bearer").Return(adminSession, nil)
	mockAuth.grantAdmin()

	// authorizeSessionApproveRIExchange: admin role short-circuits (no HasPermissionAPI call)

	// approveRIExchangeViaSession fetches the record to check status
	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:              id,
		Status:          "pending",
		ApprovalToken:   "tok",
		SourceRIIDs:     []string{"ri-123"},
		PaymentDue:      "100.00",
		CreatedByUserID: &creatorID,
	}, nil).Once()

	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything).
		Return(&config.RIExchangeRecord{ID: id, Status: "processing", SourceRIIDs: []string{"ri-123"}, PaymentDue: "100.00"}, nil)
	// executeApprovedExchange calls GetRIExchangeDailySpend + GetGlobalConfig
	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       1000,
		RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	// executeApprovedExchange will call failExchange (no real AWS SDK) which
	// returns a non-error result map. Since execErr == nil, StampRIExchangeApprovedBy
	// is called as best-effort audit trail.
	mockStore.On("FailRIExchange", ctx, id, mock.AnythingOfType("string")).Return(nil)
	mockStore.On("StampRIExchangeApprovedBy", ctx, id, adminSession.Email).Return(nil)

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer admin-bearer"},
	}
	_, err := h.approveRIExchange(ctx, req, id, "")
	require.NoError(t, err)
	// The exchange execution will fail (no real AWS SDK) but we verify the
	// dispatch reached the session-authed path.
	mockStore.AssertCalled(t, "GetRIExchangeRecord", ctx, id)
	mockStore.AssertCalled(t, "TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything)
	mockStore.AssertCalled(t, "FailRIExchange", ctx, id, mock.AnythingOfType("string"))
	mockStore.AssertCalled(t, "StampRIExchangeApprovedBy", ctx, id, adminSession.Email)
}

// TestApproveRIExchange_SessionApproveOwn verifies that a user with approve-own
// can approve a pending exchange they created, but not one created by another user.
func TestApproveRIExchange_SessionApproveOwn(t *testing.T) {
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440011"
	ownerID := "owner-uuid"
	otherID := "other-uuid"

	t.Run("own exchange allowed", func(t *testing.T) {
		mockStore := new(MockConfigStore)
		mockAuth := new(MockAuthService)
		h := &Handler{config: mockStore, auth: mockAuth}

		ownerSession := &Session{UserID: ownerID, Email: "owner@example.com"}
		mockAuth.On("ValidateSession", ctx, "owner-bearer").Return(ownerSession, nil)
		mockAuth.On("HasPermissionAPI", ctx, ownerID, auth.ActionApproveAny, auth.ResourcePurchases).Return(false, nil)
		mockAuth.On("HasPermissionAPI", ctx, ownerID, auth.ActionApproveOwn, auth.ResourcePurchases).Return(true, nil)

		// authorizeSessionApproveRIExchange fetches record for ownership check
		mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
			ID:              id,
			Status:          "pending",
			SourceRIIDs:     []string{"ri-1"},
			PaymentDue:      "50.00",
			ApprovalToken:   "tok",
			CreatedByUserID: &ownerID,
		}, nil)

		mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything).
			Return(&config.RIExchangeRecord{ID: id, Status: "processing", SourceRIIDs: []string{"ri-1"}, PaymentDue: "50.00"}, nil)
		mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
		mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
			RIExchangeMaxDailyUSD:       1000,
			RIExchangeMaxPerExchangeUSD: 500,
		}, nil)
		mockStore.On("FailRIExchange", ctx, id, mock.AnythingOfType("string")).Return(nil)
		mockStore.On("StampRIExchangeApprovedBy", ctx, id, ownerSession.Email).Return(nil)

		req := &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"authorization": "Bearer owner-bearer"},
		}
		_, err := h.approveRIExchange(ctx, req, id, "")
		require.NoError(t, err)
		mockStore.AssertCalled(t, "TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything)
	})

	t.Run("other user exchange rejected", func(t *testing.T) {
		mockStore := new(MockConfigStore)
		mockAuth := new(MockAuthService)
		h := &Handler{config: mockStore, auth: mockAuth}

		ownerSession := &Session{UserID: ownerID, Email: "owner@example.com"}
		mockAuth.On("ValidateSession", ctx, "owner-bearer").Return(ownerSession, nil)
		mockAuth.On("HasPermissionAPI", ctx, ownerID, auth.ActionApproveAny, auth.ResourcePurchases).Return(false, nil)
		mockAuth.On("HasPermissionAPI", ctx, ownerID, auth.ActionApproveOwn, auth.ResourcePurchases).Return(true, nil)

		// authorizeSessionApproveRIExchange fetches record — creator does not match
		mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
			ID:              id,
			Status:          "pending",
			ApprovalToken:   "tok",
			CreatedByUserID: &otherID,
		}, nil)

		req := &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"authorization": "Bearer owner-bearer"},
		}
		_, err := h.approveRIExchange(ctx, req, id, "")
		require.Error(t, err)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 403, ce.code)
		assert.Contains(t, err.Error(), "cannot approve another user's pending exchange")
	})
}

// TestApproveRIExchange_LegacyTokenStillWorks verifies that the token-only path
// continues to work for non-session callers after the dual-auth refactor (backwards-compat).
func TestApproveRIExchange_LegacyTokenStillWorks(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore} // no auth configured -> tryGetSession returns nil
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440012"
	token := "legacy-token"

	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: token,
		Status:        "pending",
		SourceRIIDs:   []string{"ri-1"},
		PaymentDue:    "10.00",
	}, nil)
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything).
		Return(&config.RIExchangeRecord{ID: id, Status: "processing"}, nil)
	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       1000,
		RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	mockStore.On("FailRIExchange", ctx, id, mock.AnythingOfType("string")).Return(nil)

	req := &events.LambdaFunctionURLRequest{}
	_, err := h.approveRIExchange(ctx, req, id, token)
	require.NoError(t, err)
	mockStore.AssertCalled(t, "TransitionRIExchangeStatus", ctx, id, "pending", "processing", mock.Anything)
}

func TestRejectRIExchange_MissingToken(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440004"

	_, err := h.rejectRIExchange(ctx, id, "")
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, err.Error(), "rejection token is required")
}

// TestRejectRIExchange_EmptyStoredToken is a regression test for issue #399.
// A record with an empty ApprovalToken must be rejected with 403 rather than
// being canceled by any caller passing an empty token string, because
// crypto/subtle.ConstantTimeCompare([]byte(""), []byte("")) == 1.
func TestRejectRIExchange_EmptyStoredToken(t *testing.T) {
	mockStore := new(MockConfigStore)
	h := &Handler{config: mockStore}
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-446655440005"

	// Record exists but was persisted without an ApprovalToken.
	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID:            id,
		ApprovalToken: "", // empty stored token
		Status:        "pending",
	}, nil)

	// Passing an empty token must NOT match the empty stored token.
	_, err := h.rejectRIExchange(ctx, id, "sometoken")
	assert.Error(t, err)
	ce, ok := IsClientError(err)
	assert.True(t, ok)
	assert.Equal(t, 403, ce.code)
	assert.Contains(t, err.Error(), "does not support rejection")

	mockStore.AssertExpectations(t)
}

// fakeReshapeEC2Stub is a unit-test stub for reshapeEC2Client.
// Returns a single convertible RI so the downstream
// AnalyzeReshapingWithRecs actually invokes purchaseRecLookupFromStore's
// closure — without an RI in the input slice the recs lookup is never
// called and the region-normalization assertion would never fire
// (silent test trap). Mirrors the integration-test fake but lives
// here so this unit test runs without the //go:build integration tag.
type fakeReshapeEC2Stub struct {
	instances []ec2svc.ConvertibleRI
}

func (f *fakeReshapeEC2Stub) ListConvertibleReservedInstances(_ context.Context) ([]ec2svc.ConvertibleRI, error) {
	return f.instances, nil
}

// fakeReshapeRecsStub returns a configurable RI utilization slice so
// the reshape pipeline runs without a real Cost Explorer dependency.
// AnalyzeReshapingWithRecs only emits a recommendation (and only then
// invokes the recs lookup closure) when an RI has a utilization entry
// BELOW the threshold — without one the analyzer returns nil and the
// closure never fires, producing a silent test. Seed with util-percent
// well under the request's threshold (95) to guarantee the closure
// runs.
type fakeReshapeRecsStub struct {
	utilization []recommendations.RIUtilization
}

func (f *fakeReshapeRecsStub) GetRIUtilization(_ context.Context, _ int, _ string) ([]recommendations.RIUtilization, error) {
	return f.utilization, nil
}

// TestGetReshapeRecommendations_EmptyRegionUsesConfigRegion pins the
// region-normalization fix from commit afc3aa1ff: when the caller
// omits ?region= (or sends ?region=), the handler MUST adopt
// cfg.Region (the value the AWS SDK clients are actually talking to)
// before threading the region through to the recs lookup. Without
// the normalization the recs query lands as Region:"" — an unscoped
// read that surfaces alternatives from every region onto the reshape
// page.
//
// Mock injection strategy: build a Handler with the same factory-
// injection seams the integration test uses (reshapeEC2Factory,
// reshapeRecsFactory, reshapeAccountResolver) so the request runs
// without real AWS calls or Postgres. Pre-seal awsCfgOnce with
// Region:"us-west-2" so loadAWSConfigWithRegion(ctx, "") returns a
// config carrying that region. Wire MockConfigStore.ListStoredRecommendations
// with mock.Run to capture every filter the closure passes — the
// load-bearing assertion is filter.Region == "us-west-2" (not "").
//
// Reverting the `if region == "" { region = cfg.Region }` block from
// handler_ri_exchange.go makes this test fail loudly with the captured
// filter showing Region:"".
func TestGetReshapeRecommendations_EmptyRegionUsesConfigRegion(t *testing.T) {
	const cfgRegion = "us-west-2"

	mockStore := &MockConfigStore{}
	// mock.Run captures every invocation's arguments without affecting
	// the return value. Returning nil/nil from ListStoredRecommendations
	// means "no recs in this region" which the downstream pipeline
	// treats as empty alternatives — fine for our purposes.
	var capturedFilters []config.RecommendationFilter
	mockStore.On("ListStoredRecommendations", mock.Anything, mock.Anything).
		Return([]config.RecommendationRecord(nil), nil).
		Run(func(args mock.Arguments) {
			capturedFilters = append(capturedFilters, args.Get(1).(config.RecommendationFilter))
		})

	h := &Handler{
		config: mockStore,
		auth:   &mockAuthForExchange{},
		// Inject one convertible RI so AnalyzeReshapingWithRecs
		// actually drives purchaseRecLookupFromStore — otherwise the
		// closure never fires and the assertion is silent.
		reshapeEC2Factory: func(_ aws.Config) reshapeEC2Client {
			return &fakeReshapeEC2Stub{
				instances: []ec2svc.ConvertibleRI{
					{ReservedInstanceID: "ri-1", InstanceType: "m5.xlarge", InstanceCount: 1, CurrencyCode: "USD"},
				},
			}
		},
		reshapeRecsFactory: func(_ aws.Config) reshapeRecsClient {
			// Util well below threshold (95) → analyzeRI emits a
			// recommendation → AnalyzeReshapingWithRecs invokes the
			// recs lookup closure → MockConfigStore.ListStoredRecommendations
			// captures the filter the assertion below inspects.
			return &fakeReshapeRecsStub{
				utilization: []recommendations.RIUtilization{
					{ReservedInstanceID: "ri-1", UtilizationPercent: 50.0},
				},
			}
		},
		// Bypass STS so the test is hermetic. Empty cloud-account ID
		// is the legitimate "no scope filter" path; this test isolates
		// the region-normalization regression, not account scoping.
		reshapeAccountResolver: func(_ context.Context) (string, error) { return "", nil },
	}
	// Pre-seal awsCfgOnce with the cfg.Region the handler must adopt
	// when the caller sends an empty ?region=. loadAWSConfigWithRegion
	// returns this config unmodified (region override is skipped on
	// empty input), so cfg.Region == cfgRegion downstream.
	h.awsCfgOnce.Do(func() {
		h.awsCfg = aws.Config{Region: cfgRegion}
	})

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{
			// Explicit empty value — the regression case from issue #151.
			// Equivalent to omitting the key entirely (Go map zero-value).
			"region":        "",
			"lookback_days": "30",
			"threshold":     "95.0",
		},
	}

	_, err := h.getReshapeRecommendations(context.Background(), req)
	require.NoError(t, err, "reshape handler must succeed when region is empty (cfg.Region used)")

	// Guard against a silent test: if AnalyzeReshapingWithRecs ever
	// stops invoking the closure for synthetic RIs, the region check
	// would vacuously pass. Require at least one captured filter.
	require.NotEmpty(t, capturedFilters,
		"recs lookup must be invoked at least once — without it the region-normalization "+
			"assertion would silently pass even if the fix were reverted")

	// Load-bearing assertion: every captured filter must carry the
	// normalized cfg.Region value, NOT the empty string the request
	// arrived with. If the `if region == "" { region = cfg.Region }`
	// block in getReshapeRecommendations is removed, this fails.
	for i, f := range capturedFilters {
		assert.Equal(t, cfgRegion, f.Region,
			"capturedFilters[%d].Region must equal cfg.Region (%q), not %q — empty ?region= "+
				"must normalize to cfg.Region or the recs lookup runs unscoped and leaks "+
				"alternatives from other regions onto the reshape page (commit afc3aa1ff)",
			i, cfgRegion, f.Region)
	}
}

// Suppress unused import warnings.
var _ = mock.Anything
var _ = time.Now
var _ = config.RIExchangeRecord{}

// --- Azure exchangeable RI tests ---

// stubAzureExchangeClient is a minimal implementation of azureExchangeClient
// for unit tests.
type stubAzureExchangeClient struct {
	err          error
	reservations []azurecompute.ExchangeableReservation
}

func (s *stubAzureExchangeClient) ListExchangeableReservations(_ context.Context) ([]azurecompute.ExchangeableReservation, error) {
	return s.reservations, s.err
}

func TestListExchangeableAzureRIs_RequiresPermission(t *testing.T) {
	h := &Handler{} // no auth configured
	_, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestListExchangeableAzureRIs_EmptyList(t *testing.T) {
	stub := &stubAzureExchangeClient{reservations: []azurecompute.ExchangeableReservation{}}
	h := &Handler{
		auth: &mockAuthForExchange{},
		azureExchangeFactory: func(_ string) azureExchangeClient {
			return stub
		},
	}

	res, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
	})
	require.NoError(t, err)
	resp, ok := res.(*ExchangeableAzureRIsResponse)
	require.True(t, ok)
	assert.Empty(t, resp.Reservations)
}

func TestListExchangeableAzureRIs_PopulatedList(t *testing.T) {
	want := []azurecompute.ExchangeableReservation{
		{
			ReservationOrderID:  "order-1111",
			ReservationID:       "/providers/Microsoft.Capacity/reservationOrders/order-1111/reservations/res-aaaa",
			SKU:                 "Standard_D2s_v3",
			Quantity:            2,
			Region:              "eastus",
			Term:                "P1Y",
			InstanceFlexibility: "On",
			DisplayName:         "my-reservation",
		},
	}
	stub := &stubAzureExchangeClient{reservations: want}
	h := &Handler{
		auth: &mockAuthForExchange{},
		azureExchangeFactory: func(_ string) azureExchangeClient {
			return stub
		},
	}

	res, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
	})
	require.NoError(t, err)
	resp, ok := res.(*ExchangeableAzureRIsResponse)
	require.True(t, ok)
	require.Len(t, resp.Reservations, 1)
	r := resp.Reservations[0]
	assert.Equal(t, "order-1111", r.ReservationOrderID)
	assert.Equal(t, "Standard_D2s_v3", r.SKU)
	assert.Equal(t, int32(2), r.Quantity)
	assert.Equal(t, "eastus", r.Region)
	assert.Equal(t, "P1Y", r.Term)
	assert.Equal(t, "On", r.InstanceFlexibility)
}

func TestListExchangeableAzureRIs_ClientError(t *testing.T) {
	stub := &stubAzureExchangeClient{err: fmt.Errorf("azure api unavailable")}
	h := &Handler{
		auth: &mockAuthForExchange{},
		azureExchangeFactory: func(_ string) azureExchangeClient {
			return stub
		},
	}

	_, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azure api unavailable")
}

func TestListExchangeableAzureRIs_SubscriptionIDPassedToFactory(t *testing.T) {
	var capturedSubID string
	stub := &stubAzureExchangeClient{reservations: []azurecompute.ExchangeableReservation{}}
	h := &Handler{
		auth: &mockAuthForExchange{},
		azureExchangeFactory: func(subID string) azureExchangeClient {
			capturedSubID = subID
			return stub
		},
	}

	_, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"subscription_id": "sub-abc"},
	})
	require.NoError(t, err)
	assert.Equal(t, "sub-abc", capturedSubID)
}

// --- Azure credential-resolution path tests (issue #871) ---
//
// These tests exercise the production path of buildAzureExchangeClient, which
// was previously hardcoded to azidentity.NewDefaultAzureCredential. The fix
// routes through the project's per-subscription resolver so Lambda (which has
// no ambient Azure identity) stops 500ing.
//
// No real Azure calls are made: the MockConfigStore.GetCloudAccountByExternalID
// override supplies a fake CloudAccount, and the azureExchangeFactory is NOT
// wired — the test exercises the real buildAzureExchangeClient production path.
// The credential store returns nil from LoadRaw, which causes
// ResolveAzureTokenCredentialWithOpts to fail when the account uses client_secret
// mode (expected: we assert the error rather than a 500). For the graceful-empty
// path (no account registered) we assert a clean empty response.

// TestListExchangeableAzureRIs_NoAzureAccountRegistered verifies that when no
// Azure CloudAccount is registered for the requested subscription, the handler
// returns an empty reservations list (graceful empty state) rather than a 500.
// This is the fix for issue #871: Lambda has no ambient Azure identity so the
// old DefaultAzureCredential call failed, producing an opaque 500.
func TestListExchangeableAzureRIs_NoAzureAccountRegistered(t *testing.T) {
	mockStore := &MockConfigStore{}
	// GetCloudAccountByExternalID returns (nil, nil) — no account for this sub.
	mockStore.GetCloudAccountByExternalIDFn = func(_ context.Context, provider, externalID string) (*config.CloudAccount, error) {
		require.Equal(t, "azure", provider, "provider must be azure")
		require.Equal(t, "sub-not-registered", externalID)
		return nil, nil
	}

	h := &Handler{
		auth:   &mockAuthForExchange{},
		config: mockStore,
		// azureExchangeFactory is intentionally NOT set so the production
		// buildAzureExchangeClient path runs (verifying it handles nil account).
	}

	res, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"subscription_id": "sub-not-registered"},
	})
	require.NoError(t, err, "missing Azure account must produce empty state, not an error")
	resp, ok := res.(*ExchangeableAzureRIsResponse)
	require.True(t, ok)
	assert.Empty(t, resp.Reservations, "no Azure account registered must return empty reservation list")
}

// TestListExchangeableAzureRIs_EmptySubscriptionNoAccounts verifies the graceful
// empty state when subscription_id is omitted and no Azure accounts are registered.
func TestListExchangeableAzureRIs_EmptySubscriptionNoAccounts(t *testing.T) {
	mockStore := &MockConfigStore{}
	// GetCloudAccountByExternalID with empty externalID also returns (nil, nil).
	mockStore.GetCloudAccountByExternalIDFn = func(_ context.Context, provider, externalID string) (*config.CloudAccount, error) {
		require.Equal(t, "azure", provider)
		return nil, nil
	}

	h := &Handler{
		auth:   &mockAuthForExchange{},
		config: mockStore,
	}

	res, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		// No subscription_id parameter.
	})
	require.NoError(t, err, "no Azure accounts + no subscription_id must return empty state")
	resp, ok := res.(*ExchangeableAzureRIsResponse)
	require.True(t, ok)
	assert.Empty(t, resp.Reservations)
}

// TestListExchangeableAzureRIs_AccountLookupError verifies that a DB error from
// GetCloudAccountByExternalID surfaces as an error (which maps to 500) rather
// than silently falling through. A DB outage is a server-side fault, not a
// client mistake, so 5xx is the correct classification.
func TestListExchangeableAzureRIs_AccountLookupError(t *testing.T) {
	mockStore := &MockConfigStore{}
	mockStore.GetCloudAccountByExternalIDFn = func(_ context.Context, provider, _ string) (*config.CloudAccount, error) {
		return nil, fmt.Errorf("database connection lost")
	}

	h := &Handler{
		auth:   &mockAuthForExchange{},
		config: mockStore,
	}

	_, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"subscription_id": "sub-db-error"},
	})
	require.Error(t, err, "DB lookup failure must propagate as an error")
	// Must NOT be a 4xx ClientError: DB failures are server-side faults.
	_, isClientErr := IsClientError(err)
	assert.False(t, isClientErr, "DB lookup failure must not be classified as a client (4xx) error")
	assert.Contains(t, err.Error(), "database connection lost")
}

// TestListExchangeableAzureRIs_CredentialResolutionError verifies that when a
// CloudAccount IS registered but credential resolution fails (e.g. missing stored
// secret), the error propagates as a server-side error. This guards against the
// regression where a misconfigured account silently returned an empty list
// instead of surfacing the configuration problem to the operator.
func TestListExchangeableAzureRIs_CredentialResolutionError(t *testing.T) {
	const subID = "sub-cred-error"
	mockStore := &MockConfigStore{}
	// Account exists but uses client_secret mode with no stored secret.
	mockStore.GetCloudAccountByExternalIDFn = func(_ context.Context, provider, externalID string) (*config.CloudAccount, error) {
		require.Equal(t, "azure", provider)
		require.Equal(t, subID, externalID)
		return &config.CloudAccount{
			ID:                  "acct-uuid-001",
			Provider:            "azure",
			ExternalID:          subID,
			AzureSubscriptionID: subID,
			AzureTenantID:       "tenant-001",
			AzureClientID:       "client-001",
			AzureAuthMode:       "client_secret",
			Enabled:             true,
		}, nil
	}

	// credStore.LoadRaw returns nil (no secret stored) which causes
	// ResolveAzureTokenCredentialWithOpts to return an error.
	h := &Handler{
		auth:      &mockAuthForExchange{},
		config:    mockStore,
		credStore: &MockCredentialStore{}, // LoadRaw always returns (nil, nil)
	}

	_, err := h.listExchangeableAzureRIs(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"subscription_id": subID},
	})
	require.Error(t, err, "missing credential must produce an error, not a silent empty state")
	// The error must be server-side (not a 4xx ClientError).
	_, isClientErr := IsClientError(err)
	assert.False(t, isClientErr, "credential resolution failure must not be classified as a client (4xx) error")
}

// TestBuildAzureExchangeClient_UsesResolver verifies that buildAzureExchangeClient
// calls GetCloudAccountByExternalID with the correct (provider, subscriptionID) pair,
// confirming the production path no longer uses DefaultAzureCredential. The subscription
// ID from the request must flow through to the lookup without being altered.
func TestBuildAzureExchangeClient_UsesResolver(t *testing.T) {
	const subID = "sub-resolver-test"
	var capturedProvider, capturedExtID string

	mockStore := &MockConfigStore{}
	mockStore.GetCloudAccountByExternalIDFn = func(_ context.Context, provider, externalID string) (*config.CloudAccount, error) {
		capturedProvider = provider
		capturedExtID = externalID
		// Return nil to short-circuit credential resolution (graceful empty path).
		return nil, nil
	}

	h := &Handler{
		config: mockStore,
		// No azureExchangeFactory: production path runs.
	}

	client, err := h.buildAzureExchangeClient(context.Background(), subID)
	require.NoError(t, err)
	assert.Nil(t, client, "nil account must yield nil client (graceful empty path)")
	assert.Equal(t, "azure", capturedProvider, "lookup must use provider=azure")
	assert.Equal(t, subID, capturedExtID, "lookup must use the subscription ID from the request")
}

// mockAuthForExchange is a minimal auth mock that returns an admin session.
type mockAuthForExchange struct{}

func (m *mockAuthForExchange) Login(_ context.Context, _ LoginRequest) (*LoginResponse, error) {
	return nil, nil
}
func (m *mockAuthForExchange) Logout(_ context.Context, _ string) error { return nil }
func (m *mockAuthForExchange) ValidateSession(_ context.Context, _ string) (*Session, error) {
	return &Session{UserID: "admin", Email: "admin@test.com"}, nil
}
func (m *mockAuthForExchange) ValidateCSRFToken(_ context.Context, _, _ string) error { return nil }
func (m *mockAuthForExchange) SetupAdmin(_ context.Context, _ SetupAdminRequest) (*LoginResponse, error) {
	return nil, nil
}
func (m *mockAuthForExchange) CheckAdminExists(_ context.Context) (bool, error)       { return true, nil }
func (m *mockAuthForExchange) RequestPasswordReset(_ context.Context, _ string) error { return nil }
func (m *mockAuthForExchange) ConfirmPasswordReset(_ context.Context, _ PasswordResetConfirm) error {
	return nil
}
func (m *mockAuthForExchange) ResetTokenStatus(_ context.Context, _ string) (string, string, error) {
	return "valid", "reset", nil
}
func (m *mockAuthForExchange) GetUser(_ context.Context, _ string) (*User, error) { return nil, nil }
func (m *mockAuthForExchange) UpdateUserProfile(_ context.Context, _, _, _, _ string) error {
	return nil
}
func (m *mockAuthForExchange) CreateUserAPI(_ context.Context, _ any) (any, error) { return nil, nil }
func (m *mockAuthForExchange) UpdateUserAPI(_ context.Context, _, _ string, _ any) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) DeleteUser(_ context.Context, _ string) error              { return nil }
func (m *mockAuthForExchange) ListUsersAPI(_ context.Context) (any, error)               { return nil, nil }
func (m *mockAuthForExchange) ChangePasswordAPI(_ context.Context, _, _, _ string) error { return nil }
func (m *mockAuthForExchange) CreateGroupAPI(_ context.Context, _ any) (any, error)      { return nil, nil }
func (m *mockAuthForExchange) UpdateGroupAPI(_ context.Context, _ string, _ any) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) DeleteGroup(_ context.Context, _ string) error        { return nil }
func (m *mockAuthForExchange) GetGroupAPI(_ context.Context, _ string) (any, error) { return nil, nil }
func (m *mockAuthForExchange) ListGroupsAPI(_ context.Context) (any, error)         { return nil, nil }
func (m *mockAuthForExchange) HasPermissionAPI(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
func (m *mockAuthForExchange) HasPermissionForConstraintsAPI(_ context.Context, _, _, _ string, _ []auth.PermissionConstraints) (bool, error) {
	return true, nil
}
func (m *mockAuthForExchange) GetUserPermissionsAPI(_ context.Context, _ string) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) CreateAPIKeyAPI(_ context.Context, _ string, _ any) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) ListUserAPIKeysAPI(_ context.Context, _ string) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) GetAPIKeysUsageStatsAPI(_ context.Context, _ string) (any, error) {
	return nil, nil
}
func (m *mockAuthForExchange) DeleteAPIKeyAPI(_ context.Context, _, _ string) error { return nil }
func (m *mockAuthForExchange) RevokeAPIKeyAPI(_ context.Context, _, _ string) error { return nil }
func (m *mockAuthForExchange) ValidateUserAPIKeyAPI(_ context.Context, _ string) (any, any, error) {
	return nil, nil, nil
}
func (m *mockAuthForExchange) HasAPIKeyPermissionAPI(_ context.Context, _, _, _ string) (string, string, bool, error) {
	return "admin", "", true, nil
}
func (m *mockAuthForExchange) HasAPIKeyPermissionForConstraintsAPI(_ context.Context, _, _, _, _ string, _ []auth.PermissionConstraints) (bool, error) {
	return true, nil
}
func (m *mockAuthForExchange) GetAllowedAccountsAPI(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockAuthForExchange) MFASetupAPI(_ context.Context, _, _ string) (string, string, error) {
	return "", "", nil
}
func (m *mockAuthForExchange) MFAEnableAPI(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockAuthForExchange) MFADisableAPI(_ context.Context, _, _, _ string) error { return nil }
func (m *mockAuthForExchange) MFARegenerateRecoveryCodesAPI(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Defect 1 backend: GET /api/ri-exchange/target-offerings
// ---------------------------------------------------------------------------

// stubTargetOfferingsEC2 is a test stub for targetOfferingsEC2Client.
// It returns a fixed list of ConvertibleRIs for the lookup step and a
// fixed list of TargetOfferings for the DescribeReservedInstancesOfferings
// step. Both are configurable per-test so we can exercise the 404 and
// happy paths without live AWS.
type stubTargetOfferingsEC2 struct {
	err       error
	instances []ec2svc.ConvertibleRI
	offerings []ec2svc.TargetOffering
}

func (s *stubTargetOfferingsEC2) ListConvertibleReservedInstances(_ context.Context) ([]ec2svc.ConvertibleRI, error) {
	return s.instances, s.err
}

func (s *stubTargetOfferingsEC2) ListTargetOfferings(_ context.Context, _ ec2svc.ListTargetOfferingsParams) ([]ec2svc.TargetOffering, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.offerings, nil
}

func TestListTargetOfferings_RequiresPermission(t *testing.T) {
	h := &Handler{}
	_, err := h.listTargetOfferings(context.Background(), &events.LambdaFunctionURLRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}

func TestListTargetOfferings_MissingSourceRIID(t *testing.T) {
	h := &Handler{auth: &mockAuthForExchange{}}
	_, err := h.listTargetOfferings(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, err.Error(), "source_ri_id is required")
}

func TestListTargetOfferings_SourceRINotFound(t *testing.T) {
	stub := &stubTargetOfferingsEC2{
		instances: []ec2svc.ConvertibleRI{
			{ReservedInstanceID: "ri-known", InstanceType: "m5.large"},
		},
	}
	h := &Handler{
		auth:                      &mockAuthForExchange{},
		targetOfferingsEC2Factory: func(_ aws.Config) targetOfferingsEC2Client { return stub },
	}
	h.awsCfgOnce.Do(func() { h.awsCfg = aws.Config{Region: "us-east-1"} })

	_, err := h.listTargetOfferings(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"source_ri_id": "ri-unknown"},
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 404, ce.code)
	assert.Contains(t, err.Error(), "ri-unknown")
}

func TestListTargetOfferings_HappyPath(t *testing.T) {
	sourceID := "296818b6-73f8-4cd2-94bc-dbb95f794812"
	stub := &stubTargetOfferingsEC2{
		instances: []ec2svc.ConvertibleRI{
			{
				ReservedInstanceID: sourceID,
				InstanceType:       "t3.medium",
				ProductDescription: "Linux/UNIX",
				InstanceTenancy:    "default",
				Scope:              "Region",
				Duration:           31536000,
				OfferingType:       "No Upfront",
			},
		},
		offerings: []ec2svc.TargetOffering{
			{OfferingID: "4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91", InstanceType: "m5.large", OfferingType: "No Upfront"},
			{OfferingID: "7e1234aa-0000-4567-abcd-ef0123456789", InstanceType: "m6i.large", OfferingType: "No Upfront"},
		},
	}
	h := &Handler{
		auth:                      &mockAuthForExchange{},
		targetOfferingsEC2Factory: func(_ aws.Config) targetOfferingsEC2Client { return stub },
	}
	h.awsCfgOnce.Do(func() { h.awsCfg = aws.Config{Region: "us-east-1"} })

	res, err := h.listTargetOfferings(context.Background(), &events.LambdaFunctionURLRequest{
		Headers:               map[string]string{"authorization": "Bearer test-token"},
		QueryStringParameters: map[string]string{"source_ri_id": sourceID},
	})
	require.NoError(t, err)
	resp, ok := res.(*TargetOfferingsResponse)
	require.True(t, ok)
	require.Len(t, resp.Offerings, 2)
	assert.Equal(t, "4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91", resp.Offerings[0].OfferingID)
	assert.Equal(t, "m5.large", resp.Offerings[0].InstanceType)
}

// ---------------------------------------------------------------------------
// Defect 2: offering-id UUID format validation
// ---------------------------------------------------------------------------

func TestGetExchangeQuote_InvalidOfferingIDFormat(t *testing.T) {
	h := &Handler{auth: &mockAuthForExchange{}}

	// "t3.medium" looks like an instance type, not an offering UUID
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"targets":[{"offering_id":"t3.medium","count":1}]}`,
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, err.Error(), "t3.medium")
	assert.Contains(t, err.Error(), "offering UUID")
}

func TestGetExchangeQuote_ValidOfferingIDPassesFormat(t *testing.T) {
	// A well-formed UUID should pass the format check. The AWS call will
	// fail because there's no real EC2 client wired -- but the test
	// exercises that the regex guard does NOT fire on a valid UUID.
	// We stub the handler to short-circuit before the AWS call.
	called := false
	stub := &stubTargetOfferingsEC2{
		instances: []ec2svc.ConvertibleRI{},
	}
	_ = stub
	_ = called

	h := &Handler{auth: &mockAuthForExchange{}}

	// The handler calls exchange.GetExchangeQuote next; that will fail
	// with a config error in test (no real AWS cred). We only care that
	// the regex guard doesn't block a valid UUID -- so any error after
	// passing the UUID check is acceptable.
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"targets":[{"offering_id":"4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91","count":1}]}`,
	})
	// Must NOT be the UUID format error
	if err != nil {
		assert.NotContains(t, err.Error(), "offering UUID",
			"a valid UUID must not be rejected by the format check")
	}
}

func TestValidateExecuteExchangeBody_InvalidOfferingIDFormat(t *testing.T) {
	err := validateExecuteExchangeBody(ExchangeExecuteRequestBody{
		RIIDs:            []string{"ri-123"},
		Targets:          []ExchangeTargetBody{{OfferingID: "t3.medium", Count: 1}},
		MaxPaymentDueUSD: "50.00",
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, err.Error(), "t3.medium")
	assert.Contains(t, err.Error(), "offering UUID")
}

// ---------------------------------------------------------------------------
// Defect 3: AWS 4xx -> client 4xx, error message preserved
// ---------------------------------------------------------------------------

// fakeAPIError is a minimal smithy.APIError implementation for tests.
type fakeAPIError struct {
	code    string
	message string
}

func (e *fakeAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.message }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestMapAWSExchangeError_ClientFault4xx(t *testing.T) {
	codes := []string{
		"InvalidOfferingId",
		"InvalidParameter",
		"ValidationError",
		"InvalidReservedInstancesId.NotFound",
		"InvalidInstanceID.NotFound",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			apiErr := &fakeAPIError{code: code, message: "AWS says: bad input"}
			mapped := mapAWSExchangeError("fallback msg", apiErr)
			ce, ok := IsClientError(mapped)
			require.True(t, ok, "must be ClientError")
			assert.Equal(t, 400, ce.code, "client-fault codes must map to 400")
			assert.Contains(t, mapped.Error(), "AWS says: bad input",
				"AWS error message must be preserved")
		})
	}
}

func TestMapAWSExchangeError_ServerFault5xx(t *testing.T) {
	// An AWS error with an unrecognized code must stay 500
	apiErr := &fakeAPIError{code: "InternalError", message: "AWS is having a bad day"}
	mapped := mapAWSExchangeError("exchange quote failed", apiErr)
	ce, ok := IsClientError(mapped)
	require.True(t, ok)
	assert.Equal(t, 500, ce.code)
	assert.Contains(t, mapped.Error(), "exchange quote failed")
	assert.NotContains(t, mapped.Error(), "AWS is having a bad day",
		"non-client-fault AWS message must NOT leak through")
}

func TestMapAWSExchangeError_NonAWSError(t *testing.T) {
	// A plain Go error must also stay 500
	mapped := mapAWSExchangeError("exchange quote failed", fmt.Errorf("network timeout"))
	ce, ok := IsClientError(mapped)
	require.True(t, ok)
	assert.Equal(t, 500, ce.code)
}

// TestExecuteExchange_EmptyRegionReturns400 pins finding 01-L4:
// executeExchange must reject a request with no region rather than
// silently routing the exchange to us-east-1, where RIs may not live.
// A wrong-region execute is a financially irreversible mistake.
func TestExecuteExchange_EmptyRegionReturns400(t *testing.T) {
	h := &Handler{auth: &mockAuthForExchange{}}

	_, err := h.executeExchange(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"off-1","max_payment_due_usd":"10.00"}`,
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a ClientError, got: %v", err)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, err.Error(), "region is required")
}

// TestExecuteExchange_PermissionConstraintsDenied is the SEC-01 (#1141)
// regression test for the exchange path: a session that passes the bare
// execute:ri-exchange gate (as a constrained permission does) but whose
// per-permission Constraints reject the request must receive a 403 before
// the exchange is submitted to AWS. The constraint set must carry the AWS
// EC2 scope, the request's region, the deployment's registered cloud
// account, and the max_payment_due_usd guardrail as the amount.
func TestExecuteExchange_PermissionConstraintsDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	const deploymentAccountID = "11111111-2222-3333-4444-555555555555"
	userSession := &Session{
		UserID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		Email:  "exchanger@example.com",
	}
	mockAuth.On("ValidateSession", ctx, "exchange-token").Return(userSession, nil)
	mockAuth.On("HasPermissionAPI", ctx, userSession.UserID, "execute", "ri-exchange").Return(true, nil)
	mockAuth.On("HasPermissionForConstraintsAPI", ctx, userSession.UserID, "execute", "ri-exchange",
		mock.MatchedBy(func(sets []auth.PermissionConstraints) bool {
			if len(sets) != 1 {
				return false
			}
			c := sets[0]
			return assert.ObjectsAreEqual([]string{deploymentAccountID}, c.AccountIDs) &&
				assert.ObjectsAreEqual([]string{"aws"}, c.Providers) &&
				assert.ObjectsAreEqual([]string{"ec2"}, c.Services) &&
				assert.ObjectsAreEqual([]string{"eu-central-1"}, c.Regions) &&
				c.MaxPurchaseAmount == 250.50
		})).Return(false, nil)

	h := &Handler{
		auth: mockAuth,
		reshapeAccountResolver: func(_ context.Context) (string, error) {
			return deploymentAccountID, nil
		},
	}
	_, err := h.executeExchange(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer exchange-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"off-1","max_payment_due_usd":"250.50","region":"eu-central-1"}`,
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a ClientError, got: %v", err)
	assert.Equal(t, 403, ce.code)
	assert.Contains(t, ce.Error(), "constraints")
}

// TestExecuteExchange_AccountResolutionErrorFailsClosed pins the fail-closed
// behavior of the SEC-01 AccountIDs dimension: when the deployment's cloud
// account cannot be resolved (STS error, account lookup failure), the
// handler must abort BEFORE the constraint check and the AWS call rather
// than evaluating constraints without the account dimension.
func TestExecuteExchange_AccountResolutionErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	// No HasPermissionForConstraintsAPI expectation: it must NOT be called.
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	userSession := &Session{
		UserID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		Email:  "exchanger@example.com",
	}
	mockAuth.On("ValidateSession", ctx, "exchange-token").Return(userSession, nil)
	mockAuth.On("HasPermissionAPI", ctx, userSession.UserID, "execute", "ri-exchange").Return(true, nil)

	h := &Handler{
		auth: mockAuth,
		reshapeAccountResolver: func(_ context.Context) (string, error) {
			return "", fmt.Errorf("sts get-caller-identity denied")
		},
	}
	_, err := h.executeExchange(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer exchange-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"off-1","max_payment_due_usd":"250.50","region":"eu-central-1"}`,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve cloud account scope")
}

// TestExecuteExchange_UnattributedAccountStillConstrained pins the sentinel
// behavior: when the deployment resolves to no registered cloud account
// (non-AWS host, bootstrap), the constraint set must still carry a non-empty
// AccountIDs list (the unattributed sentinel) so a permission constrained to
// specific accounts denies the exchange instead of matching the auth
// layer's "empty request list = satisfied" rule (fail closed).
func TestExecuteExchange_UnattributedAccountStillConstrained(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	userSession := &Session{
		UserID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		Email:  "exchanger@example.com",
	}
	mockAuth.On("ValidateSession", ctx, "exchange-token").Return(userSession, nil)
	mockAuth.On("HasPermissionAPI", ctx, userSession.UserID, "execute", "ri-exchange").Return(true, nil)
	mockAuth.On("HasPermissionForConstraintsAPI", ctx, userSession.UserID, "execute", "ri-exchange",
		mock.MatchedBy(func(sets []auth.PermissionConstraints) bool {
			return len(sets) == 1 &&
				assert.ObjectsAreEqual([]string{unattributedAccountConstraint}, sets[0].AccountIDs)
		})).Return(false, nil)

	h := &Handler{
		auth: mockAuth,
		reshapeAccountResolver: func(_ context.Context) (string, error) {
			return "", nil
		},
	}
	_, err := h.executeExchange(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer exchange-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"off-1","max_payment_due_usd":"250.50","region":"eu-central-1"}`,
	})
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a ClientError, got: %v", err)
	assert.Equal(t, 403, ce.code)
}

// TestGetExchangeQuote_EmptyRegionResolvesFromSDK pins finding 01-L4:
// getExchangeQuote must resolve the region from the AWS SDK chain when
// the caller omits it, matching getReshapeRecommendations, instead of
// hardcoding us-east-1. We pre-seal awsCfgOnce with a non-default region
// and verify the quote call receives that region (the call fails because
// there is no real AWS SDK, but the region check happens first via body
// validation passing; here we verify no 400 for missing region).
func TestGetExchangeQuote_EmptyRegionResolvesFromSDK(t *testing.T) {
	const cfgRegion = "eu-west-1"
	h := &Handler{auth: &mockAuthForExchange{}}
	// Pre-seal awsCfgOnce so loadAWSConfigWithRegion returns cfgRegion
	// without making a real SDK call.
	h.awsCfgOnce.Do(func() {
		h.awsCfg = aws.Config{Region: cfgRegion}
	})

	// The quote call will fail (no real AWS endpoint), but it must NOT fail
	// with a 400 "region is required" — region resolution from the SDK is
	// the accepted path for quote (non-mutation). The error will be a 500
	// from exchange.GetExchangeQuote failing against no real AWS.
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"off-1"}`,
	})
	// We expect either no error or a 500 from the AWS call — never a 400
	// complaining about a missing region.
	if err != nil {
		ce, ok := IsClientError(err)
		require.True(t, ok, "expected ClientError, got: %v", err)
		assert.NotEqual(t, 400, ce.code,
			"getExchangeQuote must not return 400 for a missing region; it must resolve from the SDK chain")
	}
}

// TestExecuteApprovedExchange_EmptyRecordRegionFails pins finding 01-L4:
// executeApprovedExchange must fail the exchange (not default to us-east-1)
// when the stored record has no region. A hardcoded fallback would execute
// a financially irreversible operation in the wrong region.
func TestExecuteApprovedExchange_EmptyRecordRegionFails(t *testing.T) {
	ctx := context.Background()
	id := "550e8400-e29b-41d4-a716-000000000001"

	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	h := &Handler{config: mockStore}

	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       1000,
		RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	// Record has no region — the handler must call failExchange, not proceed.
	mockStore.On("FailRIExchange", ctx, id, mock.MatchedBy(func(reason string) bool {
		return len(reason) > 0
	})).Return(nil)

	record := &config.RIExchangeRecord{
		ID:               id,
		Region:           "", // intentionally empty
		SourceRIIDs:      []string{"ri-1"},
		TargetOfferingID: "offering-1",
		TargetCount:      1,
		PaymentDue:       "50.00",
	}

	resp, err := h.executeApprovedExchange(ctx, id, record)
	require.NoError(t, err, "executeApprovedExchange surfaces the failure via failExchange, not via error return")
	respMap, ok := resp.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "failed", respMap["status"])
	assert.Contains(t, respMap["reason"], "no region")
}

// TestClassifyRecsAge pins the staleness classification thresholds for
// the reshape freshness banner. The three transitions are:
//
//   - < 12 h  → "" (fresh, no banner)
//   - 12–24 h → "soft" (warning banner)
//   - >= 24 h → "hard" (critical banner)
func TestClassifyRecsAge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"zero age is fresh", 0, ""},
		{"30 minutes is fresh", 30 * time.Minute, ""},
		{"just under soft threshold", reshapeSoftStaleThreshold - time.Minute, ""},
		{"exactly soft threshold", reshapeSoftStaleThreshold, "soft"},
		{"13 hours is soft", 13 * time.Hour, "soft"},
		{"just under hard threshold", reshapeHardStaleThreshold - time.Minute, "soft"},
		{"exactly hard threshold", reshapeHardStaleThreshold, "hard"},
		{"25 hours is hard", 25 * time.Hour, "hard"},
		{"48 hours is hard", 48 * time.Hour, "hard"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyRecsAge(tc.age)
			if got != tc.want {
				t.Errorf("classifyRecsAge(%v) = %q, want %q", tc.age, got, tc.want)
			}
		})
	}
}

// --- Audit actor stamping tests (issue #1009) ---

// TestApproveRIExchange_SessionActorStamped asserts that the session-authed
// approval path passes the session UserID as the actor to TransitionRIExchangeStatus
// so that transitioned_by is set on the ri_exchange_history row.
func TestApproveRIExchange_SessionActorStamped(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	const actorID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const id = "550e8400-e29b-41d4-a716-446655441001"

	adminSession := &Session{UserID: actorID, Email: "admin@example.com"}
	mockAuth.On("ValidateSession", ctx, "admin-bearer").Return(adminSession, nil)
	mockAuth.grantAdmin()

	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID: id, Status: "pending", ApprovalToken: "tok", SourceRIIDs: []string{"ri-1"}, PaymentDue: "10.00",
	}, nil)
	// Actor must be the session UserID.
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", "processing",
		mock.MatchedBy(func(a *string) bool { return a != nil && *a == actorID }),
	).Return(&config.RIExchangeRecord{ID: id, Status: "processing", SourceRIIDs: []string{"ri-1"}, PaymentDue: "10.00"}, nil)
	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD: 1000, RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	mockStore.On("FailRIExchange", ctx, id, mock.AnythingOfType("string")).Return(nil)
	mockStore.On("StampRIExchangeApprovedBy", ctx, id, adminSession.Email).Return(nil)

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer admin-bearer"},
	}
	_, err := (&Handler{config: mockStore, auth: mockAuth}).approveRIExchange(ctx, req, id, "")
	require.NoError(t, err)
}

// TestRejectRIExchange_TokenPathActorIsNil asserts that the token-based rejection
// path passes nil as the actor param so that transitioned_by = NULL on the row.
// Token-based paths have no session, so no human actor can be attributed.
func TestRejectRIExchange_TokenPathActorIsNil(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	const id = "550e8400-e29b-41d4-a716-446655441002"

	mockStore.On("GetRIExchangeRecord", ctx, id).Return(&config.RIExchangeRecord{
		ID: id, Status: "pending", ApprovalToken: "tok",
	}, nil)
	// Token path: actor must be nil. Canonical spelling used (#1277 follow-up).
	mockStore.On("TransitionRIExchangeStatus", ctx, id, "pending", config.StatusCanceled,
		(*string)(nil),
	).Return(&config.RIExchangeRecord{ID: id, Status: config.StatusCanceled}, nil)

	_, err := (&Handler{config: mockStore}).rejectRIExchange(ctx, id, "tok")
	require.NoError(t, err)
}

// ─── H1: checkDailyCap fails closed on unparseable paymentDue ─────────────────

// TestCheckDailyCap_UnparseablePaymentDue_FailsClosed is the regression test
// for H1: checkDailyCap must return a blocking reason when paymentDueStr cannot
// be parsed, never treat it as $0 (which would allow an unknown-cost exchange
// to proceed when the daily cap might be exceeded).
func TestCheckDailyCap_UnparseablePaymentDue_FailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		paymentDueStr string
	}{
		{"not-a-number", "not-a-number"},
		{"empty string", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := checkDailyCap("100.00", tc.paymentDueStr, 500.0)
			assert.NotEmpty(t, reason,
				"checkDailyCap must fail closed when paymentDueStr=%q cannot be parsed", tc.paymentDueStr)
			assert.Contains(t, reason, "could not parse payment due",
				"reason must reference the payment-due parsing failure, not treat it as $0")
		})
	}
}

// TestCheckDailyCap_ValidPaymentDue_WithinCap_Passes verifies the happy path:
// valid parseable amounts within the daily cap must return an empty reason.
func TestCheckDailyCap_ValidPaymentDue_WithinCap_Passes(t *testing.T) {
	t.Parallel()
	reason := checkDailyCap("100.00", "50.00", 500.0)
	assert.Empty(t, reason, "within-cap exchange must not be blocked")
}

// ─── H2: effectiveCap bounded by daily headroom ───────────────────────────────

// TestExecuteApprovedExchange_EffectiveCap_BoundedByDailyHeadroom is the
// regression test for H2: when dailyCap-dailySpent < perExchangeCap, the cap
// passed to Execute (MaxPaymentDueUSD) must be the daily headroom, not the full
// perExchangeCap. This prevents a fresh re-quote from accepting an amount that
// exceeds the daily budget.
func TestExecuteApprovedExchange_EffectiveCap_BoundedByDailyHeadroom(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000000012"

	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	// dailySpent=$450, dailyCap=$500, perExchangeCap=$100 -> headroom=$50
	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("450.00", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       500,
		RIExchangeMaxPerExchangeUSD: 100,
	}, nil)

	var capturedReq exchange.ExchangeExecuteRequest
	h := &Handler{
		config: mockStore,
		executeExchangeFn: func(_ context.Context, req exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
			capturedReq = req
			return "exch-h2-handler", &exchange.ExchangeQuoteSummary{PaymentDueUSDStr: "40.000000"}, nil
		},
	}

	mockStore.On("CompleteRIExchangeWithPayment", ctx, id, "exch-h2-handler", "40.000000").Return(nil)

	record := &config.RIExchangeRecord{
		ID:               id,
		Region:           "us-east-1",
		SourceRIIDs:      []string{"ri-1"},
		TargetOfferingID: "offering-1",
		TargetCount:      1,
		PaymentDue:       "40.00",
	}

	_, err := h.executeApprovedExchange(ctx, id, record)
	require.NoError(t, err)

	// MaxPaymentDueUSD must equal headroom ($50), not perExchangeCap ($100).
	require.NotNil(t, capturedReq.MaxPaymentDueUSD,
		"MaxPaymentDueUSD must be set on the Execute request")
	expectedCap := new(big.Rat).SetFrac64(50, 1)
	assert.Equal(t, 0, capturedReq.MaxPaymentDueUSD.Cmp(expectedCap),
		"MaxPaymentDueUSD must be daily headroom ($50), got $%s",
		capturedReq.MaxPaymentDueUSD.FloatString(2))
}

// ─── H3: ledger records fresh accepted amount ─────────────────────────────────

// TestExecuteApprovedExchange_AcceptedAmountFromFreshQuote is the regression
// test for H3: CompleteRIExchangeWithPayment must be called with the amount
// AWS confirmed during Execute (freshQ.PaymentDueUSDStr), not the stale
// record.PaymentDue stored from the pre-execution quote.
func TestExecuteApprovedExchange_AcceptedAmountFromFreshQuote(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000000013"

	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       1000,
		RIExchangeMaxPerExchangeUSD: 500,
	}, nil)

	// Execute returns a fresh quote ($35.50) different from pre-execution ($30.00).
	h := &Handler{
		config: mockStore,
		executeExchangeFn: func(_ context.Context, _ exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
			return "exch-h3-fresh", &exchange.ExchangeQuoteSummary{
				PaymentDueUSDStr: "35.500000",
			}, nil
		},
	}

	// The FRESH amount "35.500000" must reach CompleteRIExchangeWithPayment,
	// not the stale "30.00" from record.PaymentDue.
	mockStore.On("CompleteRIExchangeWithPayment", ctx, id, "exch-h3-fresh", "35.500000").Return(nil)

	record := &config.RIExchangeRecord{
		ID:               id,
		Region:           "us-east-1",
		SourceRIIDs:      []string{"ri-1"},
		TargetOfferingID: "offering-1",
		TargetCount:      1,
		PaymentDue:       "30.00", // stale pre-execution quote
	}

	resp, err := h.executeApprovedExchange(ctx, id, record)
	require.NoError(t, err)
	respMap, ok := resp.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "completed", respMap["status"])
}

// ─── H4: persistent ledger failure returns error ──────────────────────────────

// TestExecuteApprovedExchange_LedgerWriteFailure_ReturnsError is the regression
// test for H4: when CompleteRIExchangeWithPayment fails persistently (all retry
// attempts exhausted) after money has already moved, executeApprovedExchange must
// return an error rather than silently logging and returning a success response.
// The caller (approveRIExchange) must see this as an error so it can return
// HTTP 500 and the operator knows the ledger is inconsistent.
func TestExecuteApprovedExchange_LedgerWriteFailure_ReturnsError(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000000014"

	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	mockStore.On("GetRIExchangeDailySpend", mock.Anything, mock.Anything).Return("0", nil)
	mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD:       1000,
		RIExchangeMaxPerExchangeUSD: 500,
	}, nil)

	h := &Handler{
		config: mockStore,
		executeExchangeFn: func(_ context.Context, _ exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
			return "exch-h4-test", &exchange.ExchangeQuoteSummary{PaymentDueUSDStr: "0"}, nil
		},
	}

	// Fail all three retry attempts.
	mockStore.On("CompleteRIExchangeWithPayment", ctx, id, "exch-h4-test", "0").
		Return(fmt.Errorf("DB write failed")).Times(3)

	record := &config.RIExchangeRecord{
		ID:               id,
		Region:           "us-east-1",
		SourceRIIDs:      []string{"ri-1"},
		TargetOfferingID: "offering-1",
		TargetCount:      1,
		PaymentDue:       "0",
	}

	_, err := h.executeApprovedExchange(ctx, id, record)
	require.Error(t, err, "persistent ledger failure must propagate as a non-nil error")
	assert.Contains(t, err.Error(), "ledger update failed",
		"error must describe the ledger failure for operator triage")
	assert.Contains(t, err.Error(), "exch-h4-test",
		"error must include the exchange ID for operator correlation with AWS")
}
