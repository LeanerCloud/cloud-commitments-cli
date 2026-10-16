package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// awsFixtureStub is the offline AWS endpoint behind the duplicate check
// (checkDuplicates -> GetExistingCommitments). Every request must match a
// fixture keyed "host Action"; anything else fails the test, so a request that
// would have left the machine is reported instead of silently timing out.
type awsFixtureStub struct {
	t        *testing.T
	mu       sync.Mutex
	received map[string]int
	log      *bytes.Buffer
}

// awsFixtures answer the Query-protocol DescribeReserved* calls
// with an empty existing-commitment list.
var awsFixtures = map[string]string{
	"rds.us-east-1.amazonaws.com DescribeReservedDBInstances":        emptyRDSFixture,
	"rds.us-west-2.amazonaws.com DescribeReservedDBInstances":        emptyRDSFixture,
	"rds.eu-west-1.amazonaws.com DescribeReservedDBInstances":        emptyRDSFixture,
	"elasticache.us-east-1.amazonaws.com DescribeReservedCacheNodes": emptyElastiCacheFixture,
	"elasticache.us-west-2.amazonaws.com DescribeReservedCacheNodes": emptyElastiCacheFixture,
	"elasticache.eu-west-1.amazonaws.com DescribeReservedCacheNodes": emptyElastiCacheFixture,
}

const (
	emptyRDSFixture = `<DescribeReservedDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/">` +
		`<DescribeReservedDBInstancesResult><ReservedDBInstances/></DescribeReservedDBInstancesResult>` +
		`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedDBInstancesResponse>`
	emptyElastiCacheFixture = `<DescribeReservedCacheNodesResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/">` +
		`<DescribeReservedCacheNodesResult><ReservedCacheNodes/></DescribeReservedCacheNodesResult>` +
		`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedCacheNodesResponse>`
)

func (s *awsFixtureStub) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(b))
	key := r.URL.Host + " " + form.Get("Action")
	s.mu.Lock()
	s.received[key]++
	s.mu.Unlock()
	body, ok := awsFixtures[key]
	status := http.StatusOK
	if !ok {
		s.t.Errorf("unexpected AWS request %s %s%s", r.Method, key, r.URL.Path)
		status, body = http.StatusInternalServerError, "unexpected request"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// newAWSFixtureConfig returns an aws.Config whose HTTP client is the
// stub and whose credentials are anonymous, so requests are unsigned and need
// no environment.
func newAWSFixtureConfig(t *testing.T) (aws.Config, *awsFixtureStub) {
	t.Helper()
	stub := &awsFixtureStub{t: t, received: map[string]int{}, log: captureAppLog(t)}
	return aws.Config{
		Region:      "us-east-1",
		HTTPClient:  &http.Client{Transport: stub},
		Credentials: aws.AnonymousCredentials{},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}, stub
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
		assert.Contains(t, awsFixtures, k)
		assert.Equalf(t, 1, n, "duplicate-check requests for %s", k)
		total += n
	}
	// A fixture the SDK cannot parse only logs this warning in dry-run, so the
	// request count alone would not notice a broken fixture.
	assert.NotContains(t, s.log.String(), "Could not check for existing RIs")
	require.Equal(t, pairs, total, fmt.Sprintf("duplicate-check requests received: %v", s.received))
}
