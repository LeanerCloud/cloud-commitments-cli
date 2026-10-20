package main

import (
	"bufio"
	"context"
	"fmt"

	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// runAzureSetupCommands guides the operator through the Azure setup wizard.
//
// Step 1 (az login): performed via the Azure CLI. "az login" launches an
// interactive browser-based OAuth flow that cannot be replicated through the
// SDK on behalf of a human operator who does not yet have a credential. This
// is the only CLI call retained in this wizard.
//
// Step 2 (list subscriptions): performed via the ARM Subscriptions SDK using
// the Azure CLI credential, which reuses the session that "az login" just
// established. Fails loud if the SDK cannot authenticate (no CLI fallback).
//
// Step 3 (create service principal): performed via the Microsoft Graph SDK
// (application + service principal + password credential) and armauthorization
// (resolve the "Reservations Administrator" role definition and assign it at
// subscription scope). This is the create-for-rbac equivalent and fails loud
// on any SDK error.
//
// It returns the created service principal's credentials (nil when Step 3 was
// skipped) so the caller can store them without echoing the secret.
func runAzureSetupCommands(ctx context.Context, reader *bufio.Reader) (*AzureCredentials, error) {
	if err := azureStepLogin(reader); err != nil {
		return nil, err
	}

	subscriptionID, err := azureStepListSubscriptions(ctx, reader)
	if err != nil {
		return nil, err
	}

	return azureStepCreateServicePrincipal(ctx, reader, subscriptionID)
}

// azureStepLogin prompts to run "az login".
func azureStepLogin(reader *bufio.Reader) error {
	fmt.Println("Step 1: Azure Login")
	fmt.Println("-------------------")
	fmt.Println("This will open a browser window for Azure authentication.")
	fmt.Println()
	return promptAndRunExplicitCommand(reader, "Azure Login", "az login", "az", "login")
}

// listAzureSubscriptionsWithTimeout runs the SDK subscription listing under a
// bounded timeout so a hung ARM call cannot stall the wizard.
func listAzureSubscriptionsWithTimeout(ctx context.Context) error {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return listAzureSubscriptions(listCtx)
}

// azureStepListSubscriptions optionally lists subscriptions via SDK and prompts
// the operator to enter their subscription ID. The listing is behind a
// [R]un/[S]kip prompt so an operator who already knows their subscription ID
// can proceed even if the SDK listing would fail; when RUN it fails loud (no
// CLI fallback), instructing the operator to run "az login" first.
func azureStepListSubscriptions(ctx context.Context, reader *bufio.Reader) (string, error) {
	fmt.Println()
	fmt.Println("Step 2: Get Subscription ID")
	fmt.Println("---------------------------")

	run, err := promptRunOrSkipListing(reader, "the Azure subscription listing (via SDK)")
	if err != nil {
		return "", err
	}
	if run {
		fmt.Println("Listing your Azure subscriptions via SDK (Azure CLI credential)...")
		fmt.Println()
		if err = listAzureSubscriptionsWithTimeout(ctx); err != nil {
			return "", fmt.Errorf("failed to list Azure subscriptions via SDK: %w\n"+
				"Ensure you are authenticated: run 'az login' (Step 1) before continuing", err)
		}
		fmt.Println()
	}

	fmt.Print("Enter your Subscription ID: ")
	subscriptionID, err := readTrimmedLine(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read subscription ID: %w", err)
	}

	if subscriptionID == "" {
		return "", fmt.Errorf("subscription ID is required")
	}
	if err := validateAzureUUID(subscriptionID, "Subscription ID"); err != nil {
		return "", err
	}
	return subscriptionID, nil
}

// resolveAzureTenantID looks up the tenant ID for the given subscription via
// the ARM Subscriptions SDK, using the Azure CLI ("az login") credential.
func resolveAzureTenantID(ctx context.Context, subscriptionID string) (string, error) {
	cred, err := newAzureWizardCredential()
	if err != nil {
		return "", err
	}
	client, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create subscriptions client: %w", err)
	}
	resp, err := client.Get(ctx, subscriptionID, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get subscription %s: %w", subscriptionID, err)
	}
	if resp.TenantID == nil || *resp.TenantID == "" {
		return "", fmt.Errorf("subscription %s returned no tenant ID", subscriptionID)
	}
	return *resp.TenantID, nil
}
