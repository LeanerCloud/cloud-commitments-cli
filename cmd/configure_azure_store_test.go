package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for storeAzureCredentials function.
func TestStoreAzureCredentials(t *testing.T) {
	tests := []struct {
		mockSetup     func(*MockSecretsStore)
		validateStore func(*testing.T, *MockSecretsStore)
		creds         AzureCredentials
		name          string
		stackName     string
		wantErrMsg    string
		wantErr       bool
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
			// No mockSetup needed for error-path cases: credential validation runs
			// before any store operation, so UpdateSecret is never called.
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
