package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockSecretsStore is a mock implementation of SecretsStore for testing
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

// TestAzureCredentials_Struct tests the AzureCredentials struct
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

// TestAzureConfigOptions_Defaults tests AzureConfigOptions defaults
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

// TestAzureConfigOptions_WithValues tests AzureConfigOptions with values
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

// TestGCPCredentials_Struct tests the GCPCredentials struct
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
	assert.Contains(t, creds.PrivateKey, "PRIVATE KEY")
	assert.Contains(t, creds.ClientEmail, "iam.gserviceaccount.com")
	assert.Equal(t, "12345678901234567890", creds.ClientID)
}

// TestGCPConfigOptions_Defaults tests GCPConfigOptions defaults
func TestGCPConfigOptions_Defaults(t *testing.T) {
	opts := GCPConfigOptions{}

	assert.Equal(t, "", opts.StackName)
	assert.Equal(t, "", opts.Profile)
	assert.Equal(t, "", opts.ProjectID)
	assert.Equal(t, "", opts.CredentialsFile)
	assert.False(t, opts.Interactive)
}

// TestGCPConfigOptions_WithValues tests GCPConfigOptions with values
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

// Tests for validateAzureUUID function
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

// Tests for validateGCPProjectID function
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

// Tests for storeAzureCredentials function
func TestStoreAzureCredentials(t *testing.T) {
	tests := []struct {
		name          string
		stackName     string
		creds         AzureCredentials
		mockSetup     func(*MockSecretsStore)
		wantErr       bool
		wantErrMsg    string
		validateStore func(*testing.T, *MockSecretsStore)
	}{
		{
			name:      "Successfully store valid credentials",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "my-secret",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			mockSetup: func(m *MockSecretsStore) {
				m.listSecretsFunc = func(ctx context.Context, filter string) ([]string, error) {
					return []string{"arn:aws:secretsmanager:us-east-1:123456789012:secret:my-stack-AzureCredentials-abc123"}, nil
				}
			},
			wantErr: false,
			validateStore: func(t *testing.T, m *MockSecretsStore) {
				assert.Equal(t, "my-stack-AzureCredentials", m.listSecretsFilter)
				assert.Len(t, m.updatedSecrets, 1)

				secretID := "arn:aws:secretsmanager:us-east-1:123456789012:secret:my-stack-AzureCredentials-abc123"
				secretValue, ok := m.updatedSecrets[secretID]
				assert.True(t, ok, "Secret should be stored")

				var storedCreds AzureCredentials
				err := json.Unmarshal([]byte(secretValue), &storedCreds)
				require.NoError(t, err)
				assert.Equal(t, "12345678-1234-1234-1234-123456789abc", storedCreds.TenantID)
				assert.Equal(t, "87654321-4321-4321-4321-fedcba987654", storedCreds.ClientID)
				assert.Equal(t, "my-secret", storedCreds.ClientSecret)
				assert.Equal(t, "abcdef12-3456-7890-abcd-ef1234567890", storedCreds.SubscriptionID)
			},
		},
		{
			name:      "Store credentials when secret ARN not found",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "my-secret",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			mockSetup: func(m *MockSecretsStore) {
				m.listSecretsFunc = func(ctx context.Context, filter string) ([]string, error) {
					return []string{}, nil
				}
			},
			wantErr: false,
			validateStore: func(t *testing.T, m *MockSecretsStore) {
				assert.Equal(t, "my-stack-AzureCredentials", m.listSecretsFilter)
				assert.Len(t, m.updatedSecrets, 1)

				secretValue, ok := m.updatedSecrets["my-stack-AzureCredentials"]
				assert.True(t, ok, "Secret should be stored with name")

				var storedCreds AzureCredentials
				err := json.Unmarshal([]byte(secretValue), &storedCreds)
				require.NoError(t, err)
				assert.Equal(t, "12345678-1234-1234-1234-123456789abc", storedCreds.TenantID)
			},
		},
		{
			name:      "Error when tenant ID is missing",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "my-secret",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			wantErr:    true,
			wantErrMsg: "all credentials are required",
		},
		{
			name:      "Error when client ID is missing",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "",
				ClientSecret:   "my-secret",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			wantErr:    true,
			wantErrMsg: "all credentials are required",
		},
		{
			name:      "Error when client secret is missing",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			wantErr:    true,
			wantErrMsg: "all credentials are required",
		},
		{
			name:      "Error when subscription ID is missing",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "my-secret",
				SubscriptionID: "",
			},
			wantErr:    true,
			wantErrMsg: "all credentials are required",
		},
		{
			name:      "Error when UpdateSecret fails",
			stackName: "my-stack",
			creds: AzureCredentials{
				TenantID:       "12345678-1234-1234-1234-123456789abc",
				ClientID:       "87654321-4321-4321-4321-fedcba987654",
				ClientSecret:   "my-secret",
				SubscriptionID: "abcdef12-3456-7890-abcd-ef1234567890",
			},
			mockSetup: func(m *MockSecretsStore) {
				m.updateSecretFunc = func(ctx context.Context, secretID string, secretValue string) error {
					return errors.New("failed to update secret")
				}
			},
			wantErr:    true,
			wantErrMsg: "failed to store credentials in Secrets Manager",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := NewMockSecretsStore()

			if tt.mockSetup != nil {
				tt.mockSetup(mockStore)
			}

			err := storeAzureCredentials(ctx, mockStore, tt.stackName, tt.creds)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.wantErrMsg != "" {
					assert.Contains(t, err.Error(), tt.wantErrMsg)
				}
			} else {
				assert.NoError(t, err)
				if tt.validateStore != nil {
					tt.validateStore(t, mockStore)
				}
			}
		})
	}
}

// Tests for storeGCPCredentials function
func TestStoreGCPCredentials(t *testing.T) {
	validGCPJSON := `{
		"type": "service_account",
		"project_id": "my-project",
		"private_key_id": "key123",
		"private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg...\n-----END PRIVATE KEY-----\n",
		"client_email": "cudly@my-project.iam.gserviceaccount.com",
		"client_id": "123456789",
		"auth_uri": "https://accounts.google.com/o/oauth2/auth",
		"token_uri": "https://oauth2.googleapis.com/token"
	}`

	tests := []struct {
		name          string
		stackName     string
		credsJSON     string
		mockSetup     func(*MockSecretsStore)
		wantErr       bool
		wantErrMsg    string
		validateStore func(*testing.T, *MockSecretsStore)
	}{
		{
			name:      "Successfully store valid GCP credentials",
			stackName: "my-stack",
			credsJSON: validGCPJSON,
			mockSetup: func(m *MockSecretsStore) {
				m.listSecretsFunc = func(ctx context.Context, filter string) ([]string, error) {
					return []string{"arn:aws:secretsmanager:us-east-1:123456789012:secret:my-stack-GCPCredentials-xyz789"}, nil
				}
			},
			wantErr: false,
			validateStore: func(t *testing.T, m *MockSecretsStore) {
				assert.Equal(t, "my-stack-GCPCredentials", m.listSecretsFilter)
				assert.Len(t, m.updatedSecrets, 1)

				secretID := "arn:aws:secretsmanager:us-east-1:123456789012:secret:my-stack-GCPCredentials-xyz789"
				secretValue, ok := m.updatedSecrets[secretID]
				assert.True(t, ok, "Secret should be stored")

				var storedCreds GCPCredentials
				err := json.Unmarshal([]byte(secretValue), &storedCreds)
				require.NoError(t, err)
				assert.Equal(t, "service_account", storedCreds.Type)
				assert.Equal(t, "my-project", storedCreds.ProjectID)
				assert.Equal(t, "cudly@my-project.iam.gserviceaccount.com", storedCreds.ClientEmail)
			},
		},
		{
			name:      "Store credentials when secret ARN not found",
			stackName: "my-stack",
			credsJSON: validGCPJSON,
			mockSetup: func(m *MockSecretsStore) {
				m.listSecretsFunc = func(ctx context.Context, filter string) ([]string, error) {
					return []string{}, nil
				}
			},
			wantErr: false,
			validateStore: func(t *testing.T, m *MockSecretsStore) {
				assert.Equal(t, "my-stack-GCPCredentials", m.listSecretsFilter)
				secretValue, ok := m.updatedSecrets["my-stack-GCPCredentials"]
				assert.True(t, ok, "Secret should be stored with name")

				var storedCreds GCPCredentials
				err := json.Unmarshal([]byte(secretValue), &storedCreds)
				require.NoError(t, err)
				assert.Equal(t, "my-project", storedCreds.ProjectID)
			},
		},
		{
			name:       "Error when JSON is invalid",
			stackName:  "my-stack",
			credsJSON:  `{invalid json`,
			wantErr:    true,
			wantErrMsg: "failed to parse credentials",
		},
		{
			name:      "Error when type is not service_account",
			stackName: "my-stack",
			credsJSON: `{
				"type": "user_account",
				"project_id": "my-project",
				"private_key": "key",
				"client_email": "test@example.com"
			}`,
			wantErr:    true,
			wantErrMsg: "expected type 'service_account'",
		},
		{
			name:      "Error when project_id is missing",
			stackName: "my-stack",
			credsJSON: `{
				"type": "service_account",
				"private_key": "key",
				"client_email": "test@example.com"
			}`,
			wantErr:    true,
			wantErrMsg: "missing project_id",
		},
		{
			name:      "Error when client_email is missing",
			stackName: "my-stack",
			credsJSON: `{
				"type": "service_account",
				"project_id": "my-project",
				"private_key": "key"
			}`,
			wantErr:    true,
			wantErrMsg: "missing client_email",
		},
		{
			name:      "Error when private_key is missing",
			stackName: "my-stack",
			credsJSON: `{
				"type": "service_account",
				"project_id": "my-project",
				"client_email": "test@example.com"
			}`,
			wantErr:    true,
			wantErrMsg: "missing private_key",
		},
		{
			name:      "Error when UpdateSecret fails",
			stackName: "my-stack",
			credsJSON: validGCPJSON,
			mockSetup: func(m *MockSecretsStore) {
				m.updateSecretFunc = func(ctx context.Context, secretID string, secretValue string) error {
					return errors.New("failed to update secret")
				}
			},
			wantErr:    true,
			wantErrMsg: "failed to store credentials in Secrets Manager",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := NewMockSecretsStore()

			if tt.mockSetup != nil {
				tt.mockSetup(mockStore)
			}

			err := storeGCPCredentials(ctx, mockStore, tt.stackName, tt.credsJSON)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.wantErrMsg != "" {
					assert.Contains(t, err.Error(), tt.wantErrMsg)
				}
			} else {
				assert.NoError(t, err)
				if tt.validateStore != nil {
					tt.validateStore(t, mockStore)
				}
			}
		})
	}
}
