//go:build linux || darwin

package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

var (
	errAuditDirectorySync  = errors.New("directory sync failed")
	errAuditDirectoryClose = errors.New("directory close failed")
)

func TestProductionAuditDirectoryOpsRejectsUnrepresentableDescriptor(t *testing.T) {
	t.Parallel()
	parent := &tracedAuditDirectory{fd: uintptr(math.MaxInt) + 1}
	ops := productionAuditDirectoryOps()
	tests := []struct {
		name string
		run  func() (auditDirectoryHandle, error)
	}{
		{
			name: "mkdirat",
			run: func() (auditDirectoryHandle, error) {
				return nil, ops.mkdirAt(parent, "", 0o700)
			},
		},
		{
			name: "openat",
			run: func() (auditDirectoryHandle, error) {
				return ops.openAt(parent, "")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle, err := test.run()
			if handle != nil {
				require.NoError(t, handle.Close())
				t.Fatal("operation returned an unexpected directory handle")
			}
			require.EqualError(t, err, fmt.Sprintf(
				"audit directory file descriptor %d exceeds int range",
				parent.fd,
			))
		})
	}
}

func TestEnsureAuditLogDirectorySyncsEveryPathEdge(t *testing.T) {
	t.Parallel()
	trace := &auditDirectoryTrace{}

	require.NoError(t, ensureAuditLogDirectoryWithOps("/one/two/three/audit.jsonl", trace.ops()))

	assert.Equal(t, []string{
		"open:/", "mkdir:one", "open:one", "sync:/", "close:/",
		"mkdir:two", "open:two", "sync:one", "close:one",
		"mkdir:three", "open:three", "sync:two", "close:two", "close:three",
	}, trace.operations)
}

func TestEnsureAuditLogDirectoryJoinsSyncAndCloseErrors(t *testing.T) {
	t.Parallel()
	trace := &auditDirectoryTrace{
		syncErrors:  map[string]error{"/": errAuditDirectorySync},
		closeErrors: map[string]error{"/": errAuditDirectoryClose},
	}

	err := ensureAuditLogDirectoryWithOps("/one/audit.jsonl", trace.ops())

	require.ErrorIs(t, err, errAuditDirectorySync)
	require.ErrorIs(t, err, errAuditDirectoryClose)
	assert.Equal(t, []string{"open:/", "mkdir:one", "open:one", "sync:/", "close:/", "close:one"}, trace.operations)
}

func TestEnsureAuditLogDirectoryRetriesExistingHierarchyAfterSyncFailure(t *testing.T) {
	t.Parallel()
	first := &auditDirectoryTrace{syncErrors: map[string]error{"/": errAuditDirectorySync}}
	require.ErrorIs(t, ensureAuditLogDirectoryWithOps("/one/audit.jsonl", first.ops()), errAuditDirectorySync)

	second := &auditDirectoryTrace{mkdirErrors: map[string]error{"one": fs.ErrExist}}
	require.NoError(t, ensureAuditLogDirectoryWithOps("/one/audit.jsonl", second.ops()))
	assert.Contains(t, second.operations, "sync:/")
}

func TestEnsureAuditLogDirectoryRejectsExistingNonDirectory(t *testing.T) {
	t.Parallel()
	blocking := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocking, []byte("x"), 0o600))

	err := ensureAuditLogDirectory(filepath.Join(blocking, "audit.jsonl"))

	require.Error(t, err)
	assert.ErrorContains(t, err, blocking)
}

func TestEnsureAuditLogDirectorySupportsConcurrentCreators(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "one", "two", "three", "audit.jsonl")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- ensureAuditLogDirectory(path)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestEnsureAuditLogDirectoryCreatesPrivateNestedHierarchy(t *testing.T) {
	root := t.TempDir()
	components := []string{
		filepath.Join(root, "one"),
		filepath.Join(root, "one", "two"),
		filepath.Join(root, "one", "two", "three"),
	}
	previousUmask := unix.Umask(0)
	t.Cleanup(func() { unix.Umask(previousUmask) })

	require.NoError(t, ensureAuditLogDirectory(filepath.Join(components[2], "audit.jsonl")))

	for _, component := range components {
		info, err := os.Stat(component)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), component)
	}
}

func TestEnsureAuditLogDirectoryPreservesExistingMode(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "audit-parent")
	require.NoError(t, os.Mkdir(parent, 0o750))
	require.NoError(t, os.Chmod(parent, 0o750))

	require.NoError(t, ensureAuditLogDirectory(filepath.Join(parent, "audit.jsonl")))

	info, err := os.Stat(parent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func TestEnsureAuditLogWritableCreatesNestedHierarchy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one", "two", "three", "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	require.NoError(t, EnsureAuditLogWritable())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

func TestEnsureAuditLogDirectoryRejectsUnreadableWalkAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory read permission checks")
	}

	ancestor := filepath.Join(t.TempDir(), "ancestor")
	require.NoError(t, os.Mkdir(ancestor, 0o700))
	require.NoError(t, os.Chmod(ancestor, 0o300))
	t.Cleanup(func() { require.NoError(t, os.Chmod(ancestor, 0o700)) })

	err := ensureAuditLogDirectory(filepath.Join(ancestor, "child", "audit.jsonl"))

	require.Error(t, err)
	assert.ErrorContains(t, err, ancestor)
	assert.ErrorContains(t, err, "durability")
}

func TestEnsureAuditLogWritableRejectsUnreadableWalkAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory read permission checks")
	}

	ancestor, path := auditPathBehindUnreadableAncestor(t)
	t.Setenv(EnvAuditLog, path)

	err := EnsureAuditLogWritable()

	require.Error(t, err)
	assert.ErrorContains(t, err, ancestor)
	assert.ErrorContains(t, err, "durability")
}

func TestPurchaseAuditRejectsUnreadableWalkAncestorWithoutChangingResult(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory read permission checks")
	}

	ancestor, path := auditPathBehindUnreadableAncestor(t)
	t.Setenv(EnvAuditLog, path)

	resp, output := executeSuccessfulPurchaseWithCapturedAuditWarning(t)

	assert.True(t, resp.Success)
	assert.Equal(t, "ri-unwritable", resp.CommitmentID)
	assert.Contains(t, output, "mcp audit log")
	assert.Contains(t, output, path)
	assert.Contains(t, output, ancestor)
	assert.Contains(t, output, "durability")
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func auditPathBehindUnreadableAncestor(t *testing.T) (string, string) {
	t.Helper()
	ancestor := filepath.Join(t.TempDir(), "ancestor")
	immediateParent := filepath.Join(ancestor, "immediate")
	require.NoError(t, os.MkdirAll(immediateParent, 0o700))
	require.NoError(t, os.Chmod(ancestor, 0o300))
	t.Cleanup(func() { require.NoError(t, os.Chmod(ancestor, 0o700)) })
	return ancestor, filepath.Join(immediateParent, "audit.jsonl")
}

type auditDirectoryTrace struct {
	operations  []string
	mkdirErrors map[string]error
	syncErrors  map[string]error
	closeErrors map[string]error
}

func (t *auditDirectoryTrace) ops() auditDirectoryOps {
	return auditDirectoryOps{
		openRoot: func() (auditDirectoryHandle, error) {
			t.operations = append(t.operations, "open:/")
			return &tracedAuditDirectory{name: "/", trace: t}, nil
		},
		mkdirAt: func(_ auditDirectoryHandle, name string, _ uint32) error {
			t.operations = append(t.operations, "mkdir:"+name)
			return t.mkdirErrors[name]
		},
		openAt: func(_ auditDirectoryHandle, name string) (auditDirectoryHandle, error) {
			t.operations = append(t.operations, "open:"+name)
			return &tracedAuditDirectory{name: name, trace: t}, nil
		},
	}
}

type tracedAuditDirectory struct {
	name  string
	trace *auditDirectoryTrace
	fd    uintptr
}

func (d *tracedAuditDirectory) Fd() uintptr { return d.fd }

func (d *tracedAuditDirectory) Sync() error {
	d.trace.operations = append(d.trace.operations, "sync:"+d.name)
	return d.trace.syncErrors[d.name]
}

func (d *tracedAuditDirectory) Close() error {
	d.trace.operations = append(d.trace.operations, "close:"+d.name)
	return d.trace.closeErrors[d.name]
}
