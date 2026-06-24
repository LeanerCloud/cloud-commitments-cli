package main

import (
	"bufio"
	"context"
	"encoding/base64"
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
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	iamv1 "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
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

	credsFile, err := getGCPCredentialsFilePath(ctx, reader)
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
func getGCPCredentialsFilePath(ctx context.Context, reader *bufio.Reader) (string, error) {
	var credsFile string

	if gcpOpts.CredentialsFile != "" {
		credsFile = gcpOpts.CredentialsFile
	} else if !gcpOpts.SkipSetup {
		var err error
		credsFile, err = runGCPSetupCommands(ctx, reader)
		if err != nil {
			return "", err
		}
	}

	if credsFile == "" {
		fmt.Print("Path to GCP service account JSON key file: ")
		var readErr error
		credsFile, readErr = readTrimmedLine(reader)
		if readErr != nil {
			return "", fmt.Errorf("failed to read credentials file path: %w", readErr)
		}
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

// newGCPAPIOption returns an oauth2 token source option for the google.golang.org
// API client, using Application Default Credentials. ADC resolves credentials in
// priority order: GOOGLE_APPLICATION_CREDENTIALS env var, gcloud ADC cache
// (populated by "gcloud auth application-default login"), Workload Identity,
// Metadata Server.
//
// NOTE: "gcloud auth login" (wizard Step 1) updates the gcloud user session but
// does NOT automatically populate the ADC cache. If the operator intends to use
// these SDK calls after Step 1, they should also run:
//
//	gcloud auth application-default login
//
// The wizard Step 3 onwards calls this function; if ADC is not available the
// calls fail loud with a hint to run "gcloud auth application-default login".
func newGCPAPIOption(ctx context.Context) (option.ClientOption, error) {
	ts, err := google.DefaultTokenSource(ctx,
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/iam",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain GCP Application Default Credentials: %w\n"+
			"Hint: run 'gcloud auth application-default login' first", err)
	}
	return option.WithTokenSource(ts), nil
}

// listGCPProjects lists GCP projects accessible to the operator via the Cloud
// Resource Manager API v1 and prints them in a table. This replaces the
// "gcloud projects list" CLI call.
func listGCPProjects(ctx context.Context) error {
	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return err
	}

	svc, err := cloudresourcemanager.NewService(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to create resource manager client: %w", err)
	}

	fmt.Printf("%-30s  %-25s  %s\n", "NAME", "PROJECT_ID", "PROJECT_NUMBER")
	fmt.Println(strings.Repeat("-", 80))

	req := svc.Projects.List()
	if err := req.Pages(ctx, func(page *cloudresourcemanager.ListProjectsResponse) error {
		for _, p := range page.Projects {
			fmt.Printf("%-30s  %-25s  %d\n", p.Name, p.ProjectId, p.ProjectNumber)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to list GCP projects: %w", err)
	}
	return nil
}

// createGCPServiceAccount creates a GCP IAM service account via the IAM API v1.
// This replaces "gcloud iam service-accounts create".
func createGCPServiceAccount(ctx context.Context, projectID, saName string) (string, error) {
	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return "", err
	}

	svc, err := iamv1.NewService(ctx, opt)
	if err != nil {
		return "", fmt.Errorf("failed to create IAM client: %w", err)
	}

	req := &iamv1.CreateServiceAccountRequest{
		AccountId: saName,
		ServiceAccount: &iamv1.ServiceAccount{
			DisplayName: "CUDly Service Account",
			Description: "Service account for CUDly commitment management",
		},
	}

	sa, err := svc.Projects.ServiceAccounts.Create("projects/"+projectID, req).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create service account: %w", err)
	}

	return sa.Email, nil
}

// grantGCPIAMRole grants an IAM role to a service account on a project via the
// Cloud Resource Manager API v1. This replaces
// "gcloud projects add-iam-policy-binding".
func grantGCPIAMRole(ctx context.Context, projectID, member, role string) error {
	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return err
	}

	svc, err := cloudresourcemanager.NewService(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to create resource manager client: %w", err)
	}

	// Get current policy.
	policy, err := svc.Projects.GetIamPolicy(projectID, &cloudresourcemanager.GetIamPolicyRequest{}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for project %s: %w", projectID, err)
	}

	// Append the binding (or extend existing one).
	found := false
	for _, b := range policy.Bindings {
		if b.Role == role {
			for _, m := range b.Members {
				if m == member {
					// Already bound; nothing to do.
					return nil
				}
			}
			b.Members = append(b.Members, member)
			found = true
			break
		}
	}
	if !found {
		policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
			Role:    role,
			Members: []string{member},
		})
	}
	_, err = svc.Projects.SetIamPolicy(projectID, &cloudresourcemanager.SetIamPolicyRequest{
		Policy: policy,
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set IAM policy on project %s: %w", projectID, err)
	}
	return nil
}

// createGCPServiceAccountKey creates a JSON key for the given service account
// and writes it to keyFile. Returns the path written. This replaces
// "gcloud iam service-accounts keys create <file> --iam-account=<sa>".
func createGCPServiceAccountKey(ctx context.Context, saEmail, keyFile string) error {
	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return err
	}

	svc, err := iamv1.NewService(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to create IAM client: %w", err)
	}

	resource := fmt.Sprintf("projects/-/serviceAccounts/%s", saEmail)
	key, err := svc.Projects.ServiceAccounts.Keys.Create(resource, &iamv1.CreateServiceAccountKeyRequest{
		PrivateKeyType: "TYPE_GOOGLE_CREDENTIALS_FILE",
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to create service account key: %w", err)
	}

	// PrivateKeyData is base64-encoded JSON.
	decoded, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	if err != nil {
		return fmt.Errorf("failed to decode key data: %w", err)
	}

	if err := os.WriteFile(keyFile, decoded, 0600); err != nil {
		return fmt.Errorf("failed to write key file %s: %w", keyFile, err)
	}
	return nil
}

// runGCPSetupCommands guides the operator through GCP setup.
//
// Step 1 (gcloud auth login): performed via the GCP CLI. This is an
// interactive browser-based OAuth flow that cannot be replicated through SDK
// calls on behalf of an operator who does not yet have a credential. It
// updates the gcloud user session but does NOT populate the ADC cache needed
// by subsequent SDK calls. Operators must also run
// "gcloud auth application-default login" if they want to use ADC.
//
// Step 2 (list projects): performed via the Cloud Resource Manager SDK v1,
// using Application Default Credentials (ADC). Fails loud if ADC is not
// available (no CLI fallback).
//
// Step 3 (gcloud config set project): sets local gcloud config state. There is
// no cloud-API equivalent for writing to the local gcloud configuration file,
// so this step remains a CLI call.
//
// Steps 4-6 (create SA, grant role, create key): performed via GCP IAM and
// Cloud Resource Manager SDK v1 APIs using ADC. Fail loud on any SDK error
// (no CLI fallback).
func runGCPSetupCommands(ctx context.Context, reader *bufio.Reader) (string, error) {
	if err := gcpStepLogin(reader); err != nil {
		return "", err
	}

	projectID, err := gcpStepSelectProject(ctx, reader)
	if err != nil {
		return "", err
	}

	saEmail, err := gcpStepCreateServiceAccount(ctx, reader, projectID)
	if err != nil {
		return "", err
	}

	if err := gcpStepGrantRole(ctx, reader, projectID, saEmail); err != nil {
		return "", err
	}

	return gcpStepCreateKey(ctx, reader, saEmail)
}

// gcpStepLogin runs "gcloud auth login" interactively.
func gcpStepLogin(reader *bufio.Reader) error {
	fmt.Println("Step 1: GCP Login")
	fmt.Println("-----------------")
	fmt.Println("This will open a browser window for GCP authentication.")
	fmt.Println("After logging in, also run: gcloud auth application-default login")
	fmt.Println("(required for the SDK-based steps below)")
	fmt.Println()
	return promptAndRunGCPCommand(reader, "GCP Login", "gcloud auth login", "gcloud", "auth", "login")
}

// gcpStepSelectProject lists projects and prompts for a project ID.
func gcpStepSelectProject(ctx context.Context, reader *bufio.Reader) (string, error) {
	fmt.Println()
	fmt.Println("Step 2: Select Project")
	fmt.Println("----------------------")
	fmt.Println("Listing your GCP projects via SDK (Application Default Credentials)...")
	fmt.Println()

	if err := listGCPProjects(ctx); err != nil {
		return "", fmt.Errorf("failed to list GCP projects via SDK: %w\n"+
			"Ensure Application Default Credentials are set: run 'gcloud auth application-default login' first", err)
	}

	fmt.Println()
	projectID, err := readRequiredInputLine(reader, "Enter your Project ID from above: ", "project ID")
	if err != nil {
		return "", err
	}
	if err := validateGCPProjectID(projectID); err != nil {
		return "", err
	}

	// Set the project in the local gcloud config. This is a local operation
	// (writes to ~/.config/gcloud/properties) with no cloud-API equivalent.
	fmt.Println()
	fmt.Println("Setting gcloud project context (local config)...")
	//nolint:gosec // G204: local gcloud config write, hardcoded args + validated projectID, no shell
	cmd := exec.Command("gcloud", "config", "set", "project", projectID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to set project: %w", err)
	}
	return projectID, nil
}

// gcpStepCreateServiceAccount creates the CUDly service account via the IAM
// SDK. It fails loud on any SDK error (no CLI fallback).
func gcpStepCreateServiceAccount(ctx context.Context, reader *bufio.Reader, projectID string) (string, error) {
	saName := "cudly-service-account"
	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, projectID)

	fmt.Println()
	fmt.Println("Step 3: Create Service Account")
	fmt.Println("------------------------------")
	fmt.Println("This creates a GCP Service Account for CUDly via the IAM API.")
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? (creates service account '%s' via SDK) ", saName)

	choice, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run", "":
		email, err := createGCPServiceAccount(ctx, projectID, saName)
		if err != nil {
			return "", err
		}
		saEmail = email
		fmt.Printf("Service account created: %s\n", saEmail)
	case "s", "skip":
		fmt.Println("Skipping Create Service Account")
	default:
		fmt.Printf("Unknown option, skipping\n")
	}
	return saEmail, nil
}

// gcpStepGrantRole grants the compute.admin role to the service account via
// the Cloud Resource Manager SDK. It fails loud on any SDK error (no CLI
// fallback).
func gcpStepGrantRole(ctx context.Context, reader *bufio.Reader, projectID, saEmail string) error {
	member := fmt.Sprintf("serviceAccount:%s", saEmail)
	role := "roles/compute.admin"

	fmt.Println()
	fmt.Println("Step 4: Grant IAM Roles")
	fmt.Println("-----------------------")
	fmt.Println("Grant the required roles to the service account.")
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? (grants %s to %s on project %s via SDK) ", role, saEmail, projectID)

	choice, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run", "":
		if err := grantGCPIAMRole(ctx, projectID, member, role); err != nil {
			return err
		}
		fmt.Printf("Role %s granted to %s on project %s.\n", role, saEmail, projectID)
	case "s", "skip":
		fmt.Println("Skipping Grant IAM Roles")
	default:
		fmt.Printf("Unknown option, skipping\n")
	}
	return nil
}

// gcpStepCreateKey creates a JSON key file for the service account.
func gcpStepCreateKey(ctx context.Context, reader *bufio.Reader, saEmail string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	keyFile := filepath.Join(home, "cudly-gcp-key.json")

	fmt.Println()
	fmt.Println("Step 5: Create and Download Key")
	fmt.Println("-------------------------------")
	fmt.Println("Create a JSON key file for the service account.")
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? (creates key for %s, writes to %s via SDK) ", saEmail, keyFile)

	choice, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run", "":
		if err := createGCPServiceAccountKey(ctx, saEmail, keyFile); err != nil {
			return "", err
		}
		fmt.Printf("Key file written to: %s\n", keyFile)
	case "s", "skip":
		fmt.Println("Skipping Create Key")
	default:
		fmt.Printf("Unknown option, skipping\n")
	}

	fmt.Println()
	fmt.Printf("Key file at: %s\n", keyFile)
	fmt.Println()
	return keyFile, nil
}

// readRequiredInputLine prints prompt, reads a line, trims whitespace, and
// returns an error if the result is empty.
func readRequiredInputLine(reader *bufio.Reader, prompt, fieldName string) (string, error) {
	fmt.Print(prompt)
	value, err := readTrimmedLine(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", fieldName, err)
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", fieldName)
	}
	return value, nil
}

// promptAndRunGCPCommand shows a command and asks to run or skip.
// It is used only for the interactive "gcloud auth login" auth bootstrap
// (Step 1), which has no SDK equivalent that preserves the cached-credential UX.
func promptAndRunGCPCommand(reader *bufio.Reader, name, displayCmd string, program string, args ...string) error {
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
		return executeGCPCommand(reader, displayCmd, program, args...)
	case "s", "skip":
		fmt.Printf("Skipping %s\n", name)
		return nil
	default:
		fmt.Printf("Unknown option '%s', skipping\n", choice)
		return nil
	}
}

// executeGCPCommand runs a gcloud command with explicit program and arguments.
// It is used only for the interactive "gcloud auth login" auth bootstrap.
// The caller's reader is threaded through to the retry prompt so all input
// is consumed from one consistent buffered stream (a fresh
// bufio.NewReader(os.Stdin) here would drop input already buffered by the
// caller's reader, breaking piped input after earlier prompts).
func executeGCPCommand(reader *bufio.Reader, displayCmd string, program string, args ...string) error {
	fmt.Println()
	fmt.Printf("Executing: %s\n", displayCmd)
	fmt.Println(strings.Repeat("-", 60))

	//nolint:gosec // G204: interactive operator auth (gcloud auth login), hardcoded args, no shell
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
