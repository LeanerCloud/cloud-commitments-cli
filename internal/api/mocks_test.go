package api

import (
	"context"

	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/credentials"
	"github.com/LeanerCloud/CUDly/internal/mocks"
	"github.com/LeanerCloud/CUDly/internal/scheduler"
	"github.com/stretchr/testify/mock"
)

var _ credentials.CredentialStore = (*MockCredentialStore)(nil) // compile-time interface check

// MockConfigStore is the shared testify mock for config.StoreInterface.
// All Fn-override fields and default behaviors live in internal/mocks.
type MockConfigStore = mocks.MockConfigStore

// MockCredentialStore is a simple stub implementing credentials.CredentialStore.
// SaveCredential always returns nil; other methods are no-ops.
type MockCredentialStore struct{}

func (m *MockCredentialStore) SaveCredential(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (m *MockCredentialStore) LoadRaw(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}
func (m *MockCredentialStore) DeleteCredential(_ context.Context, _, _ string) error {
	return nil
}
func (m *MockCredentialStore) HasCredential(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *MockCredentialStore) EncryptPayload(plaintext []byte) (string, error) {
	return string(plaintext), nil // no-op: return plaintext as "encrypted" for tests
}

func (m *MockCredentialStore) DecryptPayload(ciphertext string) ([]byte, error) {
	return []byte(ciphertext), nil // no-op: return ciphertext as "decrypted" for tests
}

// MockPurchaseManager is a mock implementation of purchase.Manager.
type MockPurchaseManager struct {
	mock.Mock
}

func (m *MockPurchaseManager) ApproveExecution(ctx context.Context, execID, token, actor string) error {
	args := m.Called(ctx, execID, token, actor)
	return args.Error(0)
}

func (m *MockPurchaseManager) ApproveAndExecute(ctx context.Context, execID, actor string, transitionedBy *string) error {
	args := m.Called(ctx, execID, actor, transitionedBy)
	return args.Error(0)
}

func (m *MockPurchaseManager) CancelExecution(ctx context.Context, execID, token, actor string) error {
	args := m.Called(ctx, execID, token, actor)
	return args.Error(0)
}

// MockScheduler is a mock implementation of scheduler.Scheduler.
type MockScheduler struct {
	mock.Mock
}

func (m *MockScheduler) CollectRecommendations(ctx context.Context) (*scheduler.CollectResult, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*scheduler.CollectResult), args.Error(1)
}

func (m *MockScheduler) ListRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]config.RecommendationRecord), args.Error(1)
}

func (m *MockScheduler) GetRecommendationByID(ctx context.Context, id string) (*config.RecommendationRecord, []string, error) {
	args := m.Called(ctx, id)
	var rec *config.RecommendationRecord
	if args.Get(0) != nil {
		rec = args.Get(0).(*config.RecommendationRecord)
	}
	var hiddenBy []string
	if args.Get(1) != nil {
		hiddenBy = args.Get(1).([]string)
	}
	return rec, hiddenBy, args.Error(2)
}

// MockAuthService is a mock implementation of the auth service.
type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LoginResponse), args.Error(1)
}

func (m *MockAuthService) Logout(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockAuthService) ValidateSession(ctx context.Context, token string) (*Session, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Session), args.Error(1)
}

func (m *MockAuthService) ValidateCSRFToken(ctx context.Context, sessionToken, csrfToken string) error {
	args := m.Called(ctx, sessionToken, csrfToken)
	return args.Error(0)
}

func (m *MockAuthService) SetupAdmin(ctx context.Context, req SetupAdminRequest) (*LoginResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LoginResponse), args.Error(1)
}

func (m *MockAuthService) CheckAdminExists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthService) RequestPasswordReset(ctx context.Context, email string) error {
	args := m.Called(ctx, email)
	return args.Error(0)
}

func (m *MockAuthService) ConfirmPasswordReset(ctx context.Context, req PasswordResetConfirm) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockAuthService) ResetTokenStatus(ctx context.Context, token string) (string, string, error) {
	args := m.Called(ctx, token)
	return args.String(0), args.String(1), args.Error(2)
}

func (m *MockAuthService) GetUser(ctx context.Context, userID string) (*User, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*User), args.Error(1)
}

func (m *MockAuthService) UpdateUserProfile(ctx context.Context, userID string, email string, currentPassword string, newPassword string) error {
	args := m.Called(ctx, userID, email, currentPassword, newPassword)
	return args.Error(0)
}

// User management mock methods.
func (m *MockAuthService) CreateUserAPI(ctx context.Context, req interface{}) (interface{}, error) {
	args := m.Called(ctx, req)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) UpdateUserAPI(ctx context.Context, actorUserID, userID string, req interface{}) (interface{}, error) {
	args := m.Called(ctx, actorUserID, userID, req)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) DeleteUser(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockAuthService) ListUsersAPI(ctx context.Context) (interface{}, error) {
	args := m.Called(ctx)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) ChangePasswordAPI(ctx context.Context, userID, currentPassword, newPassword string) error {
	args := m.Called(ctx, userID, currentPassword, newPassword)
	return args.Error(0)
}

// MFA lifecycle mock methods (issue #497).
func (m *MockAuthService) MFASetupAPI(ctx context.Context, userID, password string) (string, string, error) {
	args := m.Called(ctx, userID, password)
	return args.String(0), args.String(1), args.Error(2)
}

func (m *MockAuthService) MFAEnableAPI(ctx context.Context, userID, code string) ([]string, error) {
	args := m.Called(ctx, userID, code)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *MockAuthService) MFADisableAPI(ctx context.Context, userID, password, codeOrRecovery string) error {
	args := m.Called(ctx, userID, password, codeOrRecovery)
	return args.Error(0)
}

func (m *MockAuthService) MFARegenerateRecoveryCodesAPI(ctx context.Context, userID, code string) ([]string, error) {
	args := m.Called(ctx, userID, code)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

// Group management mock methods.
func (m *MockAuthService) CreateGroupAPI(ctx context.Context, req interface{}) (interface{}, error) {
	args := m.Called(ctx, req)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) UpdateGroupAPI(ctx context.Context, groupID string, req interface{}) (interface{}, error) {
	args := m.Called(ctx, groupID, req)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) DeleteGroup(ctx context.Context, groupID string) error {
	args := m.Called(ctx, groupID)
	return args.Error(0)
}

func (m *MockAuthService) GetGroupAPI(ctx context.Context, groupID string) (interface{}, error) {
	args := m.Called(ctx, groupID)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) ListGroupsAPI(ctx context.Context) (interface{}, error) {
	args := m.Called(ctx)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) HasPermissionAPI(ctx context.Context, userID, action, resource string) (bool, error) {
	args := m.Called(ctx, userID, action, resource)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthService) HasPermissionForConstraintsAPI(ctx context.Context, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	args := m.Called(ctx, userID, action, resource, constraintSets)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthService) GetUserPermissionsAPI(ctx context.Context, userID string) (any, error) {
	args := m.Called(ctx, userID)
	return args.Get(0), args.Error(1)
}

// allowConstraintChecks stubs the SEC-01 execution-time permission
// constraint check (HasPermissionForConstraintsAPI and the user-API-key
// variant HasAPIKeyPermissionForConstraintsAPI) to succeed for any request,
// modeling a granting permission with no Constraints configured. Tests that
// target constraint behavior register explicit expectations instead.
func (m *MockAuthService) allowConstraintChecks() {
	m.On("HasPermissionForConstraintsAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(true, nil).Maybe()
	m.On("HasAPIKeyPermissionForConstraintsAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(true, nil).Maybe()
}

// grantAdmin makes every HasPermissionAPI check succeed, modeling an
// Administrators-group member. Authorization is group-membership-only after
// issue #907, so admin-gated handlers resolve "is admin" / specific permissions
// through HasPermissionAPI rather than a Session.Role short-circuit; tests that
// previously set Role:"admin" register this instead. Uses .Maybe() so handlers
// that don't reach a permission check don't fail the expectation, and matches
// any userID so a single call covers the test's admin session regardless of its
// UUID.
func (m *MockAuthService) grantAdmin() {
	m.On("HasPermissionAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(true, nil).Maybe()
	// Admins hold {admin, *}, which grants every constraint set, so the
	// SEC-01 execution-time constraint check succeeds too.
	m.On("HasPermissionForConstraintsAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(true, nil).Maybe()
	// Administrators-group members carry the "*" wildcard, surfaced as
	// unrestricted access (nil/empty). Handlers that scope by account call
	// GetAllowedAccountsAPI after the permission check, so stub it too.
	m.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).
		Return([]string(nil), nil).Maybe()
}

func (m *MockAuthService) GetAllowedAccountsAPI(ctx context.Context, userID string) ([]string, error) {
	args := m.Called(ctx, userID)
	if v := args.Get(0); v != nil {
		return v.([]string), args.Error(1)
	}
	return nil, args.Error(1)
}

// API Key management mock methods.
func (m *MockAuthService) CreateAPIKeyAPI(ctx context.Context, userID string, req interface{}) (interface{}, error) {
	args := m.Called(ctx, userID, req)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) ListUserAPIKeysAPI(ctx context.Context, userID string) (interface{}, error) {
	args := m.Called(ctx, userID)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) GetAPIKeysUsageStatsAPI(ctx context.Context, userID string) (interface{}, error) {
	args := m.Called(ctx, userID)
	return args.Get(0), args.Error(1)
}

func (m *MockAuthService) DeleteAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	args := m.Called(ctx, userID, keyID)
	return args.Error(0)
}

func (m *MockAuthService) RevokeAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	args := m.Called(ctx, userID, keyID)
	return args.Error(0)
}

func (m *MockAuthService) ValidateUserAPIKeyAPI(ctx context.Context, apiKey string) (interface{}, interface{}, error) {
	args := m.Called(ctx, apiKey)
	return args.Get(0), args.Get(1), args.Error(2)
}

func (m *MockAuthService) HasAPIKeyPermissionAPI(ctx context.Context, apiKey, action, resource string) (string, string, bool, error) {
	args := m.Called(ctx, apiKey, action, resource)
	return args.String(0), args.String(1), args.Bool(2), args.Error(3)
}

func (m *MockAuthService) HasAPIKeyPermissionForConstraintsAPI(ctx context.Context, keyID, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	args := m.Called(ctx, keyID, userID, action, resource, constraintSets)
	return args.Bool(0), args.Error(1)
}
