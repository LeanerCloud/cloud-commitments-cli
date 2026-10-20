package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"time"
)

// resolveAzureTenantIDFn and newAzureSPProvisionerFn are the cloud-facing
// constructors used by azureStepCreateServicePrincipal. They are variables so
// unit tests can substitute fakes and never reach Azure.
var (
	resolveAzureTenantIDFn  = resolveAzureTenantID
	newAzureSPProvisionerFn = func(subscriptionID string) (azureSPProvisioner, error) {
		return newGraphSPProvisioner(subscriptionID)
	}
)

// azureStepCreateServicePrincipal creates the service principal via the
// Microsoft Graph + armauthorization SDKs (the create-for-rbac equivalent) and
// returns its credentials for direct storage (nil when the operator skips the
// step). The client secret is never printed. It fails loud: any SDK error is
// returned rather than silently falling back to the CLI.
func azureStepCreateServicePrincipal(ctx context.Context, reader *bufio.Reader, subscriptionID string) (*AzureCredentials, error) {
	fmt.Println()
	fmt.Println("Step 3: Create Service Principal")
	fmt.Println("---------------------------------")
	fmt.Println("This creates an Azure Service Principal with the")
	fmt.Printf("%q role at subscription scope, via the Microsoft Graph SDK.\n", azureSPRoleName)
	fmt.Println()
	fmt.Printf("Create service principal %q with role %q at /subscriptions/%s?\n", azureSPName, azureSPRoleName, subscriptionID)
	fmt.Printf("[r]un, [S]kip (default)? ")

	choice, err := readTrimmedLine(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read service-principal choice: %w", err)
	}
	// Creating an Entra app, password and role assignment is not harmless, so
	// Enter skips, like the GCP wizard.
	choice = strings.ToLower(choice)
	if choice != "r" && choice != "run" {
		fmt.Println("Skipping Create Service Principal")
		fmt.Println()
		fmt.Println("Provide the appId, client secret and tenant ID for an existing")
		fmt.Println("service principal in the next step.")
		fmt.Println()
		return nil, nil
	}

	// The step budget must exceed roleAssignRetryBudget (3 min) plus the
	// pre-assignment overhead (tenant resolution + the application / password /
	// service-principal / role-definition Graph calls) so the PrincipalNotFound
	// retry loop in AssignRole gets its full propagation budget. A tighter
	// budget would cut the retry short via ctx cancellation, then the rollback
	// would delete the just-created application and reset the AAD replication
	// clock on every re-run. See roleAssignRetryBudget in configure_azure_sp.go.
	spCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()

	tenantID, err := resolveAzureTenantIDFn(spCtx, subscriptionID)
	if err != nil {
		return nil, err
	}

	provisioner, err := newAzureSPProvisionerFn(subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Azure SDK clients: %w\n"+
			"Ensure you are authenticated: run 'az login' (Step 1) before continuing", err)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))
	result, err := createAzureServicePrincipal(spCtx, provisioner, subscriptionID, tenantID)
	if err != nil {
		return nil, err
	}
	fmt.Println(strings.Repeat("-", 60))

	printAzureSPResult(result, subscriptionID, azureOpts.StackName)
	return &AzureCredentials{
		TenantID:       result.TenantID,
		ClientID:       result.AppID,
		ClientSecret:   result.ClientSecret,
		SubscriptionID: subscriptionID,
	}, nil
}

// printAzureSPResult prints the non-secret identifiers of the created service
// principal. The client secret is deliberately NOT printed (not even
// partially): stdout is routinely captured by script/tee, CI logs and terminal
// scrollback, and the wizard hands the secret straight to the Secrets Manager
// store step, so the operator never needs to see or retype it.
func printAzureSPResult(result azureSPResult, subscriptionID, stackName string) {
	fmt.Println()
	fmt.Println("Service principal created:")
	fmt.Println()
	fmt.Printf("  appId (Client ID):        %s\n", result.AppID)
	fmt.Println("  password (Client Secret): [hidden]")
	fmt.Printf("  tenant (Tenant ID):        %s\n", result.TenantID)
	fmt.Printf("  Subscription ID:           %s\n", subscriptionID)
	fmt.Println()
	fmt.Printf("The client secret is not displayed. It will be stored directly in the\n")
	fmt.Printf("AWS Secrets Manager secret %q (field client_secret).\n", stackName+"-AzureCredentials")
	fmt.Println()
}

// withAzureSPRecoveryHint annotates a failure that happens after the wizard
// created a service principal but before its secret was stored (AWS config
// and credentials are checked before creation, so this covers the remaining
// store-time failures such as ListSecrets/UpdateSecret errors). The secret was
// never displayed, so the operator is told how to mint a replacement instead.
func withAzureSPRecoveryHint(err error, created *AzureCredentials) error {
	if created == nil {
		return err
	}
	return fmt.Errorf("%w\nThe service principal (appId %s) was created but its client secret was NOT stored "+
		"and was never displayed. Fix the error above, then generate a new secret with "+
		"'az ad app credential reset --id %s' and re-run 'cudly configure-azure --skip-setup', "+
		"entering it at the hidden Client Secret prompt",
		err, created.ClientID, created.ClientID)
}
