package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/googleapi"
	iamv1 "google.golang.org/api/iam/v1"
)

// uploadGCPCredentialsFile stores credsFile in the secrets store. A non-empty
// mintedKeyName means the file is a key this run created: the local copy is
// removed on every path, and on failure the remote key is deleted via
// deleteKey so it does not stay active. Cleanup failures are returned.
func uploadGCPCredentialsFile(ctx context.Context, store SecretsStore, stackName, credsFile, mintedKeyName string, deleteKey func(context.Context, string) error) (creds GCPCredentials, err error) {
	done := false
	if mintedKeyName != "" {
		defer func() {
			// A panic or Goexit leaves the named err nil and done false; the
			// return value is lost, so report the outcome on stderr. The panic
			// keeps propagating.
			aborted := err == nil && !done
			if aborted {
				err = errors.New("upload aborted before completion")
			}
			// Remove the local copy first: a second Ctrl-C during the slow
			// remote delete kills the process and must not leave it on disk.
			rmErr := removeMintedGCPKey(credsFile)
			if err != nil {
				err = deleteMintedGCPKeyRemotely(err, mintedKeyName, deleteKey)
			}
			if rmErr != nil {
				err = errors.Join(err, rmErr)
			}
			if aborted {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}

	creds, credsData, err := loadAndUpdateGCPCredentials(credsFile)
	if err != nil {
		return GCPCredentials{}, err
	}

	if err := storeGCPCredentials(ctx, store, stackName, string(credsData)); err != nil {
		return GCPCredentials{}, err
	}

	done = true
	return creds, nil
}

// deleteMintedGCPKeyRemotely deletes the minted key after cause aborted the
// upload. It uses a fresh context so a canceled parent does not skip it.
func deleteMintedGCPKeyRemotely(cause error, keyName string, deleteKey func(context.Context, string) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), gcpSDKCallTimeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "Deleting minted key; if aborted, delete it with: %s\n", gcloudDeleteKeyCommand(keyName))
	if err := deleteKey(ctx, keyName); err != nil {
		return fmt.Errorf("%w; the minted key is still active in GCP and deleting it failed (%w); delete it with: %s", cause, err, gcloudDeleteKeyCommand(keyName))
	}
	return cause
}

// gcloudDeleteKeyCommand renders the manual delete command for an IAM key
// resource name (projects/<p>/serviceAccounts/<sa>/keys/<id>).
func gcloudDeleteKeyCommand(keyName string) string {
	parts := strings.Split(keyName, "/")
	if len(parts) == 6 && parts[2] == "serviceAccounts" && parts[4] == "keys" {
		return fmt.Sprintf("gcloud iam service-accounts keys delete %s --iam-account=%s", parts[5], parts[3])
	}
	return fmt.Sprintf("gcloud iam service-accounts keys delete <id of %s>", keyName)
}

// gcpKeyProvisioner abstracts the IAM service-account key operations used by
// writeServiceAccountKey. It exists so the reserve / mint / decode / write /
// rollback flow can be unit-tested with a mock without hitting GCP (mirrors
// azureSPProvisioner in configure_azure_sp.go).
type gcpKeyProvisioner interface {
	// CreateKey mints a new JSON key for saEmail and returns the key resource
	// name and the base64-encoded private key material.
	CreateKey(ctx context.Context, saEmail string) (keyName, privateKeyData string, err error)
	// DeleteKey deletes the key identified by keyName. It is the compensating
	// action used to avoid orphaning a freshly minted key on a local failure.
	DeleteKey(ctx context.Context, keyName string) error
}

// iamKeyProvisioner is the production gcpKeyProvisioner backed by the IAM API v1.
type iamKeyProvisioner struct {
	svc *iamv1.Service
}

func (k *iamKeyProvisioner) CreateKey(ctx context.Context, saEmail string) (keyName, privateKeyData string, err error) {
	resource := fmt.Sprintf("projects/-/serviceAccounts/%s", saEmail)
	key, err := k.svc.Projects.ServiceAccounts.Keys.Create(resource, &iamv1.CreateServiceAccountKeyRequest{
		PrivateKeyType: "TYPE_GOOGLE_CREDENTIALS_FILE",
	}).Context(ctx).Do()
	if err != nil {
		return "", "", err
	}
	return key.Name, key.PrivateKeyData, nil
}

// createKeyError wraps a CreateKey failure. Only a 4xx googleapi error proves
// the server rejected the request; anything else (cancellation, timeout, 5xx,
// network error) may have followed a committed mint, so warn the operator.
func createKeyError(saEmail string, err error) error {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code >= 400 && gerr.Code < 500 {
		return fmt.Errorf("failed to create service account key: %w", err)
	}
	return fmt.Errorf("failed to create service account key: %w; the key may have been created before the request ended: "+
		"list keys with `gcloud iam service-accounts keys list --iam-account=%s --managed-by=user` and delete any key created just now "+
		"with `gcloud iam service-accounts keys delete <KEY_ID> --iam-account=%s`", err, saEmail, saEmail)
}

func (k *iamKeyProvisioner) DeleteKey(ctx context.Context, keyName string) error {
	_, err := k.svc.Projects.ServiceAccounts.Keys.Delete(keyName).Context(ctx).Do()
	return err
}

// newIAMKeyProvisioner builds an iamKeyProvisioner authenticated via ADC.
func newIAMKeyProvisioner(ctx context.Context) (*iamKeyProvisioner, error) {
	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return nil, err
	}

	svc, err := iamv1.NewService(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM client: %w", err)
	}
	return &iamKeyProvisioner{svc: svc}, nil
}

// createGCPServiceAccountKey creates a JSON key for the given service account,
// writes it to keyFile and returns the key resource name. This replaces
// "gcloud iam service-accounts keys create <file> --iam-account=<sa>".
func createGCPServiceAccountKey(ctx context.Context, saEmail, keyFile string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gcpSDKCallTimeout)
	defer cancel()

	p, err := newIAMKeyProvisioner(ctx)
	if err != nil {
		return "", err
	}
	return writeServiceAccountKey(ctx, p, saEmail, keyFile)
}

// deleteGCPServiceAccountKey deletes the IAM key keyName via ADC.
func deleteGCPServiceAccountKey(ctx context.Context, keyName string) error {
	p, err := newIAMKeyProvisioner(ctx)
	if err != nil {
		return err
	}
	return p.DeleteKey(ctx, keyName)
}

// writeServiceAccountKey reserves keyFile with exclusive-create semantics
// BEFORE minting the remote key (so it never mints a key it cannot persist
// locally), then mints the key via p, decodes the base64 material and writes it
// to keyFile, returning the key resource name. If decoding or writing fails after the remote key is minted it
// deletes the remote key so it does not linger as an active, unused credential.
// Extracted from createGCPServiceAccountKey so the reserve / mint / rollback
// flow is unit-testable with a mock (no GCP credentials).
func writeServiceAccountKey(ctx context.Context, p gcpKeyProvisioner, saEmail, keyFile string) (string, error) {
	// Reserve the destination file first (fails if it already exists), so we
	// never mint a remote key we cannot persist locally.
	// #nosec G304 -- keyFile is the sole caller's fixed filename inside a private os.MkdirTemp dir (newMintedGCPKeyPath); program-controlled and not attacker input
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to reserve key file %s: %w", keyFile, err)
	}
	// Best-effort: remove the reserved file if we return before writing it.
	wrote := false
	defer func() {
		_ = f.Close()
		if !wrote {
			_ = os.Remove(keyFile)
		}
	}()

	keyName, privateKeyData, err := p.CreateKey(ctx, saEmail)
	if err != nil {
		return "", createKeyError(saEmail, err)
	}

	// From here on, any failure must delete the newly minted remote key so it
	// does not linger as an active, unused credential.
	deleteRemoteKey := func(cause error) error {
		// Use a fresh context: the parent may already be canceled/expired.
		delCtx, delCancel := context.WithTimeout(context.Background(), gcpSDKCallTimeout)
		defer delCancel()
		if delErr := p.DeleteKey(delCtx, keyName); delErr != nil {
			return fmt.Errorf("%w; additionally failed to delete the orphaned remote key %s: %w", cause, keyName, delErr)
		}
		return cause
	}

	// PrivateKeyData is base64-encoded JSON.
	decoded, err := base64.StdEncoding.DecodeString(privateKeyData)
	if err != nil {
		return "", deleteRemoteKey(fmt.Errorf("failed to decode key data: %w", err))
	}

	if _, err := f.Write(decoded); err != nil {
		return "", deleteRemoteKey(fmt.Errorf("failed to write key file %s: %w", keyFile, err))
	}
	wrote = true
	return keyName, nil
}

// mintedGCPKeyFilename is the key file's name inside its private temp dir.
const mintedGCPKeyFilename = "key.json"

// newMintedGCPKeyPath returns a key path inside a fresh 0700 temp dir, so the
// minted key is never readable by other users or left in a synced home dir.
func newMintedGCPKeyPath() (string, error) {
	dir, err := os.MkdirTemp("", "cudly-gcp-key-")
	if err != nil {
		return "", fmt.Errorf("failed to create private directory for the key: %w", err)
	}
	return filepath.Join(dir, mintedGCPKeyFilename), nil
}

// removeMintedGCPKey deletes a key path from newMintedGCPKeyPath and its dir.
// It removes non-recursively so it can never delete anything else.
func removeMintedGCPKey(keyFile string) error {
	if err := os.Remove(keyFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove the minted key file %s; delete it manually: %w", keyFile, err)
	}
	if err := os.Remove(filepath.Dir(keyFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove the minted key directory %s: %w", filepath.Dir(keyFile), err)
	}
	return nil
}
