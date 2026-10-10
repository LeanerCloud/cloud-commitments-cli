package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Regression tests for #2147: unavailable RDS inventory or lifecycle data must
// not silently disable the extended-support exclusion. Requests are answered by
// an in-process stub (AWS_ENDPOINT_URL or an injected transport).

// rdsAPIStub answers the three query-protocol calls the exclusion data needs.
// Behavior is selected by the fields; every request is recorded.
type rdsAPIStub struct {
	mu            sync.Mutex
	actions       []string
	failRegions   map[string]bool // DescribeDBInstances -> AccessDenied
	failPage2     map[string]bool // first page returns a marker, second page fails
	panicRegions  map[string]bool // transport panics (direct transport use only)
	failEngines   map[string]bool // DescribeDBMajorEngineVersions -> AccessDenied
	failRegionsEC bool            // DescribeRegions -> AccessDenied
	instances     map[string]string
	regionList    []string
}

func (s *rdsAPIStub) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.actions {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func (s *rdsAPIStub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.actions)
}

const (
	xmlContentType  = "text/xml"
	jsonContentType = "application/x-amz-json-1.1"
)

const rdsStubDenied = `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>denied by stub</Message></Error><RequestId>r</RequestId></ErrorResponse>`

func (s *rdsAPIStub) respond(authz, target, body string) (int, string, string) {
	form, _ := url.ParseQuery(body)
	action := form.Get("Action")
	region := ""
	if parts := strings.Split(authz, "/"); len(parts) > 2 {
		region = parts[2]
	}
	if target != "" {
		action = strings.TrimPrefix(target, "AWSInsightsIndexService.")
	}
	s.mu.Lock()
	s.actions = append(s.actions, action)
	s.mu.Unlock()

	switch action {
	case "GetReservationCoverage":
		return http.StatusOK, jsonContentType, `{"CoveragesByTime":[]}`
	case "GetReservationPurchaseRecommendation":
		return http.StatusOK, jsonContentType, `{"Recommendations":[{"RecommendationDetails":[{"RecommendedNumberOfInstancesToPurchase":"2","EstimatedMonthlySavingsAmount":"10","EstimatedMonthlyOnDemandCost":"30","InstanceDetails":{"RDSInstanceDetails":{"InstanceType":"db.t3.medium","Region":"us-east-1","DeploymentOption":"Single-AZ","DatabaseEngine":"MySQL"}}}]}]}`
	case "DescribeRegions":
		if s.failRegionsEC {
			return http.StatusForbidden, xmlContentType, rdsStubDenied
		}
		var items strings.Builder
		for _, r := range s.regionList {
			fmt.Fprintf(&items, "<item><regionName>%s</regionName></item>", r)
		}
		return http.StatusOK, xmlContentType, `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo>` + items.String() + `</regionInfo></DescribeRegionsResponse>`
	case "DescribeDBInstances":
		if s.panicRegions[region] {
			panic("stub panic in " + region)
		}
		if s.failRegions[region] || (s.failPage2[region] && form.Get("Marker") != "") {
			return http.StatusForbidden, xmlContentType, rdsStubDenied
		}
		marker := ""
		if s.failPage2[region] {
			marker = "<Marker>page2</Marker>"
		}
		inst := ""
		if class := s.instances[region]; class != "" {
			inst = fmt.Sprintf("<DBInstance><DBInstanceClass>%s</DBInstanceClass><Engine>mysql</Engine><EngineVersion>5.7.44</EngineVersion></DBInstance>", class)
		}
		return http.StatusOK, xmlContentType, `<DescribeDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBInstancesResult><DBInstances>` + inst + `</DBInstances>` + marker + `</DescribeDBInstancesResult></DescribeDBInstancesResponse>`
	case "DescribeDBMajorEngineVersions":
		if s.failEngines[form.Get("Engine")] {
			return http.StatusForbidden, xmlContentType, rdsStubDenied
		}
		return http.StatusOK, xmlContentType, `<DescribeDBMajorEngineVersionsResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBMajorEngineVersionsResult><DBMajorEngineVersions/></DescribeDBMajorEngineVersionsResult></DescribeDBMajorEngineVersionsResponse>`
	}
	return http.StatusBadRequest, xmlContentType, rdsStubDenied
}

// RoundTrip lets tests hand the stub to an aws.Config directly.
func (s *rdsAPIStub) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	status, contentType, body := s.respond(r.Header.Get("Authorization"), r.Header.Get("X-Amz-Target"), string(b))
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *rdsAPIStub) awsConfig() aws.Config {
	return aws.Config{
		Region:     "us-east-1",
		HTTPClient: &http.Client{Transport: s},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, nil
		}),
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}
}

// serveEnv starts the stub as a local HTTP endpoint and points the default AWS
// config chain at it, isolated from any developer credentials or config.
func (s *rdsAPIStub) serveEnv(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		status, contentType, body := s.respond(r.Header.Get("Authorization"), r.Header.Get("X-Amz-Target"), string(b))
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("AWS_ENDPOINT_URL_COST_EXPLORER", srv.URL)
}

// requireStubReached fails the test when the seam fell through to somewhere
// other than the stub, and when anything tried to purchase.
func (s *rdsAPIStub) requireStubReachedNoPurchase(t *testing.T) {
	t.Helper()
	assert.Positive(t, s.total(), "stub must have been reached")
	assert.Zero(t, s.count("Purchase"), "no purchase call may be made")
}

func newRDSStub(regions ...string) *rdsAPIStub {
	return &rdsAPIStub{
		regionList:   regions,
		failRegions:  map[string]bool{},
		failPage2:    map[string]bool{},
		panicRegions: map[string]bool{},
		failEngines:  map[string]bool{},
		instances:    map[string]string{},
	}
}

func queryRegions(t *testing.T, stub *rdsAPIStub, ctx context.Context) (map[string][]InstanceEngineVersion, map[string]error, error) {
	t.Helper()
	regions, err := getAWSRegions(ctx, stub.awsConfig())
	require.NoError(t, err)
	return queryRDSInstancesInRegions(ctx, stub.awsConfig(), regions)
}

func TestQueryRDSInstancesInRegions_AllRegionsFail(t *testing.T) {
	stub := newRDSStub("us-east-1", "eu-west-1")
	stub.failRegions["us-east-1"], stub.failRegions["eu-west-1"] = true, true

	inst, failed, err := queryRegions(t, stub, context.Background())

	require.NoError(t, err)
	assert.Empty(t, inst)
	assert.Len(t, failed, 2, "failed regions must be reported, not treated as an empty inventory")
	assert.ErrorContains(t, failed["us-east-1"], "us-east-1")
}

func TestQueryRDSInstancesInRegions_FailureMidPaginationMarksRegionFailed(t *testing.T) {
	stub := newRDSStub("us-east-1", "eu-west-1")
	stub.failPage2["us-east-1"] = true
	stub.instances["us-east-1"], stub.instances["eu-west-1"] = "db.r5.large", "db.t3.small"

	inst, failed, err := queryRegions(t, stub, context.Background())

	require.NoError(t, err)
	assert.Len(t, failed, 1)
	assert.Contains(t, failed, "us-east-1")
	assert.Contains(t, inst, "db.t3.small", "healthy region data is kept")
}

func TestQueryRDSInstancesInRegions_WorkerPanicMarksRegionFailed(t *testing.T) {
	stub := newRDSStub("us-east-1", "eu-west-1")
	stub.panicRegions["us-east-1"] = true
	stub.instances["eu-west-1"] = "db.t3.small"

	_, failed, err := queryRegions(t, stub, context.Background())

	require.NoError(t, err)
	assert.Contains(t, failed, "us-east-1")
	assert.NotContains(t, failed, "eu-west-1")
}

func TestQueryRDSInstancesInRegions_GenuineEmptyInventoryIsNotAFailure(t *testing.T) {
	stub := newRDSStub("us-east-1", "eu-west-1")

	inst, failed, err := queryRegions(t, stub, context.Background())

	require.NoError(t, err)
	assert.Empty(t, inst)
	assert.Empty(t, failed)
}

func TestQueryRDSInstancesInRegions_CancelledContextIsTerminal(t *testing.T) {
	stub := newRDSStub("us-east-1")
	ctx, cancel := context.WithCancel(context.Background())
	regions, err := getAWSRegions(ctx, stub.awsConfig())
	require.NoError(t, err)
	cancel()

	_, _, err = queryRDSInstancesInRegions(ctx, stub.awsConfig(), regions)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestQueryMajorEngineVersionsWithClient_EngineFailuresAreAggregated(t *testing.T) {
	denied := errors.New("AccessDenied")
	t.Run("all engines fail", func(t *testing.T) {
		stub := &engineKeyedRDSMajorVersionsStub{errByEngine: map[string]error{
			"mysql": denied, "postgres": denied, "aurora-mysql": denied, "aurora-postgresql": denied,
		}}
		got, err := queryMajorEngineVersionsWithClient(context.Background(), stub)
		require.Error(t, err)
		assert.Nil(t, got)
		for _, e := range []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"} {
			assert.ErrorContains(t, err, "major engine versions for "+e+":")
		}
	})
	t.Run("one engine fails", func(t *testing.T) {
		stub := &engineKeyedRDSMajorVersionsStub{errByEngine: map[string]error{"postgres": denied}}
		_, err := queryMajorEngineVersionsWithClient(context.Background(), stub)
		require.Error(t, err)
		assert.ErrorContains(t, err, "postgres")
		assert.ErrorIs(t, err, denied)
	})
	t.Run("genuinely empty lists are not an error", func(t *testing.T) {
		got, err := queryMajorEngineVersionsWithClient(context.Background(), &engineKeyedRDSMajorVersionsStub{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("canceled context stays context.Canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := queryMajorEngineVersionsWithClient(ctx, &engineKeyedRDSMajorVersionsStub{errByEngine: map[string]error{"mysql": denied}})
		assert.True(t, errors.Is(err, context.Canceled))
	})
}

func TestQueryMajorEngineVersionsWithClient_PaginationCapIsAnError(t *testing.T) {
	_, err := queryMajorEngineVersionsWithClient(context.Background(), &alwaysMarkerRDSStub{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "pagination cap")
}

// alwaysMarkerRDSStub never stops paginating.
type alwaysMarkerRDSStub struct{}

func (alwaysMarkerRDSStub) DescribeDBMajorEngineVersions(context.Context, *awsrds.DescribeDBMajorEngineVersionsInput, ...func(*awsrds.Options)) (*awsrds.DescribeDBMajorEngineVersionsOutput, error) {
	return &awsrds.DescribeDBMajorEngineVersionsOutput{Marker: aws.String("more")}, nil
}

func TestFetchEngineVersionData_ExclusionDataUnavailable(t *testing.T) {
	ctx := context.Background()

	t.Run("exclusion not needed makes no request", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.failRegionsEC = true
		stub.serveEnv(t)
		data, err := fetchEngineVersionData(ctx, Config{IncludeExtendedSupport: true}, false)
		require.NoError(t, err)
		assert.Empty(t, data.instanceVersions)
		assert.Zero(t, stub.total())
	})
	t.Run("region listing failure is fatal", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.failRegionsEC = true
		stub.serveEnv(t)
		_, err := fetchEngineVersionData(ctx, Config{}, true)
		require.Error(t, err)
		assert.ErrorContains(t, err, "--include-extended-support")
		stub.requireStubReachedNoPurchase(t)
	})
	t.Run("engine lifecycle failure is fatal", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.failEngines["postgres"] = true
		stub.serveEnv(t)
		_, err := fetchEngineVersionData(ctx, Config{}, true)
		require.Error(t, err)
		assert.ErrorContains(t, err, "postgres")
		assert.ErrorContains(t, err, "--include-extended-support")
		stub.requireStubReachedNoPurchase(t)
	})
	t.Run("one failed region is reported, not fatal", func(t *testing.T) {
		stub := newRDSStub("us-east-1", "eu-west-1")
		stub.failRegions["eu-west-1"] = true
		stub.serveEnv(t)
		data, err := fetchEngineVersionData(ctx, Config{}, true)
		require.NoError(t, err)
		assert.Contains(t, data.failedRegions, "eu-west-1")
		assert.NotContains(t, data.failedRegions, "us-east-1")
		stub.requireStubReachedNoPurchase(t)
	})
	t.Run("healthy and genuinely empty", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.serveEnv(t)
		data, err := fetchEngineVersionData(ctx, Config{}, true)
		require.NoError(t, err)
		assert.Empty(t, data.failedRegions)
		stub.requireStubReachedNoPurchase(t)
	})
}

func TestFetchEngineVersionData_CancelledContextKeepsContextError(t *testing.T) {
	stub := newRDSStub("us-east-1")
	stub.serveEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchEngineVersionData(ctx, Config{}, true)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "cancel must stay detectable through the wrapping")
}

func rdsRec(region string) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 2, Region: region,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}
}

func TestRequireRegionInventory(t *testing.T) {
	data := engineVersionData{failedRegions: map[string]error{"eu-west-1": errors.New("denied")}}

	assert.ErrorContains(t, requireRegionInventory([]common.Recommendation{rdsRec("eu-west-1")}, Config{}, data), "eu-west-1")
	assert.ErrorContains(t, requireRegionInventory([]common.Recommendation{rdsRec("")}, Config{}, data), "eu-west-1", "empty region cannot be proven healthy")
	assert.NoError(t, requireRegionInventory([]common.Recommendation{rdsRec("us-east-1")}, Config{}, data))
	assert.NoError(t, requireRegionInventory([]common.Recommendation{{Service: common.ServiceEC2, Region: "eu-west-1"}}, Config{}, data), "non-RDS recs are unaffected")
	assert.NoError(t, requireRegionInventory([]common.Recommendation{rdsRec("eu-west-1")}, Config{IncludeExtendedSupport: true}, data), "opt-in skips the check")
}

// useRealEngineVersionFetcher restores the AWS-backed fetcher (TestMain stubs it).
func useRealEngineVersionFetcher(t *testing.T) {
	t.Helper()
	orig := engineVersionFetcher
	engineVersionFetcher = fetchEngineVersionData
	t.Cleanup(func() { engineVersionFetcher = orig })
}

func TestFilterAndAdjustRecommendations_ExtendedSupportDataUnavailable(t *testing.T) {
	useRealEngineVersionFetcher(t)

	t.Run("engine lifecycle failure aborts before any rec is returned", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.failEngines["mysql"] = true
		stub.serveEnv(t)
		got, err := filterAndAdjustRecommendations(context.Background(), []common.Recommendation{rdsRec("us-east-1")}, 100, Config{})
		require.Error(t, err)
		assert.Empty(t, got)
		stub.requireStubReachedNoPurchase(t)
	})
	t.Run("rec in a failed region aborts", func(t *testing.T) {
		stub := newRDSStub("us-east-1", "eu-west-1")
		stub.failRegions["eu-west-1"] = true
		stub.serveEnv(t)
		got, err := filterAndAdjustRecommendations(context.Background(), []common.Recommendation{rdsRec("eu-west-1")}, 100, Config{})
		require.Error(t, err)
		assert.ErrorContains(t, err, "eu-west-1")
		assert.Empty(t, got)
	})
	t.Run("rec in a healthy region proceeds despite another failed region", func(t *testing.T) {
		stub := newRDSStub("us-east-1", "eu-west-1")
		stub.failRegions["eu-west-1"] = true
		stub.serveEnv(t)
		got, err := filterAndAdjustRecommendations(context.Background(), []common.Recommendation{rdsRec("us-east-1")}, 100, Config{})
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})
	t.Run("genuinely empty inventory proceeds", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.serveEnv(t)
		got, err := filterAndAdjustRecommendations(context.Background(), []common.Recommendation{rdsRec("us-east-1")}, 100, Config{})
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})
	t.Run("IncludeExtendedSupport opt-in makes no query and proceeds", func(t *testing.T) {
		stub := newRDSStub("us-east-1")
		stub.failRegionsEC = true
		stub.serveEnv(t)
		got, err := filterAndAdjustRecommendations(context.Background(), []common.Recommendation{rdsRec("us-east-1")}, 100, Config{IncludeExtendedSupport: true})
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Zero(t, stub.total())
	})
}

func TestFetchAllRecs_RDSRecInFailedRegionAborts(t *testing.T) {
	ctx := context.Background()
	awsCfg := newRDSStub("us-east-1").awsConfig() // injected stub transport: no outbound connection
	saved := saveGlobalVars()
	defer saved.restore()
	toolCfg.Coverage = 100
	toolCfg.Regions = []string{"us-east-1", "eu-west-1"}

	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool {
		return p != nil && p.Region == "us-east-1"
	})).Return([]common.Recommendation{rdsRec("us-east-1")}, nil)
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool {
		return p != nil && p.Region == "eu-west-1"
	})).Return([]common.Recommendation{rdsRec("eu-west-1")}, nil)

	data := engineVersionData{failedRegions: map[string]error{"eu-west-1": errors.New("denied")}}
	_, _, err := fetchAllRecs(ctx, awsCfg, client, NewAccountAliasCache(awsCfg), []common.ServiceType{common.ServiceRDS}, data, toolCfg, nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "eu-west-1")
}

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
