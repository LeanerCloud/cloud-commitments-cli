package auth

import (
	"context"
)

// StoreInterface defines the methods required for auth storage.
type StoreInterface interface {
	// User operations
	GetUserByID(ctx context.Context, userID string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	CreateUser(ctx context.Context, user *User) error
	UpdateUser(ctx context.Context, user *User) error
	DeleteUser(ctx context.Context, userID string) error
	ListUsers(ctx context.Context) ([]User, error)
	GetUserByResetToken(ctx context.Context, token string) (*User, error)
	AdminExists(ctx context.Context) (bool, error)
	// CreateAdminIfNone atomically inserts user as the first admin in the
	// system. Returns (true, nil) on success, (false, nil) when an admin
	// already existed (TOCTOU race winner gets false), (false, ErrEmailInUse)
	// when the email collides with an existing non-admin user, or
	// (false, err) for any other failure. Used by SetupAdmin to close the
	// bootstrap race without relying on the users_one_admin partial unique
	// index (dropped in migration 000050).
	CreateAdminIfNone(ctx context.Context, user *User) (bool, error)

	// Group operations
	GetGroup(ctx context.Context, groupID string) (*Group, error)
	CreateGroup(ctx context.Context, group *Group) error
	UpdateGroup(ctx context.Context, group *Group) error
	DeleteGroup(ctx context.Context, groupID string) error
	ListGroups(ctx context.Context) ([]Group, error)
	// CountGroupMembers returns the number of users whose group_ids array
	// contains groupID. Used to enforce the last-administrator protection
	// (issue #907) and any future per-group membership invariants.
	CountGroupMembers(ctx context.Context, groupID string) (int, error)

	// Session operations
	CreateSession(ctx context.Context, session *Session) error
	GetSession(ctx context.Context, token string) (*Session, error)
	DeleteSession(ctx context.Context, token string) error
	DeleteUserSessions(ctx context.Context, userID string) error
	CleanupExpiredSessions(ctx context.Context) error

	// API Key operations
	CreateAPIKey(ctx context.Context, key *UserAPIKey) error
	GetAPIKeyByID(ctx context.Context, keyID string) (*UserAPIKey, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*UserAPIKey, error)
	ListAPIKeysByUser(ctx context.Context, userID string) ([]*UserAPIKey, error)
	UpdateAPIKey(ctx context.Context, key *UserAPIKey) error
	UpdateAPIKeyLastUsed(ctx context.Context, keyID string) error
	// RecordAPIKeyUsage atomically updates last_used_at and increments
	// request_count_total + request_count_24h. The 24h counter resets
	// (along with its window-start) when the existing window is older
	// than 24h. See migration 000094 for the column shape.
	RecordAPIKeyUsage(ctx context.Context, keyID string) error
	DeleteAPIKey(ctx context.Context, keyID string) error

	// Health check
	Ping(ctx context.Context) error
}

// EmailSenderInterface defines the methods required for sending emails.
type EmailSenderInterface interface {
	SendPasswordResetEmail(ctx context.Context, email, resetURL string) error
	SendWelcomeEmail(ctx context.Context, email, dashboardURL, role string) error
	SendUserInviteEmail(ctx context.Context, email, setupURL string) error
}
