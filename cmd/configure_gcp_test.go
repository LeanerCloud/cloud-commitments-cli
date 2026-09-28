package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
)

// --- addMemberToPolicyBinding -------------------------------------------------

// TestAddMemberToPolicyBinding_AppendsToExistingBinding verifies a member is
// appended to an existing binding for the role and the function reports a
// change.
func TestAddMemberToPolicyBinding_AppendsToExistingBinding(t *testing.T) {
	policy := &cloudresourcemanager.Policy{
		Bindings: []*cloudresourcemanager.Binding{
			{Role: "roles/viewer", Members: []string{"user:existing@example.com"}},
		},
	}

	changed := addMemberToPolicyBinding(policy, "serviceAccount:sa@proj.iam.gserviceaccount.com", "roles/viewer")

	require.True(t, changed, "adding a new member to an existing role binding must report a change")
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, []string{
		"user:existing@example.com",
		"serviceAccount:sa@proj.iam.gserviceaccount.com",
	}, policy.Bindings[0].Members)
}

// TestAddMemberToPolicyBinding_AlreadyBoundNoChange verifies that an
// already-bound member is a no-op (returns false, no duplicate appended).
func TestAddMemberToPolicyBinding_AlreadyBoundNoChange(t *testing.T) {
	member := "serviceAccount:sa@proj.iam.gserviceaccount.com"
	policy := &cloudresourcemanager.Policy{
		Bindings: []*cloudresourcemanager.Binding{
			{Role: "roles/viewer", Members: []string{member}},
		},
	}

	changed := addMemberToPolicyBinding(policy, member, "roles/viewer")

	require.False(t, changed, "re-adding a member already bound to the role must report no change")
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, []string{member}, policy.Bindings[0].Members,
		"member must not be duplicated")
}

// TestAddMemberToPolicyBinding_CreatesMissingBinding verifies a new binding is
// appended when the role is absent from the policy.
func TestAddMemberToPolicyBinding_CreatesMissingBinding(t *testing.T) {
	policy := &cloudresourcemanager.Policy{
		Bindings: []*cloudresourcemanager.Binding{
			{Role: "roles/viewer", Members: []string{"user:existing@example.com"}},
		},
	}

	member := "serviceAccount:sa@proj.iam.gserviceaccount.com"
	changed := addMemberToPolicyBinding(policy, member, "roles/billing.projectManager")

	require.True(t, changed, "adding a member to an absent role must create the binding and report a change")
	require.Len(t, policy.Bindings, 2)
	newBinding := policy.Bindings[1]
	assert.Equal(t, "roles/billing.projectManager", newBinding.Role)
	assert.Equal(t, []string{member}, newBinding.Members)
}

// TestAddMemberToPolicyBinding_PreservesConditionalBindings is the regression
// guard for the version-3 read-modify-write round-trip: adding a member to one
// role must NOT drop or mutate a conditional (IAM condition) binding on another
// role. Losing conditional bindings would silently widen access.
func TestAddMemberToPolicyBinding_PreservesConditionalBindings(t *testing.T) {
	conditional := &cloudresourcemanager.Binding{
		Role:    "roles/storage.objectViewer",
		Members: []string{"user:auditor@example.com"},
		Condition: &cloudresourcemanager.Expr{
			Title:      "only-prod-bucket",
			Expression: `resource.name.startsWith("projects/_/buckets/prod-")`,
		},
	}
	policy := &cloudresourcemanager.Policy{
		Version: 3,
		Bindings: []*cloudresourcemanager.Binding{
			conditional,
			{Role: "roles/viewer", Members: []string{"user:existing@example.com"}},
		},
	}

	member := "serviceAccount:sa@proj.iam.gserviceaccount.com"
	changed := addMemberToPolicyBinding(policy, member, "roles/viewer")
	require.True(t, changed)

	// The conditional binding must still be present, unchanged.
	require.Len(t, policy.Bindings, 2, "no binding may be dropped")
	var found *cloudresourcemanager.Binding
	for _, b := range policy.Bindings {
		if b.Role == "roles/storage.objectViewer" {
			found = b
		}
	}
	require.NotNil(t, found, "the conditional binding must be preserved")
	require.NotNil(t, found.Condition, "the IAM condition must be preserved")
	assert.Equal(t, "only-prod-bucket", found.Condition.Title)
	assert.Equal(t, `resource.name.startsWith("projects/_/buckets/prod-")`, found.Condition.Expression)
	assert.Equal(t, []string{"user:auditor@example.com"}, found.Members,
		"the conditional binding's members must be untouched")
}

// --- writeServiceAccountKey (key-creation rollback) ---------------------------

// mockGCPKeyProvisioner is a configurable gcpKeyProvisioner used to assert the
// reserve / mint / decode / write / rollback flow of writeServiceAccountKey.
type mockGCPKeyProvisioner struct {
	createErr      error
	deleteErr      error
	keyName        string
	privateKeyData string // base64-encoded, as returned by the IAM API

	createCalled   bool
	deleteCalled   bool
	createSAEmail  string
	deletedKeyName string
}

func (m *mockGCPKeyProvisioner) CreateKey(_ context.Context, saEmail string) (string, string, error) {
	m.createCalled = true
	m.createSAEmail = saEmail
	if m.createErr != nil {
		return "", "", m.createErr
	}
	return m.keyName, m.privateKeyData, nil
}

func (m *mockGCPKeyProvisioner) DeleteKey(_ context.Context, keyName string) error {
	m.deleteCalled = true
	m.deletedKeyName = keyName
	return m.deleteErr
}

func TestWriteServiceAccountKey_Success(t *testing.T) {
	keyMaterial := []byte(`{"type":"service_account","project_id":"proj"}`)
	m := &mockGCPKeyProvisioner{
		keyName:        "projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com/keys/abc123",
		privateKeyData: base64.StdEncoding.EncodeToString(keyMaterial),
	}
	keyFile := filepath.Join(t.TempDir(), "cudly-gcp-key.json")

	_, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.NoError(t, err)

	assert.True(t, m.createCalled)
	assert.Equal(t, "sa@proj.iam.gserviceaccount.com", m.createSAEmail)
	assert.False(t, m.deleteCalled, "DeleteKey must not be called on success")

	// The decoded key material must have been written to the file.
	got, readErr := os.ReadFile(keyFile) // #nosec G304 -- test-controlled temp path
	require.NoError(t, readErr)
	assert.Equal(t, keyMaterial, got)
}

// TestWriteServiceAccountKey_DecodeFailureRollsBack verifies that when the
// returned key material is not valid base64, the minted remote key is deleted
// (so it does not linger as an active unused credential) and the reserved local
// file is cleaned up.
func TestWriteServiceAccountKey_DecodeFailureRollsBack(t *testing.T) {
	m := &mockGCPKeyProvisioner{
		keyName:        "projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com/keys/abc123",
		privateKeyData: "!!!not-base64!!!",
	}
	keyFile := filepath.Join(t.TempDir(), "cudly-gcp-key.json")

	_, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode key data")

	// The freshly minted remote key must have been deleted by its resource name.
	assert.True(t, m.deleteCalled, "the minted key must be deleted when decoding fails")
	assert.Equal(t, m.keyName, m.deletedKeyName)

	// The reserved local file must be cleaned up (nothing persisted).
	_, statErr := os.Stat(keyFile)
	assert.True(t, os.IsNotExist(statErr), "the reserved key file must be removed on failure")
}

// TestWriteServiceAccountKey_RollbackFailureSurfaced verifies that when the
// compensating DeleteKey also fails, the error names the orphaned remote key so
// the operator can delete it manually, while still surfacing the original cause.
func TestWriteServiceAccountKey_RollbackFailureSurfaced(t *testing.T) {
	m := &mockGCPKeyProvisioner{
		keyName:        "projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com/keys/orphan999",
		privateKeyData: "!!!not-base64!!!",
		deleteErr:      errors.New("delete 500"),
	}
	keyFile := filepath.Join(t.TempDir(), "cudly-gcp-key.json")

	_, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.Error(t, err)
	assert.True(t, m.deleteCalled)
	assert.Contains(t, err.Error(), "failed to decode key data", "the original cause must be surfaced")
	assert.Contains(t, err.Error(), "failed to delete the orphaned remote key")
	assert.Contains(t, err.Error(), "orphan999", "the orphaned key name must be named for manual cleanup")
}

// TestWriteServiceAccountKey_CreateFailureNoOrphan verifies that when minting
// the remote key fails, no rollback is attempted (nothing was minted) and no
// local file is left behind.
func TestWriteServiceAccountKey_CreateFailureNoOrphan(t *testing.T) {
	m := &mockGCPKeyProvisioner{
		createErr: errors.New("iam 403"),
	}
	keyFile := filepath.Join(t.TempDir(), "cudly-gcp-key.json")

	_, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create service account key")
	assert.False(t, m.deleteCalled, "no remote key was minted, so DeleteKey must not be called")

	_, statErr := os.Stat(keyFile)
	assert.True(t, os.IsNotExist(statErr), "the reserved key file must be removed when minting fails")
}

// TestWriteServiceAccountKey_ReserveFailureNoMint verifies that if the
// destination file already exists (exclusive-create fails), the remote key is
// never minted, so there is nothing to orphan.
func TestWriteServiceAccountKey_ReserveFailureNoMint(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cudly-gcp-key.json")
	require.NoError(t, os.WriteFile(keyFile, []byte("pre-existing"), 0o600))

	m := &mockGCPKeyProvisioner{
		keyName:        "projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com/keys/abc123",
		privateKeyData: base64.StdEncoding.EncodeToString([]byte("{}")),
	}

	_, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reserve key file")
	assert.False(t, m.createCalled, "the remote key must not be minted when the file cannot be reserved")

	// The pre-existing file must be left intact (we must not clobber it).
	got, readErr := os.ReadFile(keyFile) // #nosec G304 -- test-controlled temp path
	require.NoError(t, readErr)
	assert.Equal(t, []byte("pre-existing"), got)
}

// --- uploadGCPCredentialsFile (minted key cleanup, #1947) --------------------

const testMintedKeyName = "projects/proj/serviceAccounts/sa@proj.iam.gserviceaccount.com/keys/abc123"

// mintTestGCPKey mints a key into a newMintedGCPKeyPath location through the
// real writeServiceAccountKey flow, asserts it was created with 0600, and
// returns the file and the key resource name.
func mintTestGCPKey(t *testing.T) (string, string) {
	t.Helper()
	keyMaterial := []byte(`{"type":"service_account","project_id":"proj","client_email":"sa@proj.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"}`)
	m := &mockGCPKeyProvisioner{
		keyName:        testMintedKeyName,
		privateKeyData: base64.StdEncoding.EncodeToString(keyMaterial),
	}
	keyFile, err := newMintedGCPKeyPath()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(keyFile)) })

	keyName, err := writeServiceAccountKey(context.Background(), m, "sa@proj.iam.gserviceaccount.com", keyFile)
	require.NoError(t, err)
	require.Equal(t, testMintedKeyName, keyName)
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "minted key must be 0600")
	dirInfo, err := os.Stat(filepath.Dir(keyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm(), "minted key dir must be 0700")
	return keyFile, keyName
}

func assertMintedKeyGone(t *testing.T, keyFile string) {
	t.Helper()
	_, err := os.Stat(keyFile)
	assert.ErrorIs(t, err, os.ErrNotExist, "minted key file must be removed")
	_, err = os.Stat(filepath.Dir(keyFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "minted key dir must be removed")
}

func TestUploadGCPCredentialsFile_MintedKeyRemovedAfterSuccess(t *testing.T) {
	keyFile, keyName := mintTestGCPKey(t)
	store := NewMockSecretsStore()
	m := &mockGCPKeyProvisioner{}

	creds, err := uploadGCPCredentialsFile(context.Background(), store, "stack", keyFile, keyName, m.DeleteKey)
	require.NoError(t, err)
	assert.Equal(t, "sa@proj.iam.gserviceaccount.com", creds.ClientEmail)
	assert.Contains(t, store.updatedSecrets, "stack-GCPCredentials")
	assert.False(t, m.deleteCalled, "a successfully uploaded key must stay active in GCP")
	assertMintedKeyGone(t, keyFile)
}

func TestUploadGCPCredentialsFile_UploadFailureDeletesRemoteKey(t *testing.T) {
	keyFile, keyName := mintTestGCPKey(t)
	store := NewMockSecretsStore()
	store.updateSecretFunc = func(context.Context, string, string) error { return errors.New("access denied") }
	m := &mockGCPKeyProvisioner{}

	_, err := uploadGCPCredentialsFile(context.Background(), store, "stack", keyFile, keyName, m.DeleteKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.True(t, m.deleteCalled, "the minted key must be deleted from GCP when the upload fails")
	assert.Equal(t, testMintedKeyName, m.deletedKeyName)
	assertMintedKeyGone(t, keyFile)
}

func TestUploadGCPCredentialsFile_RemoteDeleteFailureNamesGcloudCommand(t *testing.T) {
	keyFile, keyName := mintTestGCPKey(t)
	store := NewMockSecretsStore()
	store.updateSecretFunc = func(context.Context, string, string) error { return errors.New("access denied") }
	m := &mockGCPKeyProvisioner{deleteErr: errors.New("permission denied")}

	_, err := uploadGCPCredentialsFile(context.Background(), store, "stack", keyFile, keyName, m.DeleteKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.Contains(t, err.Error(), "permission denied")
	assert.Contains(t, err.Error(), "gcloud iam service-accounts keys delete abc123 --iam-account=sa@proj.iam.gserviceaccount.com")
	assert.NotContains(t, err.Error(), "PRIVATE KEY", "the error must never carry key material")
	assertMintedKeyGone(t, keyFile)
}

// TestUploadGCPCredentialsFile_CanceledContextStillCleansUp models an
// interrupt during the Secrets Manager call: the remote delete must run on a
// live context even though the upload context is canceled.
func TestUploadGCPCredentialsFile_CanceledContextStillCleansUp(t *testing.T) {
	keyFile, keyName := mintTestGCPKey(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMockSecretsStore()
	store.updateSecretFunc = func(ctx context.Context, _, _ string) error { return ctx.Err() }
	var deleteCtxErr error
	deleted := false
	deleteKey := func(ctx context.Context, name string) error {
		deleted = true
		deleteCtxErr = ctx.Err()
		return nil
	}

	_, err := uploadGCPCredentialsFile(ctx, store, "stack", keyFile, keyName, deleteKey)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, deleted, "the minted key must be deleted from GCP after an interrupt")
	assert.NoError(t, deleteCtxErr, "the remote delete must not inherit the canceled context")
	assertMintedKeyGone(t, keyFile)
}

func TestUploadGCPCredentialsFile_ParseFailureDeletesRemoteKey(t *testing.T) {
	keyFile, err := newMintedGCPKeyPath()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(keyFile)) })
	require.NoError(t, os.WriteFile(keyFile, []byte("not json"), 0600))
	m := &mockGCPKeyProvisioner{}

	_, err = uploadGCPCredentialsFile(context.Background(), NewMockSecretsStore(), "stack", keyFile, testMintedKeyName, m.DeleteKey)
	require.Error(t, err)
	assert.Equal(t, testMintedKeyName, m.deletedKeyName, "a minted key that cannot be parsed must still be deleted from GCP")
	assertMintedKeyGone(t, keyFile)
}

func TestUploadGCPCredentialsFile_RemovalFailureReported(t *testing.T) {
	keyFile, keyName := mintTestGCPKey(t)
	// A read-only parent dir makes the unlink fail.
	require.NoError(t, os.Chmod(filepath.Dir(keyFile), 0500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(keyFile), 0700) })
	m := &mockGCPKeyProvisioner{}

	_, err := uploadGCPCredentialsFile(context.Background(), NewMockSecretsStore(), "stack", keyFile, keyName, m.DeleteKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to remove the minted key file")
}

func TestUploadGCPCredentialsFile_OperatorFileKept(t *testing.T) {
	keyFile, _ := mintTestGCPKey(t)
	m := &mockGCPKeyProvisioner{}

	_, err := uploadGCPCredentialsFile(context.Background(), NewMockSecretsStore(), "stack", keyFile, "", m.DeleteKey)
	require.NoError(t, err)
	_, err = os.Stat(keyFile)
	assert.NoError(t, err, "an operator-supplied credentials file must not be deleted")
	assert.False(t, m.deleteCalled, "an operator-supplied key must never be deleted from GCP")
}

func TestGcloudDeleteKeyCommand(t *testing.T) {
	assert.Equal(t,
		"gcloud iam service-accounts keys delete abc123 --iam-account=sa@proj.iam.gserviceaccount.com",
		gcloudDeleteKeyCommand(testMintedKeyName))
}
