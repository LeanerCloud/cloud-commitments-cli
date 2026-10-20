package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for storeGCPCredentials function.
func TestStoreGCPCredentials(t *testing.T) {
	t.Parallel()
	// private_key validation is presence-only; the key content is not parsed or
	// validated as a real PEM block by storeGCPCredentials.
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
		mockSetup     func(*MockSecretsStore)
		validateStore func(*testing.T, *MockSecretsStore)
		name          string
		stackName     string
		credsJSON     string
		wantErrMsg    string
		wantErr       bool
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
