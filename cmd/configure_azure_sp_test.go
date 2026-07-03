package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	armauthorization "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSPProvisioner is a configurable azureSPProvisioner used to assert that
// createAzureServicePrincipal requests the correct app name, role and scope,
// and surfaces the generated secret.
type mockSPProvisioner struct {
	// optional injected errors
	createAppErr error
	addPwErr     error
	createSPErr  error
	resolveErr   error
	assignErr    error
	deleteAppErr error

	// captured inputs
	createAppName     string
	addPasswordObjID  string
	createSPAppID     string
	resolveRoleScope  string
	resolveRoleName   string
	assignScope       string
	assignPrincipalID string
	assignRoleDefID   string
	deleteAppObjID    string

	// canned outputs
	appObjectID string
	appID       string
	secret      string
	principalID string
	roleDefID   string

	// call flags
	resolveRoleCalled bool
	assignRoleCalled  bool
	deleteAppCalled   bool
}

func (m *mockSPProvisioner) CreateApplication(_ context.Context, displayName string) (string, string, error) {
	m.createAppName = displayName
	if m.createAppErr != nil {
		return "", "", m.createAppErr
	}
	return m.appObjectID, m.appID, nil
}

func (m *mockSPProvisioner) AddPassword(_ context.Context, objectID string) (string, error) {
	m.addPasswordObjID = objectID
	if m.addPwErr != nil {
		return "", m.addPwErr
	}
	return m.secret, nil
}

func (m *mockSPProvisioner) CreateServicePrincipal(_ context.Context, appID string) (string, error) {
	m.createSPAppID = appID
	if m.createSPErr != nil {
		return "", m.createSPErr
	}
	return m.principalID, nil
}

func (m *mockSPProvisioner) ResolveRoleDefinitionID(_ context.Context, scope, roleName string) (string, error) {
	m.resolveRoleCalled = true
	m.resolveRoleScope = scope
	m.resolveRoleName = roleName
	if m.resolveErr != nil {
		return "", m.resolveErr
	}
	return m.roleDefID, nil
}

func (m *mockSPProvisioner) AssignRole(_ context.Context, scope, principalID, roleDefinitionID string) error {
	m.assignRoleCalled = true
	m.assignScope = scope
	m.assignPrincipalID = principalID
	m.assignRoleDefID = roleDefinitionID
	return m.assignErr
}

func (m *mockSPProvisioner) DeleteApplication(_ context.Context, objectID string) error {
	m.deleteAppCalled = true
	m.deleteAppObjID = objectID
	return m.deleteAppErr
}

const (
	testSubID    = "11111111-2222-3333-4444-555555555555"
	testTenantID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func TestCreateAzureServicePrincipal_Success(t *testing.T) {
	m := &mockSPProvisioner{
		appObjectID: "app-object-id",
		appID:       "app-client-id",
		secret:      "super-secret-password",
		principalID: "sp-principal-id",
		roleDefID:   "/subscriptions/" + testSubID + "/provider/.../roleDefinitions/role-guid",
	}

	result, err := createAzureServicePrincipal(context.Background(), m, testSubID, testTenantID)
	require.NoError(t, err)

	// App name must be exactly "CUDly".
	assert.Equal(t, "CUDly", m.createAppName)
	assert.Equal(t, azureSPName, m.createAppName)

	// Password added to the created application object.
	assert.Equal(t, "app-object-id", m.addPasswordObjID)

	// Service principal created for the application's client ID.
	assert.Equal(t, "app-client-id", m.createSPAppID)

	// Role resolved by exact display name at subscription scope.
	assert.True(t, m.resolveRoleCalled)
	assert.Equal(t, "Reservations Administrator", m.resolveRoleName)
	assert.Equal(t, azureSPRoleName, m.resolveRoleName)
	assert.Equal(t, "/subscriptions/"+testSubID, m.resolveRoleScope)

	// Role assignment binds the SP principal to the resolved role at subscription scope.
	assert.True(t, m.assignRoleCalled)
	assert.Equal(t, "/subscriptions/"+testSubID, m.assignScope)
	assert.Equal(t, "sp-principal-id", m.assignPrincipalID)
	assert.Equal(t, m.roleDefID, m.assignRoleDefID)

	// Result surfaces appId, secret and tenant (the create-for-rbac fields).
	assert.Equal(t, "app-client-id", result.AppID)
	assert.Equal(t, "super-secret-password", result.ClientSecret)
	assert.Equal(t, testTenantID, result.TenantID)

	// No rollback on success.
	assert.False(t, m.deleteAppCalled, "DeleteApplication must not be called on success")
}

func TestCreateAzureServicePrincipal_ErrorPropagation(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*mockSPProvisioner)
		wantErrPart   string
		wantNoAssign  bool
		wantNoResolve bool
		wantRollback  bool // DeleteApplication should be called to clean up
	}{
		{
			name:          "create application fails",
			setup:         func(m *mockSPProvisioner) { m.createAppErr = errors.New("graph 403") },
			wantErrPart:   "failed to create application registration",
			wantNoAssign:  true,
			wantNoResolve: true,
			wantRollback:  false, // nothing was created, nothing to roll back
		},
		{
			name:          "add password fails",
			setup:         func(m *mockSPProvisioner) { m.addPwErr = errors.New("graph addPassword 400") },
			wantErrPart:   "failed to add password credential",
			wantNoAssign:  true,
			wantNoResolve: true,
			wantRollback:  true,
		},
		{
			name:          "create service principal fails",
			setup:         func(m *mockSPProvisioner) { m.createSPErr = errors.New("graph sp 409") },
			wantErrPart:   "failed to create service principal",
			wantNoAssign:  true,
			wantNoResolve: true,
			wantRollback:  true,
		},
		{
			name:         "resolve role fails",
			setup:        func(m *mockSPProvisioner) { m.resolveErr = errors.New("role not found") },
			wantErrPart:  "failed to resolve",
			wantNoAssign: true,
			wantRollback: true,
		},
		{
			name:         "assign role fails",
			setup:        func(m *mockSPProvisioner) { m.assignErr = errors.New("rbac 403") },
			wantErrPart:  "failed to assign",
			wantRollback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mockSPProvisioner{
				appObjectID: "app-object-id",
				appID:       "app-client-id",
				secret:      "secret",
				principalID: "sp-principal-id",
				roleDefID:   "role-def-id",
			}
			tt.setup(m)

			_, err := createAzureServicePrincipal(context.Background(), m, testSubID, testTenantID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)

			if tt.wantNoResolve {
				assert.False(t, m.resolveRoleCalled, "ResolveRoleDefinitionID should not have been called")
			}
			if tt.wantNoAssign {
				assert.False(t, m.assignRoleCalled, "AssignRole should not have been called")
			}
			assert.Equal(t, tt.wantRollback, m.deleteAppCalled,
				"DeleteApplication call expectation mismatch")
			if tt.wantRollback {
				assert.Equal(t, "app-object-id", m.deleteAppObjID,
					"rollback should delete the created application by object ID")
			}
		})
	}
}

// TestCreateAzureServicePrincipal_RollbackFailureSurfaced verifies that when
// the compensating delete also fails, the error names the orphaned application
// so the operator can delete it manually.
func TestCreateAzureServicePrincipal_RollbackFailureSurfaced(t *testing.T) {
	m := &mockSPProvisioner{
		appObjectID:  "app-object-id",
		appID:        "app-client-id",
		secret:       "secret",
		principalID:  "sp-principal-id",
		roleDefID:    "role-def-id",
		assignErr:    errors.New("rbac 403"),
		deleteAppErr: errors.New("delete 500"),
	}

	_, err := createAzureServicePrincipal(context.Background(), m, testSubID, testTenantID)
	require.Error(t, err)
	assert.True(t, m.deleteAppCalled)
	assert.Contains(t, err.Error(), "failed to assign")
	assert.Contains(t, err.Error(), "failed to roll back")
	assert.Contains(t, err.Error(), "app-object-id")
}

func TestCreateAzureServicePrincipal_ScopeFormat(t *testing.T) {
	m := &mockSPProvisioner{
		appObjectID: "o", appID: "a", secret: "s", principalID: "p", roleDefID: "r",
	}
	_, err := createAzureServicePrincipal(context.Background(), m, testSubID, testTenantID)
	require.NoError(t, err)
	// Scope must be exactly /subscriptions/<id> (subscription scope, not resource group).
	assert.Equal(t, "/subscriptions/"+testSubID, m.resolveRoleScope)
	assert.Equal(t, "/subscriptions/"+testSubID, m.assignScope)
}

// fakeRoleAssigner is a test double for roleAssigner that fails with a
// PrincipalNotFound error for the first failsRemaining calls, then succeeds.
// If otherErr is set it is always returned instead (to test non-retryable paths).
type fakeRoleAssigner struct {
	failsRemaining       int
	principalNotFoundErr *azcore.ResponseError
	otherErr             error
	callCount            int
}

func (f *fakeRoleAssigner) Create(
	_ context.Context,
	_, _ string,
	_ armauthorization.RoleAssignmentCreateParameters,
	_ *armauthorization.RoleAssignmentsClientCreateOptions,
) (armauthorization.RoleAssignmentsClientCreateResponse, error) {
	f.callCount++
	if f.otherErr != nil {
		return armauthorization.RoleAssignmentsClientCreateResponse{}, f.otherErr
	}
	if f.failsRemaining > 0 {
		f.failsRemaining--
		return armauthorization.RoleAssignmentsClientCreateResponse{}, f.principalNotFoundErr
	}
	return armauthorization.RoleAssignmentsClientCreateResponse{}, nil
}

// newFakePrincipalNotFoundProvisioner returns a graphSPProvisioner wired to a
// fakeRoleAssigner with very short retry timing so tests complete in
// milliseconds rather than minutes.
func newFakePrincipalNotFoundProvisioner(fake *fakeRoleAssigner) *graphSPProvisioner {
	return &graphSPProvisioner{
		roleAsgn:     fake,
		retryInitial: time.Millisecond,
		retryBudget:  50 * time.Millisecond,
	}
}

// TestGraphSPProvisioner_AssignRole_RetrySucceeds verifies that AssignRole
// retries when ARM returns PrincipalNotFound and eventually succeeds.
func TestGraphSPProvisioner_AssignRole_RetrySucceeds(t *testing.T) {
	principalNotFound := &azcore.ResponseError{ErrorCode: "PrincipalNotFound", StatusCode: 400}
	fake := &fakeRoleAssigner{failsRemaining: 2, principalNotFoundErr: principalNotFound}
	p := newFakePrincipalNotFoundProvisioner(fake)

	err := p.AssignRole(context.Background(), "/subscriptions/sub", "sp-id", "role-def-id")

	require.NoError(t, err)
	assert.Equal(t, 3, fake.callCount,
		"should call Create 3 times: 2 PrincipalNotFound failures then 1 success")
}

// TestGraphSPProvisioner_AssignRole_ExhaustsRetryBudget verifies that
// AssignRole fails loud with a clear message when the service principal never
// propagates within the budget.
func TestGraphSPProvisioner_AssignRole_ExhaustsRetryBudget(t *testing.T) {
	principalNotFound := &azcore.ResponseError{ErrorCode: "PrincipalNotFound", StatusCode: 400}
	fake := &fakeRoleAssigner{failsRemaining: 100, principalNotFoundErr: principalNotFound}
	p := newFakePrincipalNotFoundProvisioner(fake)

	err := p.AssignRole(context.Background(), "/subscriptions/sub", "sp-id", "role-def-id")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not propagate",
		"error must explain that the SP did not propagate")
	assert.Contains(t, err.Error(), "sp-id",
		"error must identify the principal that failed to propagate")
	assert.True(t, fake.callCount >= 1, "should have attempted Create at least once")
}

// TestGraphSPProvisioner_AssignRole_NonRetryableError verifies that a
// non-PrincipalNotFound error is returned immediately without retrying.
func TestGraphSPProvisioner_AssignRole_NonRetryableError(t *testing.T) {
	fake := &fakeRoleAssigner{otherErr: errors.New("authorization denied")}
	p := newFakePrincipalNotFoundProvisioner(fake)

	err := p.AssignRole(context.Background(), "/subscriptions/sub", "sp-id", "role-def-id")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorization denied")
	assert.Equal(t, 1, fake.callCount, "should not retry on non-PrincipalNotFound errors")
}

// TestGraphSPProvisioner_AssignRole_ContextCancellation verifies that context
// cancellation is treated as a terminal stop and does not continue retrying.
func TestGraphSPProvisioner_AssignRole_ContextCancellation(t *testing.T) {
	principalNotFound := &azcore.ResponseError{ErrorCode: "PrincipalNotFound", StatusCode: 400}
	fake := &fakeRoleAssigner{failsRemaining: 100, principalNotFoundErr: principalNotFound}
	p := newFakePrincipalNotFoundProvisioner(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the first retry sleep fires

	err := p.AssignRole(ctx, "/subscriptions/sub", "sp-id", "role-def-id")

	require.Error(t, err)
	// The first Create call returns PrincipalNotFound; the subsequent select
	// detects ctx.Done() and returns ctx.Err() immediately.
	assert.Equal(t, 1, fake.callCount, "should stop retrying after context is cancelled")
}

// TestIsPrincipalNotFoundErr covers the error-code detection helper directly.
func TestIsPrincipalNotFoundErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "PrincipalNotFound error code",
			err:  &azcore.ResponseError{ErrorCode: "PrincipalNotFound", StatusCode: 400},
			want: true,
		},
		{
			name: "ServicePrincipalNotFound error code",
			err:  &azcore.ResponseError{ErrorCode: "ServicePrincipalNotFound", StatusCode: 400},
			want: true,
		},
		{
			name: "unrelated ARM error",
			err:  &azcore.ResponseError{ErrorCode: "AuthorizationFailed", StatusCode: 403},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("network error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPrincipalNotFoundErr(tt.err))
		})
	}
}
