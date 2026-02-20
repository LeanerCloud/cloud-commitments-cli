package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

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

To create an Azure Service Principal:
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

// storeAzureCredentials stores Azure credentials in the secrets store
func storeAzureCredentials(ctx context.Context, store SecretsStore, stackName string, creds AzureCredentials) error {
	// Validate credentials
	if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" || creds.SubscriptionID == "" {
		return fmt.Errorf("all credentials are required: tenant-id, client-id, client-secret, subscription-id")
	}

	// Validate UUID format for all ID fields
	if err := validateAzureUUID(creds.TenantID, "Tenant ID"); err != nil {
		return err
	}
	if err := validateAzureUUID(creds.ClientID, "Client ID"); err != nil {
		return err
	}
	if err := validateAzureUUID(creds.SubscriptionID, "Subscription ID"); err != nil {
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
		if err := runAzureSetupCommands(reader); err != nil {
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
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read tenant ID: %w", err)
		}
		creds.TenantID = strings.TrimSpace(input)
	}

	if creds.ClientID == "" {
		fmt.Print("Client ID (appId): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read client ID: %w", err)
		}
		creds.ClientID = strings.TrimSpace(input)
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
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read subscription ID: %w", err)
		}
		creds.SubscriptionID = strings.TrimSpace(input)
	}

	return nil
}

// runAzureSetupCommands runs the Azure CLI commands interactively
func runAzureSetupCommands(reader *bufio.Reader) error {
	fmt.Println("Step 1: Azure Login")
	fmt.Println("-------------------")
	fmt.Println("This will open a browser window for Azure authentication.")
	fmt.Println()

	if err := promptAndRunExplicitCommand(reader, "Azure Login", "az login", "az", "login"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Step 2: Get Subscription ID")
	fmt.Println("---------------------------")
	fmt.Println("List your Azure subscriptions to find the Subscription ID:")
	fmt.Println()

	if err := promptAndRunExplicitCommand(reader, "List Subscriptions", "az account list --output table", "az", "account", "list", "--output", "table"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Print("Enter your Subscription ID from above: ")
	subscriptionID, _ := reader.ReadString('\n')
	subscriptionID = strings.TrimSpace(subscriptionID)

	if subscriptionID == "" {
		return fmt.Errorf("subscription ID is required")
	}

	// Validate subscription ID to prevent command injection
	if err := validateAzureUUID(subscriptionID, "Subscription ID"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Step 3: Create Service Principal")
	fmt.Println("---------------------------------")
	fmt.Println("This creates an Azure Service Principal with Reservation Administrator role.")
	fmt.Println()

	// Build the create SP command - run directly without shell to avoid injection
	// Using exec.Command directly with proper arguments
	fmt.Printf("Command: az ad sp create-for-rbac --name CUDly --role \"Reservations Administrator\" --scopes /subscriptions/%s\n", subscriptionID)
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? ")

	choice, _ := reader.ReadString('\n')
	choice = strings.ToLower(strings.TrimSpace(choice))

	if choice == "r" || choice == "run" || choice == "" {
		fmt.Println()
		fmt.Println(strings.Repeat("-", 60))
		cmd := exec.Command("az", "ad", "sp", "create-for-rbac",
			"--name", "CUDly",
			"--role", "Reservations Administrator",
			"--scopes", fmt.Sprintf("/subscriptions/%s", subscriptionID))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fmt.Printf("Command failed: %v\n", err)
			fmt.Print("Continue anyway? [y/N]: ")
			response, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(response)) != "y" {
				return fmt.Errorf("failed to create service principal: %w", err)
			}
		}
		fmt.Println(strings.Repeat("-", 60))
	} else {
		fmt.Println("Skipping Create Service Principal")
	}

	fmt.Println()
	fmt.Println("IMPORTANT: Copy the output above! You'll need:")
	fmt.Println("  - appId      -> Client ID")
	fmt.Println("  - password   -> Client Secret")
	fmt.Println("  - tenant     -> Tenant ID")
	fmt.Printf("  - Subscription ID: %s\n", subscriptionID)
	fmt.Println()

	return nil
}

// promptAndRunExplicitCommand shows a command and asks to run or skip.
// Takes explicit program and args to avoid command injection via string splitting.
func promptAndRunExplicitCommand(reader *bufio.Reader, name, displayCmd string, program string, args ...string) error {
	fmt.Printf("Command: %s\n", displayCmd)
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? ")

	choice, _ := reader.ReadString('\n')
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "r", "run", "":
		return executeExplicitCommand(displayCmd, program, args...)
	case "s", "skip":
		fmt.Printf("Skipping %s\n", name)
		return nil
	default:
		fmt.Printf("Unknown option '%s', skipping\n", choice)
		return nil
	}
}

// executeExplicitCommand runs a command with explicit program and arguments
func executeExplicitCommand(displayCmd string, program string, args ...string) error {
	fmt.Println()
	fmt.Printf("Executing: %s\n", displayCmd)
	fmt.Println(strings.Repeat("-", 60))

	cmd := exec.Command(program, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	fmt.Println(strings.Repeat("-", 60))

	if err != nil {
		fmt.Printf("Command failed: %v\n", err)
		fmt.Print("Continue anyway? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			return fmt.Errorf("command failed: %w", err)
		}
	}

	return nil
}
