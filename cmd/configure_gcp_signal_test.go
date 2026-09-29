//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunConfigureGCP_InterruptCleansMintedKey(t *testing.T) {
	if dir := os.Getenv("CUDLY_GCP_SIGNAL_CHILD"); dir != "" {
		if err := runGCPInterruptChild(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0) // The test harness cannot write to the deliberately full stdout pipe.
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			child, dir, stdout, stderr := startGCPInterruptChild(t, "upload")
			defer stdout.Close()
			var keyFile string
			require.Eventually(t, func() bool {
				matches, err := filepath.Glob(filepath.Join(dir, "cudly-gcp-key-*", "key.json"))
				if err != nil || len(matches) != 1 {
					return false
				}
				data, err := os.ReadFile(matches[0])
				if err != nil || !bytes.Contains(data, []byte("fixture-secret")) {
					return false
				}
				keyFile = matches[0]
				return true
			}, 10*time.Second, 10*time.Millisecond)
			require.NoError(t, child.Process.Signal(sig))
			err := child.Wait()
			require.NoFileExists(t, keyFile)
			require.NoDirExists(t, filepath.Dir(keyFile))
			require.NoError(t, err, "child stderr: %s", stderr.String())
			deleted, err := os.ReadFile(filepath.Join(dir, "deleted"))
			require.NoError(t, err)
			require.Equal(t, "/v1/projects/fixture-project/serviceAccounts/cudly-service-account@fixture-project.iam.gserviceaccount.com/keys/fixture-key", string(deleted))
		})
	}
}

func TestRunConfigureGCP_InterruptDuringMint(t *testing.T) {
	child, dir, stdout, stderr := startGCPInterruptChild(t, "mint")
	defer stdout.Close()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "creating"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, child.Process.Signal(os.Interrupt))
	require.NoError(t, child.Wait(), "child stderr: %s", stderr.String())
	matches, err := filepath.Glob(filepath.Join(dir, "cudly-gcp-key-*"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.NoFileExists(t, filepath.Join(dir, "minted"))
}

func TestRunConfigureGCP_InterruptAtPromptExits(t *testing.T) {
	child, dir, stdout, stderr := startGCPInterruptChild(t, "prompt")
	defer stdout.Close()
	var output strings.Builder
	for !strings.Contains(output.String(), "the local copy is removed after upload) ") {
		var b [1]byte
		_, err := stdout.Read(b[:])
		require.NoError(t, err)
		output.WriteByte(b[0])
	}
	require.NoError(t, child.Process.Signal(os.Interrupt))
	require.Error(t, child.Wait(), "child stderr: %s", stderr.String())
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, status.Signaled())
	require.Equal(t, syscall.SIGINT, status.Signal())
	require.NoFileExists(t, filepath.Join(dir, "minted"))
}

func startGCPInterruptChild(t *testing.T, phase string) (*exec.Cmd, string, io.ReadCloser, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	adc := filepath.Join(dir, "adc.json")
	require.NoError(t, os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture","refresh_token":"fixture"}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud"), []byte("#!/bin/sh\n[ \"$*\" = \"config set project fixture-project\" ]\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunConfigureGCP_InterruptCleansMintedKey$")
	child.Env = append(os.Environ(), "CUDLY_GCP_SIGNAL_CHILD="+dir, "CUDLY_GCP_SIGNAL_PHASE="+phase, "GOOGLE_APPLICATION_CREDENTIALS="+adc,
		"TMPDIR="+dir, "PATH="+dir, "AWS_ACCESS_KEY_ID=fixture", "AWS_SECRET_ACCESS_KEY=fixture",
		"AWS_SESSION_TOKEN=", "AWS_PROFILE=", "AWS_REGION=us-east-1", "AWS_EC2_METADATA_DISABLED=true")
	stdin, err := child.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	stderr := new(bytes.Buffer)
	child.Stderr = stderr
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill() })
	input := "s\ns\ns\nfixture-project\ns\ns\n"
	if phase != "prompt" {
		input += "r\n"
	}
	_, err = io.WriteString(stdin, input)
	require.NoError(t, err)
	return child, dir, stdout, stderr
}

func runGCPInterruptChild(dir string) error {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_, _ = io.WriteString(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`)
		case r.Method == http.MethodDelete:
			if err := os.WriteFile(filepath.Join(dir, "deleted"), []byte(r.URL.Path), 0600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/keys"):
			if os.Getenv("CUDLY_GCP_SIGNAL_PHASE") == "mint" {
				if err := os.WriteFile(filepath.Join(dir, "creating"), nil, 0600); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				<-r.Context().Done()
				return
			}
			if err := os.WriteFile(filepath.Join(dir, "minted"), nil, 0600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := fillGCPChildStdout(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			material := base64.StdEncoding.EncodeToString([]byte(`{"type":"service_account","project_id":"fixture-project","client_email":"cudly-service-account@fixture-project.iam.gserviceaccount.com","private_key":"fixture-secret"}`))
			_, _ = fmt.Fprintf(w, `{"name":"projects/fixture-project/serviceAccounts/cudly-service-account@fixture-project.iam.gserviceaccount.com/keys/fixture-key","privateKeyData":%q}`, material)
		case r.Header.Get("X-Amz-Target") == "secretsmanager.ListSecrets":
			<-r.Context().Done()
		default:
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	}
	http.DefaultTransport = transport
	if err := os.Setenv("AWS_ENDPOINT_URL_SECRETS_MANAGER", server.URL); err != nil {
		return err
	}
	gcpOpts = GCPConfigOptions{StackName: "fixture-stack"}
	err := runConfigureGCP(nil, nil)
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("expected cancellation from the real coordinator, got %w", err)
	}
	return nil
}

func fillGCPChildStdout() error {
	if err := syscall.SetNonblock(1, true); err != nil {
		return err
	}
	defer syscall.SetNonblock(1, false)
	for _, chunk := range [][]byte{bytes.Repeat([]byte("x"), 4096), []byte("x")} {
		for {
			if _, err := syscall.Write(1, chunk); err != nil {
				if errors.Is(err, syscall.EAGAIN) {
					break
				}
				return err
			}
		}
	}
	return nil
}
