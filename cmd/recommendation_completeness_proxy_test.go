package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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
	t        *testing.T
	mu       sync.Mutex
	handlers sync.WaitGroup
	regions  string
	details  string
	requests map[string]int
	cert     tls.Certificate
}

func newCompletenessProxy(t *testing.T, dir, regions, details string) (*completenessProxy, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(54), Subject: pkix.Name{CommonName: "recommendation fixture"},
		DNSNames:  []string{"ce.us-east-1.amazonaws.com", "ec2.us-east-1.amazonaws.com", "rds.us-east-1.amazonaws.com"},
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
	fixture := &completenessProxy{t: t, regions: regions, details: details, requests: make(map[string]int),
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
	allowed := r.Host == "ce.us-east-1.amazonaws.com:443" || r.Host == "ec2.us-east-1.amazonaws.com:443" || r.Host == "rds.us-east-1.amazonaws.com:443"
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
	if op == "" {
		values, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			p.t.Error(parseErr)
		}
		op = values.Get("Action")
	}
	p.mu.Lock()
	p.requests[op]++
	call := p.requests[op]
	p.mu.Unlock()
	status, contentType, payload := p.operation(req.Host, op, call)
	return &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{contentType}},
		Body:   io.NopCloser(strings.NewReader(payload)), ContentLength: int64(len(payload))}
}

func (p *completenessProxy) operation(host, op string, call int) (int, string, string) {
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
	responses := map[string]string{
		"DescribeDBInstances":           "DBInstances",
		"DescribeDBMajorEngineVersions": "DBMajorEngineVersions",
		"DescribeReservedDBInstances":   "ReservedDBInstances",
	}
	if element, ok := responses[op]; ok && host == "rds.us-east-1.amazonaws.com" {
		return 200, "text/xml", fmt.Sprintf(`<%sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%sResult><%s/></%sResult></%sResponse>`, op, op, element, op, op)
	}
	p.t.Errorf("unexpected operation %s %s", host, op)
	return 403, "text/plain", "fixture rejected operation"
}

func (p *completenessProxy) assertRequests(t *testing.T) {
	t.Helper()
	p.handlers.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
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
