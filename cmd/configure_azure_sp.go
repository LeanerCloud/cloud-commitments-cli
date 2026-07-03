package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	armauthorization "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/google/uuid"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/applications"
	graphmodels "github.com/microsoftgraph/msgraph-sdk-go/models"
)

// roleAssignRetryInitial is the first back-off interval when retrying a role
// assignment that fails with PrincipalNotFound. Each subsequent interval is
// doubled up to roleAssignRetryMax.
const (
	roleAssignRetryInitial = 5 * time.Second
	roleAssignRetryMax     = 30 * time.Second
	// roleAssignRetryBudget is the ceiling on total retry time. Azure AD
	// replication is usually complete within seconds but can take up to ~10
	// minutes in the worst case (see feedback_tf_depends_on_rbac.md). Three
	// minutes covers the large majority of propagation windows without
	// keeping the operator waiting too long.
	roleAssignRetryBudget = 3 * time.Minute
)

// roleAssigner is the minimal subset of *armauthorization.RoleAssignmentsClient
// used by graphSPProvisioner. The interface exists solely to allow the retry
// logic in AssignRole to be exercised in unit tests without hitting Azure.
type roleAssigner interface {
	Create(
		ctx context.Context,
		scope, roleAssignmentName string,
		parameters armauthorization.RoleAssignmentCreateParameters,
		options *armauthorization.RoleAssignmentsClientCreateOptions,
	) (armauthorization.RoleAssignmentsClientCreateResponse, error)
}

// isPrincipalNotFoundErr reports whether err is an Azure ARM
// PrincipalNotFound / ServicePrincipalNotFound response -- the transient
// condition that occurs when a newly-created Entra ID service principal has
// not yet replicated to the ARM region handling the role-assignment request.
// It covers the canonical error codes as well as the message-body form
// returned by some older API versions.
func isPrincipalNotFoundErr(err error) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	switch respErr.ErrorCode {
	case "PrincipalNotFound", "ServicePrincipalNotFound":
		return true
	}
	// Older ARM API versions surface the condition as HTTP 400 without a
	// distinct error code; instead the response body contains the canonical
	// phrase "does not exist in the directory".
	return strings.Contains(respErr.Error(), "does not exist in the directory")
}

// azureSPName is the Azure AD application / service principal display name.
// It matches the name the previous "az ad sp create-for-rbac --name CUDly"
// call used so the resulting identity is interchangeable.
const azureSPName = "CUDly"

// azureSPRoleName is the RBAC role assigned to the service principal at
// subscription scope, matching the previous create-for-rbac invocation.
const azureSPRoleName = "Reservations Administrator"

// azureSPResult holds the credential material produced by creating the service
// principal. It mirrors the appId/password/tenant fields that
// "az ad sp create-for-rbac" prints, which the operator feeds into the
// subsequent configure step.
type azureSPResult struct {
	AppID        string // application (client) ID
	ClientSecret string // generated password / client secret
	TenantID     string // Azure AD tenant ID
}

// azureSPProvisioner abstracts the cloud operations needed to create a service
// principal and grant it the Reservations Administrator role. It exists so the
// orchestration logic can be unit-tested with a mock without depending on the
// concrete Graph / armauthorization client types (which are not interfaces).
type azureSPProvisioner interface {
	// CreateApplication creates an AAD application registration with the given
	// display name and returns its object ID and application (client) ID.
	CreateApplication(ctx context.Context, displayName string) (objectID, appID string, err error)
	// AddPassword adds a password credential to the application identified by
	// objectID and returns the generated secret text.
	AddPassword(ctx context.Context, objectID string) (secretText string, err error)
	// CreateServicePrincipal creates a service principal for the given
	// application (client) ID and returns the service principal object ID
	// (the principal ID used for role assignment).
	CreateServicePrincipal(ctx context.Context, appID string) (principalID string, err error)
	// ResolveRoleDefinitionID resolves the role definition ID for the given
	// role display name at the given scope.
	ResolveRoleDefinitionID(ctx context.Context, scope, roleName string) (roleDefinitionID string, err error)
	// AssignRole creates a role assignment binding principalID to
	// roleDefinitionID at the given scope.
	AssignRole(ctx context.Context, scope, principalID, roleDefinitionID string) error
	// DeleteApplication deletes the application registration identified by
	// objectID. Deleting the application also removes its password credentials
	// and the service principal created from it, so it serves as the
	// compensating action for a partially completed creation flow.
	DeleteApplication(ctx context.Context, objectID string) error
}

// createAzureServicePrincipal performs the full create-for-rbac equivalent:
// it creates the application + password + service principal, resolves the
// "Reservations Administrator" role at subscription scope, assigns it, and
// returns the credential material.
//
// subscriptionID must already be validated (UUID) and tenantID resolved by the
// caller. The behavior matches:
//
//	az ad sp create-for-rbac --name CUDly \
//	  --role "Reservations Administrator" \
//	  --scopes /subscriptions/<subscriptionID>
func createAzureServicePrincipal(ctx context.Context, p azureSPProvisioner, subscriptionID, tenantID string) (azureSPResult, error) {
	scope := fmt.Sprintf("/subscriptions/%s", subscriptionID)

	objectID, appID, err := p.CreateApplication(ctx, azureSPName)
	if err != nil {
		return azureSPResult{}, fmt.Errorf("failed to create application registration: %w", err)
	}

	// rollback deletes the just-created application (which cascades to its
	// password credentials and the derived service principal) so a failure in
	// a later step does not orphan Azure AD objects. The cleanup runs on a
	// fresh context in case the parent is already canceled/expired. If the
	// cleanup itself fails, the operator is told exactly what to delete by hand.
	rollback := func(cause error) (azureSPResult, error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()
		if delErr := p.DeleteApplication(cleanupCtx, objectID); delErr != nil {
			return azureSPResult{}, fmt.Errorf("%w; additionally failed to roll back application %q (appId %s) -- delete it manually: %w",
				cause, objectID, appID, delErr)
		}
		return azureSPResult{}, fmt.Errorf("%w (rolled back: deleted application %q)", cause, objectID)
	}

	secret, err := p.AddPassword(ctx, objectID)
	if err != nil {
		return rollback(fmt.Errorf("failed to add password credential: %w", err))
	}

	principalID, err := p.CreateServicePrincipal(ctx, appID)
	if err != nil {
		return rollback(fmt.Errorf("failed to create service principal: %w", err))
	}

	roleDefID, err := p.ResolveRoleDefinitionID(ctx, scope, azureSPRoleName)
	if err != nil {
		return rollback(fmt.Errorf("failed to resolve %q role definition: %w", azureSPRoleName, err))
	}

	if err := p.AssignRole(ctx, scope, principalID, roleDefID); err != nil {
		return rollback(fmt.Errorf("failed to assign %q role at %s: %w", azureSPRoleName, scope, err))
	}

	return azureSPResult{
		AppID:        appID,
		ClientSecret: secret,
		TenantID:     tenantID,
	}, nil
}

// graphSPProvisioner is the production azureSPProvisioner backed by the
// Microsoft Graph SDK (application + service principal) and armauthorization
// (role definition + role assignment).
type graphSPProvisioner struct {
	graph    *msgraphsdk.GraphServiceClient
	roleDefs *armauthorization.RoleDefinitionsClient
	roleAsgn roleAssigner
	// retryInitial and retryBudget control the PrincipalNotFound retry loop
	// in AssignRole. They are set to the package constants by
	// newGraphSPProvisioner and overridden in tests to keep test duration short.
	retryInitial time.Duration
	retryBudget  time.Duration
}

// newGraphSPProvisioner builds a graphSPProvisioner authenticated with the
// Azure CLI credential, so the session established by "az login" (wizard
// Step 1) is reused -- matching the principal used by the rest of the wizard.
// subscriptionID seeds the RoleAssignmentsClient; the actual scope is passed
// per-call to its Create method.
func newGraphSPProvisioner(subscriptionID string) (*graphSPProvisioner, error) {
	cred, err := newAzureWizardCredential()
	if err != nil {
		return nil, err
	}

	graph, err := msgraphsdk.NewGraphServiceClientWithCredentials(
		cred, []string{"https://graph.microsoft.com/.default"})
	if err != nil {
		return nil, fmt.Errorf("failed to create Microsoft Graph client: %w", err)
	}

	roleDefs, err := armauthorization.NewRoleDefinitionsClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create role definitions client: %w", err)
	}

	roleAsgn, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create role assignments client: %w", err)
	}

	return &graphSPProvisioner{
		graph:        graph,
		roleDefs:     roleDefs,
		roleAsgn:     roleAsgn,
		retryInitial: roleAssignRetryInitial,
		retryBudget:  roleAssignRetryBudget,
	}, nil
}

func (g *graphSPProvisioner) CreateApplication(ctx context.Context, displayName string) (objectID, appID string, err error) {
	app := graphmodels.NewApplication()
	app.SetDisplayName(&displayName)

	created, err := g.graph.Applications().Post(ctx, app, nil)
	if err != nil {
		return "", "", err
	}
	oid := created.GetId()
	aid := created.GetAppId()
	if oid == nil || aid == nil {
		return "", "", fmt.Errorf("application created but Graph returned no id/appId")
	}
	return *oid, *aid, nil
}

func (g *graphSPProvisioner) AddPassword(ctx context.Context, objectID string) (string, error) {
	body := applications.NewItemAddPasswordPostRequestBody()
	cred := graphmodels.NewPasswordCredential()
	displayName := azureSPName + "-secret"
	cred.SetDisplayName(&displayName)
	body.SetPasswordCredential(cred)

	result, err := g.graph.Applications().ByApplicationId(objectID).AddPassword().Post(ctx, body, nil)
	if err != nil {
		return "", err
	}
	secret := result.GetSecretText()
	if secret == nil || *secret == "" {
		return "", fmt.Errorf("password credential created but Graph returned no secret text")
	}
	return *secret, nil
}

func (g *graphSPProvisioner) CreateServicePrincipal(ctx context.Context, appID string) (string, error) {
	sp := graphmodels.NewServicePrincipal()
	sp.SetAppId(&appID)

	created, err := g.graph.ServicePrincipals().Post(ctx, sp, nil)
	if err != nil {
		return "", err
	}
	principalID := created.GetId()
	if principalID == nil {
		return "", fmt.Errorf("service principal created but Graph returned no id")
	}
	return *principalID, nil
}

func (g *graphSPProvisioner) DeleteApplication(ctx context.Context, objectID string) error {
	return g.graph.Applications().ByApplicationId(objectID).Delete(ctx, nil)
}

func (g *graphSPProvisioner) ResolveRoleDefinitionID(ctx context.Context, scope, roleName string) (string, error) {
	filter := fmt.Sprintf("roleName eq '%s'", roleName)
	pager := g.roleDefs.NewListPager(scope, &armauthorization.RoleDefinitionsClientListOptions{
		Filter: &filter,
	})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for _, rd := range page.Value {
			if rd == nil || rd.ID == nil {
				continue
			}
			if rd.Properties != nil && rd.Properties.RoleName != nil && *rd.Properties.RoleName == roleName {
				return *rd.ID, nil
			}
		}
	}
	return "", fmt.Errorf("role definition %q not found at scope %s", roleName, scope)
}

func (g *graphSPProvisioner) AssignRole(ctx context.Context, scope, principalID, roleDefinitionID string) error {
	principalType := armauthorization.PrincipalTypeServicePrincipal
	params := armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      &principalID,
			RoleDefinitionID: &roleDefinitionID,
			PrincipalType:    &principalType,
		},
	}

	// Azure AD replication is eventually consistent: a service principal
	// created moments ago may not yet be visible to the ARM role-assignment
	// API in a different region, returning PrincipalNotFound. Retry with
	// bounded exponential back-off until the SP propagates or the budget is
	// exhausted (see also: feedback_tf_depends_on_rbac.md).
	deadline := time.Now().Add(g.retryBudget)
	delay := g.retryInitial
	for {
		_, err := g.roleAsgn.Create(ctx, scope, uuid.NewString(), params, nil)
		if err == nil {
			return nil
		}
		if !isPrincipalNotFoundErr(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"service principal %q did not propagate to ARM within %v "+
					"(Azure AD replication is eventually consistent -- "+
					"https://learn.microsoft.com/en-us/azure/role-based-access-control/troubleshooting): %w",
				principalID, g.retryBudget, err)
		}
		// Context cancellation is terminal: do not continue retrying.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, roleAssignRetryMax)
	}
}
