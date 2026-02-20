package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/spf13/cobra"
)

// gcpProjectIDRegex validates GCP project IDs (lowercase letters, digits, hyphens, 6-30 chars)
var gcpProjectIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// validateGCPProjectID validates a GCP project ID to prevent command injection
func validateGCPProjectID(projectID string) error {
	if !gcpProjectIDRegex.MatchString(projectID) {
		return fmt.Errorf("invalid GCP project ID format: must be 6-30 lowercase letters, digits, or hyphens, starting with a letter")
	}
	return nil
}

// GCPCredentials holds the GCP Service Account credentials
type GCPCredentials struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"`
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id,omitempty"`
	AuthURI                 string `json:"auth_uri,omitempty"`
	TokenURI                string `json:"token_uri,omitempty"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url,omitempty"`
	ClientX509CertURL       string `json:"client_x509_cert_url,omitempty"`
}

// GCPConfigOptions holds configuration for the GCP config command
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

	configureGCPCmd.Flags().StringVar(&gcpOpts.StackName, "stack-name", "cudly", "CUDly CloudFormation stack name")
	configureGCPCmd.Flags().StringVar(&gcpOpts.Profile, "profile", "", "AWS profile to use")
	configureGCPCmd.Flags().StringVarP(&gcpOpts.CredentialsFile, "credentials-file", "f", "", "Path to GCP service account JSON key file")
	configureGCPCmd.Flags().StringVar(&gcpOpts.ProjectID, "project-id", "", "GCP Project ID (overrides value in credentials file)")
	configureGCPCmd.Flags().BoolVarP(&gcpOpts.Interactive, "interactive", "i", false, "Prompt for credentials file interactively")
	configureGCPCmd.Flags().BoolVar(&gcpOpts.SkipSetup, "skip-setup", false, "Skip GCP CLI setup commands (gcloud login, create service account)")
}

// storeGCPCredentials stores GCP credentials in the secrets store
func storeGCPCredentials(ctx context.Context, store SecretsStore, stackName string, credsJSON string) error {
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

	// Try to find the actual secret ARN by listing secrets
	arns, err := store.ListSecrets(ctx, secretName)

	// Use the ARN if found, otherwise use the name (will fail if secret doesn't exist)
	secretID := secretName
	if err == nil && len(arns) > 0 {
		secretID = arns[0]
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

	credsFile, err := getGCPCredentialsFilePath(reader)
	if err != nil {
		return err
	}

	cfg, err := loadAWSConfigForGCP(ctx)
	if err != nil {
		return err
	}

	creds, credsData, err := loadAndUpdateGCPCredentials(credsFile)
	if err != nil {
		return err
	}

	smClient := secretsmanager.NewFromConfig(cfg)
	store := NewAWSSecretsStore(smClient)

	if err := storeGCPCredentials(ctx, store, gcpOpts.StackName, string(credsData)); err != nil {
		return err
	}

	printGCPConfigurationSuccess(creds)
	return nil
}

// getGCPCredentialsFilePath determines the credentials file path from options or user input
func getGCPCredentialsFilePath(reader *bufio.Reader) (string, error) {
	var credsFile string

	if gcpOpts.CredentialsFile != "" {
		credsFile = gcpOpts.CredentialsFile
	} else if !gcpOpts.SkipSetup {
		var err error
		credsFile, err = runGCPSetupCommands(reader)
		if err != nil {
			return "", err
		}
	}

	if credsFile == "" {
		fmt.Print("Path to GCP service account JSON key file: ")
		credsFile, _ = reader.ReadString('\n')
		credsFile = strings.TrimSpace(credsFile)
	}

	if credsFile == "" {
		return "", fmt.Errorf("credentials file is required")
	}

	return credsFile, nil
}

// loadAWSConfigForGCP loads AWS configuration with optional profile
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

// loadAndUpdateGCPCredentials loads, parses, and optionally updates GCP credentials
func loadAndUpdateGCPCredentials(credsFile string) (GCPCredentials, []byte, error) {
	expandedPath := expandHomeDirectory(credsFile)

	credsData, err := os.ReadFile(expandedPath)
	if err != nil {
		return GCPCredentials{}, nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds GCPCredentials
	if err := json.Unmarshal(credsData, &creds); err != nil {
		return GCPCredentials{}, nil, fmt.Errorf("failed to parse credentials file: %w", err)
	}

	if gcpOpts.ProjectID != "" {
		creds.ProjectID = gcpOpts.ProjectID
		credsData, err = json.Marshal(creds)
		if err != nil {
			return GCPCredentials{}, nil, fmt.Errorf("failed to marshal updated credentials: %w", err)
		}
	}

	return creds, credsData, nil
}

// expandHomeDirectory expands ~ to the user's home directory
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

// printGCPConfigurationSuccess prints success message with credentials info
func printGCPConfigurationSuccess(creds GCPCredentials) {
	log.Printf("GCP credentials stored successfully in Secrets Manager")
	fmt.Println("\nGCP configuration complete!")
	fmt.Printf("Service Account: %s\n", creds.ClientEmail)
	fmt.Printf("Project ID: %s\n", creds.ProjectID)
	fmt.Println("\nCUDly can now manage GCP Committed Use Discounts.")
}

// runGCPSetupCommands runs the GCP CLI commands interactively
func runGCPSetupCommands(reader *bufio.Reader) (string, error) {
	fmt.Println("Step 1: GCP Login")
	fmt.Println("-----------------")
	fmt.Println("This will open a browser window for GCP authentication.")
	fmt.Println()

	if err := promptAndRunGCPCommand(reader, "GCP Login", "gcloud auth login", "gcloud", "auth", "login"); err != nil {
		return "", err
	}

	fmt.Println()
	fmt.Println("Step 2: Select Project")
	fmt.Println("----------------------")
	fmt.Println("List your GCP projects:")
	fmt.Println()

	if err := promptAndRunGCPCommand(reader, "List Projects", "gcloud projects list", "gcloud", "projects", "list"); err != nil {
		return "", err
	}

	fmt.Println()
	fmt.Print("Enter your Project ID from above: ")
	projectID, _ := reader.ReadString('\n')
	projectID = strings.TrimSpace(projectID)

	if projectID == "" {
		return "", fmt.Errorf("project ID is required")
	}

	// Validate project ID to prevent command injection
	if err := validateGCPProjectID(projectID); err != nil {
		return "", err
	}

	// Set the project - use exec.Command with arguments instead of shell
	fmt.Println()
	fmt.Println("Setting project...")
	cmd := exec.Command("gcloud", "config", "set", "project", projectID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to set project: %w", err)
	}

	fmt.Println()
	fmt.Println("Step 3: Create Service Account")
	fmt.Println("------------------------------")
	fmt.Println("This creates a GCP Service Account for CUDly.")
	fmt.Println()

	saName := "cudly-service-account"
	createSaDisplay := fmt.Sprintf(`gcloud iam service-accounts create %s --display-name="CUDly Service Account" --description="Service account for CUDly commitment management"`, saName)

	if err := promptAndRunGCPCommand(reader, "Create Service Account", createSaDisplay,
		"gcloud", "iam", "service-accounts", "create", saName,
		"--display-name=CUDly Service Account",
		"--description=Service account for CUDly commitment management"); err != nil {
		return "", err
	}

	fmt.Println()
	fmt.Println("Step 4: Grant IAM Roles")
	fmt.Println("-----------------------")
	fmt.Println("Grant the required roles to the service account.")
	fmt.Println()

	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, projectID)

	// Grant Compute Admin role for commitment management
	grantRoleDisplay := fmt.Sprintf(`gcloud projects add-iam-policy-binding %s --member="serviceAccount:%s" --role="roles/compute.admin"`, projectID, saEmail)

	if err := promptAndRunGCPCommand(reader, "Grant Compute Admin Role", grantRoleDisplay,
		"gcloud", "projects", "add-iam-policy-binding", projectID,
		fmt.Sprintf("--member=serviceAccount:%s", saEmail),
		"--role=roles/compute.admin"); err != nil {
		return "", err
	}

	fmt.Println()
	fmt.Println("Step 5: Create and Download Key")
	fmt.Println("-------------------------------")
	fmt.Println("Create a JSON key file for the service account.")
	fmt.Println()

	// Get home directory for default key path
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	keyFile := filepath.Join(home, "cudly-gcp-key.json")

	createKeyDisplay := fmt.Sprintf(`gcloud iam service-accounts keys create %s --iam-account=%s`, keyFile, saEmail)

	if err := promptAndRunGCPCommand(reader, "Create Key File", createKeyDisplay,
		"gcloud", "iam", "service-accounts", "keys", "create", keyFile,
		fmt.Sprintf("--iam-account=%s", saEmail)); err != nil {
		return "", err
	}

	fmt.Println()
	fmt.Printf("Key file created at: %s\n", keyFile)
	fmt.Println()

	return keyFile, nil
}

// promptAndRunGCPCommand shows a command and asks to run or skip.
// Takes explicit program and args to avoid command injection via string splitting.
func promptAndRunGCPCommand(reader *bufio.Reader, name, displayCmd string, program string, args ...string) error {
	fmt.Printf("Command: %s\n", displayCmd)
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? ")

	choice, _ := reader.ReadString('\n')
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "r", "run", "":
		return executeGCPCommand(displayCmd, program, args...)
	case "s", "skip":
		fmt.Printf("Skipping %s\n", name)
		return nil
	default:
		fmt.Printf("Unknown option '%s', skipping\n", choice)
		return nil
	}
}

// executeGCPCommand runs a gcloud command with explicit program and arguments
func executeGCPCommand(displayCmd string, program string, args ...string) error {
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
