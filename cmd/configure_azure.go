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
	"regexp"
	"strings"

	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// azureUUIDRegex validates Azure UUIDs (subscription IDs, tenant IDs, client IDs).
var azureUUIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateAzureUUID validates an Azure UUID to prevent command injection.
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
	if err != nil && (!errors.Is(err, io.EOF) || input == "") {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// promptRunOrSkipListing asks whether to run an interactive SDK listing step
// (e.g. list subscriptions / projects) or skip straight to entering the ID.
// It returns true to run the listing and false to skip. Skipping is a
// deliberate operator choice for someone who already knows their ID; the
// listing still fails loud WHEN RUN (skip is not a silent fallback on error).
// Empty input or "r"/"run" runs the listing; "s"/"skip" skips it.
func promptRunOrSkipListing(reader *bufio.Reader, what string) (bool, error) {
	fmt.Printf("[R]un %s, or [S]kip to enter the ID directly? ", what)
	choice, err := readTrimmedLine(reader)
	if err != nil {
		return false, fmt.Errorf("failed to read choice: %w", err)
	}
	switch strings.ToLower(choice) {
	case "s", "skip":
		return false, nil
	default:
		return true, nil
	}
}

// AzureCredentials holds the Azure Service Principal credentials.
type AzureCredentials struct {
	TenantID       string `json:"tenant_id"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"` // #nosec G117 -- operator-supplied credential input read from the user's own Azure service principal; marshaled only to store in AWS Secrets Manager, never a hardcoded secret and never logged (verified)
	SubscriptionID string `json:"subscription_id"`
}

// AzureConfigOptions holds configuration for the Azure config command.
type AzureConfigOptions struct {
	StackName      string
	Profile        string
	TenantID       string
	ClientID       string
	ClientSecret   string // #nosec G117 -- operator-supplied credential input from a CLI flag or interactive prompt; never a hardcoded secret and never logged (verified)
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

You can provide credentials via flags or interactively (the client secret is
read from a prompt, so it stays out of shell history and process listings):
  cudly configure-azure --stack-name my-cudly --tenant-id xxx --client-id xxx --subscription-id xxx
  cudly configure-azure --stack-name my-cudly --interactive

To create an Azure Service Principal manually:
  az login
  az ad sp create-for-rbac --name "CUDly" --role "Reservations Administrator" --scopes /subscriptions/<subscription-id>`,
	RunE: runConfigureAzure,
}

func init() {
	rootCmd.AddCommand(configureAzureCmd)

	configureAzureCmd.Flags().StringVar(&azureOpts.StackName, "stack-name", "cudly", "Name of the CUDly deployment; prefixes the AWS Secrets Manager secret (<stack-name>-AzureCredentials)")
	configureAzureCmd.Flags().StringVar(&azureOpts.Profile, "profile", "", "AWS profile to use")
	configureAzureCmd.Flags().StringVar(&azureOpts.TenantID, "tenant-id", "", "Azure AD Tenant ID")
	configureAzureCmd.Flags().StringVar(&azureOpts.ClientID, "client-id", "", "Azure Service Principal Client ID")
	configureAzureCmd.Flags().StringVar(&azureOpts.ClientSecret, "client-secret", "", "Azure Service Principal Client Secret")
	if err := configureAzureCmd.Flags().MarkDeprecated("client-secret", "a secret on the command line leaks into shell history and process listings; omit it and enter it at the interactive Client Secret prompt"); err != nil {
		panic(err)
	}
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

// storeAzureCredentials stores Azure credentials in the secrets store.
func storeAzureCredentials(ctx context.Context, store SecretsStore, stackName string, creds AzureCredentials) error {
	if err := validateAzureCredentialFields(creds); err != nil {
		return err
	}

	// Build expected secret name pattern
	secretName := fmt.Sprintf("%s-AzureCredentials", stackName)

	secretID, err := resolveSecretID(ctx, store, secretName)
	if err != nil {
		return err
	}

	// Marshal credentials to JSON
	credJSON, err := json.Marshal(creds) // #nosec G117 -- AzureCredentials marshaled intentionally for Secrets Manager storage
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

func runConfigureAzure(_ *cobra.Command, _ []string) error {
	return configureAzure(context.Background(), bufio.NewReader(os.Stdin))
}

// configureAzure runs the configure-azure flow, reading operator input from
// reader.
func configureAzure(ctx context.Context, reader *bufio.Reader) error {
	fmt.Println("Configure Azure Service Principal credentials for CUDly")
	fmt.Println("========================================================")
	fmt.Println()

	// Open the Secrets Manager store (AWS config + credentials) BEFORE the
	// wizard can create a service principal: these are the likeliest
	// failures and have no side effects, so failing here never leaves an
	// orphaned service principal whose undisplayed secret is lost.
	store, err := openAzureSecretsStoreFn(ctx)
	if err != nil {
		return err
	}

	// Run Azure CLI setup if not skipped. When the wizard creates a service
	// principal, its credentials are carried straight into the store step so
	// the client secret never round-trips through the terminal.
	var created *AzureCredentials
	if !azureOpts.SkipSetup {
		if created, err = runAzureSetupCommands(ctx, reader); err != nil {
			return err
		}
	}

	creds, err := collectAzureCredentials(reader, created)
	if err != nil {
		return err
	}

	if err := storeAzureCredentials(ctx, store, azureOpts.StackName, creds); err != nil {
		return withAzureSPRecoveryHint(err, created)
	}

	// Zero out sensitive data from memory
	azureOpts.ClientSecret = ""
	creds.ClientSecret = ""
	if created != nil {
		created.ClientSecret = ""
	}

	log.Printf("Azure credentials stored successfully in Secrets Manager")
	fmt.Println("\nAzure configuration complete!")
	fmt.Println("CUDly can now manage Azure Reserved Instances and Savings Plans.")

	return nil
}

// openAzureSecretsStoreFn builds the Secrets Manager store used by
// configureAzure. It is a variable so unit tests can substitute a mock and
// never reach AWS.
var openAzureSecretsStoreFn = openAzureSecretsStore

// openAzureSecretsStore loads the AWS config and resolves AWS credentials, then
// returns a Secrets Manager store. Resolving credentials up front makes a
// missing or expired AWS login fail before any Azure resource is created.
func openAzureSecretsStore(ctx context.Context) (SecretsStore, error) {
	cfg, err := loadAWSConfigForAzure(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.Credentials == nil {
		return nil, fmt.Errorf("failed to resolve AWS credentials: no credential provider configured")
	}
	credCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := cfg.Credentials.Retrieve(credCtx); err != nil {
		return nil, fmt.Errorf("failed to resolve AWS credentials: %w", err)
	}
	return NewAWSSecretsStore(secretsmanager.NewFromConfig(cfg)), nil
}

// loadAWSConfigForAzure loads AWS configuration with optional profile.
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

// collectAzureCredentials collects Azure credentials interactively or from flags.
// When created is non-nil (the wizard just created a service principal), its
// credentials are used as-is and nothing is prompted: the freshly generated
// client secret is never displayed, so the operator could not retype it anyway.
func collectAzureCredentials(reader *bufio.Reader, created *AzureCredentials) (AzureCredentials, error) {
	if created != nil {
		fmt.Println("\nUsing the service principal created in Step 3.")
		return *created, nil
	}

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

	fmt.Println("\nEnter the credentials of an existing Azure Service Principal:")
	fmt.Println()

	if err := promptForAzureCredentialFields(reader, &creds); err != nil {
		return AzureCredentials{}, err
	}

	return creds, nil
}

// promptForAzureCredentialFields prompts for missing credential fields.
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
		// int(os.Stdin.Fd()) is portable: syscall.Stdin is an int on Unix but a
		// Handle on Windows, so passing it to term.ReadPassword (which takes an
		// int) breaks GOOS=windows builds.
		secret, err := term.ReadPassword(int(os.Stdin.Fd())) // #nosec G115 -- OS file descriptors fit in int on every supported platform
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
