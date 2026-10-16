package main

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// awsFixtureStub is the offline AWS endpoint behind the tests that build their
// own aws.Config. Every request must match a fixture keyed "service operation";
// anything else fails the test, so a request that would have left the machine
// is reported instead of silently timing out. The region in the host is
// ignored for the lookup but kept in received ("host operation").
type awsFixtureStub struct {
	t        *testing.T
	mu       sync.Mutex
	received map[string]int
	forms    map[string][]url.Values // request forms per "host operation", in arrival order
	fixtures map[string]awsFixture
	log      *bytes.Buffer
}

// awsFixture is one canned response; a zero status means 200.
type awsFixture struct {
	body   string
	status int
}

// awsFixtures hold the default per-operation responses: empty existing
// commitments, one SDK-parseable body per protocol (Query XML, rest-json).
var awsFixtures = map[string]awsFixture{
	"rds DescribeReservedDBInstances":        {body: emptyRDSFixture},
	"elasticache DescribeReservedCacheNodes": {body: emptyElastiCacheFixture},
	"savingsplans DescribeSavingsPlans":      {body: `{"savingsPlans":[]}`},
	"ec2 DescribeRegions":                    {body: describeRegionsFixture},
	// A non-retryable error, for tests of the fetch-failure path.
	"ce GetReservationCoverage": {status: http.StatusBadRequest, body: `{"__type":"AccessDeniedException","message":"denied by fixture"}`},
}

const (
	emptyRDSFixture = `<DescribeReservedDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/">` +
		`<DescribeReservedDBInstancesResult><ReservedDBInstances/></DescribeReservedDBInstancesResult>` +
		`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedDBInstancesResponse>`
	emptyElastiCacheFixture = `<DescribeReservedCacheNodesResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/">` +
		`<DescribeReservedCacheNodesResult><ReservedCacheNodes/></DescribeReservedCacheNodesResult>` +
		`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedCacheNodesResponse>`
	// Unsorted on purpose: getAllAWSRegions must sort.
	describeRegionsFixture = `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId>` +
		`<regionInfo><item><regionName>us-west-2</regionName></item><item><regionName>eu-west-1</regionName></item></regionInfo></DescribeRegionsResponse>`
)

// operation names the API call: the Query "Action", else the JSON
// X-Amz-Target suffix, else the last path segment (rest-json).
func awsOperation(r *http.Request, form url.Values) string {
	if a := form.Get("Action"); a != "" {
		return a
	}
	if tgt := r.Header.Get("X-Amz-Target"); tgt != "" {
		_, op, _ := strings.Cut(tgt, ".")
		return op
	}
	return path.Base(r.URL.Path)
}

func (s *awsFixtureStub) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	form, _ := url.ParseQuery(string(b))
	op := awsOperation(r, form)
	s.mu.Lock()
	s.received[r.URL.Host+" "+op]++
	s.forms[r.URL.Host+" "+op] = append(s.forms[r.URL.Host+" "+op], form)
	s.mu.Unlock()
	svc, _, _ := strings.Cut(r.URL.Host, ".")
	fx, ok := s.fixtures[svc+" "+op]
	if !ok {
		s.t.Errorf("unexpected AWS request %s %s %s", r.Method, r.URL.Host, op)
		fx = awsFixture{status: http.StatusInternalServerError, body: "unexpected request"}
	}
	if fx.status == 0 {
		fx.status = http.StatusOK
	}
	contentType := "application/x-amz-json-1.1"
	if strings.HasPrefix(fx.body, "<") {
		contentType = "text/xml"
	}
	return &http.Response{
		StatusCode: fx.status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(fx.body)),
		Request:    r,
	}, nil
}

// newAWSFixtureConfig returns an aws.Config whose HTTP client is the
// stub and whose credentials are anonymous, so requests are unsigned and need
// no environment.
func newAWSFixtureConfig(t *testing.T) (aws.Config, *awsFixtureStub) {
	t.Helper()
	stub := &awsFixtureStub{t: t, received: map[string]int{}, forms: map[string][]url.Values{}, fixtures: maps.Clone(awsFixtures), log: captureAppLog(t)}
	return aws.Config{
		Region:      "us-east-1",
		HTTPClient:  &http.Client{Transport: stub},
		Credentials: aws.AnonymousCredentials{},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}, stub
}

// requireRequests asserts the exact "host operation" request counts the stub
// received, so a test cannot pass without its AWS calls reaching the stub.
func (s *awsFixtureStub) requireRequests(t *testing.T, want map[string]int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, want, s.received)
}

// requireDuplicateChecks asserts the duplicate check really ran against the
// stub: one call per (service, region) pair, no other request, and every
// response parsed.
func (s *awsFixtureStub) requireDuplicateChecks(t *testing.T, pairs int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for k, n := range s.received {
		host, op, _ := strings.Cut(k, " ")
		svc, _, _ := strings.Cut(host, ".")
		assert.Contains(t, s.fixtures, svc+" "+op)
		assert.Equalf(t, 1, n, "duplicate-check requests for %s", k)
		total += n
	}
	// A fixture the SDK cannot parse only logs this warning in dry-run, so the
	// request count alone would not notice a broken fixture.
	assert.NotContains(t, s.log.String(), "Could not check for existing RIs")
	require.Equal(t, pairs, total, fmt.Sprintf("duplicate-check requests received: %v", s.received))
}
