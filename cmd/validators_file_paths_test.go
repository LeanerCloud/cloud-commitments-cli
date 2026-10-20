package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFilePaths(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	tests := []struct {
		setupFunc func() func()
		name      string
		errMsg    string
		wantErr   bool
	}{
		{
			name: "valid CSV output path",
			setupFunc: func() func() {
				toolCfg.CSVOutput = filepath.Join(tmpDir, "output.csv")
				toolCfg.CSVInput = ""
				return func() {}
			},
			wantErr: false,
		},
		{
			name: "valid CSV input path",
			setupFunc: func() func() {
				// Create a test CSV file
				inputPath := filepath.Join(tmpDir, "input.csv")
				if err := os.WriteFile(inputPath, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVInput = inputPath
				toolCfg.CSVOutput = ""
				return func() {
					os.Remove(inputPath)
				}
			},
			wantErr: false,
		},
		{
			name: "CSV output directory does not exist",
			setupFunc: func() func() {
				toolCfg.CSVOutput = "/nonexistent/directory/output.csv"
				toolCfg.CSVInput = ""
				return func() {}
			},
			wantErr: true,
			errMsg:  "output directory does not exist",
		},
		{
			name: "CSV output file already exists",
			setupFunc: func() func() {
				p := filepath.Join(tmpDir, "existing.csv")
				if err := os.WriteFile(p, []byte("keep"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVOutput = p
				return func() { os.Remove(p) }
			},
			wantErr: true,
			errMsg:  "output path already exists",
		},
		{
			name: "CSV output path is an existing directory",
			setupFunc: func() func() {
				toolCfg.CSVOutput = tmpDir
				return func() {}
			},
			wantErr: true,
			errMsg:  "output path already exists",
		},
		{
			name: "CSV output is a dangling symlink",
			setupFunc: func() func() {
				p := filepath.Join(tmpDir, "dangling.csv")
				if err := os.Symlink(filepath.Join(tmpDir, "missing-target"), p); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				toolCfg.CSVOutput = p
				return func() { os.Remove(p) }
			},
			wantErr: true,
			errMsg:  "output path already exists",
		},
		{
			name: "CSV input equals output",
			setupFunc: func() func() {
				p := filepath.Join(tmpDir, "same.csv")
				if err := os.WriteFile(p, []byte("keep"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVOutput = p
				toolCfg.CSVInput = p
				return func() { os.Remove(p) }
			},
			wantErr: true,
			errMsg:  "output path already exists",
		},
		{
			name: "CSV input file does not exist",
			setupFunc: func() func() {
				toolCfg.CSVInput = filepath.Join(tmpDir, "nonexistent.csv")
				toolCfg.CSVOutput = ""
				return func() {}
			},
			wantErr: true,
			errMsg:  "input CSV file does not exist",
		},
		{
			name: "CSV input file wrong extension",
			setupFunc: func() func() {
				// Create a test file with wrong extension
				inputPath := filepath.Join(tmpDir, "input.txt")
				if err := os.WriteFile(inputPath, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				toolCfg.CSVInput = inputPath
				toolCfg.CSVOutput = ""
				return func() {
					os.Remove(inputPath)
				}
			},
			wantErr: true,
			errMsg:  "input file must have .csv extension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			t.Cleanup(saveGlobalVars().restore)
			toolCfg = Config{}
			cleanup := tt.setupFunc()
			defer cleanup()

			// Execute
			err := validateFilePaths()

			// Verify
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateFilePaths() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateFilePaths() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateFilePaths() unexpected error = %v", err)
				}
			}
		})
	}
}
