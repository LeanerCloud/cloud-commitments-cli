package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type completenessProxy struct {
	t          *testing.T
	mu         sync.Mutex
	handlers   sync.WaitGroup
	regions    string
	details    string
	service    string
	requests   map[string]int
	spRequests []completenessSPRequest
	engines    []string
	cert       tls.Certificate
}

type completenessSPRequest struct {
	SavingsPlansType     string
	NextPageToken        string
	TermInYears          string
	PaymentOption        string
	LookbackPeriodInDays string
	AccountScope         string
}

func newCompletenessProxy(t *testing.T, dir, regions, details, service string) (*completenessProxy, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(54), Subject: pkix.Name{CommonName: "recommendation fixture"},
		DNSNames:  []string{"ce.us-east-1.amazonaws.com", "ec2.us-east-1.amazonaws.com", "rds.us-east-1.amazonaws.com", "savingsplans.amazonaws.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, certPEM, 0600))
	fixture := &completenessProxy{t: t, regions: regions, details: details, service: service, requests: make(map[string]int),
		cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveConnect))
	t.Cleanup(func() {
		server.Close()
		fixture.handlers.Wait()
	})
	return fixture, server.URL, caPath
}

func (p *completenessProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	p.handlers.Add(1)
	defer p.handlers.Done()
	allowed := r.Host == "ce.us-east-1.amazonaws.com:443" || r.Host == "ec2.us-east-1.amazonaws.com:443" || r.Host == "rds.us-east-1.amazonaws.com:443" || r.Host == "savingsplans.amazonaws.com:443"
	if r.Method != http.MethodConnect || !allowed {
		p.t.Errorf("unexpected CONNECT %s %s", r.Method, r.Host)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		p.t.Error(err)
		return
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(40 * time.Second)); err != nil {
		p.t.Error(err)
		return
	}
	if _, err := fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.t.Error(err)
		return
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{p.cert}, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		p.t.Errorf("TLS handshake: %v", err)
		return
	}
	reader := bufio.NewReader(secure)
	for {
		req, err := http.ReadRequest(reader)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			p.t.Errorf("read request: %v", err)
			return
		}
		if req.Host != strings.TrimSuffix(r.Host, ":443") {
			p.t.Errorf("tunnel host mismatch: %s", req.Host)
			return
		}
		response := p.respond(req)
		err = response.Write(secure)
		_ = response.Body.Close()
		if err != nil {
			p.t.Errorf("write response: %v", err)
			return
		}
	}
}

func (p *completenessProxy) respond(req *http.Request) *http.Response {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		p.t.Error(err)
	}
	op := req.Header.Get("X-Amz-Target")
	if req.Host == "savingsplans.amazonaws.com" && op == "" {
		op = strings.TrimPrefix(req.URL.Path, "/")
		if req.Method != http.MethodPost || req.URL.Path != "/DescribeSavingsPlans" || req.URL.RawQuery != "" {
			p.t.Errorf("unexpected Savings Plans REST request %s %s", req.Method, req.URL)
			op = "fixture-rejected"
		} else {
			var inventory struct {
				States     []string `json:"states"`
				MaxResults int      `json:"maxResults"`
				NextToken  string   `json:"nextToken"`
			}
			if decodeErr := json.Unmarshal(body, &inventory); decodeErr != nil || inventory.MaxResults != 100 || inventory.NextToken != "" || strings.Join(inventory.States, ",") != "active,payment-pending,pending-return,queued" {
				p.t.Errorf("unexpected SP inventory request: %s (%v)", body, decodeErr)
				op = "fixture-rejected"
			}
		}
	} else if op == "" {
		values, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			p.t.Error(parseErr)
		}
		op = values.Get("Action")
		if p.service != "rds" && req.Host == "rds.us-east-1.amazonaws.com" {
			want := url.Values{"Action": {op}, "Version": {"2014-10-31"}}
			if op == "DescribeDBMajorEngineVersions" {
				engine := values.Get("Engine")
				want.Set("Engine", engine)
				p.mu.Lock()
				p.engines = append(p.engines, engine)
				p.mu.Unlock()
			}
			if req.Method != http.MethodPost || req.URL.Path != "/" || req.URL.RawQuery != "" || values.Encode() != want.Encode() {
				p.t.Errorf("unexpected SP ancillary RDS request %s %s: %s", req.Method, req.URL, body)
				op = "fixture-rejected"
			}
		}
	}
	p.mu.Lock()
	p.requests[op]++
	call := p.requests[op]
	p.mu.Unlock()
	status, contentType, payload := p.operation(req.Host, op, call)
	if p.service != "rds" && req.Host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation" {
		status, contentType, payload = p.spRecommendation(body)
	}
	return &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{contentType}},
		Body:   io.NopCloser(strings.NewReader(payload)), ContentLength: int64(len(payload))}
}

func (p *completenessProxy) operation(host, op string, call int) (int, string, string) {
	responses := map[string]string{
		"DescribeDBInstances":           "DBInstances",
		"DescribeDBMajorEngineVersions": "DBMajorEngineVersions",
		"DescribeReservedDBInstances":   "ReservedDBInstances",
	}
	if element, ok := responses[op]; ok && host == "rds.us-east-1.amazonaws.com" && (p.service == "rds" || op != "DescribeReservedDBInstances") {
		return 200, "text/xml", fmt.Sprintf(`<%sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%sResult><%s/></%sResult></%sResponse>`, op, op, element, op, op)
	}
	if p.service != "rds" {
		if host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation" {
			return 200, "application/x-amz-json-1.1", ""
		}
		if host == "savingsplans.amazonaws.com" && op == "DescribeSavingsPlans" {
			return 200, "application/json", `{"savingsPlans":[]}`
		}
		if host == "ec2.us-east-1.amazonaws.com" && op == "DescribeRegions" {
			return 200, "text/xml", `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo><item><regionName>us-east-1</regionName></item></regionInfo></DescribeRegionsResponse>`
		}
		p.t.Errorf("unexpected SP operation %s %s", host, op)
		return 403, "text/plain", "fixture rejected operation"
	}
	if host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetReservationCoverage" {
		return 200, "application/x-amz-json-1.1", `{"CoveragesByTime":[]}`
	}
	if host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetReservationPurchaseRecommendation" {
		if p.details == "api-error" {
			return 400, "application/x-amz-json-1.1", `{"__type":"AccessDeniedException","message":"fixture denied recommendations"}`
		}
		const valid = `{"RecommendedNumberOfInstancesToPurchase":"2","EstimatedMonthlySavingsAmount":"10",
			"EstimatedMonthlyOnDemandCost":"30","InstanceDetails":{"RDSInstanceDetails":{
			"InstanceType":"db.t3.medium","Region":"us-east-1","DeploymentOption":"Single-AZ"}}}`
		const invalid = `{"RecommendedNumberOfInstancesToPurchase":"not-a-number"}`
		details := map[string]string{
			"valid":       valid,
			"mixed":       valid + "," + invalid,
			"all-invalid": invalid,
			"empty":       "",
		}
		return 200, "application/x-amz-json-1.1", `{"Recommendations":[{"RecommendationDetails":[` + details[p.details] + `]}]}`
	}
	if host == "ec2.us-east-1.amazonaws.com" && op == "DescribeRegions" {
		if p.regions == "fallback" && call == 2 {
			return 400, "text/xml", `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>fixture denied region discovery</Message></Error></Errors></Response>`
		}
		return 200, "text/xml", `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo><item><regionName>us-east-1</regionName></item></regionInfo></DescribeRegionsResponse>`
	}
	p.t.Errorf("unexpected operation %s %s", host, op)
	return 403, "text/plain", "fixture rejected operation"
}

func (p *completenessProxy) assertRequests(t *testing.T) {
	t.Helper()
	p.handlers.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.service != "rds" {
		p.assertSPRequests(t)
		return
	}
	ceCalls := 1
	if p.regions == "fallback" {
		ceCalls = 6
		if p.details == "valid" || p.details == "mixed" {
			ceCalls++
		}
	}
	require.Equal(t, ceCalls, p.requests["AWSInsightsIndexService.GetReservationPurchaseRecommendation"], "operations=%v", p.requests)
	wantRegions := 2
	if p.regions == "explicit" {
		wantRegions = 1
	}
	require.Equal(t, wantRegions, p.requests["DescribeRegions"], "region calls")
	t.Logf("actual root command, config loader, SDK and CSV; synthetic operations=%v", p.requests)
}

func completenessSPTypes(service string) []string {
	if service == "savingsplans" {
		return []string{"COMPUTE_SP", "EC2_INSTANCE_SP", "SAGEMAKER_SP", "DATABASE_SP"}
	}
	for _, plan := range []string{"COMPUTE_SP", "EC2_INSTANCE_SP", "SAGEMAKER_SP", "DATABASE_SP"} {
		if completenessSPService(plan) == strings.Replace(service, "savingsplans-", "savings-plans-", 1) {
			return []string{plan}
		}
	}
	panic("unexpected fixture service: " + service)
}

func completenessSPService(plan string) string {
	return map[string]string{"COMPUTE_SP": "savings-plans-compute", "EC2_INSTANCE_SP": "savings-plans-ec2instance", "SAGEMAKER_SP": "savings-plans-sagemaker", "DATABASE_SP": "savings-plans-database"}[plan]
}

func (p *completenessProxy) spRecommendation(body []byte) (int, string, string) {
	var request completenessSPRequest
	if err := json.Unmarshal(body, &request); err != nil {
		p.t.Errorf("decode SP request: %v", err)
		return 403, "application/json", `{}`
	}
	p.mu.Lock()
	p.spRequests = append(p.spRequests, request)
	p.mu.Unlock()
	if p.details == "api-error" || (p.details == "failed-type" && (p.service != "savingsplans" || request.SavingsPlansType == "DATABASE_SP")) || request.NextPageToken == "unfinished" {
		return 400, "application/x-amz-json-1.1", `{"__type":"AccessDeniedException","message":"fixture denied recommendations"}`
	}
	const valid = `{"HourlyCommitmentToPurchase":"2","EstimatedMonthlySavingsAmount":"10","UpfrontCost":"3","CurrentAverageHourlyOnDemandSpend":"4"}`
	const invalid = `{"HourlyCommitmentToPurchase":"not-a-number","EstimatedMonthlySavingsAmount":"10","UpfrontCost":"3"}`
	details := valid
	if request.SavingsPlansType == "EC2_INSTANCE_SP" {
		details = strings.TrimSuffix(valid, "}") + `,"SavingsPlansDetails":{"Region":"us-east-1"}}`
	}
	switch p.details {
	case "mixed":
		details += "," + invalid
	case "all-invalid":
		details = invalid
	case "empty":
		details = ""
	}
	token := ""
	if p.details == "late-page" {
		token = `,"NextPageToken":"unfinished"`
	}
	return 200, "application/x-amz-json-1.1", `{"SavingsPlansPurchaseRecommendation":{"SavingsPlansPurchaseRecommendationDetails":[` + details + `]}` + token + `}`
}

func (p *completenessProxy) assertSPRequests(t *testing.T) {
	t.Helper()
	types := completenessSPTypes(p.service)
	expected := make([]completenessSPRequest, 0, len(types)*2)
	for _, plan := range types {
		request := completenessSPRequest{SavingsPlansType: plan, TermInYears: "ONE_YEAR", PaymentOption: "NO_UPFRONT", LookbackPeriodInDays: "SEVEN_DAYS", AccountScope: "LINKED"}
		expected = append(expected, request)
		if p.details == "late-page" {
			request.NextPageToken = "unfinished"
			expected = append(expected, request)
		}
	}
	require.Equal(t, expected, p.spRequests, "actual SDK SP request tuples")
	rows := len(types)
	switch p.details {
	case "empty", "all-invalid", "api-error":
		rows = 0
	case "failed-type":
		if p.service == "savingsplans" {
			rows--
		} else {
			rows = 0
		}
	}
	operations := map[string]int{"DescribeRegions": 1, "DescribeDBInstances": 1, "DescribeDBMajorEngineVersions": 4, "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation": len(expected)}
	if rows > 0 {
		operations["DescribeSavingsPlans"] = rows
	}
	require.Equal(t, operations, p.requests, "unexpected read or purchase")
	require.Equal(t, []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"}, p.engines, "ancillary RDS engine reads")
	t.Logf("actual root command, SP SDK and CSV; synthetic operations=%v", p.requests)
}
