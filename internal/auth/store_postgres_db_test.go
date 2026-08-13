//go:build integration
// +build integration

package auth

// store_postgres_db_test.go - DB-backed integration tests for the
// security-critical PostgresStore bootstrap paths (TEST-01, issue #1153).
// These run the real SQL against a migrated PostgreSQL testcontainer,
// proving the 19-column bootstrap INSERT, the NOT EXISTS guard and its
// agreement with AdminExists (active-membership semantics), and the
// insert-once guarantee under two concurrent CreateAdminIfNone calls.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/database"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getAuthTestMigrationsPath returns the absolute path to the migrations directory.
func getAuthTestMigrationsPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "database", "postgres", "migrations")
}

// setupAuthTestDB starts a PostgreSQL container and runs all migrations
// without seeding a bootstrap admin (empty adminEmail), so tests start from
// the "fresh deployment, no admin yet" state CreateAdminIfNone exists for.
func setupAuthTestDB(t *testing.T) *database.Connection {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() {
		container.Cleanup(context.Background())
	})

	// Migrating is the thing under test here, not a precondition of it: a
	// migration that will not apply is a broken schema, and skipping on it would
	// report the whole suite green on exactly the defect it exists to catch
	// (issue #1597).
	require.NoError(t,
		migrations.RunMigrations(ctx, container.DB.Pool(), getAuthTestMigrationsPath(), "", ""),
		"migrations failed to apply to a fresh database")

	return container.DB
}

// resetUsers wipes the users table between scenarios. TRUNCATE is used
// deliberately: the trg_min_one_admin_delete row trigger (migration 000065)
// would otherwise veto deleting the last admin, and TRUNCATE does not fire
// row-level triggers. CASCADE clears dependent rows (sessions, api_keys).
func resetUsers(t *testing.T, db *database.Connection) {
	t.Helper()
	_, err := db.Exec(context.Background(), "TRUNCATE users CASCADE")
	require.NoError(t, err)
}

func newBootstrapCandidate(email string) *User {
	return &User{
		Email:        email,
		PasswordHash: "test-hash",
		Salt:         "test-salt",
		Active:       true,
	}
}

// TestIntegration_CreateAdminIfNone_Bootstrap exercises the real bootstrap
// INSERT end to end: column ordering, the Administrators-group forcing logic,
// and the NOT EXISTS guard's agreement with AdminExists.
func TestIntegration_CreateAdminIfNone_Bootstrap(t *testing.T) {
	db := setupAuthTestDB(t)
	store := NewPostgresStore(db)
	ctx := context.Background()

	t.Run("inserts first admin and forces Administrators group", func(t *testing.T) {
		resetUsers(t, db)

		user := newBootstrapCandidate("first-admin@example.com")
		created, err := store.CreateAdminIfNone(ctx, user)
		require.NoError(t, err)
		assert.True(t, created)

		// The inserted row must satisfy the membership predicate AdminExists uses.
		exists, err := store.AdminExists(ctx)
		require.NoError(t, err)
		assert.True(t, exists, "bootstrap admin must be visible to AdminExists")

		stored, err := store.GetUserByEmail(ctx, "first-admin@example.com")
		require.NoError(t, err)
		assert.Contains(t, stored.GroupIDs, DefaultAdminGroupID,
			"bootstrap admin must carry the Administrators group even though the caller did not supply it")

		count, err := store.CountGroupMembers(ctx, DefaultAdminGroupID)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("second sequential call is a no-op", func(t *testing.T) {
		resetUsers(t, db)

		created, err := store.CreateAdminIfNone(ctx, newBootstrapCandidate("admin-a@example.com"))
		require.NoError(t, err)
		require.True(t, created)

		created, err = store.CreateAdminIfNone(ctx, newBootstrapCandidate("admin-b@example.com"))
		require.NoError(t, err)
		assert.False(t, created, "an active admin already exists, so the guarded insert must not fire")

		count, err := store.CountGroupMembers(ctx, DefaultAdminGroupID)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "the losing call must not have inserted a row")
	})

	t.Run("inactive admin counts as no admin, matching AdminExists", func(t *testing.T) {
		resetUsers(t, db)

		// Insert an INACTIVE Administrators-group member through the regular,
		// unguarded create path (INSERT does not fire the min-one-admin trigger).
		inactive := newBootstrapCandidate("inactive-admin@example.com")
		inactive.Active = false
		inactive.GroupIDs = []string{DefaultAdminGroupID}
		require.NoError(t, store.CreateUser(ctx, inactive))

		exists, err := store.AdminExists(ctx)
		require.NoError(t, err)
		require.False(t, exists, "inactive admin must not count as an existing admin")

		// The NOT EXISTS guard must agree: bootstrap proceeds.
		created, err := store.CreateAdminIfNone(ctx, newBootstrapCandidate("real-admin@example.com"))
		require.NoError(t, err)
		assert.True(t, created,
			"CreateAdminIfNone must treat an inactive-only admin set as 'no admin', mirroring AdminExists")

		count, err := store.CountGroupMembers(ctx, DefaultAdminGroupID)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "CountGroupMembers counts members regardless of active flag")
	})

	t.Run("email collision with existing non-admin surfaces ErrEmailInUse", func(t *testing.T) {
		resetUsers(t, db)

		regular := newBootstrapCandidate("taken@example.com")
		// users_min_one_group (migration 000057) requires at least one group;
		// any seeded non-admin group satisfies it.
		regular.GroupIDs = []string{DefaultPurchaserGroupID}
		require.NoError(t, store.CreateUser(ctx, regular))

		created, err := store.CreateAdminIfNone(ctx, newBootstrapCandidate("taken@example.com"))
		assert.False(t, created)
		assert.ErrorIs(t, err, ErrEmailInUse)
	})
}

// TestIntegration_CreateAdminIfNone_ConcurrentBootstrapOnce proves the
// insert-once semantics under two concurrent CreateAdminIfNone calls: exactly
// one caller wins and exactly one admin row exists afterwards, across
// repeated barrier-synchronized attempts.
func TestIntegration_CreateAdminIfNone_ConcurrentBootstrapOnce(t *testing.T) {
	db := setupAuthTestDB(t)
	store := NewPostgresStore(db)
	ctx := context.Background()

	const iterations = 25

	for i := 0; i < iterations; i++ {
		resetUsers(t, db)

		type outcome struct {
			created bool
			err     error
		}
		results := make(chan outcome, 2)
		start := make(chan struct{})

		for n := 0; n < 2; n++ {
			user := newBootstrapCandidate(fmt.Sprintf("admin-%d-%d@example.com", i, n))
			go func(u *User) {
				<-start
				created, err := store.CreateAdminIfNone(ctx, u)
				results <- outcome{created: created, err: err}
			}(user)
		}
		close(start)

		winners := 0
		for n := 0; n < 2; n++ {
			r := <-results
			require.NoError(t, r.err, "iteration %d", i)
			if r.created {
				winners++
			}
		}
		require.Equal(t, 1, winners,
			"iteration %d: exactly one of two concurrent bootstrap calls must win", i)

		count, err := store.CountGroupMembers(ctx, DefaultAdminGroupID)
		require.NoError(t, err)
		require.Equal(t, 1, count,
			"iteration %d: exactly one admin row must exist after concurrent bootstrap", i)
	}
}
