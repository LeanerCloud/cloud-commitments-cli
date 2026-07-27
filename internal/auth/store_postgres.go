package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBConnection defines the interface for database operations needed by PostgresStore.
type DBConnection interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
	Ping(ctx context.Context) error
}

// PostgresStore implements StoreInterface using PostgreSQL.
type PostgresStore struct {
	db DBConnection
}

// NewPostgresStore creates a new PostgreSQL-backed auth store.
func NewPostgresStore(db DBConnection) *PostgresStore {
	return &PostgresStore{db: db}
}

// Verify PostgresStore implements StoreInterface.
var _ StoreInterface = (*PostgresStore)(nil)

// ==========================================
// USER OPERATIONS
// ==========================================

// GetUserByID retrieves a user by ID.
func (s *PostgresStore) GetUserByID(ctx context.Context, userID string) (*User, error) {
	query := `
		SELECT id, email, password_hash, salt, group_ids, active,
		       mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
		       mfa_recovery_codes, password_reset_token, password_reset_expiry,
		       failed_login_attempts, locked_until, password_history,
		       created_at, updated_at, last_login_at
		FROM users
		WHERE id = $1
	`

	return s.scanUser(s.db.QueryRow(ctx, query, userID))
}

// GetUserByEmail retrieves a user by email.
func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, salt, group_ids, active,
		       mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
		       mfa_recovery_codes, password_reset_token, password_reset_expiry,
		       failed_login_attempts, locked_until, password_history,
		       created_at, updated_at, last_login_at
		FROM users
		WHERE email = $1
	`

	user, err := s.scanUser(s.db.QueryRow(ctx, query, email))
	if err != nil {
		return nil, err
	}
	return user, nil
}

// CreateUser creates a new user.
func (s *PostgresStore) CreateUser(ctx context.Context, user *User) error {
	// Generate UUID if not provided
	if user.ID == "" {
		user.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	query := `
		INSERT INTO users (
			id, email, password_hash, salt, group_ids, active,
			mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
			mfa_recovery_codes, password_reset_token, password_reset_expiry,
			failed_login_attempts, locked_until, password_history,
			created_at, updated_at, last_login_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
	`

	// Substitute an empty slice for nil so the Postgres TEXT[] column
	// stays at its default '{}' rather than NULL. The schema declares
	// the column NOT NULL DEFAULT '{}' (migration 000052); passing a
	// raw nil here triggers "violates not-null constraint" on insert.
	recoveryCodes := user.MFARecoveryCodes
	if recoveryCodes == nil {
		recoveryCodes = []string{}
	}

	_, err := s.db.Exec(ctx, query,
		user.ID,
		user.Email,
		user.PasswordHash,
		user.Salt,
		user.GroupIDs,
		user.Active,
		user.MFAEnabled,
		user.MFASecret,
		user.MFAPendingSecret,
		user.MFAPendingSecretExpiresAt,
		recoveryCodes,
		user.PasswordResetToken,
		user.PasswordResetExpiry,
		user.FailedLoginAttempts,
		user.LockedUntil,
		user.PasswordHistory,
		user.CreatedAt,
		user.UpdatedAt,
		user.LastLoginAt,
	)

	if err != nil {
		if isEmailDuplicateError(err) {
			return fmt.Errorf("email already in use")
		}
		if isDuplicateKeyError(err) {
			return fmt.Errorf("failed to create user: unique constraint violation: %w", err)
		}
		return fmt.Errorf("failed to create user: %w", err)
	}

	return nil
}

// isDuplicateKeyError checks if the error is a PostgreSQL unique constraint violation (code 23505).
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// isEmailDuplicateError returns true only when the violated constraint is the
// users_email_key (or users_email_unique) constraint, so we don't mistakenly
// surface "email already in use" for ID collisions or other uniqueness violations.
func isEmailDuplicateError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName == "users_email_key" ||
			pgErr.ConstraintName == "users_email_unique"
	}
	return false
}

// isLastAdminConstraintViolation reports whether err is the deferred trigger
// exception from migration 000065 (trg_min_one_admin). The trigger uses
// RAISE EXCEPTION with a message prefixed by "last_admin_constraint_violation"
// and PostgreSQL error code P0001 (raise_exception). Detecting by message
// prefix rather than code alone avoids false positives from other user-raised
// exceptions in the same codebase.
//
// The function also handles plain errors whose message contains the sentinel
// prefix, which allows unit tests to simulate the trigger violation without a
// live PostgreSQL connection.
func isLastAdminConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	const sentinel = "last_admin_constraint_violation"
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "P0001" && strings.HasPrefix(pgErr.Message, sentinel)
	}
	// Fallback for unit-test stubs that return a plain error with the sentinel
	// message (no pgconn available in mock-based tests).
	return strings.HasPrefix(err.Error(), sentinel)
}

// UpdateUser updates an existing user.
func (s *PostgresStore) UpdateUser(ctx context.Context, user *User) error {
	user.UpdatedAt = time.Now()

	query := `
		UPDATE users SET
			email = $2,
			password_hash = $3,
			salt = $4,
			group_ids = $5,
			active = $6,
			mfa_enabled = $7,
			mfa_secret = $8,
			mfa_pending_secret = $9,
			mfa_pending_secret_expires_at = $10,
			mfa_recovery_codes = $11,
			password_reset_token = $12,
			password_reset_expiry = $13,
			failed_login_attempts = $14,
			locked_until = $15,
			password_history = $16,
			updated_at = $17,
			last_login_at = $18
		WHERE id = $1
	`

	// See CreateUser for the nil-slice substitution rationale.
	recoveryCodes := user.MFARecoveryCodes
	if recoveryCodes == nil {
		recoveryCodes = []string{}
	}

	result, err := s.db.Exec(ctx, query,
		user.ID,
		user.Email,
		user.PasswordHash,
		user.Salt,
		user.GroupIDs,
		user.Active,
		user.MFAEnabled,
		user.MFASecret,
		user.MFAPendingSecret,
		user.MFAPendingSecretExpiresAt,
		recoveryCodes,
		user.PasswordResetToken,
		user.PasswordResetExpiry,
		user.FailedLoginAttempts,
		user.LockedUntil,
		user.PasswordHistory,
		user.UpdatedAt,
		user.LastLoginAt,
	)

	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", user.ID)
	}

	return nil
}

// DeleteUser deletes a user.
func (s *PostgresStore) DeleteUser(ctx context.Context, userID string) error {
	query := `DELETE FROM users WHERE id = $1`

	result, err := s.db.Exec(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", userID)
	}

	return nil
}

// ListUsers lists all users.
func (s *PostgresStore) ListUsers(ctx context.Context) ([]User, error) {
	// LIMIT provides a safety cap against unbounded memory allocation on large installations.
	// Pagination support should be added if this limit proves insufficient.
	query := `
		SELECT id, email, password_hash, salt, group_ids, active,
		       mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
		       mfa_recovery_codes, password_reset_token, password_reset_expiry,
		       failed_login_attempts, locked_until, password_history,
		       created_at, updated_at, last_login_at
		FROM users
		ORDER BY created_at DESC
		LIMIT 10000
	`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		user, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		if user == nil {
			continue
		}
		users = append(users, *user)
	}

	return users, rows.Err()
}

// GetUserByResetToken retrieves a user by password reset token without
// filtering on expiry. Both callers (ResetTokenStatus and
// validateResetToken) perform their own expiry check on the returned
// row, so the SQL must surface expired rows; otherwise an expired
// token returns pgx.ErrNoRows and ResetTokenStatus misclassifies it
// as "used" instead of "expired" (QA bug 11.2).
func (s *PostgresStore) GetUserByResetToken(ctx context.Context, token string) (*User, error) {
	query := `
		SELECT id, email, password_hash, salt, group_ids, active,
		       mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
		       mfa_recovery_codes, password_reset_token, password_reset_expiry,
		       failed_login_attempts, locked_until, password_history,
		       created_at, updated_at, last_login_at
		FROM users
		WHERE password_reset_token = $1
	`

	return s.scanUser(s.db.QueryRow(ctx, query, token))
}

// adminGroupContainsClause is the SQL predicate matching users who are members
// of the Administrators group. Authorization is group-membership-only after
// issue #907, so "is an admin" == "group_ids contains the Administrators group
// UUID". $1 must be bound to DefaultAdminGroupID.
const adminGroupContainsClause = `group_ids @> ARRAY[$1::uuid]`

// AdminExists checks if any active Administrators-group member exists. This is
// the group-membership replacement for the former role = 'admin' check.
func (s *PostgresStore) AdminExists(ctx context.Context) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM users WHERE ` + adminGroupContainsClause + ` AND active = true)`

	var exists bool
	err := s.db.QueryRow(ctx, query, DefaultAdminGroupID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check admin existence: %w", err)
	}

	return exists, nil
}

// CountGroupMembers returns the number of users whose group_ids contains
// groupID. Used to enforce last-administrator protection (issue #907).
func (s *PostgresStore) CountGroupMembers(ctx context.Context, groupID string) (int, error) {
	query := `SELECT COUNT(*) FROM users WHERE group_ids @> ARRAY[$1::uuid]`

	var count int
	if err := s.db.QueryRow(ctx, query, groupID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count group members: %w", err)
	}
	return count, nil
}

// adminInvariantAdvisoryLockKey is the transaction-scoped advisory lock key
// serializing writes that affect the "at least/at most the right number of
// admins" invariants. It MUST stay equal to the key used by
// check_min_one_admin() in migration 000065 so bootstrap inserts and
// admin-demoting commits serialize against each other as well as among
// themselves.
const adminInvariantAdvisoryLockKey = 8059058058580001

// CreateAdminIfNone atomically inserts user as the first admin in the
// system. Returns (true, nil) when the insert succeeded; (false, nil)
// when an admin already existed (TOCTOU race — both callers passed
// AdminExists, only one wins the insert); (false, ErrEmailInUse) when
// the email collides with an existing (non-admin) user; (false, err)
// for any other failure.
//
// The conditional INSERT closes the bootstrap race without the
// users_one_admin partial unique index (dropped in migration 000050),
// but a single INSERT … WHERE NOT EXISTS statement is NOT race-free on
// its own: under READ COMMITTED each statement's snapshot is taken at
// statement start, so two concurrent calls can both see "no admin" and
// both insert (reproduced by
// TestIntegration_CreateAdminIfNone_ConcurrentBootstrapOnce). To close
// that window the insert runs in a transaction that first takes the
// transaction-scoped advisory lock shared with the min-one-admin
// trigger (migration 000065): the second caller blocks until the first
// commits, and its INSERT statement then takes a fresh snapshot that
// sees the committed admin, so the NOT EXISTS guard suppresses the
// duplicate. The lock auto-releases at COMMIT or ROLLBACK.
func (s *PostgresStore) CreateAdminIfNone(ctx context.Context, user *User) (bool, error) {
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	// The NOT EXISTS guard matches AdminExists's semantics exactly
	// (Administrators-group member AND active=true) so the fast-path check
	// and the atomic insert agree: a deployment with only inactive admins is
	// treated as "no admin" by both, and the insert proceeds. If they
	// disagreed, AdminExists could report false while this insert
	// failed silently on the WHERE clause, leaving the operator with
	// a recoverable-looking error and no admin.
	//
	// The bootstrap admin's group_ids is forced to include the Administrators
	// group regardless of the caller-supplied slice — the method's contract is
	// "create admin if none", and an admin row that did NOT carry the
	// Administrators group would not satisfy the membership predicate, silently
	// breaking that contract.
	query := `
		INSERT INTO users (
			id, email, password_hash, salt, group_ids, active,
			mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,
			mfa_recovery_codes, password_reset_token, password_reset_expiry,
			failed_login_attempts, locked_until, password_history,
			created_at, updated_at, last_login_at
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE group_ids @> ARRAY[$20::uuid] AND active = true)
	`

	// See CreateUser for the nil-slice substitution rationale. Force the
	// Administrators group onto the bootstrap admin so it satisfies the
	// membership predicate above (deduped to avoid a doubled entry if the
	// caller already supplied it).
	recoveryCodes := user.MFARecoveryCodes
	if recoveryCodes == nil {
		recoveryCodes = []string{}
	}
	groupIDs := user.GroupIDs
	if !containsGroup(groupIDs, DefaultAdminGroupID) {
		groupIDs = append(append([]string(nil), groupIDs...), DefaultAdminGroupID)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to begin admin bootstrap transaction: %w", err)
	}
	// Rollback is a no-op after a successful Commit; on any earlier return
	// it also releases the advisory lock. Matches the project convention
	// (e.g. internal/config/store_postgres.go) for deferred rollback.
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			_ = rbErr // rollback error intentionally discarded; Postgres logs rollback failures
		}
	}()

	// Serialize against concurrent bootstrap calls and against the
	// min-one-admin deferred trigger (see adminInvariantAdvisoryLockKey).
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminInvariantAdvisoryLockKey); err != nil {
		return false, fmt.Errorf("failed to acquire admin bootstrap lock: %w", err)
	}

	tag, err := tx.Exec(ctx, query,
		user.ID, user.Email, user.PasswordHash, user.Salt,
		groupIDs, user.Active, user.MFAEnabled, user.MFASecret,
		user.MFAPendingSecret, user.MFAPendingSecretExpiresAt, recoveryCodes,
		user.PasswordResetToken, user.PasswordResetExpiry,
		user.FailedLoginAttempts, user.LockedUntil, user.PasswordHistory,
		user.CreatedAt, user.UpdatedAt, user.LastLoginAt,
		DefaultAdminGroupID,
	)
	if err != nil {
		// Email uniqueness can still fire if the bootstrap caller reuses
		// an email already held by a non-admin user. Surface as the same
		// sentinel the regular create path uses so callers get a clean
		// "email already in use" instead of a raw DB error.
		if isEmailDuplicateError(err) {
			return false, ErrEmailInUse
		}
		return false, fmt.Errorf("failed to create admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("failed to commit admin bootstrap transaction: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

// ==========================================
// GROUP OPERATIONS
// ==========================================

// GetGroup retrieves a group by ID.
func (s *PostgresStore) GetGroup(ctx context.Context, groupID string) (*Group, error) {
	query := `
		SELECT id, name, description, permissions, allowed_accounts,
		       system_managed, created_at, updated_at, created_by
		FROM groups
		WHERE id = $1
	`

	return s.scanGroup(s.db.QueryRow(ctx, query, groupID))
}

// CreateGroup creates a new group.
func (s *PostgresStore) CreateGroup(ctx context.Context, group *Group) error {
	// Generate UUID if not provided
	if group.ID == "" {
		group.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	group.CreatedAt = now
	group.UpdatedAt = now

	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(group.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		INSERT INTO groups (
			id, name, description, permissions, allowed_accounts,
			created_at, updated_at, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	// created_by is a nullable UUID FK; convert empty string to NULL so
	// Postgres doesn't reject the insert with "invalid input syntax for
	// type uuid" when the caller doesn't have a user ID.
	var createdBy any = group.CreatedBy
	if group.CreatedBy == "" {
		createdBy = nil
	}

	_, err = s.db.Exec(ctx, query,
		group.ID,
		group.Name,
		group.Description,
		permissionsJSON,
		group.AllowedAccounts,
		group.CreatedAt,
		group.UpdatedAt,
		createdBy,
	)

	if err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}

	return nil
}

// UpdateGroup updates an existing group.
func (s *PostgresStore) UpdateGroup(ctx context.Context, group *Group) error {
	group.UpdatedAt = time.Now()

	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(group.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		UPDATE groups SET
			name = $2,
			description = $3,
			permissions = $4,
			allowed_accounts = $5,
			updated_at = $6
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query,
		group.ID,
		group.Name,
		group.Description,
		permissionsJSON,
		group.AllowedAccounts,
		group.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to update group: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("group not found: %s", group.ID)
	}

	return nil
}

// DeleteGroup deletes a group.
func (s *PostgresStore) DeleteGroup(ctx context.Context, groupID string) error {
	query := `DELETE FROM groups WHERE id = $1`

	result, err := s.db.Exec(ctx, query, groupID)
	if err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("group not found: %s", groupID)
	}

	return nil
}

// ListGroups lists all groups.
func (s *PostgresStore) ListGroups(ctx context.Context) ([]Group, error) {
	// LIMIT provides a safety cap against unbounded memory allocation.
	// Pagination support should be added if this limit proves insufficient.
	query := `
		SELECT id, name, description, permissions, allowed_accounts,
		       system_managed, created_at, updated_at, created_by
		FROM groups
		ORDER BY created_at DESC
		LIMIT 10000
	`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}
	defer rows.Close()

	groups := make([]Group, 0)
	for rows.Next() {
		group, err := s.scanGroup(rows)
		if err != nil {
			return nil, err
		}
		if group == nil {
			continue
		}
		groups = append(groups, *group)
	}

	return groups, rows.Err()
}

// ==========================================
// SESSION OPERATIONS
// ==========================================

// CreateSession creates a new session.
func (s *PostgresStore) CreateSession(ctx context.Context, session *Session) error {
	query := `
		INSERT INTO sessions (
			token, user_id, email, expires_at, created_at,
			user_agent, ip_address, csrf_token
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	_, err := s.db.Exec(ctx, query,
		session.Token,
		session.UserID,
		session.Email,
		session.ExpiresAt,
		session.CreatedAt,
		session.UserAgent,
		session.IPAddress,
		session.CSRFToken,
	)

	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	return nil
}

// GetSession retrieves a session by token.
func (s *PostgresStore) GetSession(ctx context.Context, token string) (*Session, error) {
	query := `
		SELECT token, user_id, email, expires_at, created_at,
		       user_agent, ip_address, csrf_token
		FROM sessions
		WHERE token = $1 AND expires_at > NOW()
	`

	var session Session
	err := s.db.QueryRow(ctx, query, token).Scan(
		&session.Token,
		&session.UserID,
		&session.Email,
		&session.ExpiresAt,
		&session.CreatedAt,
		&session.UserAgent,
		&session.IPAddress,
		&session.CSRFToken,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("session not found or expired")
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return &session, nil
}

// DeleteSession deletes a session.
func (s *PostgresStore) DeleteSession(ctx context.Context, token string) error {
	query := `DELETE FROM sessions WHERE token = $1`

	_, err := s.db.Exec(ctx, query, token)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

// DeleteUserSessions deletes all sessions for a user.
func (s *PostgresStore) DeleteUserSessions(ctx context.Context, userID string) error {
	query := `DELETE FROM sessions WHERE user_id = $1`

	_, err := s.db.Exec(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user sessions: %w", err)
	}

	return nil
}

// CleanupExpiredSessions deletes expired sessions.
func (s *PostgresStore) CleanupExpiredSessions(ctx context.Context) error {
	query := `DELETE FROM sessions WHERE expires_at <= NOW()`

	result, err := s.db.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to cleanup expired sessions: %w", err)
	}

	logging.Debugf("Cleaned up %d expired sessions", result.RowsAffected())
	return nil
}

// ==========================================
// API KEY OPERATIONS
// ==========================================

// CreateAPIKey creates a new API key.
func (s *PostgresStore) CreateAPIKey(ctx context.Context, key *UserAPIKey) error {
	// Generate UUID if not provided
	if key.ID == "" {
		key.ID = uuid.New().String()
	}

	// Set timestamps
	key.CreatedAt = time.Now()

	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(key.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		INSERT INTO api_keys (
			id, user_id, name, key_prefix, key_hash, permissions,
			is_active, expires_at, created_at, last_used_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err = s.db.Exec(ctx, query,
		key.ID,
		key.UserID,
		key.Name,
		key.KeyPrefix,
		key.KeyHash,
		permissionsJSON,
		key.IsActive,
		key.ExpiresAt,
		key.CreatedAt,
		key.LastUsedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create API key: %w", err)
	}

	return nil
}

// GetAPIKeyByID retrieves an API key by ID.
func (s *PostgresStore) GetAPIKeyByID(ctx context.Context, keyID string) (*UserAPIKey, error) {
	query := `
		SELECT id, user_id, name, key_prefix, key_hash, permissions,
		       is_active, expires_at, created_at, last_used_at,
		       request_count_total, request_count_24h
		FROM api_keys
		WHERE id = $1
	`

	return s.scanAPIKey(s.db.QueryRow(ctx, query, keyID))
}

// GetAPIKeyByHash retrieves an API key by hash.
func (s *PostgresStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*UserAPIKey, error) {
	query := `
		SELECT id, user_id, name, key_prefix, key_hash, permissions,
		       is_active, expires_at, created_at, last_used_at,
		       request_count_total, request_count_24h
		FROM api_keys
		WHERE key_hash = $1 AND is_active = true
		  AND (expires_at IS NULL OR expires_at > NOW())
	`

	return s.scanAPIKey(s.db.QueryRow(ctx, query, keyHash))
}

// ListAPIKeysByUser lists all API keys for a user.
func (s *PostgresStore) ListAPIKeysByUser(ctx context.Context, userID string) ([]*UserAPIKey, error) {
	query := `
		SELECT id, user_id, name, key_prefix, key_hash, permissions,
		       is_active, expires_at, created_at, last_used_at,
		       request_count_total, request_count_24h
		FROM api_keys
		WHERE user_id = $1
		ORDER BY created_at DESC
	`

	rows, err := s.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list API keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*UserAPIKey, 0)
	for rows.Next() {
		key, err := s.scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		if key == nil {
			continue
		}
		keys = append(keys, key)
	}

	return keys, rows.Err()
}

// UpdateAPIKey updates an API key.
func (s *PostgresStore) UpdateAPIKey(ctx context.Context, key *UserAPIKey) error {
	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(key.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		UPDATE api_keys SET
			name = $2,
			permissions = $3,
			is_active = $4,
			expires_at = $5,
			last_used_at = $6
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query,
		key.ID,
		key.Name,
		permissionsJSON,
		key.IsActive,
		key.ExpiresAt,
		key.LastUsedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to update API key: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("API key not found: %s", key.ID)
	}

	return nil
}

// UpdateAPIKeyLastUsed atomically updates the last_used_at timestamp for an
// API key. Retained for backwards compatibility with callers that only need
// the timestamp update -- production code routes through RecordAPIKeyUsage,
// which additionally maintains the usage counters from migration 000094.
func (s *PostgresStore) UpdateAPIKeyLastUsed(ctx context.Context, keyID string) error {
	query := `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`
	result, err := s.db.Exec(ctx, query, keyID)
	if err != nil {
		return fmt.Errorf("failed to update API key last used: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("API key not found: %s", keyID)
	}
	return nil
}

// RecordAPIKeyUsage atomically updates last_used_at, increments
// request_count_total, and increments-or-resets request_count_24h based on
// whether the rolling 24h window is still current.
//
// The single UPDATE does the window decision inline:
//   - if window_start IS NULL OR (NOW() - window_start) >= 24h, start a
//     fresh window (window_start = NOW(), count = 1).
//   - otherwise increment the existing count.
//
// Doing it in one statement keeps the update atomic per row, so two
// concurrent calls can't both see "stale window" and reset to 1 in
// parallel -- pgx serializes updates to the same row.
func (s *PostgresStore) RecordAPIKeyUsage(ctx context.Context, keyID string) error {
	query := `
		UPDATE api_keys
		SET last_used_at = NOW(),
		    request_count_total = request_count_total + 1,
		    request_count_24h = CASE
		        WHEN request_count_24h_window_start IS NULL
		             OR NOW() - request_count_24h_window_start >= INTERVAL '24 hours'
		        THEN 1
		        ELSE request_count_24h + 1
		    END,
		    request_count_24h_window_start = CASE
		        WHEN request_count_24h_window_start IS NULL
		             OR NOW() - request_count_24h_window_start >= INTERVAL '24 hours'
		        THEN NOW()
		        ELSE request_count_24h_window_start
		    END
		WHERE id = $1
	`
	result, err := s.db.Exec(ctx, query, keyID)
	if err != nil {
		return fmt.Errorf("failed to record API key usage: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("API key not found: %s", keyID)
	}
	return nil
}

// DeleteAPIKey deletes an API key.
func (s *PostgresStore) DeleteAPIKey(ctx context.Context, keyID string) error {
	query := `DELETE FROM api_keys WHERE id = $1`

	result, err := s.db.Exec(ctx, query, keyID)
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("API key not found: %s", keyID)
	}

	return nil
}

// ==========================================
// HELPER FUNCTIONS
// ==========================================

// Scanner interface for both Row and Rows.
type Scanner interface {
	Scan(dest ...any) error
}

// scanUser scans a user from a database row.
func (s *PostgresStore) scanUser(scanner Scanner) (*User, error) {
	var user User
	var groupIDs []string
	var passwordHistory []string
	var recoveryCodes []string
	var resetExpiry, lockedUntil, lastLoginAt, mfaPendingExpiry sql.NullTime
	var mfaSecret, mfaPendingSecret, resetToken sql.NullString

	err := scanner.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Salt,
		&groupIDs,
		&user.Active,
		&user.MFAEnabled,
		&mfaSecret,
		&mfaPendingSecret,
		&mfaPendingExpiry,
		&recoveryCodes,
		&resetToken,
		&resetExpiry,
		&user.FailedLoginAttempts,
		&lockedUntil,
		&passwordHistory,
		&user.CreatedAt,
		&user.UpdatedAt,
		&lastLoginAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to scan user: %w", err)
	}

	user.GroupIDs = groupIDs
	user.PasswordHistory = passwordHistory
	user.MFARecoveryCodes = recoveryCodes

	// Handle nullable strings
	if mfaSecret.Valid {
		user.MFASecret = mfaSecret.String
	}
	if mfaPendingSecret.Valid {
		user.MFAPendingSecret = mfaPendingSecret.String
	}
	if resetToken.Valid {
		user.PasswordResetToken = resetToken.String
	}

	// Handle nullable timestamps
	if mfaPendingExpiry.Valid {
		user.MFAPendingSecretExpiresAt = &mfaPendingExpiry.Time
	}
	if resetExpiry.Valid {
		user.PasswordResetExpiry = &resetExpiry.Time
	}
	if lockedUntil.Valid {
		user.LockedUntil = &lockedUntil.Time
	}
	if lastLoginAt.Valid {
		user.LastLoginAt = &lastLoginAt.Time
	}

	return &user, nil
}

// scanGroup scans a group from a database row.
func (s *PostgresStore) scanGroup(scanner Scanner) (*Group, error) {
	var group Group
	var permissionsJSON []byte
	var allowedAccounts []string
	var createdBy sql.NullString

	err := scanner.Scan(
		&group.ID,
		&group.Name,
		&group.Description,
		&permissionsJSON,
		&allowedAccounts,
		&group.SystemManaged,
		&group.CreatedAt,
		&group.UpdatedAt,
		&createdBy,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to scan group: %w", err)
	}

	// Unmarshal permissions — guard against NULL column, consistent with scanAPIKey
	if len(permissionsJSON) > 0 {
		if err := json.Unmarshal(permissionsJSON, &group.Permissions); err != nil {
			return nil, fmt.Errorf("failed to unmarshal permissions: %w", err)
		}
	}

	group.AllowedAccounts = allowedAccounts

	if createdBy.Valid {
		group.CreatedBy = createdBy.String
	}

	return &group, nil
}

// scanAPIKey scans an API key from a database row.
func (s *PostgresStore) scanAPIKey(scanner Scanner) (*UserAPIKey, error) {
	var key UserAPIKey
	var permissionsJSON []byte
	var expiresAt, lastUsedAt sql.NullTime

	err := scanner.Scan(
		&key.ID,
		&key.UserID,
		&key.Name,
		&key.KeyPrefix,
		&key.KeyHash,
		&permissionsJSON,
		&key.IsActive,
		&expiresAt,
		&key.CreatedAt,
		&lastUsedAt,
		&key.RequestCountTotal,
		&key.RequestCount24h,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to scan API key: %w", err)
	}

	// Unmarshal permissions
	if len(permissionsJSON) > 0 {
		if err := json.Unmarshal(permissionsJSON, &key.Permissions); err != nil {
			return nil, fmt.Errorf("failed to unmarshal permissions: %w", err)
		}
	}

	// Handle nullable timestamps
	if expiresAt.Valid {
		key.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		key.LastUsedAt = &lastUsedAt.Time
	}

	return &key, nil
}

// Ping checks the database connection health.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}
