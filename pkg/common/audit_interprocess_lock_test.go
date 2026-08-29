//go:build linux || darwin

package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	auditLockHelperEnv      = "CUDLY_AUDIT_LOCK_HELPER"
	auditLockHelperValue    = "1"
	auditLockHelperPathEnv  = "CUDLY_AUDIT_LOCK_HELPER_PATH"
	auditLockHelperReadyEnv = "CUDLY_AUDIT_LOCK_HELPER_READY"
)

func TestWriteAuditRecordWaitsForTransactionLockBeforeRepairingPartialRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	firstRecord := map[string]any{
		"run_id":  "run-parent",
		"status":  "started",
		"padding": strings.Repeat("x", 128),
	}
	firstJSON, err := json.Marshal(firstRecord)
	require.NoError(t, err)
	require.Greater(t, len(firstJSON), 1)

	secondRecord := auditLockHelperRecord()
	secondJSON, err := json.Marshal(secondRecord)
	require.NoError(t, err)

	holder, err := openAuditLogForAppend(path, 0o644)
	require.NoError(t, err)
	holderClosed := false
	holderLocked := false
	t.Cleanup(func() {
		if holderLocked {
			require.NoError(t, holder.Unlock())
		}
		if !holderClosed {
			require.NoError(t, holder.Close())
		}
	})

	require.NoError(t, holder.Lock())
	holderLocked = true
	n, err := holder.Write(firstJSON)
	require.NoError(t, err)
	require.Equal(t, len(firstJSON), n)
	require.NoError(t, holder.Sync())

	readyPath := filepath.Join(t.TempDir(), "helper-ready")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestAuditLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		auditLockHelperEnv+"="+auditLockHelperValue,
		auditLockHelperPathEnv+"="+path,
		auditLockHelperReadyEnv+"="+readyPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	childReaped := false
	t.Cleanup(func() {
		cancel()
		if childReaped {
			return
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		}
	})

	waitForHelperReady(t, ctx, readyPath)
	if err := helperStillWaiting(done, path); err != nil {
		childReaped = true
		require.NoError(t, err)
	}

	n, err = holder.Write([]byte{'\n'})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, holder.Sync())
	require.NoError(t, holder.Unlock())
	holderLocked = false
	require.NoError(t, holder.Close())
	holderClosed = true

	select {
	case err := <-done:
		childReaped = true
		require.NoError(t, err, stderr.String())
	case <-ctx.Done():
		require.FailNow(t, "helper did not finish after audit lock release", ctx.Err().Error())
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	expected := append(append(append([]byte(nil), firstJSON...), '\n'), secondJSON...)
	expected = append(expected, '\n')
	require.Equal(t, expected, data)

	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	require.Len(t, lines, 2)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &decoded))
	assert.Equal(t, "run-parent", decoded["run_id"])
	require.NoError(t, json.Unmarshal(lines[1], &decoded))
	assert.Equal(t, "run-helper", decoded["run_id"])
}

func TestAuditLockHelperProcess(t *testing.T) {
	if os.Getenv(auditLockHelperEnv) != auditLockHelperValue {
		return
	}

	path := os.Getenv(auditLockHelperPathEnv)
	readyPath := os.Getenv(auditLockHelperReadyEnv)
	require.NotEmpty(t, path)
	require.NotEmpty(t, readyPath)

	require.NoError(t, os.WriteFile(readyPath, []byte("ready"), 0o600))
	require.NoError(t, WriteAuditRecord(auditLockHelperRecord(), path))
}

func auditLockHelperRecord() AuditRecord {
	return AuditRecord{
		RunID:     "run-helper",
		Status:    "success",
		Timestamp: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
}

func waitForHelperReady(t *testing.T, ctx context.Context, readyPath string) {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			require.NoError(t, err)
		}

		select {
		case <-ctx.Done():
			require.FailNow(t, "helper did not signal readiness", ctx.Err().Error())
		case <-ticker.C:
		}
	}
}

func helperStillWaiting(done <-chan error, path string) error {
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("helper completed while audit lock was held: err=%v audit_log=%q", err, data)
	case <-timer.C:
		return nil
	}
}
