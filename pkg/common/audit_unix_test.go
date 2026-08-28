//go:build unix

package common

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAuditRecord_RejectsNonRegularTargetEvenIfStartupWasBypassed(t *testing.T) {
	t.Parallel()
	path := requireNonRegularAuditPath(t, "/dev/null")

	err := WriteAuditRecord(AuditRecord{
		RunID:     "run-dev-null",
		Status:    "skipped",
		Timestamp: time.Now().UTC(),
	}, path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-regular audit log target")
}

func TestCheckAuditLogWritable_RejectsUnixNonRegularTargets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	cases := []struct {
		name string
		path string
	}{
		{name: "character-device", path: requireNonRegularAuditPath(t, "/dev/null")},
	}

	fifoPath := filepath.Join(dir, "audit.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err == nil {
		readEnd, openErr := os.OpenFile(fifoPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		require.NoError(t, openErr)
		defer readEnd.Close()
		cases = append(cases, struct {
			name string
			path string
		}{name: "fifo", path: fifoPath})
	} else {
		t.Logf("skipping FIFO target case: %v", err)
	}

	socketDir := shortSocketTempDir(t)
	socketPath := filepath.Join(socketDir, "audit.sock")
	if listener, err := net.Listen("unix", socketPath); err == nil {
		defer listener.Close()
		cases = append(cases, struct {
			name string
			path string
		}{name: "unix-socket", path: socketPath})
	} else {
		t.Logf("skipping Unix socket target case: %v", err)
	}

	nullLink := filepath.Join(dir, "null-link")
	if err := os.Symlink("/dev/null", nullLink); err == nil {
		cases = append(cases, struct {
			name string
			path string
		}{name: "symlink-to-character-device", path: nullLink})
	} else {
		t.Logf("skipping symlink-to-device target case: %v", err)
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := CheckAuditLogWritable(tc.path)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "non-regular audit log target")
		})
	}
}

func TestCheckAuditLogWritable_RejectsDanglingSymlink(t *testing.T) {
	t.Parallel()
	link := filepath.Join(t.TempDir(), "dangling-audit.jsonl")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-audit.jsonl"), link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	err := CheckAuditLogWritable(link)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve audit log symlink")
}

func requireNonRegularAuditPath(t *testing.T, path string) string {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("%s unavailable: %v", path, err)
	}
	if info.Mode().IsRegular() {
		t.Skipf("%s is a regular file in this environment", path)
	}
	return path
}

func shortSocketTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "cudly-audit-sock-")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(dir))
	})
	return dir
}
