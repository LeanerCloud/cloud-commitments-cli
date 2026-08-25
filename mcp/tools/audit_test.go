package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

// auditTestRecommendation mirrors testRecommendation but uses a 1yr term so
// tests here can assert the raw AuditRecord.Term (months) conversion
// independently of the purchase_test.go fixtures, which use "3yr".
func auditTestRecommendation() common.Recommendation {
	rec := testRecommendation()
	rec.Term = "1yr"
	return rec
}

// readAuditLines reads path and splits it into non-empty JSONL lines.
func readAuditLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-controlled temp path
	require.NoError(t, err)
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestAuditLogPathDefaultWhenUnset proves an unset EnvAuditLog resolves to
// the default path under XDG_STATE_HOME, not to "disabled". Not parallel:
// t.Setenv forbids it.
func TestAuditLogPathDefaultWhenUnset(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	prev, had := os.LookupEnv(EnvAuditLog)
	require.NoError(t, os.Unsetenv(EnvAuditLog))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(EnvAuditLog, prev)
		}
	})

	path, enabled, err := AuditLogPath()
	require.NoError(t, err)
	assert.True(t, enabled, "an unset variable must select the default path, not disable auditing")
	assert.Equal(t, filepath.Join(stateDir, "cudly", "mcp-audit.jsonl"), path)
}

// TestAuditLogEmptyValueDisables proves EnvAuditLog="" is the explicit
// operator opt-out: AuditLogPath reports disabled, and a full real purchase
// through ExecutePurchase writes no file anywhere under a temp
// XDG_STATE_HOME. Not parallel: t.Setenv forbids it.
func TestAuditLogEmptyValueDisables(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv(EnvAuditLog, "")

	_, enabled, err := AuditLogPath()
	require.NoError(t, err)
	assert.False(t, enabled)

	fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-disabled"}}
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
		CredentialScope: "test-scope",
		ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "disabled auditing must not create anything under the state dir")
}

// TestAuditLogWhitespaceValueDisables covers the typo case: a value that is
// only whitespace disables auditing rather than resolving to a file whose
// name is a space.
func TestAuditLogWhitespaceValueDisables(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(EnvAuditLog, "   ")

	path, enabled, err := AuditLogPath()
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Empty(t, path)
}

// TestAuditLogPathIsTrimmed proves surrounding whitespace in the override is
// stripped rather than becoming part of the resolved filename.
func TestAuditLogPathIsTrimmed(t *testing.T) {
	want := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, "  "+want+"  ")

	path, enabled, err := AuditLogPath()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, want, path)
}

// TestAuditLogOverrideWins proves an explicit EnvAuditLog path wins over
// XDG_STATE_HOME. Not parallel: t.Setenv forbids it.
func TestAuditLogOverrideWins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	custom := filepath.Join(t.TempDir(), "custom-audit.jsonl")
	t.Setenv(EnvAuditLog, custom)

	path, enabled, err := AuditLogPath()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, custom, path)
}

// TestPreviewWritesSkippedRecord proves a preview purchase (dry_run=true) is
// recorded with status "skipped", dry_run=true, the cudly-mcp source, a
// non-empty run_id, and the term converted to months. Not parallel:
// t.Setenv forbids it.
func TestPreviewWritesSkippedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	lines := readAuditLines(t, path)
	require.Len(t, lines, 1, "a preview must write exactly one audit line")

	var record common.AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	assert.Equal(t, "skipped", record.Status)
	assert.True(t, record.DryRun)
	assert.Equal(t, common.PurchaseSourceMCP, record.Source)
	assert.NotEmpty(t, record.RunID)
	assert.Equal(t, 12, record.Term, `a "1yr" term must convert to 12 months`)
}

// TestSuccessfulPurchaseWritesSuccessRecord proves a completed real purchase
// is recorded with status "success" and the provider's CommitmentID. Not
// parallel: t.Setenv forbids it.
func TestSuccessfulPurchaseWritesSuccessRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-success-1"}}
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
		CredentialScope: "test-scope",
		ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	lines := readAuditLines(t, path)
	require.Len(t, lines, 1)
	var record common.AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	assert.Equal(t, "success", record.Status)
	assert.False(t, record.DryRun)
	assert.Equal(t, "ri-success-1", record.CommitmentID)
}

// TestProviderErrorWritesErrorRecord proves a provider-side purchase failure
// (Go error from PurchaseCommitment) is recorded with status "error" and a
// non-empty error message, and that ExecutePurchase still returns its error
// to the caller unchanged. Not parallel: t.Setenv forbids it.
func TestProviderErrorWritesErrorRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	fake := &fakeServiceClient{purchaseErr: errors.New("AWS API: InsufficientInstanceCapacity")}
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
		CredentialScope: "test-scope",
		ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.Error(t, err, "the audit write must not swallow or change the provider error")
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "InsufficientInstanceCapacity")

	lines := readAuditLines(t, path)
	require.Len(t, lines, 1)
	var record common.AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	assert.Equal(t, "error", record.Status)
	assert.NotEmpty(t, record.ErrorMessage)
}

// TestProviderReportedFailureMapsToErrorStatus proves a PurchaseResult with
// Success=false (no Go error) is recorded as "error", never "success". Not
// parallel: t.Setenv forbids it.
func TestProviderReportedFailureMapsToErrorStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: false}}
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
		CredentialScope: "test-scope",
		ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.NoError(t, err, "a provider-reported failure surfaces via the response, not a Go error")
	require.NotNil(t, resp)
	assert.False(t, resp.Success)

	lines := readAuditLines(t, path)
	require.Len(t, lines, 1)
	var record common.AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	assert.Equal(t, "error", record.Status, "Success=false must never be recorded as success")
}

// TestTwoPurchasesShareOneRunID proves every purchase in one process
// correlates through the same auditRunID. Not parallel: t.Setenv forbids it.
func TestTwoPurchasesShareOneRunID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditLog, path)

	for i := 0; i < 2; i++ {
		fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-run"}}
		_, err := ExecutePurchase(context.Background(), PurchaseRequest{
			Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
			CredentialScope: "test-scope",
			ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
		})
		require.NoError(t, err)
	}

	lines := readAuditLines(t, path)
	require.Len(t, lines, 2)

	var first, second common.AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.NotEmpty(t, first.RunID)
	assert.Equal(t, first.RunID, second.RunID)
}

// TestUnwritablePathWarnsAndDoesNotChangeResult proves that when the audit
// log cannot be written, recordPurchaseAudit warns on stderr naming the path
// and error, and ExecutePurchase still returns its normal, unmodified
// result. The path's parent is a regular file (not a directory), so
// os.MkdirAll deterministically fails regardless of process privileges (a
// chmod-based unwritable directory is ineffective when running as root, and
// CI may run as root). Not parallel: t.Setenv forbids it, and this test
// swaps the shared standard-logger output.
func TestUnwritablePathWarnsAndDoesNotChangeResult(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte("x"), 0o600))
	badPath := filepath.Join(blockingFile, "mcp-audit.jsonl")
	t.Setenv(EnvAuditLog, badPath)

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	fake := &fakeServiceClient{purchaseResult: common.PurchaseResult{Success: true, CommitmentID: "ri-unwritable"}}
	resp, err := ExecutePurchase(context.Background(), PurchaseRequest{
		Region: "us-east-1", Recommendation: auditTestRecommendation(), DryRun: false, Confirm: true,
		CredentialScope: "test-scope",
		ResolveClient:   func(_ context.Context) (provider.ServiceClient, error) { return fake, nil },
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Equal(t, "ri-unwritable", resp.CommitmentID)

	out := buf.String()
	assert.Contains(t, out, "mcp audit log")
	assert.Contains(t, out, badPath)
}
