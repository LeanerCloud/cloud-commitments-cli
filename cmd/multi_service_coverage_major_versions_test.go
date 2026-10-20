package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineKeyedRDSMajorVersionsStub implements RDSMajorVersionsClient with
// canned per-engine responses and records which engines were queried, so
// queryMajorEngineVersionsWithClient can be tested hermetically (no AWS
// credentials, no network).
type engineKeyedRDSMajorVersionsStub struct {
	versionsByEngine map[string][]rdstypes.DBMajorEngineVersion
	errByEngine      map[string]error
	enginesQueried   []string
}

func (s *engineKeyedRDSMajorVersionsStub) DescribeDBMajorEngineVersions(
	_ context.Context,
	params *awsrds.DescribeDBMajorEngineVersionsInput,
	_ ...func(*awsrds.Options),
) (*awsrds.DescribeDBMajorEngineVersionsOutput, error) {
	engine := aws.ToString(params.Engine)
	s.enginesQueried = append(s.enginesQueried, engine)
	if err := s.errByEngine[engine]; err != nil {
		return nil, err
	}
	return &awsrds.DescribeDBMajorEngineVersionsOutput{
		DBMajorEngineVersions: s.versionsByEngine[engine],
	}, nil
}

// TestQueryMajorEngineVersionsWithClient_Success replaces a former
// live-credential test: it stubs the RDS client and asserts the merged
// engine:major-version map deterministically.
func TestQueryMajorEngineVersionsWithClient_Success(t *testing.T) {
	stub := &engineKeyedRDSMajorVersionsStub{
		versionsByEngine: map[string][]rdstypes.DBMajorEngineVersion{
			"mysql":    {rdsMajorVersion("mysql", "5.7"), rdsMajorVersion("mysql", "8.0")},
			"postgres": {rdsMajorVersion("postgres", "13")},
		},
	}

	result, err := queryMajorEngineVersionsWithClient(context.Background(), stub)
	require.NoError(t, err)

	assert.ElementsMatch(t,
		[]string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"},
		stub.enginesQueried,
		"must query every supported engine exactly once")

	require.Len(t, result, 3)
	assert.Equal(t, "mysql", result["mysql:5.7"].Engine)
	assert.Equal(t, "5.7", result["mysql:5.7"].MajorEngineVersion)
	assert.Equal(t, "8.0", result["mysql:8.0"].MajorEngineVersion)
	assert.Equal(t, "13", result["postgres:13"].MajorEngineVersion)
	require.Len(t, result["postgres:13"].SupportedEngineLifecycles, 1,
		"lifecycle data must be carried through from the API response")
}

// TestQueryMajorEngineVersionsWithClient_EngineErrorContinues asserts that one
// engine failing does not stop the remaining engine queries, but the overall
// call fails: missing lifecycle data must not silently disable the
// extended-support exclusion (#2147).
func TestQueryMajorEngineVersionsWithClient_EngineErrorContinues(t *testing.T) {
	stub := &engineKeyedRDSMajorVersionsStub{
		versionsByEngine: map[string][]rdstypes.DBMajorEngineVersion{
			"postgres": {rdsMajorVersion("postgres", "15")},
		},
		errByEngine: map[string]error{
			"mysql": errors.New("DescribeDBMajorEngineVersions throttled"),
		},
	}

	result, err := queryMajorEngineVersionsWithClient(context.Background(), stub)
	require.Error(t, err, "an unavailable engine lifecycle query must fail the call")
	assert.Nil(t, result)

	assert.ElementsMatch(t,
		[]string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"},
		stub.enginesQueried,
		"a failing engine must not stop the remaining engine queries")
}

// TestQueryMajorEngineVersions_ProfileSelection asserts validation-profile
// precedence deterministically: the configured profiles point at names
// guaranteed not to exist in the (redirected, empty) shared config, so
// config loading fails fast without any live AWS call and the error names
// the profile the function actually selected.
func TestQueryMajorEngineVersions_ProfileSelection(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		profile           string
		validationProfile string
		wantProfileInErr  string
	}{
		{
			name:              "validation profile takes precedence",
			profile:           "cudly-test-main-profile-does-not-exist",
			validationProfile: "cudly-test-validation-profile-does-not-exist",
			wantProfileInErr:  "cudly-test-validation-profile-does-not-exist",
		},
		{
			name:             "falls back to main profile",
			profile:          "cudly-test-main-profile-does-not-exist",
			wantProfileInErr: "cudly-test-main-profile-does-not-exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_CONFIG_FILE", os.DevNull)
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)

			_, err := queryMajorEngineVersions(ctx, Config{
				Profile:           tt.profile,
				ValidationProfile: tt.validationProfile,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to load AWS config")
			assert.Contains(t, err.Error(), tt.wantProfileInErr,
				"error must name the selected profile")
		})
	}
}
