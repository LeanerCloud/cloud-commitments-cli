package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSPProvisioner is a configurable azureSPProvisioner used to assert that
// createAzureServicePrincipal requests the correct app name, role and scope,
// and surfaces the generated secret.
type mockSPProvisioner struct {
	// captured inputs
	createAppName     string
	addPasswordObjID  string
	createSPAppID     string
	resolveRoleScope  string
	resolveRoleName   string
	assignScope       string
	assignPrincipalID string
	assignRoleDefID   string

	// canned outputs
	appObjectID string
	appID       string
	secret      string
	principalID string
	roleDefID   string

	// optional injected errors
	createAppErr error
	addPwErr     error
	createSPErr  error
	resolveErr   error
	assignErr    error

	// call flags
	resolveRoleCalled bool
	assignRoleCalled  bool
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
}

func TestCreateAzureServicePrincipal_ErrorPropagation(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*mockSPProvisioner)
		wantErrPart   string
		wantNoAssign  bool
		wantNoResolve bool
	}{
		{
			name:          "create application fails",
			setup:         func(m *mockSPProvisioner) { m.createAppErr = errors.New("graph 403") },
			wantErrPart:   "failed to create application registration",
			wantNoAssign:  true,
			wantNoResolve: true,
		},
		{
			name:          "add password fails",
			setup:         func(m *mockSPProvisioner) { m.addPwErr = errors.New("graph addPassword 400") },
			wantErrPart:   "failed to add password credential",
			wantNoAssign:  true,
			wantNoResolve: true,
		},
		{
			name:          "create service principal fails",
			setup:         func(m *mockSPProvisioner) { m.createSPErr = errors.New("graph sp 409") },
			wantErrPart:   "failed to create service principal",
			wantNoAssign:  true,
			wantNoResolve: true,
		},
		{
			name:         "resolve role fails",
			setup:        func(m *mockSPProvisioner) { m.resolveErr = errors.New("role not found") },
			wantErrPart:  "failed to resolve",
			wantNoAssign: true,
		},
		{
			name:        "assign role fails",
			setup:       func(m *mockSPProvisioner) { m.assignErr = errors.New("rbac 403") },
			wantErrPart: "failed to assign",
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
		})
	}
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
