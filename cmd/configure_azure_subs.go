package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// newAzureWizardCredential builds the credential used by the interactive Azure
// setup wizard. It binds explicitly to the Azure CLI session (the "az login"
// the operator runs in Step 1) via AzureCLICredential rather than
// DefaultAzureCredential, whose chain prioritizes environment / workload /
// managed-identity credentials and could otherwise resolve to a different
// principal than the one the operator just signed in as.
func newAzureWizardCredential() (azcore.TokenCredential, error) {
	cred, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Azure CLI credential: %w\n"+
			"Ensure you are authenticated: run 'az login' (Step 1) before continuing", err)
	}
	return cred, nil
}

// listAzureSubscriptions retrieves the operator's subscriptions via the ARM
// Subscriptions SDK and prints them in a table matching "az account list"
// output. It uses the Azure CLI credential so the listing matches the
// operator's active "az login" session.
func listAzureSubscriptions(ctx context.Context) error {
	cred, err := newAzureWizardCredential()
	if err != nil {
		return err
	}

	client, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create subscriptions client: %w", err)
	}

	fmt.Printf("%-40s  %-38s  %s\n", "Name", "SubscriptionId", "State")
	fmt.Println(strings.Repeat("-", 95))

	pager := client.NewListPager(nil)
	for pager.More() {
		page, pageErr := pager.NextPage(ctx)
		if pageErr != nil {
			return fmt.Errorf("failed to list subscriptions: %w", pageErr)
		}
		for _, sub := range page.Value {
			name := ""
			subID := ""
			state := ""
			if sub.DisplayName != nil {
				name = *sub.DisplayName
			}
			if sub.SubscriptionID != nil {
				subID = *sub.SubscriptionID
			}
			if sub.State != nil {
				state = string(*sub.State)
			}
			fmt.Printf("%-40s  %-38s  %s\n", name, subID, state)
		}
	}
	return nil
}
