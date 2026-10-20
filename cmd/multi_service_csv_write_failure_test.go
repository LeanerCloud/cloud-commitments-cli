package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test loadRecommendationsFromCSV with file errors.
func TestLoadRecommendationsFromCSV_FileErrors(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		errContains string
	}{
		{
			name: "Non-existent file",
			setup: func(t *testing.T) string {
				return "/nonexistent/path/to/file.csv"
			},
			errContains: "failed to open CSV file",
		},
		{
			name: "Directory instead of file",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			errContains: "",
		},
		{
			name: "Empty file (no header)",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				csvPath := filepath.Join(tmpDir, "empty.csv")
				err := os.WriteFile(csvPath, []byte(""), 0644)
				require.NoError(t, err)
				return csvPath
			},
			errContains: "failed to read CSV header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			_, err := loadRecommendationsFromCSV(path)
			assert.Error(t, err)
			if tt.errContains != "" {
				assert.Contains(t, err.Error(), tt.errContains)
			}
		})
	}
}

// failingWriter rejects every write, standing in for a full disk or a
// network mount that fails when the buffered CSV is flushed.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// Issue #2004: csv.Writer buffers everything, so the bytes only reach the
// file at Flush. A Flush failure must surface instead of a nil return.
func TestWriteMultiServiceCSV_FlushErrorIsReturned(t *testing.T) {
	results := []common.PurchaseResult{{
		Recommendation: common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 1},
		Success:        true,
		Timestamp:      time.Now(),
	}}
	err := writeMultiServiceCSV(failingWriter{}, results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
}

// Issue #2004: a run with no purchase results writes no file, so it must
// not claim "CSV report written to".
func TestWriteReportAndSummary_NoResultsDoesNotClaimCSVWritten(t *testing.T) {
	var logs strings.Builder
	previous := AppLogger.Writer()
	AppLogger.SetOutput(&logs)
	t.Cleanup(func() { AppLogger.SetOutput(previous) })

	path := filepath.Join(t.TempDir(), "report.csv")
	writeReportAndSummary(nil, nil, true, Config{CSVOutput: path}, nil)

	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	assert.NotContains(t, logs.String(), "CSV report written to")
	assert.Contains(t, logs.String(), "CSV report not written")
}

// Issue #2004: a failed write must not be reported as written either.
func TestWriteCSVReport_FailureDoesNotClaimCSVWritten(t *testing.T) {
	var logs strings.Builder
	previous := AppLogger.Writer()
	AppLogger.SetOutput(&logs)
	t.Cleanup(func() { AppLogger.SetOutput(previous) })

	results := []common.PurchaseResult{{
		Recommendation: common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 1},
		Success:        true,
		Timestamp:      time.Now(),
	}}
	writeCSVReport(results, filepath.Join(t.TempDir(), "missing-dir", "report.csv"))
	assert.NotContains(t, logs.String(), "CSV report written to")

	logs.Reset()
	path := filepath.Join(t.TempDir(), "report.csv")
	writeCSVReport(results, path)
	assert.Contains(t, logs.String(), "CSV report written to: "+path)
}
