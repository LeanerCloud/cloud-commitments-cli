package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockRateLimiter is a mock implementation of RateLimiterInterface.
type MockRateLimiter struct {
	mock.Mock
}

func (m *MockRateLimiter) Allow(ctx context.Context, key, limitType string) (bool, error) {
	args := m.Called(ctx, key, limitType)
	return args.Bool(0), args.Error(1)
}

func (m *MockRateLimiter) AllowWithIP(ctx context.Context, ip, limitType string) (bool, error) {
	args := m.Called(ctx, ip, limitType)
	return args.Bool(0), args.Error(1)
}

func (m *MockRateLimiter) AllowWithEmail(ctx context.Context, email, limitType string) (bool, error) {
	args := m.Called(ctx, email, limitType)
	return args.Bool(0), args.Error(1)
}

func (m *MockRateLimiter) AllowWithUser(ctx context.Context, userID, limitType string) (bool, error) {
	args := m.Called(ctx, userID, limitType)
	return args.Bool(0), args.Error(1)
}

func TestHandler_listAPIKeys_Success(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	expectedKeys := []map[string]interface{}{
		{"key_id": "key-1", "name": "Test Key 1"},
		{"key_id": "key-2", "name": "Test Key 2"},
	}
	mockAuth.On("ListUserAPIKeysAPI", ctx, "user-123").Return(expectedKeys, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
	}

	result, err := handler.listAPIKeys(ctx, req)
	require.NoError(t, err)

	keys, ok := result.([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, keys, 2)
}

func TestHandler_listAPIKeys_NoAuthService(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.listAPIKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication service not configured")
}

func TestHandler_listAPIKeys_NoToken(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{},
	}

	_, err := handler.listAPIKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no authorization token provided")
}

func TestHandler_listAPIKeys_InvalidSession(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	mockAuth.On("ValidateSession", ctx, "invalid-token").Return(nil, errors.New("invalid session"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer invalid-token",
		},
	}

	_, err := handler.listAPIKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid session")
}

func TestHandler_listAPIKeys_ServiceError(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("ListUserAPIKeysAPI", ctx, "user-123").Return(nil, errors.New("database error"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
	}

	_, err := handler.listAPIKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list API keys")
}

func TestHandler_createAPIKey_Success(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockRateLimiter := new(MockRateLimiter)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockRateLimiter.On("AllowWithUser", ctx, "user-123", "admin").Return(true, nil)

	expectedResult := map[string]string{"api_key": "new-key-value", "key_id": "key-123"}
	mockAuth.On("CreateAPIKeyAPI", ctx, "user-123", mock.Anything).Return(expectedResult, nil)

	handler := &Handler{auth: mockAuth, rateLimiter: mockRateLimiter}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		Body: `{"name": "My API Key"}`,
	}

	result, err := handler.createAPIKey(ctx, req)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHandler_createAPIKey_NoAuthService(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication service not configured")
}

func TestHandler_createAPIKey_NoToken(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no authorization token provided")
}

func TestHandler_createAPIKey_InvalidSession(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	mockAuth.On("ValidateSession", ctx, "bad-token").Return(nil, errors.New("invalid session"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer bad-token",
		},
	}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid session")
}

func TestHandler_createAPIKey_RateLimited(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockRateLimiter := new(MockRateLimiter)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockRateLimiter.On("AllowWithUser", ctx, "user-123", "admin").Return(false, nil)

	handler := &Handler{auth: mockAuth, rateLimiter: mockRateLimiter}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		Body: `{"name": "My API Key"}`,
	}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many requests")
}

func TestHandler_createAPIKey_InvalidBody(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		Body: `invalid json`,
	}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request body")
}

func TestHandler_createAPIKey_ServiceError(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("CreateAPIKeyAPI", ctx, "user-123", mock.Anything).Return(nil, errors.New("creation failed"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		Body: `{"name": "My API Key"}`,
	}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create API key")
}

func TestHandler_deleteAPIKey_Success(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("DeleteAPIKeyAPI", ctx, "user-123", "11111111-1111-1111-1111-111111111111").Return(nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111",
			},
		},
	}

	result, err := handler.deleteAPIKey(ctx, req)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "deleted", resultMap["status"])
}

func TestHandler_deleteAPIKey_NoAuthService(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication service not configured")
}

func TestHandler_deleteAPIKey_NoToken(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no authorization token provided")
}

func TestHandler_deleteAPIKey_InvalidPath(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api",
			},
		},
	}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing key ID")
}

func TestHandler_deleteAPIKey_InvalidUUID(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/invalid-uuid",
			},
		},
	}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a valid UUID")
}

func TestHandler_deleteAPIKey_ServiceError(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("DeleteAPIKeyAPI", ctx, "user-123", "11111111-1111-1111-1111-111111111111").Return(errors.New("delete failed"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111",
			},
		},
	}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete API key")
}

func TestHandler_revokeAPIKey_Success(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("RevokeAPIKeyAPI", ctx, "user-123", "11111111-1111-1111-1111-111111111111").Return(nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111/revoke",
			},
		},
	}

	result, err := handler.revokeAPIKey(ctx, req)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "revoked", resultMap["status"])
}

func TestHandler_revokeAPIKey_NoAuthService(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication service not configured")
}

func TestHandler_revokeAPIKey_NoToken(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no authorization token provided")
}

func TestHandler_revokeAPIKey_InvalidPath(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api",
			},
		},
	}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing key ID")
}

func TestHandler_revokeAPIKey_InvalidUUID(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/invalid-uuid/revoke",
			},
		},
	}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a valid UUID")
}

func TestHandler_revokeAPIKey_ServiceError(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("RevokeAPIKeyAPI", ctx, "user-123", "11111111-1111-1111-1111-111111111111").Return(errors.New("revoke failed"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111/revoke",
			},
		},
	}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to revoke API key")
}

// Permission-denial tests: a non-admin user without the required api-keys
// permission must be rejected with 403 BEFORE any owner-scoped operation runs.

func TestHandler_listAPIKeys_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "viewer-1", Email: "viewer@example.com"}
	mockAuth.On("ValidateSession", ctx, "viewer-token").Return(session, nil)
	mockAuth.On("HasPermissionAPI", ctx, "viewer-1", "view", "api-keys").Return(false, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer viewer-token"},
	}

	_, err := handler.listAPIKeys(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	mockAuth.AssertNotCalled(t, "ListUserAPIKeysAPI", mock.Anything, mock.Anything)
}

func TestHandler_createAPIKey_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "viewer-1", Email: "viewer@example.com"}
	mockAuth.On("ValidateSession", ctx, "viewer-token").Return(session, nil)
	mockAuth.On("HasPermissionAPI", ctx, "viewer-1", "create", "api-keys").Return(false, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer viewer-token"},
		Body:    `{"name": "should not happen"}`,
	}

	_, err := handler.createAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	mockAuth.AssertNotCalled(t, "CreateAPIKeyAPI", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandler_deleteAPIKey_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "viewer-1", Email: "viewer@example.com"}
	mockAuth.On("ValidateSession", ctx, "viewer-token").Return(session, nil)
	mockAuth.On("HasPermissionAPI", ctx, "viewer-1", "delete", "api-keys").Return(false, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer viewer-token"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111",
			},
		},
	}

	_, err := handler.deleteAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	mockAuth.AssertNotCalled(t, "DeleteAPIKeyAPI", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandler_revokeAPIKey_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "viewer-1", Email: "viewer@example.com"}
	mockAuth.On("ValidateSession", ctx, "viewer-token").Return(session, nil)
	// Revoke reuses the "delete" verb in the permission model.
	mockAuth.On("HasPermissionAPI", ctx, "viewer-1", "delete", "api-keys").Return(false, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer viewer-token"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Path: "/api/api-keys/11111111-1111-1111-1111-111111111111/revoke",
			},
		},
	}

	_, err := handler.revokeAPIKey(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	mockAuth.AssertNotCalled(t, "RevokeAPIKeyAPI", mock.Anything, mock.Anything, mock.Anything)
}

// listAPIKeysUsageStats handler -- section-level summary (issue #340/#344
// deferred sub-task). Mirrors the listAPIKeys test layout: happy path for
// an authenticated session, a no-auth-service failure, a service-error
// passthrough, and a permission-denied path for a caller lacking the
// view/api-keys permission.

func TestHandler_listAPIKeysUsageStats_Success(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()

	expectedStats := map[string]any{
		"total_active":            2,
		"total_requests_24h":      int64(15),
		"total_requests_lifetime": int64(120),
		"top_keys":                []map[string]any{{"id": "key-1", "name": "k1", "key_prefix": "abc12345", "request_count_24h": int64(10)}},
	}
	mockAuth.On("GetAPIKeysUsageStatsAPI", ctx, "user-123").Return(expectedStats, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer test-token"},
	}

	result, err := handler.listAPIKeysUsageStats(ctx, req)
	require.NoError(t, err)
	got, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 2, got["total_active"])
	mockAuth.AssertCalled(t, "GetAPIKeysUsageStatsAPI", ctx, "user-123")
}

func TestHandler_listAPIKeysUsageStats_NoAuthService(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}

	req := &events.LambdaFunctionURLRequest{}

	_, err := handler.listAPIKeysUsageStats(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication service not configured")
}

func TestHandler_listAPIKeysUsageStats_ServiceError(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "user-123", Email: "user@example.com"}
	mockAuth.On("ValidateSession", ctx, "test-token").Return(session, nil)
	mockAuth.grantAdmin()
	mockAuth.On("GetAPIKeysUsageStatsAPI", ctx, "user-123").Return(nil, errors.New("database error"))

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer test-token"},
	}

	_, err := handler.listAPIKeysUsageStats(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load API key usage stats")
}

func TestHandler_listAPIKeysUsageStats_PermissionDenied(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)

	session := &Session{UserID: "viewer-1", Email: "viewer@example.com"}
	mockAuth.On("ValidateSession", ctx, "viewer-token").Return(session, nil)
	mockAuth.On("HasPermissionAPI", ctx, "viewer-1", "view", "api-keys").Return(false, nil)

	handler := &Handler{auth: mockAuth}

	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer viewer-token"},
	}

	_, err := handler.listAPIKeysUsageStats(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	mockAuth.AssertNotCalled(t, "GetAPIKeysUsageStatsAPI", mock.Anything, mock.Anything)
}

func TestFormatTimePtr(t *testing.T) {
	t.Run("nil pointer returns empty string", func(t *testing.T) {
		result := formatTimePtr(nil)
		assert.Empty(t, result)
	})

	t.Run("valid time formats correctly", func(t *testing.T) {
		testTime := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
		result := formatTimePtr(&testTime)
		assert.Equal(t, "2024-06-15T10:30:00Z", result)
	})

	t.Run("time with timezone formats correctly", func(t *testing.T) {
		loc, _ := time.LoadLocation("America/New_York")
		testTime := time.Date(2024, 6, 15, 10, 30, 0, 0, loc)
		result := formatTimePtr(&testTime)
		assert.Contains(t, result, "2024-06-15T10:30:00")
	})
}

// TestRequirePermission_UserAPIKey is the regression test for issue #1142:
// user API keys previously hit a hard 401 in requirePermission (only the
// admin API key and bearer-token sessions were recognized), so the per-key
// scoped permissions persisted at key-creation time were never enforced.
// requirePermission now authorizes user API keys against their effective
// permissions via HasAPIKeyPermissionAPI.
func TestRequirePermission_UserAPIKey(t *testing.T) {
	ctx := context.Background()

	// The exact failing request shape: x-api-key header carrying a user API
	// key, no bearer token (Authorization / X-Authorization absent).
	newReq := func() *events.LambdaFunctionURLRequest {
		return &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"x-api-key": "cudly-user-key"},
		}
	}

	t.Run("regression #1142: key with in-scope permission is authorized as its owner", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		t.Cleanup(func() { mockAuth.AssertExpectations(t) })
		mockAuth.On("HasAPIKeyPermissionAPI", ctx, "cudly-user-key", "view", "recommendations").
			Return("user-123", "key-abc", true, nil)

		handler := &Handler{auth: mockAuth}
		session, err := handler.requirePermission(ctx, newReq(), "view", "recommendations")
		require.NoError(t, err)
		require.NotNil(t, session)
		assert.Equal(t, "user-123", session.UserID)
		// Key ID must be threaded so requirePermissionConstraints can evaluate
		// key-scoped caps (adversarial-review F2).
		assert.Equal(t, "key-abc", session.UserAPIKeyID)
	})

	t.Run("regression #1142: scoped key is denied an out-of-scope permission with 403", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		t.Cleanup(func() { mockAuth.AssertExpectations(t) })
		mockAuth.On("HasAPIKeyPermissionAPI", ctx, "cudly-user-key", "execute", "purchases").
			Return("user-123", "", false, nil)

		handler := &Handler{auth: mockAuth}
		session, err := handler.requirePermission(ctx, newReq(), "execute", "purchases")
		require.Error(t, err)
		assert.Nil(t, session)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 403, ce.code)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("invalid key with no bearer token is rejected with 401", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		t.Cleanup(func() { mockAuth.AssertExpectations(t) })
		mockAuth.On("HasAPIKeyPermissionAPI", ctx, "cudly-user-key", "view", "recommendations").
			Return("", "", false, errors.New("invalid API key"))

		handler := &Handler{auth: mockAuth}
		session, err := handler.requirePermission(ctx, newReq(), "view", "recommendations")
		require.Error(t, err)
		assert.Nil(t, session)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 401, ce.code)
	})

	t.Run("invalid key falls through to a valid bearer session", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		t.Cleanup(func() { mockAuth.AssertExpectations(t) })
		mockAuth.On("HasAPIKeyPermissionAPI", ctx, "stale-key", "view", "recommendations").
			Return("", "", false, errors.New("invalid API key"))
		mockAuth.On("ValidateSession", ctx, "session-token").
			Return(&Session{UserID: "user-456"}, nil)
		mockAuth.On("HasPermissionAPI", ctx, "user-456", "view", "recommendations").
			Return(true, nil)

		handler := &Handler{auth: mockAuth}
		req := &events.LambdaFunctionURLRequest{
			Headers: map[string]string{
				"x-api-key":     "stale-key",
				"Authorization": "Bearer session-token",
			},
		}
		session, err := handler.requirePermission(ctx, req, "view", "recommendations")
		require.NoError(t, err)
		require.NotNil(t, session)
		assert.Equal(t, "user-456", session.UserID)
	})

	t.Run("admin API key still bypasses the per-key permission lookup", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		t.Cleanup(func() { mockAuth.AssertExpectations(t) })

		handler := &Handler{auth: mockAuth, apiKey: "admin-infra-key"}
		req := &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"x-api-key": "admin-infra-key"},
		}
		session, err := handler.requirePermission(ctx, req, "view", "recommendations")
		require.NoError(t, err)
		require.NotNil(t, session)
		assert.Equal(t, apiKeyAdminUserID, session.UserID)
		mockAuth.AssertNotCalled(t, "HasAPIKeyPermissionAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// TestGetRecommendations_UserAPIKey exercises the real failing scenario from
// issue #1142 end to end at the handler layer: GET /api/recommendations with
// only an x-api-key user key returned 401 before the fix even when the key
// was scoped to view:recommendations.
func TestGetRecommendations_UserAPIKey(t *testing.T) {
	ctx := context.Background()

	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("HasAPIKeyPermissionAPI", ctx, "cudly-user-key", "view", "recommendations").
		Return("user-123", "key-abc", true, nil)
	// Account scoping resolves against the key's owning user.
	mockAuth.On("GetAllowedAccountsAPI", ctx, "user-123").
		Return([]string(nil), nil)

	mockScheduler := new(MockScheduler)
	t.Cleanup(func() { mockScheduler.AssertExpectations(t) })
	mockScheduler.On("ListRecommendations", ctx, mock.Anything).
		Return([]config.RecommendationRecord{}, nil)

	handler := &Handler{auth: mockAuth, scheduler: mockScheduler}
	req := &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"x-api-key": "cudly-user-key"},
	}

	result, err := handler.getRecommendations(ctx, req, map[string]string{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Summary.TotalCount)
}
