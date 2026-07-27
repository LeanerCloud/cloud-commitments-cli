package auth

// store_postgres_pgxmock_test.go - pgxmock tests for the PostgresStore SQL
// paths that the MockDBConnection suite never executes (TEST-01, issue #1153):
// CreateAdminIfNone (bootstrap-race guard), CountGroupMembers, ListUsers,
// ListAPIKeysByUser, UpdateAPIKeyLastUsed and Ping. These tests pin the actual
// SQL text (argument count and order, the NOT EXISTS predicate) so that
// column drift or predicate divergence between AdminExists and
// CreateAdminIfNone fails a test instead of shipping green.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAuthPgxMock creates a pgxmock pool with regexp query matching and
// registers the expectations check as cleanup so no test can forget it.
func newAuthPgxMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})
	return mock
}

// createAdminIfNoneSQLRegex matches the full conditional bootstrap INSERT:
// 19 insert columns fed by SELECT $1..$19, guarded by the NOT EXISTS
// predicate bound to $20.
const createAdminIfNoneSQLRegex = `(?s)INSERT INTO users \(\s*` +
	`id, email, password_hash, salt, group_ids, active,\s*` +
	`mfa_enabled, mfa_secret, mfa_pending_secret, mfa_pending_secret_expires_at,\s*` +
	`mfa_recovery_codes, password_reset_token, password_reset_expiry,\s*` +
	`failed_login_attempts, locked_until, password_history,\s*` +
	`created_at, updated_at, last_login_at\s*\)\s*` +
	`SELECT \$1, \$2, \$3, \$4, \$5, \$6, \$7, \$8, \$9, \$10, \$11, \$12, \$13, \$14, \$15, \$16, \$17, \$18, \$19\s*` +
	`WHERE NOT EXISTS \(SELECT 1 FROM users WHERE group_ids @> ARRAY\[\$20::uuid\] AND active = true\)`

// bootstrapAdminUser returns a minimal valid first-admin candidate.
func bootstrapAdminUser() *User {
	return &User{
		ID:           "11111111-2222-4333-8444-555555555555",
		Email:        "bootstrap-admin@example.com",
		PasswordHash: "hash",
		Salt:         "salt",
		Active:       true,
	}
}

// createAdminArgs builds the 20 expected Exec arguments for CreateAdminIfNone:
// exact matches for the security-relevant ones (id, email, group_ids, active
// and the $20 predicate binding) and AnyArg for incidental fields whose
// values the function fills in (timestamps) or passes through untouched.
func createAdminArgs(user *User, wantGroupIDs []string) []any {
	return []any{
		user.ID,                  // $1
		user.Email,               // $2
		user.PasswordHash,        // $3
		user.Salt,                // $4
		wantGroupIDs,             // $5 - must contain the Administrators group
		user.Active,              // $6
		user.MFAEnabled,          // $7
		pgxmock.AnyArg(),         // $8 mfa_secret
		pgxmock.AnyArg(),         // $9 mfa_pending_secret
		pgxmock.AnyArg(),         // $10 mfa_pending_secret_expires_at
		pgxmock.AnyArg(),         // $11 mfa_recovery_codes
		pgxmock.AnyArg(),         // $12 password_reset_token
		pgxmock.AnyArg(),         // $13 password_reset_expiry
		user.FailedLoginAttempts, // $14
		pgxmock.AnyArg(),         // $15 locked_until
		pgxmock.AnyArg(),         // $16 password_history
		pgxmock.AnyArg(),         // $17 created_at (set inside)
		pgxmock.AnyArg(),         // $18 updated_at (set inside)
		pgxmock.AnyArg(),         // $19 last_login_at
		DefaultAdminGroupID,      // $20 NOT EXISTS predicate binding
	}
}

// advisoryLockSQLRegex matches the bootstrap serialization lock statement.
const advisoryLockSQLRegex = `SELECT pg_advisory_xact_lock\(\$1\)`

// expectBootstrapTxPrefix registers the transaction-open and advisory-lock
// expectations that precede the guarded bootstrap INSERT.
func expectBootstrapTxPrefix(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec(advisoryLockSQLRegex).
		WithArgs(adminInvariantAdvisoryLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

// ---- CreateAdminIfNone ------------------------------------------------------

func TestPGXMock_CreateAdminIfNone_InsertsAndForcesAdminGroup(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()
	user.GroupIDs = []string{"99999999-aaaa-4bbb-8ccc-dddddddddddd"}

	// The Administrators group must be appended to the caller-supplied slice.
	wantGroups := []string{"99999999-aaaa-4bbb-8ccc-dddddddddddd", DefaultAdminGroupID}
	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, wantGroups)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	require.NoError(t, err)
	assert.True(t, created)
	assert.False(t, user.CreatedAt.IsZero(), "CreatedAt must be set by the store")
	assert.False(t, user.UpdatedAt.IsZero(), "UpdatedAt must be set by the store")
}

func TestPGXMock_CreateAdminIfNone_DoesNotDuplicateAdminGroup(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()
	user.GroupIDs = []string{DefaultAdminGroupID}

	// Caller already supplied the Administrators group: pass through as-is.
	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, []string{DefaultAdminGroupID})...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	require.NoError(t, err)
	assert.True(t, created)
}

func TestPGXMock_CreateAdminIfNone_GeneratesIDWhenEmpty(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()
	user.ID = ""

	args := createAdminArgs(user, []string{DefaultAdminGroupID})
	args[0] = pgxmock.AnyArg() // generated UUID
	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(args...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEmpty(t, user.ID, "an ID must be generated when none is provided")
}

func TestPGXMock_CreateAdminIfNone_AdminAlreadyExists(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()

	// The NOT EXISTS guard suppressed the insert: zero rows affected means
	// another caller won the bootstrap race. Not an error.
	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, []string{DefaultAdminGroupID})...).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectCommit()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	require.NoError(t, err)
	assert.False(t, created)
}

func TestPGXMock_CreateAdminIfNone_EmailCollision(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()

	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, []string{DefaultAdminGroupID})...).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"})
	mock.ExpectRollback()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	assert.False(t, created)
	assert.ErrorIs(t, err, ErrEmailInUse)
}

func TestPGXMock_CreateAdminIfNone_OtherDBError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()

	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, []string{DefaultAdminGroupID})...).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	assert.False(t, created)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create admin")
	assert.NotErrorIs(t, err, ErrEmailInUse)
}

func TestPGXMock_CreateAdminIfNone_BeginError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectBegin().WillReturnError(errors.New("pool exhausted"))

	created, err := store.CreateAdminIfNone(context.Background(), bootstrapAdminUser())
	assert.False(t, created)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to begin admin bootstrap transaction")
}

func TestPGXMock_CreateAdminIfNone_LockError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec(advisoryLockSQLRegex).
		WithArgs(adminInvariantAdvisoryLockKey).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	created, err := store.CreateAdminIfNone(context.Background(), bootstrapAdminUser())
	assert.False(t, created)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to acquire admin bootstrap lock")
}

func TestPGXMock_CreateAdminIfNone_CommitError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	user := bootstrapAdminUser()

	expectBootstrapTxPrefix(mock)
	mock.ExpectExec(createAdminIfNoneSQLRegex).
		WithArgs(createAdminArgs(user, []string{DefaultAdminGroupID})...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit().WillReturnError(errors.New("connection lost"))
	mock.ExpectRollback()

	created, err := store.CreateAdminIfNone(context.Background(), user)
	assert.False(t, created, "an unconfirmed insert must not be reported as created")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to commit admin bootstrap transaction")
}

// ---- AdminExists predicate consistency --------------------------------------

// TestPGXMock_AdminExists_PredicateMatchesBootstrapGuard pins the AdminExists
// SQL to the same membership predicate the CreateAdminIfNone guard uses. The
// two must agree (see the comment in store_postgres.go); each side is pinned
// by its own regex so drift in either fails a test.
func TestPGXMock_AdminExists_PredicateMatchesBootstrapGuard(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE group_ids @> ARRAY\[\$1::uuid\] AND active = true\)`).
		WithArgs(DefaultAdminGroupID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	exists, err := store.AdminExists(context.Background())
	require.NoError(t, err)
	assert.True(t, exists)
}

// ---- CountGroupMembers -------------------------------------------------------

func TestPGXMock_CountGroupMembers_Success(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM users WHERE group_ids @> ARRAY\[\$1::uuid\]`).
		WithArgs(DefaultAdminGroupID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountGroupMembers(context.Background(), DefaultAdminGroupID)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestPGXMock_CountGroupMembers_QueryError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM users`).
		WithArgs(DefaultAdminGroupID).
		WillReturnError(errors.New("db down"))

	count, err := store.CountGroupMembers(context.Background(), DefaultAdminGroupID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to count group members")
	assert.Equal(t, 0, count)
}

// ---- ListUsers ---------------------------------------------------------------

// userColumns matches the SELECT column order in ListUsers / scanUser.
var userColumns = []string{
	"id", "email", "password_hash", "salt", "group_ids", "active",
	"mfa_enabled", "mfa_secret", "mfa_pending_secret", "mfa_pending_secret_expires_at",
	"mfa_recovery_codes", "password_reset_token", "password_reset_expiry",
	"failed_login_attempts", "locked_until", "password_history",
	"created_at", "updated_at", "last_login_at",
}

func TestPGXMock_ListUsers_Success(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lastLogin := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	rows := pgxmock.NewRows(userColumns).
		AddRow(
			"user-1", "admin@example.com", "hash1", "salt1",
			[]string{DefaultAdminGroupID}, true,
			true, "mfa-secret", nil, nil,
			[]string{"code1"}, nil, nil,
			0, nil, []string{"old-hash"},
			created, created, lastLogin,
		).
		AddRow(
			"user-2", "user@example.com", "hash2", "salt2",
			[]string{}, false,
			false, nil, nil, nil,
			[]string{}, nil, nil,
			2, nil, []string{},
			created, created, nil,
		)

	mock.ExpectQuery(`(?s)SELECT id, email, password_hash, salt, group_ids, active,.*FROM users\s+ORDER BY created_at DESC\s+LIMIT 10000`).
		WillReturnRows(rows)

	users, err := store.ListUsers(context.Background())
	require.NoError(t, err)
	require.Len(t, users, 2)

	assert.Equal(t, "user-1", users[0].ID)
	assert.Equal(t, "admin@example.com", users[0].Email)
	assert.Equal(t, []string{DefaultAdminGroupID}, users[0].GroupIDs)
	assert.True(t, users[0].Active)
	assert.Equal(t, "mfa-secret", users[0].MFASecret)
	require.NotNil(t, users[0].LastLoginAt)
	assert.Equal(t, lastLogin, *users[0].LastLoginAt)

	assert.Equal(t, "user-2", users[1].ID)
	assert.False(t, users[1].Active)
	assert.Empty(t, users[1].MFASecret)
	assert.Nil(t, users[1].LastLoginAt)
	assert.Equal(t, 2, users[1].FailedLoginAttempts)
}

func TestPGXMock_ListUsers_Empty(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`(?s)SELECT id, email,.*FROM users`).
		WillReturnRows(pgxmock.NewRows(userColumns))

	users, err := store.ListUsers(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, users)
	assert.Empty(t, users)
}

func TestPGXMock_ListUsers_QueryError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`(?s)SELECT id, email,.*FROM users`).
		WillReturnError(errors.New("db down"))

	users, err := store.ListUsers(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list users")
	assert.Nil(t, users)
}

func TestPGXMock_ListUsers_RowError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := pgxmock.NewRows(userColumns).
		AddRow(
			"user-1", "admin@example.com", "hash1", "salt1",
			[]string{}, true,
			false, nil, nil, nil,
			[]string{}, nil, nil,
			0, nil, []string{},
			created, created, nil,
		).
		RowError(0, errors.New("connection reset mid-iteration"))

	mock.ExpectQuery(`(?s)SELECT id, email,.*FROM users`).WillReturnRows(rows)

	_, err := store.ListUsers(context.Background())
	require.Error(t, err)
}

// ---- ListAPIKeysByUser ---------------------------------------------------------

// apiKeyColumns matches the SELECT column order in ListAPIKeysByUser / scanAPIKey.
var apiKeyColumns = []string{
	"id", "user_id", "name", "key_prefix", "key_hash", "permissions",
	"is_active", "expires_at", "created_at", "last_used_at",
	"request_count_total", "request_count_24h",
}

func TestPGXMock_ListAPIKeysByUser_Success(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	expires := created.Add(24 * time.Hour)
	lastUsed := created.Add(time.Hour)

	rows := pgxmock.NewRows(apiKeyColumns).
		AddRow(
			"key-1", "user-1", "ci key", "cudly_ab", "hash-1",
			[]byte(`[{"action":"view","resource":"recommendations"}]`), true, expires, created, lastUsed,
			int64(42), int64(3),
		).
		AddRow(
			"key-2", "user-1", "old key", "cudly_cd", "hash-2",
			[]byte(`[]`), false, nil, created, nil,
			int64(0), int64(0),
		)

	mock.ExpectQuery(`(?s)SELECT id, user_id, name, key_prefix, key_hash, permissions,.*FROM api_keys\s+WHERE user_id = \$1\s+ORDER BY created_at DESC`).
		WithArgs("user-1").
		WillReturnRows(rows)

	keys, err := store.ListAPIKeysByUser(context.Background(), "user-1")
	require.NoError(t, err)
	require.Len(t, keys, 2)

	assert.Equal(t, "key-1", keys[0].ID)
	assert.Equal(t, "user-1", keys[0].UserID)
	assert.Equal(t, []Permission{{Action: "view", Resource: "recommendations"}}, keys[0].Permissions)
	assert.True(t, keys[0].IsActive)
	require.NotNil(t, keys[0].ExpiresAt)
	assert.Equal(t, expires, *keys[0].ExpiresAt)
	require.NotNil(t, keys[0].LastUsedAt)
	assert.Equal(t, lastUsed, *keys[0].LastUsedAt)
	assert.Equal(t, int64(42), keys[0].RequestCountTotal)
	assert.Equal(t, int64(3), keys[0].RequestCount24h)

	assert.Equal(t, "key-2", keys[1].ID)
	assert.False(t, keys[1].IsActive)
	assert.Nil(t, keys[1].ExpiresAt)
	assert.Nil(t, keys[1].LastUsedAt)
	assert.Equal(t, int64(0), keys[1].RequestCountTotal)
	assert.Equal(t, int64(0), keys[1].RequestCount24h)
}

func TestPGXMock_ListAPIKeysByUser_QueryError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectQuery(`(?s)SELECT id, user_id,.*FROM api_keys`).
		WithArgs("user-1").
		WillReturnError(errors.New("db down"))

	keys, err := store.ListAPIKeysByUser(context.Background(), "user-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list API keys")
	assert.Nil(t, keys)
}

// ---- UpdateAPIKeyLastUsed ------------------------------------------------------

func TestPGXMock_UpdateAPIKeyLastUsed_Success(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`UPDATE api_keys SET last_used_at = NOW\(\) WHERE id = \$1`).
		WithArgs("key-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.UpdateAPIKeyLastUsed(context.Background(), "key-1")
	assert.NoError(t, err)
}

func TestPGXMock_UpdateAPIKeyLastUsed_NotFound(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`UPDATE api_keys SET last_used_at = NOW\(\) WHERE id = \$1`).
		WithArgs("missing-key").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.UpdateAPIKeyLastUsed(context.Background(), "missing-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key not found")
}

func TestPGXMock_UpdateAPIKeyLastUsed_ExecError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`UPDATE api_keys SET last_used_at = NOW\(\) WHERE id = \$1`).
		WithArgs("key-1").
		WillReturnError(errors.New("db down"))

	err := store.UpdateAPIKeyLastUsed(context.Background(), "key-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update API key last used")
}

// ---- RecordAPIKeyUsage ----------------------------------------------------------

func TestPGXMock_RecordAPIKeyUsage_Success(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`(?s)UPDATE api_keys\s+SET last_used_at = NOW\(\),.*WHERE id = \$1`).
		WithArgs("key-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.RecordAPIKeyUsage(context.Background(), "key-1")
	assert.NoError(t, err)
}

func TestPGXMock_RecordAPIKeyUsage_NotFound(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`(?s)UPDATE api_keys\s+SET last_used_at = NOW\(\),.*WHERE id = \$1`).
		WithArgs("missing-key").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := store.RecordAPIKeyUsage(context.Background(), "missing-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key not found")
}

func TestPGXMock_RecordAPIKeyUsage_ExecError(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectExec(`(?s)UPDATE api_keys\s+SET last_used_at = NOW\(\),.*WHERE id = \$1`).
		WithArgs("key-1").
		WillReturnError(errors.New("db down"))

	err := store.RecordAPIKeyUsage(context.Background(), "key-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to record API key usage")
}

// ---- Ping ----------------------------------------------------------------------

func TestPGXMock_Ping(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectPing()
	assert.NoError(t, store.Ping(context.Background()))
}

func TestPGXMock_Ping_Error(t *testing.T) {
	mock := newAuthPgxMock(t)
	store := NewPostgresStore(mock)

	mock.ExpectPing().WillReturnError(errors.New("connection lost"))
	assert.Error(t, store.Ping(context.Background()))
}
