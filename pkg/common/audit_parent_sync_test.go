//go:build linux || darwin

package common

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

var (
	errAuditParentBind         = errors.New("bind second audit parent failed")
	errAuditTestParentSyncOne  = errors.New("audit test first parent sync failed")
	errAuditTestParentSyncTwo  = errors.New("audit test second parent sync failed")
	errAuditTestParentCloseOne = errors.New("audit test first parent close failed")
	errAuditTestParentCloseTwo = errors.New("audit test second parent close failed")
)

const auditParentFIFOPathEnv = "CUDLY_TEST_AUDIT_PARENT_FIFO_PATH"

func TestAuditFileDescriptorRejectsUnrepresentableValue(t *testing.T) {
	t.Parallel()
	fd := uintptr(math.MaxInt) + 1

	_, err := auditFileDescriptor(fd)

	require.EqualError(t, err, fmt.Sprintf("audit file descriptor %d exceeds int range", fd))
}

func TestAuditOSFileSyncAndCloseContinueAfterParentErrors(t *testing.T) {
	t.Parallel()
	raw, err := os.CreateTemp(t.TempDir(), "audit.jsonl")
	require.NoError(t, err)
	first := &trackingAuditParentHandle{
		syncErr:  errAuditTestParentSyncOne,
		closeErr: errAuditTestParentCloseOne,
	}
	second := &trackingAuditParentHandle{
		syncErr:  errAuditTestParentSyncTwo,
		closeErr: errAuditTestParentCloseTwo,
	}
	file := &auditOSFile{
		File: raw,
		parents: []auditParentDir{
			{path: "first", handle: first},
			{path: "second", handle: second},
		},
	}

	syncErr := file.syncParents()
	require.ErrorIs(t, syncErr, errAuditTestParentSyncOne)
	require.ErrorIs(t, syncErr, errAuditTestParentSyncTwo)
	assert.Equal(t, 1, first.syncCalls)
	assert.Equal(t, 1, second.syncCalls)

	closeErr := file.Close()
	require.ErrorIs(t, closeErr, errAuditTestParentCloseOne)
	require.ErrorIs(t, closeErr, errAuditTestParentCloseTwo)
	assert.Equal(t, 1, first.closeCalls)
	assert.Equal(t, 1, second.closeCalls)
	_, statErr := raw.Stat()
	assert.True(t, errors.Is(statErr, os.ErrClosed), statErr)
}

func TestOpenAuditLogForAppendClosesPartialBindingAndAuditDescriptor(t *testing.T) {
	t.Parallel()
	configuredParent := t.TempDir()
	path := filepath.Join(configuredParent, "audit.jsonl")
	parentInfo, err := os.Stat(configuredParent)
	require.NoError(t, err)
	first := &trackingAuditParentDescriptor{
		trackingAuditParentHandle: trackingAuditParentHandle{closeErr: errAuditTestParentCloseOne},
		info:                      parentInfo,
	}
	second := &trackingAuditParentDescriptor{
		trackingAuditParentHandle: trackingAuditParentHandle{closeErr: errAuditTestParentCloseTwo},
		info:                      parentInfo,
	}
	openCalls := 0
	entryCalls := 0
	identity := auditFileIdentity{device: 1, inode: 2}
	ops := auditParentOps{
		abs:          func(string) (string, error) { return path, nil },
		evalSymlinks: func(string) (string, error) { return filepath.Join(t.TempDir(), "audit.jsonl"), nil },
		openDirectory: func(string) (auditParentDescriptor, error) {
			openCalls++
			if openCalls == 1 {
				return first, nil
			}
			return second, nil
		},
		fileIdentity: func(*os.File) (auditFileIdentity, error) { return identity, nil },
		entryIdentityAt: func(auditParentDescriptor, string) (auditFileIdentity, error) {
			entryCalls++
			if entryCalls == 2 {
				return auditFileIdentity{}, errAuditParentBind
			}
			return identity, nil
		},
	}

	var opened *os.File
	_, err = openAuditLogForAppendWithBinder(
		path,
		0o600,
		func(path string, file *os.File) ([]auditParentDir, error) {
			opened = file
			return bindAuditLogParentsWithOps(path, file, ops)
		},
	)

	require.ErrorIs(t, err, errAuditParentBind)
	require.ErrorIs(t, err, errAuditTestParentCloseOne)
	require.ErrorIs(t, err, errAuditTestParentCloseTwo)
	assert.Equal(t, 1, first.closeCalls)
	assert.Equal(t, 1, second.closeCalls)
	require.NotNil(t, opened)
	_, statErr := opened.Stat()
	assert.True(t, errors.Is(statErr, os.ErrClosed), statErr)
}

func TestParentSyncFailureSuppressesWorkAndJoinsTransactionLifecycle(t *testing.T) {
	t.Parallel()
	f := &failingParentAuditLogFile{
		fakeAuditLogFile: &fakeAuditLogFile{
			unlockErr: errAuditTestUnlock,
			closeErr:  errAuditTestClose,
		},
		parentSyncErr:  errAuditTestParentSyncOne,
		parentCloseErr: errAuditTestParentCloseOne,
	}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	for _, want := range []error{
		errAuditTestParentSyncOne,
		errAuditTestUnlock,
		errAuditTestParentCloseOne,
		errAuditTestClose,
	} {
		require.ErrorIs(t, err, want)
	}
	assert.Equal(t, []string{
		"lock",
		"sync-parent-1",
		"unlock",
		"close-parent-1",
		"close",
	}, f.operations)
	assert.Empty(t, f.writes)
}

func TestProbeAuditLogWritableSyncsParentsBeforeUnlock(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{}

	require.NoError(t, probeAuditLogWritable("audit.jsonl", f))

	assert.Equal(t, []string{"lock", "sync-parents", "unlock", "close"}, f.operations)
}

func TestOpenAuditLogForAppendDeduplicatesSymlinkedParentAlias(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	targetParent := filepath.Join(root, "target")
	aliasParent := filepath.Join(root, "alias")
	require.NoError(t, os.Mkdir(targetParent, 0o700))
	require.NoError(t, os.Symlink(targetParent, aliasParent))
	path := filepath.Join(aliasParent, "audit.jsonl")

	f, err := openAuditLogForAppend(path, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	require.NoError(t, f.syncParents())
	osFile, ok := f.(*auditOSFile)
	require.True(t, ok)
	assert.Len(t, osFile.parents, 1)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

func TestOpenAuditLogForAppendBindsBothFinalSymlinkParents(t *testing.T) {
	t.Parallel()
	configuredParent := t.TempDir()
	targetParent := t.TempDir()
	target := filepath.Join(targetParent, "audit.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("existing\n"), 0o600))
	configured := filepath.Join(configuredParent, "audit.jsonl")
	require.NoError(t, os.Symlink(target, configured))

	f, err := openAuditLogForAppend(configured, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	require.NoError(t, f.syncParents())
	osFile, ok := f.(*auditOSFile)
	require.True(t, ok)
	assert.Len(t, osFile.parents, 2)
}

func TestOpenAuditLogForAppendRejectsRetargetedFinalSymlinkAndClosesAuditFile(t *testing.T) {
	t.Parallel()
	configuredParent := t.TempDir()
	targetParent := t.TempDir()
	targetA := filepath.Join(targetParent, "audit-a.jsonl")
	targetB := filepath.Join(targetParent, "audit-b.jsonl")
	require.NoError(t, os.WriteFile(targetA, []byte("a\n"), 0o600))
	require.NoError(t, os.WriteFile(targetB, []byte("b\n"), 0o600))
	configured := filepath.Join(configuredParent, "audit.jsonl")
	require.NoError(t, os.Symlink(targetA, configured))

	var opened *os.File
	_, err := openAuditLogForAppendWithBinder(
		configured,
		0o600,
		func(path string, file *os.File) ([]auditParentDir, error) {
			opened = file
			require.NoError(t, os.Remove(configured))
			require.NoError(t, os.Symlink(targetB, configured))
			return bindAuditLogParents(path, file)
		},
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "changed while binding")
	require.NotNil(t, opened)
	_, statErr := opened.Stat()
	require.Error(t, statErr)
	assert.True(t, errors.Is(statErr, os.ErrClosed), statErr)
}

func TestRetainedAuditParentSurvivesConfiguredParentReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configuredParent := filepath.Join(root, "current")
	require.NoError(t, os.Mkdir(configuredParent, 0o700))
	path := filepath.Join(configuredParent, "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("existing\n"), 0o600))

	f, err := openAuditLogForAppend(path, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })

	movedParent := filepath.Join(root, "moved")
	require.NoError(t, os.Rename(configuredParent, movedParent))
	require.NoError(t, os.Symlink(filepath.Join(root, "missing"), configuredParent))

	require.NoError(t, f.syncParents())
}

func TestOpenAuditParentDirectoryRejectsFIFONonblocking(t *testing.T) {
	if fifo := os.Getenv(auditParentFIFOPathEnv); fifo != "" {
		descriptor, err := openAuditParentDirectory(fifo)
		if descriptor != nil {
			require.NoError(t, descriptor.Close())
		}
		require.Error(t, err)
		return
	}

	fifo := filepath.Join(t.TempDir(), "parent-fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOpenAuditParentDirectoryRejectsFIFONonblocking$")
	cmd.Env = append(os.Environ(), auditParentFIFOPathEnv+"="+fifo)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("opening an audit parent FIFO blocked instead of rejecting a non-directory: %v", ctx.Err())
	}
	require.NoError(t, err, string(output))
}

type failingParentAuditLogFile struct {
	*fakeAuditLogFile
	parentSyncErr  error
	parentCloseErr error
}

func (f *failingParentAuditLogFile) syncParents() error {
	f.operations = append(f.operations, "sync-parent-1")
	return f.parentSyncErr
}

func (f *failingParentAuditLogFile) Close() error {
	f.operations = append(f.operations, "close-parent-1")
	return errors.Join(f.parentCloseErr, f.fakeAuditLogFile.Close())
}

type trackingAuditParentHandle struct {
	syncErr    error
	closeErr   error
	syncCalls  int
	closeCalls int
}

func (h *trackingAuditParentHandle) Sync() error {
	h.syncCalls++
	return h.syncErr
}

func (h *trackingAuditParentHandle) Close() error {
	h.closeCalls++
	return h.closeErr
}

type trackingAuditParentDescriptor struct {
	trackingAuditParentHandle
	info fs.FileInfo
}

func (d *trackingAuditParentDescriptor) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *trackingAuditParentDescriptor) Fd() uintptr                { return 0 }
