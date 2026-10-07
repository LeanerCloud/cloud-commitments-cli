package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiPageRDSMajorVersionsMock implements RDSMajorVersionsClient and returns
// distinct pages based on the Marker in the incoming request.
type multiPageRDSMajorVersionsMock struct {
	pages  []*awsrds.DescribeDBMajorEngineVersionsOutput
	tokens []string // tokens[i] triggers pages[i+1]; first call has empty marker
	calls  int
}

func (m *multiPageRDSMajorVersionsMock) DescribeDBMajorEngineVersions(
	_ context.Context,
	params *awsrds.DescribeDBMajorEngineVersionsInput,
	_ ...func(*awsrds.Options),
) (*awsrds.DescribeDBMajorEngineVersionsOutput, error) {
	idx := 0
	incoming := aws.ToString(params.Marker)
	for i, tok := range m.tokens {
		if tok == incoming {
			idx = i + 1
			break
		}
	}
	if incoming == "" {
		idx = 0
	}
	m.calls++
	if idx >= len(m.pages) {
		return nil, fmt.Errorf("unexpected RDS Marker %q", incoming)
	}
	return m.pages[idx], nil
}

// rdsMajorVersion builds a minimal DBMajorEngineVersion for tests.
func rdsMajorVersion(engine, major string) rdstypes.DBMajorEngineVersion { //nolint:unparam // param intentional for interface consistency/future use
	return rdstypes.DBMajorEngineVersion{
		Engine:             aws.String(engine),
		MajorEngineVersion: aws.String(major),
		SupportedEngineLifecycles: []rdstypes.SupportedEngineLifecycle{
			{
				LifecycleSupportName:      rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport,
				LifecycleSupportStartDate: aws.Time(time.Now().AddDate(-1, 0, 0)),
				LifecycleSupportEndDate:   aws.Time(time.Now().AddDate(2, 0, 0)),
			},
		},
	}
}

// TestFetchMajorEngineVersionsForEngine_Paginates asserts that all pages are
// fetched and results accumulated (issue #692 regression test).
func TestFetchMajorEngineVersionsForEngine_Paginates(t *testing.T) {
	mock := &multiPageRDSMajorVersionsMock{
		pages: []*awsrds.DescribeDBMajorEngineVersionsOutput{
			{
				DBMajorEngineVersions: []rdstypes.DBMajorEngineVersion{
					rdsMajorVersion("mysql", "5.7"),
					rdsMajorVersion("mysql", "8.0"),
				},
				Marker: aws.String("tok1"),
			},
			{
				DBMajorEngineVersions: []rdstypes.DBMajorEngineVersion{
					rdsMajorVersion("mysql", "8.4"),
					rdsMajorVersion("mysql", "9.0"),
				},
				Marker: aws.String("tok2"),
			},
			{
				DBMajorEngineVersions: []rdstypes.DBMajorEngineVersion{
					rdsMajorVersion("mysql", "9.1"),
				},
				Marker: nil,
			},
		},
		tokens: []string{"tok1", "tok2"},
	}

	versionInfo := make(map[string]MajorEngineVersionInfo)
	err := fetchMajorEngineVersionsForEngine(context.Background(), mock, "mysql", versionInfo)
	require.NoError(t, err)
	// 2 + 2 + 1 = 5 versions across 3 pages
	assert.Len(t, versionInfo, 5, "must accumulate all versions across pages")
	assert.Equal(t, 3, mock.calls, "must call API once per page")
	assert.Contains(t, versionInfo, "mysql:5.7")
	assert.Contains(t, versionInfo, "mysql:9.1")
}

// TestFetchMajorEngineVersionsForEngine_EmptyMarkerTerminates asserts that an
// empty-string Marker is treated as terminal (parity with PR #690).
func TestFetchMajorEngineVersionsForEngine_EmptyMarkerTerminates(t *testing.T) {
	mock := &multiPageRDSMajorVersionsMock{
		pages: []*awsrds.DescribeDBMajorEngineVersionsOutput{
			{
				DBMajorEngineVersions: []rdstypes.DBMajorEngineVersion{
					rdsMajorVersion("mysql", "8.0"),
				},
				Marker: aws.String(""), // empty string -- must terminate
			},
		},
		tokens: []string{},
	}

	versionInfo := make(map[string]MajorEngineVersionInfo)
	err := fetchMajorEngineVersionsForEngine(context.Background(), mock, "mysql", versionInfo)
	require.NoError(t, err)
	assert.Len(t, versionInfo, 1)
	assert.Equal(t, 1, mock.calls, "empty-string Marker must terminate after page 1")
}

// alwaysNextPageRDSMock returns pages each carrying a non-nil non-empty Marker.
type alwaysNextPageRDSMock struct {
	calls int
}

func (m *alwaysNextPageRDSMock) DescribeDBMajorEngineVersions(
	_ context.Context,
	_ *awsrds.DescribeDBMajorEngineVersionsInput,
	_ ...func(*awsrds.Options),
) (*awsrds.DescribeDBMajorEngineVersionsOutput, error) {
	m.calls++
	return &awsrds.DescribeDBMajorEngineVersionsOutput{
		DBMajorEngineVersions: []rdstypes.DBMajorEngineVersion{
			rdsMajorVersion("mysql", fmt.Sprintf("5.%d", m.calls)),
		},
		Marker: aws.String(fmt.Sprintf("tok%d", m.calls)),
	}, nil
}

// TestFetchMajorEngineVersionsForEngine_PaginationCapError asserts that
// exceeding maxEngineVersionPages returns a diagnostic error (issue #692).
func TestFetchMajorEngineVersionsForEngine_PaginationCapError(t *testing.T) {
	mock := &alwaysNextPageRDSMock{}
	versionInfo := make(map[string]MajorEngineVersionInfo)

	err := fetchMajorEngineVersionsForEngine(context.Background(), mock, "mysql", versionInfo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Equal(t, maxEngineVersionPages, mock.calls,
		"must stop exactly at the cap")
}

// cancelOnFirstQueryRDSMock cancels the context from inside the first API call
// and returns the SDK-shaped error a canceled request produces, so the fan-out
// loop sees both a real ctx cancellation and a wrapped context error.
type cancelOnFirstQueryRDSMock struct {
	cancel         context.CancelFunc
	enginesQueried []string
	wrap           func(error) error
}

func (m *cancelOnFirstQueryRDSMock) DescribeDBMajorEngineVersions(
	_ context.Context,
	params *awsrds.DescribeDBMajorEngineVersionsInput,
	_ ...func(*awsrds.Options),
) (*awsrds.DescribeDBMajorEngineVersionsOutput, error) {
	m.enginesQueried = append(m.enginesQueried, aws.ToString(params.Engine))
	m.cancel()
	err := context.Canceled
	if m.wrap != nil {
		err = m.wrap(err)
	}
	return nil, err
}

// TestQueryMajorEngineVersionsWithClient_CtxCancelIsTerminal asserts that a
// canceled context stops the per-engine fan-out instead of being downgraded to
// a warning: the loop must return the error and must not keep querying the
// remaining engines (issue #1325).
func TestQueryMajorEngineVersionsWithClient_CtxCancelIsTerminal(t *testing.T) {
	tests := []struct {
		name string
		wrap func(error) error
	}{
		{name: "bare context.Canceled"},
		{
			// The AWS SDK wraps transport errors; the mock cancels the real
			// context, so detection via ctx.Err() works regardless of wrapping.
			name: "SDK-wrapped context.Canceled",
			wrap: func(err error) error {
				return fmt.Errorf("operation error RDS: DescribeDBMajorEngineVersions, %w", err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mock := &cancelOnFirstQueryRDSMock{cancel: cancel, wrap: tt.wrap}

			result, err := queryMajorEngineVersionsWithClient(ctx, mock)

			require.ErrorIs(t, err, context.Canceled,
				"a canceled context must not be reported as a successful query")
			assert.Nil(t, result,
				"partial version info must not be handed back as if it were complete")
			assert.Equal(t, []string{"mysql"}, mock.enginesQueried,
				"cancellation must stop the fan-out after the first engine")
		})
	}
}

// TestQueryMajorEngineVersionsWithClient_CtxDeadlineIsTerminal asserts the same
// for an expired deadline (issue #1325): it fails fast before the first call.
func TestQueryMajorEngineVersionsWithClient_CtxDeadlineIsTerminal(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Minute))
	defer cancel()

	mock := &cancelOnFirstQueryRDSMock{cancel: func() {}}

	_, err := queryMajorEngineVersionsWithClient(ctx, mock)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, mock.enginesQueried,
		"an expired deadline must not issue API calls")
}

// TestQueryMajorEngineVersionsWithClient_CtxAlreadyCanceled asserts that a
// context canceled before the call fails fast without spending a single API
// call (issue #1325).
func TestQueryMajorEngineVersionsWithClient_CtxAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := &cancelOnFirstQueryRDSMock{cancel: func() {}}

	_, err := queryMajorEngineVersionsWithClient(ctx, mock)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, mock.enginesQueried,
		"an already-canceled context must not issue API calls")
}
