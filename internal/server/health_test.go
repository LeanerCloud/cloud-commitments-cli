package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/database"
	"github.com/LeanerCloud/CUDly/internal/testutil"
)

// mockAuthStoreForHealth implements auth.StoreInterface for health check tests.
type mockAuthStoreForHealth struct{}

func (m *mockAuthStoreForHealth) GetUserByID(ctx context.Context, userID string) (*auth.User, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) GetUserByEmail(ctx context.Context, email string) (*auth.User, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) CreateUser(ctx context.Context, user *auth.User) error {
	return nil
}

func (m *mockAuthStoreForHealth) UpdateUser(ctx context.Context, user *auth.User) error {
	return nil
}

func (m *mockAuthStoreForHealth) DeleteUser(ctx context.Context, userID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) ListUsers(ctx context.Context) ([]auth.User, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) GetUserByResetToken(ctx context.Context, token string) (*auth.User, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) AdminExists(ctx context.Context) (bool, error) {
	return false, nil
}

func (m *mockAuthStoreForHealth) CreateAdminIfNone(ctx context.Context, user *auth.User) (bool, error) {
	return true, nil
}

func (m *mockAuthStoreForHealth) GetGroup(ctx context.Context, groupID string) (*auth.Group, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) CountGroupMembers(ctx context.Context, groupID string) (int, error) {
	return 0, nil
}

func (m *mockAuthStoreForHealth) CreateGroup(ctx context.Context, group *auth.Group) error {
	return nil
}

func (m *mockAuthStoreForHealth) UpdateGroup(ctx context.Context, group *auth.Group) error {
	return nil
}

func (m *mockAuthStoreForHealth) DeleteGroup(ctx context.Context, groupID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) ListGroups(ctx context.Context) ([]auth.Group, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) CreateSession(ctx context.Context, session *auth.Session) error {
	return nil
}

func (m *mockAuthStoreForHealth) GetSession(ctx context.Context, token string) (*auth.Session, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) DeleteSession(ctx context.Context, token string) error {
	return nil
}

func (m *mockAuthStoreForHealth) DeleteUserSessions(ctx context.Context, userID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) CleanupExpiredSessions(ctx context.Context) error {
	return nil
}

func (m *mockAuthStoreForHealth) CreateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	return nil
}

func (m *mockAuthStoreForHealth) GetAPIKeyByID(ctx context.Context, keyID string) (*auth.UserAPIKey, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) GetAPIKeyByHash(ctx context.Context, keyHash string) (*auth.UserAPIKey, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) ListAPIKeysByUser(ctx context.Context, userID string) ([]*auth.UserAPIKey, error) {
	return nil, nil
}

func (m *mockAuthStoreForHealth) UpdateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	return nil
}

func (m *mockAuthStoreForHealth) UpdateAPIKeyLastUsed(ctx context.Context, keyID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) RecordAPIKeyUsage(ctx context.Context, keyID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) DeleteAPIKey(ctx context.Context, keyID string) error {
	return nil
}

func (m *mockAuthStoreForHealth) Ping(ctx context.Context) error {
	return nil
}

// createHealthyAuthService creates an auth service with a mock store for health tests.
func createHealthyAuthService() *auth.Service {
	return auth.NewService(auth.ServiceConfig{
		Store: &mockAuthStoreForHealth{},
	})
}

func TestHandleHealthCheck(t *testing.T) {
	tests := []struct {
		setupApp       func(*Application)
		name           string
		expectedHealth string
		expectedStatus int
	}{
		{
			name: "healthy application",
			setupApp: func(app *Application) {
				app.Version = "test-version"
				app.Config = &mockConfigStoreForHealth{}
				app.Auth = createHealthyAuthService()
			},
			expectedStatus: 200,
			expectedHealth: "healthy",
		},
		{
			name: "application with version",
			setupApp: func(app *Application) {
				app.Version = "v1.2.3"
				app.Config = &mockConfigStoreForHealth{}
				app.Auth = createHealthyAuthService()
			},
			expectedStatus: 200,
			expectedHealth: "healthy",
		},
		{
			name: "degraded when config is nil",
			setupApp: func(app *Application) {
				app.Version = "test"
				app.Config = nil
				app.Auth = createHealthyAuthService()
			},
			expectedStatus: 200,
			expectedHealth: "degraded",
		},
		{
			name: "degraded when auth is nil",
			setupApp: func(app *Application) {
				app.Version = "test"
				app.Config = &mockConfigStoreForHealth{}
				app.Auth = nil
			},
			expectedStatus: 200,
			expectedHealth: "degraded",
		},
		{
			name: "degraded when both nil",
			setupApp: func(app *Application) {
				app.Version = "test"
			},
			expectedStatus: 200,
			expectedHealth: "degraded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &Application{}
			if tt.setupApp != nil {
				tt.setupApp(app)
			}

			req := httptest.NewRequestWithContext(context.Background(), "GET", "/health", nil)
			w := httptest.NewRecorder()

			app.handleHealthCheck(w, req)

			testutil.AssertEqual(t, tt.expectedStatus, w.Code)

			// Verify JSON response structure
			var health HealthStatus
			err := json.Unmarshal(w.Body.Bytes(), &health)
			testutil.AssertNoError(t, err)

			testutil.AssertEqual(t, tt.expectedHealth, health.Status)

			if app.Version != "" {
				testutil.AssertEqual(t, app.Version, health.Version)
			}

			testutil.AssertTrue(t, !health.Timestamp.IsZero(), "Timestamp should be set")
			testutil.AssertTrue(t, health.Checks != nil, "Checks map should not be nil")
		})
	}
}

func TestHealthCheckSecurityHeaders(t *testing.T) {
	app := &Application{
		Version: "test",
		Config:  &mockConfigStoreForHealth{},
		Auth:    createHealthyAuthService(),
		appConfig: ApplicationConfig{
			CORSAllowedOrigin: "https://example.com",
		},
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/health", nil)
	w := httptest.NewRecorder()
	app.handleHealthCheck(w, req)

	testutil.AssertEqual(t, 200, w.Code)

	// Verify security headers are present
	securityHeaders := []string{
		"Strict-Transport-Security",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"X-Xss-Protection",
		"Content-Security-Policy",
		"Permissions-Policy",
		"Referrer-Policy",
		"Cache-Control",
	}
	for _, header := range securityHeaders {
		testutil.AssertTrue(t, w.Header().Get(header) != "", "Expected header "+header+" to be present")
	}

	// Verify CORS headers
	testutil.AssertEqual(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
	testutil.AssertTrue(t, w.Header().Get("Access-Control-Allow-Methods") != "", "Expected CORS methods header")
	testutil.AssertTrue(t, w.Header().Get("Access-Control-Allow-Headers") != "", "Expected CORS headers header")
}

func TestHealthCheckNoCORSWhenNotConfigured(t *testing.T) {
	app := &Application{
		Version: "test",
		Config:  &mockConfigStoreForHealth{},
		Auth:    createHealthyAuthService(),
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/health", nil)
	w := httptest.NewRecorder()
	app.handleHealthCheck(w, req)

	testutil.AssertEqual(t, 200, w.Code)

	// CORS headers should be absent when origin is not configured
	testutil.AssertEqual(t, "", w.Header().Get("Access-Control-Allow-Origin"))

	// Security headers should still be present
	testutil.AssertTrue(t, w.Header().Get("Strict-Transport-Security") != "", "HSTS should be present even without CORS")
}

func TestCheckConfigStore(t *testing.T) {
	tests := []struct {
		name           string
		setupApp       func(*Application)
		expectedStatus string
	}{
		{
			name: "nil config store",
			setupApp: func(app *Application) {
				app.Config = nil
			},
			expectedStatus: "unhealthy",
		},
		{
			name: "config store present",
			setupApp: func(app *Application) {
				app.Config = &mockConfigStoreForHealth{}
			},
			expectedStatus: "healthy",
		},
		{
			name: "nil config store with pending db",
			setupApp: func(app *Application) {
				app.Config = nil
				app.dbConfig = &databaseConfigStub{}
			},
			expectedStatus: "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)
			app := &Application{}
			if tt.setupApp != nil {
				tt.setupApp(app)
			}

			result := app.checkConfigStore(ctx)
			testutil.AssertEqual(t, tt.expectedStatus, result.Status)
		})
	}
}

func TestCheckAuthStore(t *testing.T) {
	tests := []struct {
		name           string
		setupApp       func(*Application)
		expectedStatus string
	}{
		{
			name: "nil auth service",
			setupApp: func(app *Application) {
				app.Auth = nil
			},
			expectedStatus: "unhealthy",
		},
		{
			name: "auth service present",
			setupApp: func(app *Application) {
				app.Auth = createHealthyAuthService()
			},
			expectedStatus: "healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)
			app := &Application{}
			if tt.setupApp != nil {
				tt.setupApp(app)
			}

			result := app.checkAuthStore(ctx)
			testutil.AssertEqual(t, tt.expectedStatus, result.Status)
		})
	}
}

func TestCheckMigrations_Disabled(t *testing.T) {
	t.Run("nil dbConfig (non-PostgreSQL mode)", func(t *testing.T) {
		app := &Application{}
		got := app.checkMigrations()
		testutil.AssertEqual(t, "disabled", got.Status)
	})
	t.Run("AutoMigrate off", func(t *testing.T) {
		app := &Application{dbConfig: &database.Config{AutoMigrate: false}}
		got := app.checkMigrations()
		testutil.AssertEqual(t, "disabled", got.Status)
	})
}

func TestCheckMigrations_Pending(t *testing.T) {
	app := &Application{dbConfig: &database.Config{AutoMigrate: true}}
	// migrationFinishedAt is zero value → pending.
	got := app.checkMigrations()
	testutil.AssertEqual(t, "pending", got.Status)
}

func TestCheckMigrations_Failed(t *testing.T) {
	app := &Application{dbConfig: &database.Config{AutoMigrate: true}}
	app.recordMigrationResult(errors.New("dirty at 27"))
	got := app.checkMigrations()
	testutil.AssertEqual(t, "failed", got.Status)
	if got.Message == "" || got.Message != "dirty at 27" {
		t.Fatalf("expected message to contain the error; got %q", got.Message)
	}
}

func TestCheckMigrations_Healthy(t *testing.T) {
	app := &Application{dbConfig: &database.Config{AutoMigrate: true}}
	app.recordMigrationResult(nil)
	got := app.checkMigrations()
	testutil.AssertEqual(t, "healthy", got.Status)
	// recordMigrationResult sets migrationFinishedAt to now; message says
	// "last run 0s ago" at the second-precision this code truncates to.
	if !containsSubstr(got.Message, "last run") {
		t.Fatalf("expected message to mention last run; got %q", got.Message)
	}
	// Sanity: finishedAt is close to now.
	_, finishedAt := app.snapshotMigrationState()
	if time.Since(finishedAt) > 5*time.Second {
		t.Fatalf("finishedAt too old: %v", finishedAt)
	}
}

// containsSubstr is a tiny local helper so we don't pull in strings for one check.
func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
