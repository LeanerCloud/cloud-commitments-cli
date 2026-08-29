package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errAuditTestWrite          = errors.New("audit test write failed")
	errAuditTestSeparatorWrite = errors.New("audit test separator write failed")
	errAuditTestSync           = errors.New("audit test sync failed")
	errAuditTestClose          = errors.New("audit test close failed")
	errAuditTestLock           = errors.New("audit test lock failed")
	errAuditTestUnlock         = errors.New("audit test unlock failed")
)

func TestAppendJSONLReportsWriteErrorAndCloses(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{writeResults: []auditWriteResult{{n: 0, err: errAuditTestWrite}}}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestWrite)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
	assert.Equal(t, [][]byte{[]byte(`{"ok":true}` + "\n")}, f.writes)
}

func TestAppendJSONLLockFailureClosesWithoutWrites(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{lockErr: errAuditTestLock, closeErr: errAuditTestClose}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestLock)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 1, f.lockCalls)
	assert.Equal(t, 0, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 0, f.unlockCalls)
	assert.Equal(t, 1, f.closeCalls)
	assert.Equal(t, []string{"lock", "close"}, f.operations)
}

func TestAppendJSONLNormalOperationOrder(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{}

	require.NoError(t, appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`)))

	assert.Equal(t, []string{"lock", "write", "sync", "unlock", "close"}, f.operations)
}

func TestAppendJSONLReportsShortWriteAndCloses(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)
	f := &fakeAuditLogFile{writeResults: []auditWriteResult{{n: len(payload)}, {n: 1}}}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, 2, f.writeCalls)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
	assert.Equal(t, [][]byte{append(append([]byte(nil), payload...), '\n'), {'\n'}}, f.writes)
}

func TestAppendJSONLTerminatesPartialWriteBeforeNextAppend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		writeErr error
	}{
		{name: "write error", writeErr: errAuditTestWrite},
		{name: "short write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "audit.jsonl")
			first := []byte(`{"first":true}`)
			second := []byte(`{"second":true}`)
			fragmentLen := 5

			file, err := openAuditLogForAppend(path, 0o644)
			require.NoError(t, err)
			partial := &partialWriteAuditLogFile{
				auditLogFile: file,
				writeN:       fragmentLen,
				writeErr:     tt.writeErr,
			}

			err = appendJSONLFile(path, partial, first)
			assert.ErrorContains(t, err, "write audit record to "+path)
			if tt.writeErr != nil {
				require.ErrorIs(t, err, tt.writeErr)
			} else {
				require.ErrorIs(t, err, io.ErrShortWrite)
			}

			require.NoError(t, appendJSONL(path, second, 0o644))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			expected := append(append(append([]byte(nil), first[:fragmentLen]...), '\n'), second...)
			expected = append(expected, '\n')
			require.Equal(t, expected, data)

			lines := bytes.Split(data, []byte{'\n'})
			require.Len(t, lines, 3)
			var decoded map[string]bool
			require.NoError(t, json.Unmarshal(lines[1], &decoded))
			assert.True(t, decoded["second"])
		})
	}
}

func TestAppendJSONLReportsSyncErrorAndCloses(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{syncErr: errAuditTestSync}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestSync)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestAppendJSONLJoinsSyncAndCloseErrors(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{syncErr: errAuditTestSync, closeErr: errAuditTestClose}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestSync)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestAppendJSONLJoinsWriteAndCloseErrors(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{
		writeResults: []auditWriteResult{{n: 0, err: errAuditTestWrite}},
		closeErr:     errAuditTestClose,
	}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestWrite)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestAppendJSONLJoinsShortWriteAndCloseErrors(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)
	f := &fakeAuditLogFile{
		writeResults: []auditWriteResult{{n: len(payload)}, {n: 1}},
		closeErr:     errAuditTestClose,
	}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, io.ErrShortWrite)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 2, f.writeCalls)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestAppendJSONLJoinsPartialWriteAndSeparatorErrors(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)

	tests := []struct {
		name         string
		separatorErr error
		wantErr      error
	}{
		{name: "write error", separatorErr: errAuditTestSeparatorWrite, wantErr: errAuditTestSeparatorWrite},
		{name: "short write", wantErr: io.ErrShortWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeAuditLogFile{writeResults: []auditWriteResult{
				{n: len(payload), err: errAuditTestWrite},
				{n: 0, err: tt.separatorErr},
			}}

			err := appendJSONLFile("audit.jsonl", f, payload)

			require.ErrorIs(t, err, errAuditTestWrite)
			require.ErrorIs(t, err, tt.wantErr)
			assert.ErrorContains(t, err, "write audit record separator to audit.jsonl")
			assert.Equal(t, 2, f.writeCalls)
			assert.Equal(t, [][]byte{append(append([]byte(nil), payload...), '\n'), {'\n'}}, f.writes)
			assert.Equal(t, 0, f.syncCalls)
			assert.Equal(t, 1, f.closeCalls)
		})
	}
}

func TestAppendJSONLJoinsPartialWriteRepairSyncAndCloseErrors(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)
	f := &fakeAuditLogFile{
		writeResults: []auditWriteResult{
			{n: len(payload), err: errAuditTestWrite},
			{n: 1},
		},
		syncErr:  errAuditTestSync,
		closeErr: errAuditTestClose,
	}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, errAuditTestWrite)
	require.ErrorIs(t, err, errAuditTestSync)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.ErrorContains(t, err, "sync audit record separator to audit.jsonl")
	assert.Equal(t, 2, f.writeCalls)
	assert.Equal(t, [][]byte{append(append([]byte(nil), payload...), '\n'), {'\n'}}, f.writes)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestAppendJSONLJoinsPartialRepairUnlockAndCloseErrors(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)
	f := &fakeAuditLogFile{
		writeResults: []auditWriteResult{
			{n: len(payload), err: errAuditTestWrite},
			{n: 1},
		},
		syncErr:   errAuditTestSync,
		unlockErr: errAuditTestUnlock,
		closeErr:  errAuditTestClose,
	}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, errAuditTestWrite)
	require.ErrorIs(t, err, errAuditTestSync)
	require.ErrorIs(t, err, errAuditTestUnlock)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.ErrorContains(t, err, "sync audit record separator to audit.jsonl")
	assert.Equal(t, []string{"lock", "write", "write", "sync", "unlock", "close"}, f.operations)
}

func TestAppendJSONLUnlockFailureClosesLast(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{unlockErr: errAuditTestUnlock, closeErr: errAuditTestClose}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestUnlock)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, []string{"lock", "write", "sync", "unlock", "close"}, f.operations)
}

func TestAppendJSONLReturnsCloseOnlyError(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{closeErr: errAuditTestClose}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 1, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
}

func TestProbeAuditLogWritableUsesLockUnlockClose(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{}

	require.NoError(t, probeAuditLogWritable("audit.jsonl", f))

	assert.Equal(t, []string{"lock", "unlock", "close"}, f.operations)
}

func TestProbeAuditLogWritableJoinsLockAndCloseErrorsWithoutWrites(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{lockErr: errAuditTestLock, closeErr: errAuditTestClose}

	err := probeAuditLogWritable("audit.jsonl", f)

	require.ErrorIs(t, err, errAuditTestLock)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, []string{"lock", "close"}, f.operations)
	assert.Equal(t, 0, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
}

func TestProbeAuditLogWritableJoinsUnlockAndCloseErrors(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{unlockErr: errAuditTestUnlock, closeErr: errAuditTestClose}

	err := probeAuditLogWritable("audit.jsonl", f)

	require.ErrorIs(t, err, errAuditTestUnlock)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, []string{"lock", "unlock", "close"}, f.operations)
}

type fakeAuditLogFile struct {
	writeResults []auditWriteResult
	lockErr      error
	syncErr      error
	unlockErr    error
	closeErr     error

	lockCalls   int
	writeCalls  int
	syncCalls   int
	unlockCalls int
	closeCalls  int
	writes      [][]byte
	operations  []string
}

type auditWriteResult struct {
	n   int
	err error
}

type partialWriteAuditLogFile struct {
	auditLogFile
	writeN   int
	writeErr error
	wrote    bool
}

func (f *partialWriteAuditLogFile) Write(p []byte) (int, error) {
	if f.wrote {
		return f.auditLogFile.Write(p)
	}
	f.wrote = true
	n, err := f.auditLogFile.Write(p[:f.writeN])
	if err != nil {
		return n, err
	}
	return n, f.writeErr
}

func (f *fakeAuditLogFile) Lock() error {
	f.lockCalls++
	f.operations = append(f.operations, "lock")
	return f.lockErr
}

func (f *fakeAuditLogFile) Write(p []byte) (int, error) {
	f.writeCalls++
	f.operations = append(f.operations, "write")
	f.writes = append(f.writes, append([]byte(nil), p...))
	if f.writeCalls <= len(f.writeResults) {
		result := f.writeResults[f.writeCalls-1]
		return result.n, result.err
	}
	return len(p), nil
}

func (f *fakeAuditLogFile) Sync() error {
	f.syncCalls++
	f.operations = append(f.operations, "sync")
	return f.syncErr
}

func (f *fakeAuditLogFile) Unlock() error {
	f.unlockCalls++
	f.operations = append(f.operations, "unlock")
	return f.unlockErr
}

func (f *fakeAuditLogFile) Close() error {
	f.closeCalls++
	f.operations = append(f.operations, "close")
	return f.closeErr
}
