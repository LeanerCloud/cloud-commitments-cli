package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/spf13/cobra"
)

// gcpProjectIDRegex validates GCP project IDs (lowercase letters, digits, hyphens, 6-30 chars).
var gcpProjectIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// validateGCPProjectID validates a GCP project ID to prevent command injection.
func validateGCPProjectID(projectID string) error {
	if !gcpProjectIDRegex.MatchString(projectID) {
		return fmt.Errorf("invalid GCP project ID format: must be 6-30 lowercase letters, digits, or hyphens, starting with a letter")
	}
	return nil
}

// GCPCredentials holds the GCP Service Account credentials.
type GCPCredentials struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"` // #nosec G117 -- operator-supplied credential input read from the user's own GCP service-account key file; marshaled only to store in AWS Secrets Manager, never a hardcoded secret and never logged (verified)
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id,omitempty"`
	AuthURI                 string `json:"auth_uri,omitempty"`
	TokenURI                string `json:"token_uri,omitempty"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url,omitempty"`
	ClientX509CertURL       string `json:"client_x509_cert_url,omitempty"`
}

// GCPConfigOptions holds configuration for the GCP config command.
type GCPConfigOptions struct {
	StackName       string
	Profile         string
	CredentialsFile string
	ProjectID       string
	Interactive     bool
	SkipSetup       bool
}

var gcpOpts = GCPConfigOptions{}

var configureGCPCmd = &cobra.Command{
	Use:   "configure-gcp",
	Short: "Configure GCP credentials for CUDly",
	Long: `Configure GCP Service Account credentials for multi-cloud commitment management.

This command stores your GCP credentials in AWS Secrets Manager for use by CUDly.

You can provide credentials via a JSON key file:
  cudly configure-gcp --stack-name my-cudly --credentials-file ~/gcp-service-account.json

Or run interactively to create a new service account:
  cudly configure-gcp --stack-name my-cudly --interactive`,
	RunE: runConfigureGCP,
}

func init() {
	rootCmd.AddCommand(configureGCPCmd)

	configureGCPCmd.Flags().StringVar(&gcpOpts.StackName, "stack-name", "cudly", "Name of the CUDly deployment; prefixes the AWS Secrets Manager secret (<stack-name>-GCPCredentials)")
	configureGCPCmd.Flags().StringVar(&gcpOpts.Profile, "profile", "", "AWS profile to use")
	configureGCPCmd.Flags().StringVarP(&gcpOpts.CredentialsFile, "credentials-file", "f", "", "Path to GCP service account JSON key file")
	configureGCPCmd.Flags().StringVar(&gcpOpts.ProjectID, "project-id", "", "GCP Project ID (overrides value in credentials file)")
	configureGCPCmd.Flags().BoolVarP(&gcpOpts.Interactive, "interactive", "i", false, "Prompt for credentials file interactively")
	configureGCPCmd.Flags().BoolVar(&gcpOpts.SkipSetup, "skip-setup", false, "Skip GCP CLI setup commands (gcloud login, create service account)")
}

// storeGCPCredentials stores GCP credentials in the secrets store.
func storeGCPCredentials(ctx context.Context, store SecretsStore, stackName, credsJSON string) error {
	// Validate that we have valid JSON
	var creds GCPCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Validate credentials
	if creds.Type != "service_account" {
		return fmt.Errorf("invalid credentials file: expected type 'service_account', got '%s'", creds.Type)
	}

	if creds.ProjectID == "" {
		return fmt.Errorf("credentials file is missing project_id")
	}

	if creds.ClientEmail == "" {
		return fmt.Errorf("credentials file is missing client_email")
	}

	if creds.PrivateKey == "" {
		return fmt.Errorf("credentials file is missing private_key")
	}

	// Build expected secret name pattern
	secretName := fmt.Sprintf("%s-GCPCredentials", stackName)

	secretID, err := resolveSecretID(ctx, store, secretName)
	if err != nil {
		return err
	}

	// Store credentials in Secrets Manager (using the original JSON format)
	err = store.UpdateSecret(ctx, secretID, credsJSON)
	if err != nil {
		return fmt.Errorf("failed to store credentials in Secrets Manager: %w", err)
	}

	return nil
}

func runConfigureGCP(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Configure GCP Service Account credentials for CUDly")
	fmt.Println("===================================================")
	fmt.Println()

	// Load the AWS config before the wizard can mint a key, so a config error
	// never strands a freshly minted key.
	cfg, err := loadAWSConfigForGCP(ctx)
	if err != nil {
		return err
	}
	store := NewAWSSecretsStore(secretsmanager.NewFromConfig(cfg))

	var finished atomic.Bool
	var release context.CancelFunc
	finish := func() {
		// Store before release(): the cancel wakes the goroutine, which reads it.
		finished.Store(true)
		if release != nil {
			release()
		}
	}
	defer finish()
	beginUpload := func() context.Context {
		if release == nil {
			sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			release = stop
			ctx = sigCtx
			go func() {
				<-sigCtx.Done()
				// Restore default signal handling so a second Ctrl-C aborts
				// the cleanup; stop() also cancels sigCtx, hence the flag.
				stop()
				if !finished.Load() {
					fmt.Fprintln(os.Stderr, "Interrupted: finishing cleanup (up to 60s). Press Ctrl-C again to abort immediately.")
				}
			}()
		}
		return ctx
	}
	credsFile, mintedKeyName, err := getGCPCredentialsFilePath(ctx, reader, beginUpload)
	if err != nil {
		return err
	}

	creds, err := uploadGCPCredentialsFile(beginUpload(), store, gcpOpts.StackName, credsFile, mintedKeyName, deleteGCPServiceAccountKey)
	if err != nil {
		return err
	}

	finish()
	if mintedKeyName != "" {
		fmt.Println("Removed the local copy of the minted key.")
	}
	printGCPConfigurationSuccess(creds)
	return nil
}

// getGCPCredentialsFilePath determines the credentials file path from options
// or user input. mintedKeyName is the IAM key resource name when the setup
// wizard created the file, and empty otherwise.
func getGCPCredentialsFilePath(ctx context.Context, reader *bufio.Reader, beginUpload func() context.Context) (credsFile, mintedKeyName string, err error) {
	if gcpOpts.CredentialsFile != "" {
		credsFile = gcpOpts.CredentialsFile
	} else if !gcpOpts.SkipSetup {
		credsFile, mintedKeyName, err = runGCPSetupCommands(ctx, reader, beginUpload)
		if err != nil {
			return "", "", err
		}
		if credsFile != "" {
			return credsFile, mintedKeyName, nil
		}
	}

	if credsFile == "" {
		fmt.Print("Path to GCP service account JSON key file: ")
		credsFile, err = readTrimmedLine(reader)
		if err != nil {
			return "", "", fmt.Errorf("failed to read credentials file path: %w", err)
		}
	}

	if credsFile == "" {
		return "", "", fmt.Errorf("credentials file is required")
	}

	return credsFile, "", nil
}

// loadAWSConfigForGCP loads AWS configuration with optional profile.
func loadAWSConfigForGCP(ctx context.Context) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if gcpOpts.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(gcpOpts.Profile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cfg, nil
}

// loadAndUpdateGCPCredentials loads, parses, and optionally updates GCP credentials.
func loadAndUpdateGCPCredentials(credsFile string) (GCPCredentials, []byte, error) {
	expandedPath := filepath.Clean(expandHomeDirectory(credsFile))

	// #nosec G304 G703 -- expandedPath is the operator's own GCP service-account key file, supplied via the --credentials-file flag or the interactive prompt of this local `configure-gcp` command; filepath.Clean applied above and it is trusted operator input, not attacker-controlled
	credsData, err := os.ReadFile(expandedPath)
	if err != nil {
		return GCPCredentials{}, nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds GCPCredentials
	if err = json.Unmarshal(credsData, &creds); err != nil {
		return GCPCredentials{}, nil, fmt.Errorf("failed to parse credentials file: %w", err)
	}

	if gcpOpts.ProjectID != "" {
		creds.ProjectID = gcpOpts.ProjectID
		credsData, err = json.Marshal(creds) // #nosec G117 -- GCPCredentials marshaled intentionally for Secrets Manager storage
		if err != nil {
			return GCPCredentials{}, nil, fmt.Errorf("failed to marshal updated credentials: %w", err)
		}
	}

	return creds, credsData, nil
}

// expandHomeDirectory expands ~ to the user's home directory.
func expandHomeDirectory(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	return strings.Replace(path, "~", home, 1)
}

// printGCPConfigurationSuccess prints success message with credentials info.
func printGCPConfigurationSuccess(creds GCPCredentials) {
	log.Printf("GCP credentials stored successfully in Secrets Manager")
	fmt.Println("\nGCP configuration complete!")
	fmt.Printf("Service Account: %s\n", creds.ClientEmail)
	fmt.Printf("Project ID: %s\n", creds.ProjectID)
	fmt.Println("\nCUDly can now manage GCP Committed Use Discounts.")
}
