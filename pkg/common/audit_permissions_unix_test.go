//go:build unix

package common

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckAuditLogWritable_CreatesMissingAuditLogReadableByDownstreamTools(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), "audit.jsonl")

	require.NoError(t, CheckAuditLogWritable(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestCheckAuditLogWritable_DoesNotChmodExistingAuditLog(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("line1\n"), 0o600))

	require.NoError(t, CheckAuditLogWritable(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "line1\n", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestCheckAuditLogWritable_RejectsWriteOnlyAuditLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file read permission checks")
	}

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("line1\n"), 0o200))

	err := CheckAuditLogWritable(path)

	require.Error(t, err)
	assert.ErrorContains(t, err, "not readable and writable")
}
