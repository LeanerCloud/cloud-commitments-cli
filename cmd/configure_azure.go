package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// azureUUIDRegex validates Azure UUIDs (subscription IDs, tenant IDs, client IDs)
var azureUUIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateAzureUUID validates an Azure UUID to prevent command injection
func validateAzureUUID(uuid, fieldName string) error {
	if !azureUUIDRegex.MatchString(uuid) {
		return fmt.Errorf("invalid %s format: must be a valid UUID (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)", fieldName)
	}
	return nil
}

// readTrimmedLine reads one line from reader and returns it with surrounding
// whitespace trimmed. io.EOF is tolerated when data was read — a final
// unterminated line from piped input (e.g. `printf "r" | cudly configure-azure`)
// is still valid input. io.EOF with no data, or any other error, is returned.
func readTrimmedLine(reader *bufio.Reader) (string, error) {
	input, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && input != "") {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// AzureCredentials holds the Azure Service Principal credentials
type AzureCredentials struct {
	TenantID       string `json:"tenant_id"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	SubscriptionID string `json:"subscription_id"`
}

// AzureConfigOptions holds configuration for the Azure config command
type AzureConfigOptions struct {
	StackName      string
	Profile        string
	TenantID       string
	ClientID       string
	ClientSecret   string
	SubscriptionID string
	Interactive    bool
	SkipSetup      bool
}

var azureOpts = AzureConfigOptions{}

var configureAzureCmd = &cobra.Command{
	Use:   "configure-azure",
	Short: "Configure Azure credentials for CUDly",
	Long: `Configure Azure Service Principal credentials for multi-cloud commitment management.

This command stores your Azure credentials in AWS Secrets Manager for use by CUDly.

You can provide credentials via flags or interactively:
  cudly configure-azure --stack-name my-cudly --tenant-id xxx --client-id xxx --client-secret xxx --subscription-id xxx
  cudly configure-azure --stack-name my-cudly --interactive

To create an Azure Service Principal manually:
  az login
  az ad sp create-for-rbac --name "CUDly" --role "Reservation Administrator" --scopes /subscriptions/<subscription-id>`,
	RunE: runConfigureAzure,
}

func init() {
	rootCmd.AddCommand(configureAzureCmd)

	configureAzureCmd.Flags().StringVar(&azureOpts.StackName, "stack-name", "cudly", "CUDly CloudFormation stack name")
	configureAzureCmd.Flags().StringVar(&azureOpts.Profile, "profile", "", "AWS profile to use")
	configureAzureCmd.Flags().StringVar(&azureOpts.TenantID, "tenant-id", "", "Azure AD Tenant ID")
	configureAzureCmd.Flags().StringVar(&azureOpts.ClientID, "client-id", "", "Azure Service Principal Client ID")
	configureAzureCmd.Flags().StringVar(&azureOpts.ClientSecret, "client-secret", "", "Azure Service Principal Client Secret")
	configureAzureCmd.Flags().StringVar(&azureOpts.SubscriptionID, "subscription-id", "", "Azure Subscription ID")
	configureAzureCmd.Flags().BoolVarP(&azureOpts.Interactive, "interactive", "i", false, "Prompt for credentials interactively")
	configureAzureCmd.Flags().BoolVar(&azureOpts.SkipSetup, "skip-setup", false, "Skip Azure CLI setup commands (az login, create service principal)")
}

// validateAzureCredentialFields checks that all required fields are non-empty
// and that UUID-typed fields have the correct format.
func validateAzureCredentialFields(creds AzureCredentials) error {
	if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" || creds.SubscriptionID == "" {
		return fmt.Errorf("all credentials are required: tenant-id, client-id, client-secret, subscription-id")
	}
	if err := validateAzureUUID(creds.TenantID, "Tenant ID"); err != nil {
		return err
	}
	if err := validateAzureUUID(creds.ClientID, "Client ID"); err != nil {
		return err
	}
	return validateAzureUUID(creds.SubscriptionID, "Subscription ID")
}

// storeAzureCredentials stores Azure credentials in the secrets store
func storeAzureCredentials(ctx context.Context, store SecretsStore, stackName string, creds AzureCredentials) error {
	if err := validateAzureCredentialFields(creds); err != nil {
		return err
	}

	// Build expected secret name pattern
	secretName := fmt.Sprintf("%s-AzureCredentials", stackName)

	// Try to find the actual secret ARN by listing secrets
	arns, err := store.ListSecrets(ctx, secretName)

	// Use the ARN if found, otherwise use the name (will fail if secret doesn't exist)
	secretID := secretName
	if err == nil && len(arns) > 0 {
		secretID = arns[0]
	}

	// Marshal credentials to JSON
	credJSON, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	// Store credentials in Secrets Manager
	err = store.UpdateSecret(ctx, secretID, string(credJSON))
	if err != nil {
		return fmt.Errorf("failed to store credentials in Secrets Manager: %w", err)
	}

	return nil
}

func runConfigureAzure(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Configure Azure Service Principal credentials for CUDly")
	fmt.Println("========================================================")
	fmt.Println()

	// Run Azure CLI setup if not skipped
	if !azureOpts.SkipSetup {
		if err := runAzureSetupCommands(ctx, reader); err != nil {
			return err
		}
	}

	cfg, err := loadAWSConfigForAzure(ctx)
	if err != nil {
		return err
	}

	creds, err := collectAzureCredentials(reader)
	if err != nil {
		return err
	}

	smClient := secretsmanager.NewFromConfig(cfg)
	store := NewAWSSecretsStore(smClient)

	if err := storeAzureCredentials(ctx, store, azureOpts.StackName, creds); err != nil {
		return err
	}

	// Zero out sensitive data from memory
	azureOpts.ClientSecret = ""
	creds.ClientSecret = ""

	log.Printf("Azure credentials stored successfully in Secrets Manager")
	fmt.Println("\nAzure configuration complete!")
	fmt.Println("CUDly can now manage Azure Reserved Instances and Savings Plans.")

	return nil
}

// loadAWSConfigForAzure loads AWS configuration with optional profile
func loadAWSConfigForAzure(ctx context.Context) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if azureOpts.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(azureOpts.Profile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cfg, nil
}

// collectAzureCredentials collects Azure credentials interactively or from flags
func collectAzureCredentials(reader *bufio.Reader) (AzureCredentials, error) {
	creds := AzureCredentials{
		TenantID:       azureOpts.TenantID,
		ClientID:       azureOpts.ClientID,
		ClientSecret:   azureOpts.ClientSecret,
		SubscriptionID: azureOpts.SubscriptionID,
	}

	needsInput := azureOpts.Interactive || (creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" || creds.SubscriptionID == "")
	if !needsInput {
		return creds, nil
	}

	fmt.Println("\nEnter the credentials from the Service Principal output above:")
	fmt.Println()

	if err := promptForAzureCredentialFields(reader, &creds); err != nil {
		return AzureCredentials{}, err
	}

	return creds, nil
}

// promptForAzureCredentialFields prompts for missing credential fields
func promptForAzureCredentialFields(reader *bufio.Reader, creds *AzureCredentials) error {
	if creds.TenantID == "" {
		fmt.Print("Azure Tenant ID: ")
		input, err := readTrimmedLine(reader)
		if err != nil {
			return fmt.Errorf("failed to read tenant ID: %w", err)
		}
		creds.TenantID = input
	}

	if creds.ClientID == "" {
		fmt.Print("Client ID (appId): ")
		input, err := readTrimmedLine(reader)
		if err != nil {
			return fmt.Errorf("failed to read client ID: %w", err)
		}
		creds.ClientID = input
	}

	if creds.ClientSecret == "" {
		fmt.Print("Client Secret (password): ")
		secret, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return fmt.Errorf("failed to read secret: %w", err)
		}
		fmt.Println()
		creds.ClientSecret = string(secret)
	}

	if creds.SubscriptionID == "" {
		fmt.Print("Subscription ID: ")
		input, err := readTrimmedLine(reader)
		if err != nil {
			return fmt.Errorf("failed to read subscription ID: %w", err)
		}
		creds.SubscriptionID = input
	}

	return nil
}

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
func runAzureSetupCommands(ctx context.Context, reader *bufio.Reader) error {
	if err := azureStepLogin(reader); err != nil {
		return err
	}

	subscriptionID, err := azureStepListSubscriptions(ctx, reader)
	if err != nil {
		return err
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

// azureStepListSubscriptions lists subscriptions via SDK and prompts the
// operator to enter their subscription ID. It fails loud (no CLI fallback): if
// the Azure CLI credential cannot authenticate, it returns an error
// instructing the operator to run "az login" first.
func azureStepListSubscriptions(ctx context.Context, reader *bufio.Reader) (string, error) {
	fmt.Println()
	fmt.Println("Step 2: Get Subscription ID")
	fmt.Println("---------------------------")
	fmt.Println("Listing your Azure subscriptions via SDK (Azure CLI credential)...")
	fmt.Println()

	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := listAzureSubscriptions(listCtx); err != nil {
		return "", fmt.Errorf("failed to list Azure subscriptions via SDK: %w\n"+
			"Ensure you are authenticated: run 'az login' (Step 1) before continuing", err)
	}

	fmt.Println()
	fmt.Print("Enter your Subscription ID from above: ")
	subscriptionID, err := readTrimmedLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read subscription ID: %w", err)
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

// azureStepCreateServicePrincipal creates the service principal via the
// Microsoft Graph + armauthorization SDKs (the create-for-rbac equivalent) and
// prints the resulting credential material. It fails loud: any SDK error is
// returned rather than silently falling back to the CLI.
func azureStepCreateServicePrincipal(ctx context.Context, reader *bufio.Reader, subscriptionID string) error {
	fmt.Println()
	fmt.Println("Step 3: Create Service Principal")
	fmt.Println("---------------------------------")
	fmt.Println("This creates an Azure Service Principal with the")
	fmt.Printf("%q role at subscription scope, via the Microsoft Graph SDK.\n", azureSPRoleName)
	fmt.Println()
	fmt.Printf("Create service principal %q with role %q at /subscriptions/%s?\n", azureSPName, azureSPRoleName, subscriptionID)
	fmt.Printf("[R]un, [S]kip? ")

	choice, err := readTrimmedLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read service-principal choice: %w", err)
	}
	choice = strings.ToLower(choice)
	if choice != "r" && choice != "run" && choice != "" {
		fmt.Println("Skipping Create Service Principal")
		fmt.Println()
		fmt.Println("Provide the appId, client secret and tenant ID for an existing")
		fmt.Println("service principal in the next step.")
		fmt.Println()
		return nil
	}

	spCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	tenantID, err := resolveAzureTenantID(spCtx, subscriptionID)
	if err != nil {
		return err
	}

	provisioner, err := newGraphSPProvisioner(subscriptionID)
	if err != nil {
		return fmt.Errorf("failed to initialize Azure SDK clients: %w\n"+
			"Ensure you are authenticated: run 'az login' (Step 1) before continuing", err)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))
	result, err := createAzureServicePrincipal(spCtx, provisioner, subscriptionID, tenantID)
	if err != nil {
		return err
	}
	fmt.Println(strings.Repeat("-", 60))

	printAzureSPResult(result, subscriptionID)
	return nil
}

// printAzureSPResult prints the credential material in the same shape that
// "az ad sp create-for-rbac" prints, so the operator can feed it into the
// credential collection step that follows.
func printAzureSPResult(result azureSPResult, subscriptionID string) {
	fmt.Println()
	fmt.Println("Service principal created. Credential material:")
	fmt.Println()
	fmt.Printf("  appId (Client ID):       %s\n", result.AppID)
	fmt.Printf("  password (Client Secret): %s\n", result.ClientSecret)
	fmt.Printf("  tenant (Tenant ID):       %s\n", result.TenantID)
	fmt.Println()
	fmt.Println("IMPORTANT: copy the client secret now -- it cannot be retrieved later.")
	fmt.Println("You'll enter these values in the next step:")
	fmt.Println("  - appId      -> Client ID")
	fmt.Println("  - password   -> Client Secret")
	fmt.Println("  - tenant     -> Tenant ID")
	fmt.Printf("  - Subscription ID: %s\n", subscriptionID)
	fmt.Println()
}

// promptAndRunExplicitCommand shows a command and asks to run or skip.
// Takes explicit program and args to avoid command injection via string splitting.
func promptAndRunExplicitCommand(reader *bufio.Reader, name, displayCmd string, program string, args ...string) error {
	fmt.Printf("Command: %s\n", displayCmd)
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? ")

	choice, err := readTrimmedLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.ToLower(choice)

	switch choice {
	case "r", "run", "":
		return executeExplicitCommand(reader, displayCmd, program, args...)
	case "s", "skip":
		fmt.Printf("Skipping %s\n", name)
		return nil
	default:
		fmt.Printf("Unknown option '%s', skipping\n", choice)
		return nil
	}
}

// executeExplicitCommand runs a command with explicit program and arguments.
// It is used only for the interactive "az login" auth bootstrap (Step 1),
// which has no SDK equivalent that preserves the cached-credential UX.
// The caller's reader is threaded through to the retry prompt so all input
// is consumed from one consistent buffered stream (a fresh
// bufio.NewReader(os.Stdin) here would drop input already buffered by the
// caller's reader, breaking piped input after earlier prompts).
func executeExplicitCommand(reader *bufio.Reader, displayCmd string, program string, args ...string) error {
	fmt.Println()
	fmt.Printf("Executing: %s\n", displayCmd)
	fmt.Println(strings.Repeat("-", 60))

	//nolint:gosec // G204: interactive operator auth (az login), hardcoded args, no shell
	cmd := exec.Command(program, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	fmt.Println(strings.Repeat("-", 60))

	if err != nil {
		fmt.Printf("Command failed: %v\n", err)
		fmt.Print("Continue anyway? [y/N]: ")
		response, readErr := readTrimmedLine(reader)
		if readErr != nil {
			return fmt.Errorf("failed to read response: %w", readErr)
		}
		if strings.ToLower(response) != "y" {
			return fmt.Errorf("command failed: %w", err)
		}
	}

	return nil
}
