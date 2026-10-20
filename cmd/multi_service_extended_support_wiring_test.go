package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The main pipeline wiring (runToolMultiService) is exercised in a re-exec'd
// child: the child runs the real command with the real engine-version fetcher
// against this process's stub endpoint, and exits through log.Fatalf.
func TestRunToolMultiService_ExtendedSupportExclusionWiring(t *testing.T) {
	if os.Getenv("CUDLY_EXCL_CHILD") == "1" {
		engineVersionFetcher = fetchEngineVersionData
		rootCmd.SetArgs(strings.Fields(os.Getenv("CUDLY_EXCL_ARGS")))
		require.NoError(t, rootCmd.Execute())
		return
	}

	const denyHint = "Cannot apply extended-support exclusion"
	tests := []struct {
		name      string
		setup     func(*rdsAPIStub)
		args      string
		wantExit1 bool
		wantRDS   bool // exclusion queries expected
	}{
		{"purchase run, engine lifecycle denied", func(s *rdsAPIStub) { s.failEngines["mysql"] = true }, "--purchase", true, true},
		{"dry run, engine lifecycle denied", func(s *rdsAPIStub) { s.failEngines["mysql"] = true }, "", true, true},
		{"dry run, rec in a region whose inventory is denied", func(s *rdsAPIStub) { s.failRegions["us-east-1"] = true }, "", true, true},
		{"opt-in skips every exclusion query", func(s *rdsAPIStub) { s.failEngines["mysql"], s.failRegions["us-east-1"] = true, true }, "--include-extended-support", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newRDSStub("us-east-1")
			tt.setup(stub)
			stub.serveEnv(t)
			dir := t.TempDir()
			args := "--services rds --term 1 --coverage 100 --regions us-east-1 --output " + filepath.Join(dir, "out.csv") +
				" --audit-log " + filepath.Join(dir, "audit.jsonl") + " " + tt.args
			child := exec.Command(os.Args[0], "-test.run=^TestRunToolMultiService_ExtendedSupportExclusionWiring$")
			child.Env = append(os.Environ(), "CUDLY_EXCL_CHILD=1", "CUDLY_EXCL_ARGS="+args, "AWS_REGION=us-east-1")
			out, err := child.CombinedOutput()

			assert.Positive(t, stub.total(), "child must have reached the stub:\n%s", out)
			assert.Zero(t, stub.count("Purchase"), "no purchase call may be made")
			if tt.wantExit1 {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, "output:\n%s", out)
				assert.Equal(t, 1, exitErr.ExitCode())
				assert.Contains(t, string(out), denyHint)
				assert.Contains(t, string(out), "--include-extended-support")
				return
			}
			require.NoError(t, err, "output:\n%s", out)
			assert.Zero(t, stub.count("DescribeDBInstances"))
			assert.Zero(t, stub.count("DescribeDBMajorEngineVersions"))
		})
	}
}
