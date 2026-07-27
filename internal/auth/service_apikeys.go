package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateAPIKey creates a new user API key with scoped permissions
// Returns the full API key (shown only once), key info, and error.
func (s *Service) CreateAPIKey(ctx context.Context, userID, name string, permissions []Permission, expiresAt *time.Time) (string, *UserAPIKey, error) {
	// Validate user exists and is active
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, fmt.Errorf("user not found")
		}
		return "", nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return "", nil, fmt.Errorf("user not found")
	}
	if !user.Active {
		return "", nil, fmt.Errorf("user account is not active")
	}

	// Validate name
	if name == "" {
		return "", nil, fmt.Errorf("API key name is required")
	}

	// Generate a secure random key (32 bytes = 256 bits)
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", nil, fmt.Errorf("failed to generate random key: %w", err)
	}

	// Base64 encode to create the API key
	apiKey := base64.RawURLEncoding.EncodeToString(keyBytes)

	// Compute SHA-256 hash of the key for storage
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := base64.RawURLEncoding.EncodeToString(hash[:])

	// Extract key prefix (first 8 chars) for display. The key is always 43 chars
	// (32 random bytes base64url-encoded), so [:8] is safe today. The guard makes
	// the function robust to future changes in key-generation length (03-L2).
	keyPrefix := apiKey
	if len(apiKey) > 8 {
		keyPrefix = apiKey[:8]
	}

	// Validate permissions - ensure they don't exceed user's permissions
	if err := s.validateAPIKeyPermissions(ctx, user, permissions); err != nil {
		return "", nil, fmt.Errorf("invalid permissions: %w", err)
	}

	// Create UserAPIKey record
	now := time.Now()
	keyID := uuid.New().String()

	userAPIKey := &UserAPIKey{
		ID:          keyID,
		UserID:      userID,
		Name:        name,
		KeyPrefix:   keyPrefix,
		KeyHash:     keyHash,
		Permissions: permissions,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
		LastUsedAt:  nil,
		IsActive:    true,
	}

	// Store the API key
	if err := s.store.CreateAPIKey(ctx, userAPIKey); err != nil {
		return "", nil, fmt.Errorf("failed to create API key: %w", err)
	}

	logging.Infof("Created API key %s for user %s", keyPrefix, userID)

	return apiKey, userAPIKey, nil
}

// validateAPIKeyPermissions ensures the key's permissions don't exceed the
// user's permissions. Administrators-group members hold {admin, *}, so the
// per-permission HasPermission check below already passes for every requested
// permission; no role-based short-circuit is needed.
func (s *Service) validateAPIKeyPermissions(ctx context.Context, user *User, permissions []Permission) error {
	// Get user's auth context to check their permissions
	authCtx, err := s.GetAuthContext(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("failed to get user permissions: %w", err)
	}

	// Validate each requested permission
	for _, perm := range permissions {
		if !authCtx.HasPermission(perm.Action, perm.Resource) {
			return fmt.Errorf("user does not have permission for action=%s resource=%s", perm.Action, perm.Resource)
		}
	}

	return nil
}

// ListUserAPIKeys retrieves all API keys for a user.
func (s *Service) ListUserAPIKeys(ctx context.Context, userID string) ([]*UserAPIKey, error) {
	// Validate user exists
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	keys, err := s.store.ListAPIKeysByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list API keys: %w", err)
	}

	return keys, nil
}

// GetAPIKeyByHash retrieves an API key by its hash (for authentication).
func (s *Service) GetAPIKeyByHash(ctx context.Context, keyHash string) (*UserAPIKey, error) {
	key, err := s.store.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // treat not-found as nil key; callers check for nil
		}
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}

	return key, nil
}

// authorizeAPIKeyAccess looks up the key and the caller, then returns the key
// together with a permission error if the caller is neither the key owner nor
// an admin. The action string ("revoke" / "delete") is used only in the error
// message so callers get a context-specific message. Pulled out of
// RevokeAPIKey and DeleteAPIKey to deduplicate the identical ownership check
// and keep both functions under the cyclomatic limit.
func (s *Service) authorizeAPIKeyAccess(ctx context.Context, userID, keyID, action string) (*UserAPIKey, error) {
	key, err := s.store.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("API key not found")
		}
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}
	if key == nil {
		return nil, fmt.Errorf("API key not found")
	}

	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	if key.UserID != userID {
		isAdmin, err := s.UserHasAdminCapability(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to check admin capability: %w", err)
		}
		if !isAdmin {
			return nil, fmt.Errorf("unauthorized: cannot %s another user's API key", action)
		}
	}

	return key, nil
}

// RevokeAPIKey deactivates an API key (soft delete).
func (s *Service) RevokeAPIKey(ctx context.Context, userID, keyID string) error {
	key, err := s.authorizeAPIKeyAccess(ctx, userID, keyID, "revoke")
	if err != nil {
		return err
	}

	// Revoke the key
	key.IsActive = false
	if err := s.store.UpdateAPIKey(ctx, key); err != nil {
		return fmt.Errorf("failed to revoke API key: %w", err)
	}

	logging.Infof("Revoked API key %s for user %s", key.KeyPrefix, key.UserID)

	return nil
}

// DeleteAPIKey permanently deletes an API key.
func (s *Service) DeleteAPIKey(ctx context.Context, userID, keyID string) error {
	key, err := s.authorizeAPIKeyAccess(ctx, userID, keyID, "delete")
	if err != nil {
		return err
	}

	// Delete the key
	if err := s.store.DeleteAPIKey(ctx, keyID); err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	logging.Infof("Deleted API key %s for user %s", key.KeyPrefix, key.UserID)

	return nil
}

// validateAPIKeyStatus checks that the key is active and not expired.
func validateAPIKeyStatus(key *UserAPIKey) error {
	if !key.IsActive {
		return fmt.Errorf("API key is revoked")
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return fmt.Errorf("API key has expired")
	}
	return nil
}

// lookupAPIKeyUser retrieves and validates the user associated with an API key.
func (s *Service) lookupAPIKeyUser(ctx context.Context, userID string) (*User, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user account not found")
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("user account not found")
	}
	if !user.Active {
		return nil, fmt.Errorf("user account is not active")
	}
	return user, nil
}

// ValidateUserAPIKey validates an API key and returns the key info and associated user.
func (s *Service) ValidateUserAPIKey(ctx context.Context, apiKey string) (*UserAPIKey, *User, error) {
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := base64.RawURLEncoding.EncodeToString(hash[:])

	key, err := s.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate API key: %w", err)
	}
	if key == nil {
		return nil, nil, fmt.Errorf("invalid API key")
	}

	// Constant-time comparison after fetch to close the SQL-equality timing oracle.
	// The DB lookup already found the row by hash; this redundant check ensures the
	// hot path does not leak timing information through early return differences.
	if subtle.ConstantTimeCompare([]byte(key.KeyHash), []byte(keyHash)) != 1 {
		return nil, nil, fmt.Errorf("invalid API key")
	}

	err = validateAPIKeyStatus(key)
	if err != nil {
		return nil, nil, err
	}

	user, err := s.lookupAPIKeyUser(ctx, key.UserID)
	if err != nil {
		return nil, nil, err
	}

	// Record usage (last_used_at + counters) asynchronously to avoid
	// blocking the authentication hot path. singleflight.Group ensures at
	// most one in-flight DB write per keyID at any moment: subsequent
	// concurrent requests for the same key are deduplicated rather than
	// spawning an unbounded number of goroutines (DoS amplifier on a
	// revoked key). The store does an atomic single-row UPDATE -- see
	// PostgresStore.RecordAPIKeyUsage for the rolling-24h-window logic.
	keyID := key.ID
	go func() {
		if _, sfErr, _ := s.lastUsedSFG.Do(keyID, func() (any, error) {
			defer func() {
				if r := recover(); r != nil {
					logging.Warnf("service_apikeys: RecordUsage goroutine panic: %v", r)
				}
			}()
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.RecordUsage(updateCtx, keyID); err != nil {
				logging.Debugf("Failed to record API key usage for key %s: %v", keyID, err)
			}
			return nil, nil
		}); sfErr != nil {
			logging.Debugf("service_apikeys: lastUsedSFG returned error for key %s: %v", keyID, sfErr)
		}
	}()

	return key, user, nil
}

// UpdateLastUsed updates only the last used timestamp for an API key.
// Retained for backwards compatibility; new code paths should use
// RecordUsage so the request_count_* counters stay current.
func (s *Service) UpdateLastUsed(ctx context.Context, keyID string) error {
	return s.store.UpdateAPIKeyLastUsed(ctx, keyID)
}

// RecordUsage updates last_used_at and increments both the lifetime and
// rolling-24h request counters for the key. See
// PostgresStore.RecordAPIKeyUsage for the atomic SQL.
func (s *Service) RecordUsage(ctx context.Context, keyID string) error {
	return s.store.RecordAPIKeyUsage(ctx, keyID)
}

// computeEffectivePermissionsFromAuthCtx returns the subset of key permissions
// that the owner's authCtx also grants at the action/resource level, keeping
// the key's own constraint limits. If the key has no specific permissions the
// owner's full permission set is returned (key inherits owner). The result
// carries the key's constraints, not the owner's; callers that need both
// sources must check ownerAuthCtx.Permissions independently.
func computeEffectivePermissionsFromAuthCtx(key *UserAPIKey, authCtx *AuthContext) []Permission {
	if len(key.Permissions) == 0 {
		return authCtx.Permissions
	}
	effectivePerms := make([]Permission, 0, len(key.Permissions))
	for _, keyPerm := range key.Permissions {
		if authCtx.HasPermission(keyPerm.Action, keyPerm.Resource) {
			effectivePerms = append(effectivePerms, keyPerm)
		}
	}
	return effectivePerms
}

// ComputeEffectivePermissions computes the intersection of API key permissions and user permissions.
// This ensures an API key cannot grant more permissions than the user has.
//
// Administrators-group members carry {admin, *}: with no key-specific
// permissions their full {admin, *} context is returned, and a scoped admin
// key's permissions all pass the HasPermission intersection below, so the
// group-derived path preserves the previous role == admin behavior without a
// special case.
func (s *Service) ComputeEffectivePermissions(ctx context.Context, apiKey *UserAPIKey, user *User) ([]Permission, error) {
	authCtx, err := s.GetAuthContext(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user auth context: %w", err)
	}
	return computeEffectivePermissionsFromAuthCtx(apiKey, authCtx), nil
}
