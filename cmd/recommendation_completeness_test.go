package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRecommendationCompletenessCommand(t *testing.T) {
	if os.Getenv("CUDLY_COMPLETENESS_CHILD") == "1" {
		runCompletenessCommandChild(t)
		return
	}
	for _, regionMode := range []string{"explicit", "default", "fallback"} {
		for _, details := range []string{"valid", "mixed", "all-invalid", "empty", "api-error"} {
			t.Run(regionMode+"/"+details, func(t *testing.T) {
				runCompletenessScenario(t, "rds", regionMode, details)
			})
		}
	}
	for _, service := range []string{"savingsplans-compute", "savingsplans-ec2instance", "savingsplans-sagemaker", "savingsplans-database", "savingsplans"} {
		for _, details := range []string{"valid", "mixed", "all-invalid", "empty", "api-error", "failed-type", "late-page"} {
			t.Run(service+"/"+details, func(t *testing.T) {
				runCompletenessScenario(t, service, "default", details)
			})
		}
	}
}

func runCompletenessScenario(t *testing.T, service, regionMode, details string) {
	t.Helper()
	dir := t.TempDir()
	fixture, proxyURL, caPath := newCompletenessProxy(t, dir, regionMode, details, service)
	output := filepath.Join(dir, "recommendations.csv")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecommendationCompletenessCommand$", "-test.v")
	child.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir,
		"CUDLY_COMPLETENESS_CHILD=1", "CUDLY_COMPLETENESS_OUTPUT=" + output,
		"CUDLY_COMPLETENESS_AUDIT=" + filepath.Join(dir, "audit.jsonl"),
		"CUDLY_COMPLETENESS_REGIONS=" + regionMode,
		"CUDLY_COMPLETENESS_SERVICE=" + service,
		"AWS_ACCESS_KEY_ID=synthetic", "AWS_SECRET_ACCESS_KEY=synthetic", "AWS_REGION=us-east-1",
		"AWS_EC2_METADATA_DISABLED=true", "AWS_MAX_ATTEMPTS=1",
		"AWS_CONFIG_FILE=" + filepath.Join(dir, "absent-config"),
		"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(dir, "absent-credentials"),
		"HTTPS_PROXY=" + proxyURL, "HTTP_PROXY=" + proxyURL, "AWS_CA_BUNDLE=" + caPath,
	}
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	require.NoError(t, child.Run(), "stdout: %s\nstderr: %s", &stdout, &stderr)
	fixture.assertRequests(t)
	logs := stdout.String() + stderr.String()
	for _, failure := range []string{"Could not check", "Failed to query", "request send failed", "certificate", "TLS handshake"} {
		require.NotContains(t, logs, failure, "unexpected ancillary error")
	}
	if service == "rds" {
		assertCompletenessDiagnostics(t, regionMode, details, stdout.String(), stderr.String())
		assertCompletenessCSV(t, output, details, logs)
	} else {
		assertSPCompleteness(t, output, service, details, logs)
	}
}

func runCompletenessCommandChild(t *testing.T) {
	t.Helper()
	ca, err := os.ReadFile(os.Getenv("AWS_CA_BUNDLE"))
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca), "invalid fixture CA")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(transport.CloseIdleConnections)
	args := []string{"--services", os.Getenv("CUDLY_COMPLETENESS_SERVICE"), "--term", "1", "--include-extended-support", "--coverage", "100",
		"--output", os.Getenv("CUDLY_COMPLETENESS_OUTPUT"), "--audit-log", os.Getenv("CUDLY_COMPLETENESS_AUDIT")}
	if os.Getenv("CUDLY_COMPLETENESS_REGIONS") == "explicit" {
		args = append(args, "--regions", "us-east-1")
	}
	rootCmd.SetArgs(args)
	require.NoError(t, rootCmd.Execute())
}

func assertSPCompleteness(t *testing.T, output, service, details, logs string) {
	t.Helper()
	types := completenessSPTypes(service)
	warnings, failedDetails, failedScopes := 0, 0, 0
	survivors := append([]string(nil), types...)
	switch details {
	case "mixed", "all-invalid":
		warnings, failedDetails = len(types), 1
	case "late-page":
		warnings, failedScopes = len(types), 1
	case "failed-type":
		if service == "savingsplans" {
			survivors = types[:3]
		} else {
			survivors = nil
		}
	}
	if details == "empty" || details == "all-invalid" || details == "api-error" {
		survivors = nil
	}
	require.Equal(t, warnings, strings.Count(logs, "incomplete AWS recommendations:"), logs)
	if warnings > 0 {
		want := fmt.Sprintf("incomplete AWS recommendations: %d failed details, %d failed scopes:", failedDetails, failedScopes)
		require.Equal(t, warnings, strings.Count(logs, want), logs)
		if failedDetails > 0 {
			require.Contains(t, logs, "not-a-number")
		}
	}
	failures := 0
	switch details {
	case "api-error":
		failures = len(types)
	case "failed-type":
		failures = 1
	}
	require.Equal(t, failures, strings.Count(logs, "Failed to fetch recommendations:"), logs)
	if failures > 0 || details == "late-page" {
		require.Contains(t, logs, "fixture denied recommendations")
	}
	require.NotContains(t, logs, "Region discovery incomplete")
	data, err := os.ReadFile(output)
	if len(survivors) == 0 {
		require.ErrorIs(t, err, os.ErrNotExist, "unexpected CSV: %s\n%s", data, logs)
		require.NotContains(t, logs, "CSV report written")
		return
	}
	require.NoError(t, err, logs)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, len(survivors)+2, logs)
	require.Equal(t, "TOTAL", rows[len(rows)-1][0])
	sort.Slice(survivors, func(i, j int) bool {
		return completenessSPService(survivors[i]) < completenessSPService(survivors[j])
	})
	for i, plan := range survivors {
		columns := make(map[string]string)
		for j, header := range rows[0] {
			columns[header] = rows[i+1][j]
		}
		for field, want := range map[string]string{"Service": completenessSPService(plan), "Region": "", "ResourceType": "", "Count": "1", "Term": "1yr", "PaymentOption": "no-upfront", "UpfrontPayment": "3.00", "RecurringMonthlyCost": "1460.00", "EstimatedSavings": "10.00", "Success": "true", "Error": ""} {
			require.Equal(t, want, columns[field], "CSV %s; row=%v", field, rows[i+1])
		}
	}
	require.Contains(t, logs, "CSV report written")
}

func assertCompletenessDiagnostics(t *testing.T, regionMode, details, stdout, stderr string) {
	t.Helper()
	malformed := details == "mixed" || details == "all-invalid"
	if malformed {
		count := 1
		if regionMode == "fallback" {
			count = 6
		}
		want := fmt.Sprintf("incomplete AWS recommendations: %d failed details, 0 failed scopes:", count)
		for _, diagnostic := range []string{want, "not-a-number", "detail"} {
			require.Contains(t, stdout, diagnostic, "stderr: %s", stderr)
		}
		if regionMode == "fallback" {
			require.Contains(t, stdout, "Region discovery incomplete")
		}
	} else {
		require.NotContains(t, stdout, "incomplete AWS recommendations")
	}
	if details == "api-error" {
		require.Contains(t, stdout+stderr, "fixture denied recommendations")
	} else {
		require.NotContains(t, stdout+stderr, "Failed to fetch recommendations", "partial collection treated as fatal")
		require.NotContains(t, stdout+stderr, "failed to discover regions", "partial collection treated as fatal")
	}
}

func assertCompletenessCSV(t *testing.T, output, details, logs string) {
	t.Helper()
	data, err := os.ReadFile(output)
	if details != "valid" && details != "mixed" {
		require.ErrorIs(t, err, os.ErrNotExist, "unexpected CSV: %s\n%s", data, logs)
		require.NotContains(t, logs, "CSV report written", "false CSV success for %s", details)
		return
	}
	require.NoError(t, err, "CSV: %s", logs)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 3, "want header, exact surviving RDS row and total: %s", logs)
	require.Equal(t, "TOTAL", rows[2][0])
	columns := make(map[string]string)
	for i, header := range rows[0] {
		columns[header] = rows[1][i]
	}
	for field, want := range map[string]string{"Service": "rds", "Region": "us-east-1", "ResourceType": "db.t3.medium", "Count": "2"} {
		require.Equal(t, want, columns[field], "CSV %s; row=%v", field, rows[1])
	}
	require.Contains(t, logs, "CSV report written")
}

func TestRecommendationCompletenessHelpers(t *testing.T) {
	partial := &recommendations.IncompleteRecommendationsError{
		FailedDetails: 2, FailedScopes: 1,
		Causes: []error{errors.New("rds block 0 detail 1: invalid quantity"), errors.New("rds block 1 detail 0: invalid cost"), errors.New("rds 3yr AllUpfront: denied")},
	}
	survivors := []common.Recommendation{{Service: common.ServiceRDS, Region: "us-east-1", Count: 2}}
	for _, tc := range []struct {
		name string
		err  error
		recs []common.Recommendation
		keep bool
	}{
		{"partial", fmt.Errorf("wrapped: %w", partial), survivors, true},
		{"all-invalid", partial, nil, true},
		{"ordinary", errors.New("API failed"), survivors, false},
		{"canceled", context.Canceled, survivors, false},
		{"deadline", context.DeadlineExceeded, survivors, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := AppLogger.Writer()
			AppLogger.SetOutput(&output)
			t.Cleanup(func() { AppLogger.SetOutput(previous) })
			ctx := context.Background()
			client := &MockRecommendationsClient{}
			client.On("GetRecommendationsForService", ctx, common.ServiceRDS).Return(tc.recs, tc.err)
			client.On("GetRecommendations", ctx, mock.Anything).Return(tc.recs, tc.err)
			regions, err := discoverRegionsForService(ctx, client, common.ServiceRDS)
			got := fetchRecommendationsForRegion(ctx, client, common.ServiceRDS, "us-east-1", Config{TermYears: 1})
			if !tc.keep {
				require.ErrorIs(t, err, tc.err)
				require.Nil(t, regions)
				require.Nil(t, got)
				require.NotContains(t, output.String(), "incomplete AWS recommendations")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.recs, got)
				require.Len(t, regions, len(tc.recs))
				require.Equal(t, 2, strings.Count(output.String(), "2 failed details, 1 failed scopes"))
				for _, cause := range partial.Causes {
					require.Equal(t, 2, strings.Count(output.String(), cause.Error()))
				}
			}
			client.AssertExpectations(t)
		})
	}
}
