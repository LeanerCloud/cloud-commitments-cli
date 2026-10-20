package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	secretsmgrtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
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

type fakeSecretsClient struct {
	pages []*secretsmanager.ListSecretsOutput
	calls int
	loop  bool
}

func (f *fakeSecretsClient) ListSecrets(_ context.Context, in *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	f.calls++
	if f.loop {
		return &secretsmanager.ListSecretsOutput{NextToken: aws.String("again")}, nil
	}
	idx := 0
	if in.NextToken != nil {
		idx = 1
	}
	return f.pages[idx], nil
}

func (f *fakeSecretsClient) UpdateSecret(context.Context, *secretsmanager.UpdateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.UpdateSecretOutput, error) {
	return &secretsmanager.UpdateSecretOutput{}, nil
}

func TestAWSSecretsStore_ListSecretsPaginates(t *testing.T) {
	fake := &fakeSecretsClient{pages: []*secretsmanager.ListSecretsOutput{
		{SecretList: []secretsmgrtypes.SecretListEntry{{ARN: aws.String(oldARN)}}, NextToken: aws.String("t2")},
		{SecretList: []secretsmgrtypes.SecretListEntry{{ARN: aws.String(wantedARN)}}},
	}}
	store := &AWSSecretsStore{client: fake}
	got, err := resolveSecretID(context.Background(), store, wantedName)
	require.NoError(t, err)
	assert.Equal(t, wantedARN, got)
	assert.Equal(t, 2, fake.calls)
}

func TestAWSSecretsStore_ListSecretsFailsAtPageCap(t *testing.T) {
	fake := &fakeSecretsClient{loop: true}
	_, err := (&AWSSecretsStore{client: fake}).ListSecrets(context.Background(), wantedName)
	require.ErrorContains(t, err, "pagination cap")
	assert.Equal(t, maxSecretListPages, fake.calls)
}

func TestAWSSecretsStore_ListSecretsHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeSecretsClient{loop: true}
	_, err := (&AWSSecretsStore{client: fake}).ListSecrets(ctx, wantedName)
	require.ErrorIs(t, err, context.Canceled)
}
