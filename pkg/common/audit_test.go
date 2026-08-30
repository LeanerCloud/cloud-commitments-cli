package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAuditRecord_Append(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	r1 := AuditRecord{RunID: "run-1", Status: "success", Timestamp: time.Now().UTC()}
	r2 := AuditRecord{RunID: "run-2", Status: "error", Timestamp: time.Now().UTC()}

	require.NoError(t, WriteAuditRecord(r1, path))
	require.NoError(t, WriteAuditRecord(r2, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	assert.Len(t, lines, 2)

	var parsed AuditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &parsed))
	assert.Equal(t, "run-1", parsed.RunID)

	require.NoError(t, json.Unmarshal([]byte(lines[1]), &parsed))
	assert.Equal(t, "run-2", parsed.RunID)
}

func TestWriteAuditRecord_EmptyRunID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	err := WriteAuditRecord(AuditRecord{Status: "success"}, path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "RunID")
}

func TestWriteAuditRecord_NonwritablePath(t *testing.T) {
	t.Parallel()
	err := WriteAuditRecord(AuditRecord{RunID: "x"}, "/nonexistent/dir/audit.jsonl")
	assert.Error(t, err)
}

func TestAuditRecord_JSONRoundtrip(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"foo":"bar"}`)
	ts := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	record := AuditRecord{
		RunID:             "run-abc",
		Provider:          ProviderAWS,
		AccountID:         "123456789",
		AccountName:       "prod",
		Region:            "us-east-1",
		Service:           "ec2",
		ResourceType:      "m5.large",
		CommitmentType:    CommitmentReservedInstance,
		Term:              12,
		Count:             5,
		EstimatedCost:     1000.0,
		EstimatedSavings:  300.0,
		CommitmentID:      "ri-abc123",
		Status:            "success",
		ErrorMessage:      "",
		Timestamp:         ts,
		DryRun:            false,
		RawRecommendation: raw,
	}

	data, err := json.Marshal(record)
	require.NoError(t, err)

	var parsed AuditRecord
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, record.RunID, parsed.RunID)
	assert.Equal(t, record.Provider, parsed.Provider)
	assert.Equal(t, record.Term, parsed.Term)
	assert.Equal(t, record.Timestamp.UTC(), parsed.Timestamp.UTC())
	assert.Equal(t, string(raw), string(parsed.RawRecommendation))

	// Timestamp must serialize as RFC3339 string (not unix timestamp)
	var raw2 map[string]any
	require.NoError(t, json.Unmarshal(data, &raw2))
	tsStr, ok := raw2["timestamp"].(string)
	require.True(t, ok, "timestamp must be a string")
	_, err = time.Parse(time.RFC3339, tsStr)
	assert.NoError(t, err, "timestamp must be RFC3339")
}

func TestRawRecommendation_Omitempty(t *testing.T) {
	t.Parallel()
	record := AuditRecord{RunID: "run-x", Status: "skipped"}
	data, err := json.Marshal(record)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "raw_recommendation")
	assert.NotContains(t, string(data), "credential_scope")
}

func TestAuditRecord_CredentialScopeJSON(t *testing.T) {
	t.Parallel()
	record := AuditRecord{RunID: "run-x", CredentialScope: "profile-a"}
	data, err := json.Marshal(record)
	require.NoError(t, err)

	var parsed AuditRecord
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "profile-a", parsed.CredentialScope)
	assert.Contains(t, string(data), `"credential_scope":"profile-a"`)
}

func TestNewAuditRecord_Fields(t *testing.T) {
	t.Parallel()
	rec := Recommendation{
		Provider:         ProviderAWS,
		Account:          "111",
		AccountName:      "staging",
		Region:           "eu-west-1",
		Service:          ServiceEC2,
		ResourceType:     "c5.xlarge",
		CommitmentType:   CommitmentReservedInstance,
		Term:             "1yr",
		Count:            3,
		CommitmentCost:   500.0,
		EstimatedSavings: 150.0,
	}
	result := PurchaseResult{CommitmentID: "ri-xyz", Success: true}

	ar := NewAuditRecord("run-001", rec, result, "success", false, PurchaseSourceCLI)

	assert.Equal(t, "run-001", ar.RunID)
	assert.Equal(t, ProviderAWS, ar.Provider)
	assert.Equal(t, "111", ar.AccountID)
	assert.Equal(t, "staging", ar.AccountName)
	assert.Empty(t, ar.CredentialScope)
	assert.Equal(t, "eu-west-1", ar.Region)
	assert.Equal(t, 12, ar.Term)
	assert.Equal(t, 3, ar.Count)
	assert.Equal(t, 500.0, ar.EstimatedCost)
	assert.Equal(t, 150.0, ar.EstimatedSavings)
	assert.Equal(t, "ri-xyz", ar.CommitmentID)
	assert.Equal(t, "success", ar.Status)
	assert.Equal(t, false, ar.DryRun)
	assert.WithinDuration(t, time.Now().UTC(), ar.Timestamp, 5*time.Second)
}

func TestCheckAuditLogWritable_WritablePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	require.NoError(t, CheckAuditLogWritable(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

func TestCheckAuditLogWritable_AllowsSymlinkToRegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "audit.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("existing\n"), 0o600))
	link := filepath.Join(dir, "audit-link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	require.NoError(t, CheckAuditLogWritable(link))
}

func TestCheckAuditLogWritable_RejectsNonRegularTargets(t *testing.T) {
	t.Parallel()

	err := CheckAuditLogWritable(t.TempDir())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-regular audit log target")
}

// The unwritable path is a child of a regular file rather than a 0555
// directory: a root process ignores the permission bits, so a chmod-based
// case would pass vacuously in root-based CI.
func TestCheckAuditLogWritable_UnwritablePath(t *testing.T) {
	t.Parallel()
	notADir := filepath.Join(t.TempDir(), "regular-file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0600))

	path := filepath.Join(notADir, "audit.jsonl")
	err := CheckAuditLogWritable(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), path)
}

func TestCheckAuditLogWritable_DoesNotTruncateExistingContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("line1\n"), 0644))

	require.NoError(t, CheckAuditLogWritable(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "line1\n", string(data))
}

func TestTermMonths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input    string
		expected int
	}{
		{"1yr", 12},
		{"3yr", 36},
		{"", 0},
		{"unknown", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, termMonths(tc.input))
		})
	}
}
