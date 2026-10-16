package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGCPAPI points every Google API call at a local counter and installs ADC
// fixtures, so no test input can reach real GCP. It returns the count of
// non-token API requests seen.
func stubGCPAPI(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`))
			return
		}
		calls.Add(1)
		http.Error(w, `{"error":{"code":400,"message":"fixture"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	orig := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = orig
		transport.CloseIdleConnections()
	})

	adc := filepath.Join(t.TempDir(), "adc.json")
	require.NoError(t, os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture","refresh_token":"fixture"}`), 0600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	return &calls
}

// TestGCPWizardSteps_DefaultIsSkip proves Enter (empty input) skips every
// configure-gcp [r]un/[S]kip step instead of changing cloud identity state.
func TestGCPWizardSteps_DefaultIsSkip(t *testing.T) {
	const sa = "cudly-service-account@fixture-project.iam.gserviceaccount.com"
	steps := map[string]func(in string) error{
		"create-service-account": func(in string) error {
			// A skip continues to the existing-account prompt; supply one so
			// the step returns cleanly either way.
			_, err := gcpStepCreateServiceAccount(context.Background(), bufio.NewReader(strings.NewReader(in+sa+"\n")), "fixture-project")
			return err
		},
		"grant-roles": func(in string) error {
			return gcpStepGrantRole(context.Background(), bufio.NewReader(strings.NewReader(in)), "fixture-project", sa)
		},
		"create-key": func(in string) error {
			_, _, err := gcpStepCreateKey(bufio.NewReader(strings.NewReader(in)), sa, context.Background)
			return err
		},
	}
	for name, step := range steps {
		for _, tc := range []struct {
			label, input string
			wantCall     bool
		}{
			{"empty", "\n", false},
			{"r", "r\n", true},
			{"s", "s\n", false},
		} {
			t.Run(name+"/"+tc.label, func(t *testing.T) {
				calls := stubGCPAPI(t)
				out := captureGCPStdout(t, func() { _ = step(tc.input) })
				if tc.input == "\n" {
					assert.Contains(t, out, "Skipping")
					assert.NotContains(t, out, "Unknown option")
				}
				if tc.wantCall {
					assert.Positive(t, calls.Load(), "r must run the step")
				} else {
					assert.Zero(t, calls.Load(), "%q must not call GCP", tc.input)
				}
			})
		}
	}
}

func TestPromptAndRunGCPCommand_DefaultIsSkip(t *testing.T) {
	for _, tc := range []struct {
		label, input string
		wantRun      bool
	}{{"empty", "\n", false}, {"r", "r\n", true}, {"s", "s\n", false}} {
		t.Run(tc.label, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ran")
			gcloud := filepath.Join(t.TempDir(), "gcloud")
			require.NoError(t, os.WriteFile(gcloud, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700))
			out := captureGCPStdout(t, func() {
				require.NoError(t, promptAndRunGCPCommand(bufio.NewReader(strings.NewReader(tc.input)), "GCP Login", "gcloud auth login", gcloud))
			})
			if tc.input == "\n" {
				assert.Contains(t, out, "Skipping GCP Login")
				assert.NotContains(t, out, "Unknown option")
			}
			_, err := os.Stat(marker)
			if tc.wantRun {
				assert.NoError(t, err, "r must run the command")
			} else {
				assert.True(t, os.IsNotExist(err), "%q must not run the command", tc.input)
			}
		})
	}
}

func captureGCPStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stdout = orig }()
		fn()
	}()
	require.NoError(t, w.Close())
	return <-done
}
