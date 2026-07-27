package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/logging"
	"github.com/aws/aws-lambda-go/events"
)

// API Key handlers
//
// All handlers gate on `requirePermission` for the `api-keys` resource so the
// permission system controls who can mint/list/revoke keys, not just who's
// authenticated. Keys remain owner-scoped — the handler still uses
// session.UserID from requirePermission's return value to scope the operation
// to the calling user. There is no separate "revoke" action in the permission
// model, so revoke reuses "delete".

// listAPIKeys handles GET /api/api-keys.
func (h *Handler) listAPIKeys(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "api-keys")
	if err != nil {
		return nil, err
	}

	keys, err := h.auth.ListUserAPIKeysAPI(ctx, session.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to list API keys: %w", err)
	}

	return keys, nil
}

// listAPIKeysUsageStats handles GET /api/api-keys/usage-stats.
//
// Returns a section-level summary scoped to the calling user's own keys:
// total active, total requests (24h + lifetime), and the top-3 keys by
// 24h activity. Scope is identical to listAPIKeys -- both gate on the
// same view/api-keys permission so the section header and the row list
// can never disagree about visibility.
func (h *Handler) listAPIKeysUsageStats(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "api-keys")
	if err != nil {
		return nil, err
	}

	stats, err := h.auth.GetAPIKeysUsageStatsAPI(ctx, session.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to load API key usage stats: %w", err)
	}

	return stats, nil
}

// createAPIKey handles POST /api/api-keys.
func (h *Handler) createAPIKey(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "create", "api-keys")
	if err != nil {
		return nil, err
	}

	// Rate limiting: 30 admin operations per user per minute.
	// Fail-open: this endpoint is already behind requirePermission so the auth
	// surface is lower than the unauthenticated credential endpoints. Emit a
	// high-severity alert so the fail-open window is observable (02-M1).
	if h.rateLimiter != nil {
		allowed, rateLimitErr := h.rateLimiter.AllowWithUser(ctx, session.UserID, "admin")
		if rateLimitErr != nil {
			logging.Errorf("ALERT: rate limiter error on admin operation for user %s; proceeding fail-open (02-M1): %v",
				session.UserID, rateLimitErr)
		} else if !allowed {
			return nil, NewClientError(429, "too many requests, please slow down")
		}
	}

	var createReq CreateAPIKeyRequest
	err = json.Unmarshal([]byte(req.Body), &createReq)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	result, err := h.auth.CreateAPIKeyAPI(ctx, session.UserID, createReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	return result, nil
}

// deleteAPIKey handles DELETE /api/api-keys/{id}.
func (h *Handler) deleteAPIKey(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "delete", "api-keys")
	if err != nil {
		return nil, err
	}

	keyID, err := apiKeyIDFromPath(req.RequestContext.HTTP.Path, false)
	if err != nil {
		return nil, err
	}

	if err := h.auth.DeleteAPIKeyAPI(ctx, session.UserID, keyID); err != nil {
		return nil, fmt.Errorf("failed to delete API key: %w", err)
	}

	return map[string]string{"status": "deleted"}, nil
}

// revokeAPIKey handles POST /api/api-keys/{id}/revoke.
func (h *Handler) revokeAPIKey(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	// No "revoke" verb in the permission model — reuse "delete".
	session, err := h.requirePermission(ctx, req, "delete", "api-keys")
	if err != nil {
		return nil, err
	}

	keyID, err := apiKeyIDFromPath(req.RequestContext.HTTP.Path, true)
	if err != nil {
		return nil, err
	}

	if err := h.auth.RevokeAPIKeyAPI(ctx, session.UserID, keyID); err != nil {
		return nil, fmt.Errorf("failed to revoke API key: %w", err)
	}

	return map[string]string{"status": "revoked"}, nil
}

// apiKeyIDFromPath extracts the key ID from /api/api-keys/{id} or
// /api/api-keys/{id}/revoke depending on `hasTrailingAction`.
func apiKeyIDFromPath(path string, hasTrailingAction bool) (string, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	minLen := 3
	if hasTrailingAction {
		minLen = 4
	}
	if len(parts) < minLen {
		return "", NewClientError(400, "invalid path: missing key ID")
	}
	idx := len(parts) - 1
	if hasTrailingAction {
		idx = len(parts) - 2
	}
	keyID := parts[idx]
	if err := validateUUID(keyID); err != nil {
		return "", err
	}
	return keyID, nil
}

// Helper function to format time pointer as string.
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}
