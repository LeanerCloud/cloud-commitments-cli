package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setConfirmationStdin(t *testing.T, input string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString(input)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	previous := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = previous
		assert.NoError(t, reader.Close())
	})
}

func assertNoPurchaseOutput(t *testing.T, cfg Config) {
	t.Helper()
	_, err := os.Stat(cfg.CSVOutput)
	require.True(t, os.IsNotExist(err), "refused purchase must not produce a report: %v", err)
	data, err := os.ReadFile(cfg.AuditLog)
	if err != nil {
		require.True(t, os.IsNotExist(err), "unexpected audit read error: %v", err)
	}
	assert.Empty(t, data, "refused purchase must not produce purchase audit records")
}

func TestRunPurchaseAndReportRequiresTerminal(t *testing.T) {
	setConfirmationStdin(t, "yes\n")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected purchase request", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != server.Listener.Addr().String() {
			return nil, fmt.Errorf("nonlocal provider address rejected: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	t.Cleanup(transport.CloseIdleConnections)
	awsCfg := aws.Config{
		Region: "us-east-1", BaseEndpoint: aws.String(server.URL), HTTPClient: &http.Client{Transport: transport},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		}),
	}
	for _, service := range getAllServices() {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dryRun=%t", service, dryRun), func(t *testing.T) {
				dir := t.TempDir()
				cfg := Config{AuditLog: filepath.Join(dir, "audit.jsonl"), CSVOutput: filepath.Join(dir, "report.csv")}
				recs := []common.Recommendation{
					{Service: service, Region: "us-east-1", ResourceType: "test-instance", Count: 2, EstimatedSavings: 50},
					{Service: service, Region: "us-west-2", ResourceType: "test-instance", Count: 3, EstimatedSavings: 75},
				}
				output := captureAppOutput(t, func() {
					runPurchaseAndReport(context.Background(), awsCfg, scorer.ScoredResult{Passed: recs}, dryRun, cfg, &common.DropSummary{})
				})
				assert.Zero(t, requests.Load(), "confirmation refusal and dry runs must never reach a purchase provider")
				if dryRun {
					assert.NotContains(t, output, "Purchase canceled")
					_, rows := readPurchaseReport(t, cfg.CSVOutput)
					assert.Len(t, rows, 2)
					data, err := os.ReadFile(cfg.AuditLog)
					require.NoError(t, err)
					assert.Contains(t, string(data), `"dry_run":true`)
				} else {
					assert.Contains(t, output, "Purchase canceled")
					assertNoPurchaseOutput(t, cfg)
				}
			})
		}
	}
}

func TestRunToolFromCSVRequiresTerminal(t *testing.T) {
	mode := os.Getenv("CUDLY_CONFIRMATION_TEST_MODE")
	if mode == "" {
		for _, childMode := range []string{"purchase", "dry-run"} {
			t.Run(childMode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunToolFromCSVRequiresTerminal$", "-test.v")
				child.Env = []string{"CUDLY_CONFIRMATION_TEST_MODE=" + childMode}
				output, err := child.CombinedOutput()
				require.NoError(t, err, "%s", output)
			})
		}
		return
	}
	actualPurchase := mode == "purchase"
	setConfirmationStdin(t, "yes\n")
	isolateAWSEnv(t)
	var purchases atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || r.URL.IsAbs() {
			t.Errorf("nonlocal provider request rejected: %s %s", r.Method, r.URL)
			http.Error(w, "nonlocal request denied", http.StatusForbidden)
			return
		}
		if !assert.NoError(t, r.ParseForm()) {
			return
		}
		action := r.Form.Get("Action")
		body := ""
		switch action {
		case "DescribeRegions":
			_, _ = fmt.Fprint(w, `<DescribeRegionsResponse><regionInfo><item><regionName>us-east-1</regionName></item></regionInfo></DescribeRegionsResponse>`)
			return
		case "DescribeDBInstances":
			body = `<DBInstances/>`
		case "DescribeDBMajorEngineVersions":
			body = `<DBMajorEngineVersions/>`
		case "DescribeReservedDBInstances":
			body = `<ReservedDBInstances/>`
		default:
			purchases.Add(1)
			t.Errorf("unexpected provider action %q", action)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `<%sResponse><%sResult>%s</%sResult></%sResponse>`, action, action, body, action, action)
	}))
	t.Cleanup(server.Close)
	// A fresh process prevents Go's cached proxy settings from preceding this fixture.
	t.Setenv("HTTP_PROXY", server.URL)
	t.Setenv("HTTPS_PROXY", server.URL)
	t.Setenv("NO_PROXY", "")
	for _, key := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_RDS", "AWS_ENDPOINT_URL_EC2"} {
		t.Setenv(key, server.URL)
	}
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")
	cfg := toolCfg
	cfg.ActualPurchase = actualPurchase
	cfg.Coverage = 100
	cfg.IncludeExtendedSupport = true
	cfg.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.CSVOutput = filepath.Join(t.TempDir(), "report.csv")
	cfg.CSVInput = writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption,EstimatedSavings
rds,us-east-1,db.t3.small,postgres,single-az,2,1yr,all-upfront,50
rds,us-west-2,db.t3.small,postgres,single-az,3,1yr,all-upfront,75
`)
	output := captureAppOutput(t, func() {
		require.NoError(t, runToolFromCSV(context.Background(), cfg))
	})
	assert.Zero(t, purchases.Load())
	if actualPurchase {
		assert.Contains(t, output, "Purchase canceled")
		assertNoPurchaseOutput(t, cfg)
	} else {
		assert.NotContains(t, output, "Purchase canceled")
		_, rows := readPurchaseReport(t, cfg.CSVOutput)
		assert.Len(t, rows, 2)
		data, err := os.ReadFile(cfg.AuditLog)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"dry_run":true`)
	}
}
