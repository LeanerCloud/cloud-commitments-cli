package common

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errAuditTestWrite = errors.New("audit test write failed")
	errAuditTestSync  = errors.New("audit test sync failed")
	errAuditTestClose = errors.New("audit test close failed")
)

func TestAppendJSONLReportsWriteErrorAndCloses(t *testing.T) {
	t.Parallel()
	f := &fakeAuditLogFile{writeN: 0, writeErr: errAuditTestWrite}

	err := appendJSONLFile("audit.jsonl", f, []byte(`{"ok":true}`))

	require.ErrorIs(t, err, errAuditTestWrite)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
	assert.Equal(t, []byte(`{"ok":true}`+"\n"), f.wrote)
}

func TestAppendJSONLReportsShortWriteAndCloses(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"ok":true}`)
	f := &fakeAuditLogFile{writeN: len(payload)}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
	assert.Equal(t, []byte(`{"ok":true}`+"\n"), f.wrote)
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
	f := &fakeAuditLogFile{writeN: 0, writeErr: errAuditTestWrite, closeErr: errAuditTestClose}

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
	f := &fakeAuditLogFile{writeN: len(payload), closeErr: errAuditTestClose}

	err := appendJSONLFile("audit.jsonl", f, payload)

	require.ErrorIs(t, err, io.ErrShortWrite)
	require.ErrorIs(t, err, errAuditTestClose)
	assert.Equal(t, 1, f.writeCalls)
	assert.Equal(t, 0, f.syncCalls)
	assert.Equal(t, 1, f.closeCalls)
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

type fakeAuditLogFile struct {
	writeN   int
	writeErr error
	syncErr  error
	closeErr error

	writeCalls int
	syncCalls  int
	closeCalls int
	wrote      []byte
}

func (f *fakeAuditLogFile) Write(p []byte) (int, error) {
	f.writeCalls++
	f.wrote = append([]byte(nil), p...)
	if f.writeErr != nil || f.writeN != 0 {
		return f.writeN, f.writeErr
	}
	return len(p), nil
}

func (f *fakeAuditLogFile) Sync() error {
	f.syncCalls++
	return f.syncErr
}

func (f *fakeAuditLogFile) Close() error {
	f.closeCalls++
	return f.closeErr
}
