package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	arnPrefix  = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:"
	wantedARN  = arnPrefix + "cudly-AzureCredentials-AbCdEf"
	oldARN     = arnPrefix + "cudly-AzureCredentials-old-GhIjKl"
	wantedName = "cudly-AzureCredentials"
)

func listStore(arns ...string) *MockSecretsStore {
	m := NewMockSecretsStore()
	m.listSecretsFunc = func(context.Context, string) ([]string, error) { return arns, nil }
	return m
}

func TestResolveSecretID(t *testing.T) {
	tests := []struct {
		name    string
		arns    []string
		want    string
		wantErr bool
	}{
		{"exact match after prefix match", []string{oldARN, wantedARN}, wantedARN, false},
		{"prefix match only falls back to name", []string{oldARN}, wantedName, false},
		{"no results falls back to name", nil, wantedName, false},
		{"two exact matches error", []string{wantedARN, arnPrefix + "cudly-AzureCredentials-MnOpQr"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSecretID(context.Background(), listStore(tt.arns...), wantedName)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStoreAzureCredentials_SkipsPrefixMatch(t *testing.T) {
	store := listStore(oldARN, wantedARN)
	creds := AzureCredentials{TenantID: testTenantID, ClientID: testSPAppID, ClientSecret: testSPSecret, SubscriptionID: testSubID}
	require.NoError(t, storeAzureCredentials(context.Background(), store, "cudly", creds))
	assert.Contains(t, store.updatedSecrets, wantedARN)
	assert.NotContains(t, store.updatedSecrets, oldARN)
}

func TestStoreGCPCredentials_SkipsPrefixMatch(t *testing.T) {
	gcpOld := arnPrefix + "cudly-GCPCredentials-old-GhIjKl"
	gcpWanted := arnPrefix + "cudly-GCPCredentials-AbCdEf"
	store := listStore(gcpOld, gcpWanted)
	creds := `{"type":"service_account","project_id":"p","client_email":"a@b.c","private_key":"k"}`
	require.NoError(t, storeGCPCredentials(context.Background(), store, "cudly", creds))
	assert.Contains(t, store.updatedSecrets, gcpWanted)
	assert.NotContains(t, store.updatedSecrets, gcpOld)
}
