package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// hookTransport is an offline AWS RoundTripper whose responses and side
// effects are chosen by the test. The library clones an *http.Client and keeps
// its Transport, so it is wrapped in one.
type hookTransport struct {
	mu       sync.Mutex
	paths    []string
	handle   func(r *http.Request) (status int, body string)
	handleMu sync.Mutex
}

func (h *hookTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.paths = append(h.paths, r.URL.Path)
	h.mu.Unlock()
	h.handleMu.Lock()
	status, body := h.handle(r)
	h.handleMu.Unlock()
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (h *hookTransport) count(suffix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

func hookedAWSConfig(h *hookTransport) aws.Config {
	return aws.Config{
		HTTPClient: &http.Client{Transport: h},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, nil
		}),
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}
}

func captureAppLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := AppLogger.Writer()
	AppLogger.SetOutput(&buf)
	t.Cleanup(func() { AppLogger.SetOutput(orig) })
	return &buf
}

func slowDelay(t *testing.T) {
	t.Helper()
	t.Setenv("DISABLE_PURCHASE_DELAY", "")
	orig := purchaseDelay
	purchaseDelay = time.Hour
	t.Cleanup(func() { purchaseDelay = orig })
}

func offeringSP(id string, commitment float64) common.Recommendation {
	rec := parserShapedSP()
	rec.Details = &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: commitment, OfferingID: id}
	return rec
}

func auditRows(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var rows []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			rows = append(rows, l)
		}
	}
	return rows
}

func TestExecuteAndReport_CancelDuringPurchaseFinishesInFlightAndReportsWhatRan(t *testing.T) {
	slowDelay(t)
	out := captureAppLog(t)
	dir := t.TempDir()
	cfg := Config{AuditLog: filepath.Join(dir, "audit.jsonl"), CSVOutput: filepath.Join(dir, "report.csv")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ctxErrAtPurchase error
	h := &hookTransport{}
	h.handle = func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CreateSavingsPlan"):
			cancel() // Ctrl-C arrives while the purchase request is in flight
			ctxErrAtPurchase = r.Context().Err()
			return http.StatusOK, `{"savingsPlanId":"sp-111"}`
		case strings.HasSuffix(r.URL.Path, "/DescribeSavingsPlans"):
			return http.StatusOK, `{"savingsPlans":[]}`
		}
		return http.StatusBadRequest, `{"__type":"ValidationException","message":"unexpected call"}`
	}
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2), offeringSP("o-3", 3)}

	executeAndReport(ctx, hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.NoError(t, ctxErrAtPurchase, "the in-flight purchase must not see the parent cancellation")
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"), "no purchase may start after the interrupt")
	rows := auditRows(t, cfg.AuditLog)
	require.Len(t, rows, 1, "the completed purchase is audited")
	assert.Contains(t, rows[0], `"status":"success"`)
	assert.Contains(t, rows[0], "sp-111")

	f, err := os.Open(cfg.CSVOutput)
	require.NoError(t, err, "the report is still written")
	defer f.Close()
	csvRows, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.Len(t, csvRows, 3, "header + the one purchase + TOTAL")
	assert.Contains(t, out.String(), "Run interrupted: 1 of 3 recommendation(s) attempted")
}

func TestExecuteAndReport_ErroredPurchaseOnInterruptSaysVerifyInAWS(t *testing.T) {
	slowDelay(t)
	out := captureAppLog(t)
	dir := t.TempDir()
	cfg := Config{AuditLog: filepath.Join(dir, "audit.jsonl"), CSVOutput: filepath.Join(dir, "report.csv")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hookTransport{}
	h.handle = func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/CreateSavingsPlan") {
			cancel()
			return http.StatusInternalServerError, `{"__type":"InternalServerException","message":"boom"}`
		}
		return http.StatusOK, `{"savingsPlans":[]}`
	}

	executeAndReport(ctx, hookedAWSConfig(h), []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}, false, "run-1", cfg, nil)

	assert.Contains(t, out.String(), "verify in AWS before re-running")
	rows := auditRows(t, cfg.AuditLog)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0], `"status":"error"`, "audit status stays as it is today")
}

func TestProcessPurchaseLoop_AuditRecordWrittenBeforeDelay(t *testing.T) {
	slowDelay(t)
	captureAppLog(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recs := []common.Recommendation{validEC2Rec(), validEC2Rec()}
	recs[1].ResourceType = "m5.xlarge"
	mockClient := &MockServiceClient{}
	mockClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-1"}, nil)

	var rowsAtWait []string
	seen := false
	orig := waitBetweenPurchases
	waitBetweenPurchases = func(ctx context.Context) {
		if !seen {
			seen = true
			rowsAtWait = auditRows(t, cfg.AuditLog)
		}
		cancel() // Ctrl-C during the delay
	}
	t.Cleanup(func() { waitBetweenPurchases = orig })

	results := processPurchaseLoop(ctx, recs, "us-east-1", false, mockClient, cfg, "run-1")

	require.Len(t, rowsAtWait, 1, "the purchase that just completed must already be audited when the delay starts")
	require.Len(t, results, 1, "the second recommendation is not attempted after the interrupt")
	mockClient.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
}

func TestExecutePurchasePipeline_AuditRecordWrittenBeforeDelay(t *testing.T) {
	slowDelay(t)
	captureAppLog(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hookTransport{handle: func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/CreateSavingsPlan") {
			return http.StatusOK, `{"savingsPlanId":"sp-1"}`
		}
		return http.StatusOK, `{"savingsPlans":[]}`
	}}
	var rowsAtWait []string
	seen := false
	orig := waitBetweenPurchases
	waitBetweenPurchases = func(context.Context) {
		if !seen {
			seen = true
			rowsAtWait = auditRows(t, cfg.AuditLog)
		}
		cancel()
	}
	t.Cleanup(func() { waitBetweenPurchases = orig })

	executePurchasePipeline(ctx, hookedAWSConfig(h), []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}, false, "run-1", cfg)

	require.Len(t, rowsAtWait, 1)
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"))
}

func TestWaitBetweenPurchases_ReturnsPromptlyOnCancel(t *testing.T) {
	slowDelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	waitBetweenPurchases(ctx)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRegisterShutdownSignalHandler_CancelSetsFlagAndPrintsNoticeOnce(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := registerShutdownSignalHandler(ctx)
	require.False(t, shutdownRequested.Load())

	cancel()
	require.Eventually(t, shutdownRequested.Load, 2*time.Second, 5*time.Millisecond)
	cleanup()
	shutdownRequested.Store(false)
	assert.Equal(t, 1, strings.Count(buf.String(), "stopping after the in-flight purchase"))
	assert.Contains(t, buf.String(), `Ctrl-\ force-quits`)
}

// A second Ctrl-C must be absorbed, not kill the process mid-purchase. If the
// context's stop func were called on the first signal, the default SIGINT
// handler would return and the second signal below would terminate the test
// binary.
func TestInvocationContext_FurtherSigintsAreAbsorbed(t *testing.T) {
	ctx, stop := invocationContext()
	defer stop()

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not cancel the invocation context")
	}
	time.Sleep(200 * time.Millisecond) // let any early stop() take effect
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	time.Sleep(200 * time.Millisecond) // the process is still here to run this line
	assert.Error(t, ctx.Err())
}

func TestProcessPurchaseLoop_CancelDuringPurchaseDoesNotCancelTheInFlightCall(t *testing.T) {
	slowDelay(t)
	captureAppLog(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recs := []common.Recommendation{validEC2Rec(), validEC2Rec()}
	recs[1].ResourceType = "m5.xlarge"
	var ctxErrAtPurchase error
	mockClient := &MockServiceClient{}
	mockClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			cancel() // Ctrl-C while the purchase call is in flight
			ctxErrAtPurchase = args.Get(0).(context.Context).Err()
		}).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-1"}, nil)

	results := processPurchaseLoop(ctx, recs, "us-east-1", false, mockClient, cfg, "run-1")

	require.NoError(t, ctxErrAtPurchase)
	require.Len(t, results, 1)
	assert.True(t, results[0].Success)
	assert.Len(t, auditRows(t, cfg.AuditLog), 1)
	mockClient.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
}
