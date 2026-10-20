package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// MockSecretsStore is a mock implementation of SecretsStore for testing.
type MockSecretsStore struct {
	listSecretsFunc   func(ctx context.Context, filter string) ([]string, error)
	updateSecretFunc  func(ctx context.Context, secretID string, secretValue string) error
	updatedSecrets    map[string]string // Track updated secrets
	listSecretsFilter string            // Track last filter used
}

func NewMockSecretsStore() *MockSecretsStore {
	return &MockSecretsStore{
		updatedSecrets: make(map[string]string),
	}
}

func (m *MockSecretsStore) ListSecrets(ctx context.Context, filter string) ([]string, error) {
	m.listSecretsFilter = filter
	if m.listSecretsFunc != nil {
		return m.listSecretsFunc(ctx, filter)
	}
	return []string{}, nil
}

func (m *MockSecretsStore) UpdateSecret(ctx context.Context, secretID string, secretValue string) error {
	m.updatedSecrets[secretID] = secretValue
	if m.updateSecretFunc != nil {
		return m.updateSecretFunc(ctx, secretID, secretValue)
	}
	return nil
}

// TestAzureCredentials_Struct tests the AzureCredentials struct.
func TestAzureCredentials_Struct(t *testing.T) {
	creds := AzureCredentials{
		TenantID:       "tenant-123",
		ClientID:       "client-456",
		ClientSecret:   "secret-789",
		SubscriptionID: "sub-abc",
	}

	assert.Equal(t, "tenant-123", creds.TenantID)
	assert.Equal(t, "client-456", creds.ClientID)
	assert.Equal(t, "secret-789", creds.ClientSecret)
	assert.Equal(t, "sub-abc", creds.SubscriptionID)
}

// TestAzureConfigOptions_Defaults tests AzureConfigOptions defaults.
func TestAzureConfigOptions_Defaults(t *testing.T) {
	opts := AzureConfigOptions{}

	assert.Equal(t, "", opts.StackName)
	assert.Equal(t, "", opts.Profile)
	assert.Equal(t, "", opts.TenantID)
	assert.Equal(t, "", opts.ClientID)
	assert.Equal(t, "", opts.ClientSecret)
	assert.Equal(t, "", opts.SubscriptionID)
	assert.False(t, opts.Interactive)
}

// TestAzureConfigOptions_WithValues tests AzureConfigOptions with values.
func TestAzureConfigOptions_WithValues(t *testing.T) {
	opts := AzureConfigOptions{
		StackName:      "my-cudly",
		Profile:        "production",
		TenantID:       "tenant-id",
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		SubscriptionID: "subscription-id",
		Interactive:    true,
	}

	assert.Equal(t, "my-cudly", opts.StackName)
	assert.Equal(t, "production", opts.Profile)
	assert.Equal(t, "tenant-id", opts.TenantID)
	assert.Equal(t, "client-id", opts.ClientID)
	assert.Equal(t, "client-secret", opts.ClientSecret)
	assert.Equal(t, "subscription-id", opts.SubscriptionID)
	assert.True(t, opts.Interactive)
}

// TestGCPCredentials_Struct tests the GCPCredentials struct.
func TestGCPCredentials_Struct(t *testing.T) {
	creds := GCPCredentials{
		Type:         "service_account",
		ProjectID:    "my-project",
		PrivateKeyID: "key-123",
		PrivateKey:   "-----BEGIN PRIVATE KEY-----\n...",
		ClientEmail:  "sa@project.iam.gserviceaccount.com",
		ClientID:     "12345678901234567890",
	}

	assert.Equal(t, "service_account", creds.Type)
	assert.Equal(t, "my-project", creds.ProjectID)
	assert.Equal(t, "key-123", creds.PrivateKeyID)
	assert.Equal(t, "-----BEGIN PRIVATE KEY-----\n...", creds.PrivateKey)
	assert.Equal(t, "sa@project.iam.gserviceaccount.com", creds.ClientEmail)
	assert.Equal(t, "12345678901234567890", creds.ClientID)
}

// TestGCPConfigOptions_Defaults tests GCPConfigOptions defaults.
func TestGCPConfigOptions_Defaults(t *testing.T) {
	opts := GCPConfigOptions{}

	assert.Equal(t, "", opts.StackName)
	assert.Equal(t, "", opts.Profile)
	assert.Equal(t, "", opts.ProjectID)
	assert.Equal(t, "", opts.CredentialsFile)
	assert.False(t, opts.Interactive)
}

// TestGCPConfigOptions_WithValues tests GCPConfigOptions with values.
func TestGCPConfigOptions_WithValues(t *testing.T) {
	opts := GCPConfigOptions{
		StackName:       "my-cudly",
		Profile:         "production",
		ProjectID:       "my-gcp-project",
		CredentialsFile: "/path/to/credentials.json",
		Interactive:     true,
	}

	assert.Equal(t, "my-cudly", opts.StackName)
	assert.Equal(t, "production", opts.Profile)
	assert.Equal(t, "my-gcp-project", opts.ProjectID)
	assert.Equal(t, "/path/to/credentials.json", opts.CredentialsFile)
	assert.True(t, opts.Interactive)
}

// Tests for validateAzureUUID function.
func TestValidateAzureUUID(t *testing.T) {
	tests := []struct {
		name      string
		uuid      string
		fieldName string
		wantErr   bool
	}{
		{
			name:      "Valid UUID - all lowercase",
			uuid:      "12345678-1234-1234-1234-123456789abc",
			fieldName: "Tenant ID",
			wantErr:   false,
		},
		{
			name:      "Valid UUID - all uppercase",
			uuid:      "12345678-1234-1234-1234-123456789ABC",
			fieldName: "Client ID",
			wantErr:   false,
		},
		{
			name:      "Valid UUID - mixed case",
			uuid:      "12345678-1234-1234-1234-123456789AbC",
			fieldName: "Subscription ID",
			wantErr:   false,
		},
		{
			name:      "Valid UUID - all zeros",
			uuid:      "00000000-0000-0000-0000-000000000000",
			fieldName: "Tenant ID",
			wantErr:   false,
		},
		{
			name:      "Valid UUID - all f's",
			uuid:      "ffffffff-ffff-ffff-ffff-ffffffffffff",
			fieldName: "Client ID",
			wantErr:   false,
		},
		{
			name:      "Invalid UUID - missing dashes",
			uuid:      "12345678123412341234123456789abc",
			fieldName: "Tenant ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - wrong dash positions",
			uuid:      "123456781-234-1234-1234-123456789abc",
			fieldName: "Client ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - too short",
			uuid:      "12345678-1234-1234-1234-123456789ab",
			fieldName: "Subscription ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - too long",
			uuid:      "12345678-1234-1234-1234-123456789abcd",
			fieldName: "Tenant ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - contains invalid character g",
			uuid:      "12345678-1234-1234-1234-123456789abg",
			fieldName: "Client ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - contains special characters",
			uuid:      "12345678-1234-1234-1234-123456789ab!",
			fieldName: "Subscription ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - empty string",
			uuid:      "",
			fieldName: "Tenant ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - command injection attempt",
			uuid:      "12345678-1234-1234-1234-123456789abc; rm -rf /",
			fieldName: "Subscription ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - SQL injection attempt",
			uuid:      "12345678-1234-1234-1234-123456789abc' OR '1'='1",
			fieldName: "Client ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - spaces",
			uuid:      "12345678-1234-1234-1234-123456789abc ",
			fieldName: "Tenant ID",
			wantErr:   true,
		},
		{
			name:      "Invalid UUID - newline",
			uuid:      "12345678-1234-1234-1234-123456789abc\n",
			fieldName: "Client ID",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAzureUUID(tt.uuid, tt.fieldName)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.fieldName)
				assert.Contains(t, err.Error(), "invalid")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Tests for validateGCPProjectID function.
func TestValidateGCPProjectID(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
		wantErr   bool
	}{
		{
			name:      "Valid project ID - minimum length (6 chars)",
			projectID: "my-pro",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - maximum length (30 chars)",
			projectID: "my-very-long-project-id-123456",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - all lowercase letters",
			projectID: "myproject",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - with numbers",
			projectID: "project123",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - with hyphens",
			projectID: "my-project-123",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - starts with letter",
			projectID: "a12345",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - ends with number",
			projectID: "myproject1",
			wantErr:   false,
		},
		{
			name:      "Valid project ID - ends with letter",
			projectID: "project-a",
			wantErr:   false,
		},
		{
			name:      "Invalid project ID - too short (5 chars)",
			projectID: "short",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - too long (31 chars)",
			projectID: "my-very-very-long-project-id-31",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - starts with number",
			projectID: "123project",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - starts with hyphen",
			projectID: "-myproject",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - ends with hyphen",
			projectID: "myproject-",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - contains uppercase",
			projectID: "MyProject",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - contains underscore",
			projectID: "my_project",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - contains space",
			projectID: "my project",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - contains special characters",
			projectID: "my-project!",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - empty string",
			projectID: "",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - command injection attempt",
			projectID: "myproject; rm -rf /",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - path traversal attempt",
			projectID: "../../../etc/passwd",
			wantErr:   true,
		},
		{
			name:      "Invalid project ID - contains dot",
			projectID: "my.project",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGCPProjectID(tt.projectID)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid GCP project ID format")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
