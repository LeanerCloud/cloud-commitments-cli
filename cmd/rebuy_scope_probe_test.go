package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCoverage serves canned pages in order (repeating the last one) and
// records the requests it receives.
type fakeCoverage struct {
	t       *testing.T
	pages   []*costexplorer.GetReservationCoverageOutput
	err     error
	noCalls bool
	inputs  []costexplorer.GetReservationCoverageInput
}

func (f *fakeCoverage) GetReservationCoverage(_ context.Context, in *costexplorer.GetReservationCoverageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationCoverageOutput, error) {
	if f.noCalls {
		f.t.Error("unexpected Cost Explorer call")
	}
	f.inputs = append(f.inputs, *in)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return &costexplorer.GetReservationCoverageOutput{}, nil
	}
	return f.pages[min(len(f.inputs), len(f.pages))-1], nil
}

// groupsPage is one response page whose groups carry the given accounts under
// Cost Explorer's camelCase attribute key, with an optional next-page token.
func groupsPage(next string, accounts ...string) *costexplorer.GetReservationCoverageOutput {
	groups := make([]types.ReservationCoverageGroup, 0, len(accounts))
	for _, account := range accounts {
		groups = append(groups, types.ReservationCoverageGroup{Attributes: map[string]string{"linkedAccount": account}})
	}
	out := &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: groups}}}
	if next != "" {
		out.NextPageToken = aws.String(next)
	}
	return out
}

type codedError struct{ code string }

func (e codedError) Error() string     { return e.code }
func (e codedError) ErrorCode() string { return e.code }

func probeCfg() Config {
	cfg := gatedCfg()
	cfg.CallerAccountID = "111111111111"
	return cfg
}

func TestProbeServiceAccounts(t *testing.T) {
	ctx := context.Background()
	t.Run("caller only across pages passes", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{
			groupsPage("p2", "111111111111"), groupsPage("", "111111111111", "111111111111")}}
		require.NoError(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg()))
		require.Len(t, fake.inputs, 2)
		assert.Nil(t, fake.inputs[0].NextPageToken)
		assert.Equal(t, "p2", aws.ToString(fake.inputs[1].NextPageToken))
	})
	t.Run("a member that sees only itself passes", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("", "111111111111")}}
		require.NoError(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg()))
	})
	t.Run("a standalone account with no groups passes", func(t *testing.T) {
		fake := &fakeCoverage{t: t}
		require.NoError(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg()))
	})
	t.Run("foreign account refuses at the first foreign page without paging on", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("more", "111111111111", "222222222222")}}
		err := probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Equal(t, "account 222222222222 has in-scope ec2 usage, but the credentials are for account 111111111111; gated rebuy needs a standalone account, or a management account whose member accounts have no in-scope usage; otherwise omit --rebuy-window-days", err.Error())
		assert.Len(t, fake.inputs, 1)
	})
	t.Run("foreign account only in the RDS pool refuses with the RDS filter", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("", "333333333333")}}
		err := probeServiceAccounts(ctx, fake, common.ServiceRDS, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "333333333333")
		assert.Contains(t, err.Error(), "rds")
		require.Len(t, fake.inputs, 1)
		assert.Equal(t, []string{"Amazon Relational Database Service"}, fake.inputs[0].Filter.Dimensions.Values)
	})
	t.Run("an account compared exactly: padding is foreign", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("", " 111111111111")}}
		require.ErrorIs(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg()), errRebuyScope)
	})
	t.Run("a group without the attribute refuses", func(t *testing.T) {
		page := &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: []types.ReservationCoverageGroup{{Attributes: map[string]string{"instanceType": "m5.large"}}}}}}
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{page}}
		err := probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "no linked-account attribute")
	})
	t.Run("the page cap refuses", func(t *testing.T) {
		fake := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("again", "111111111111")}}
		err := probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Len(t, fake.inputs, maxProbePages)
		assert.Contains(t, err.Error(), "more than 100 pages")
	})
	t.Run("a call error refuses", func(t *testing.T) {
		fake := &fakeCoverage{t: t, err: errors.New("throttled")}
		err := probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Equal(t, "the scope check could not run (throttled); fix access or retry, or omit --rebuy-window-days", err.Error())
	})
	t.Run("AccessDenied adds the Cost Explorer remedy", func(t *testing.T) {
		fake := &fakeCoverage{t: t, err: codedError{"AccessDeniedException"}}
		err := probeServiceAccounts(ctx, fake, common.ServiceEC2, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Equal(t, "the scope check could not run (AccessDeniedException); fix access or retry, grant ce:GetReservationCoverage or enable Cost Explorer access for this account, or omit --rebuy-window-days", err.Error())
	})
	t.Run("an unmapped service refuses without a call", func(t *testing.T) {
		fake := &fakeCoverage{t: t, noCalls: true}
		err := probeServiceAccounts(ctx, fake, common.ServiceCompute, probeCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "no Cost Explorer service mapping")
	})
}

func TestProbeRequestShape(t *testing.T) {
	ctx := context.Background()
	services := map[common.ServiceType]string{
		common.ServiceEC2:         "Amazon Elastic Compute Cloud - Compute",
		common.ServiceRDS:         "Amazon Relational Database Service",
		common.ServiceElastiCache: "Amazon ElastiCache",
		common.ServiceOpenSearch:  "Amazon OpenSearch Service",
		common.ServiceRedshift:    "Amazon Redshift",
		common.ServiceMemoryDB:    "Amazon MemoryDB",
	}
	for service, want := range services {
		fake := &fakeCoverage{t: t}
		require.NoError(t, probeServiceAccounts(ctx, fake, service, probeCfg()))
		require.Len(t, fake.inputs, 1)
		in := fake.inputs[0]
		assert.Equal(t, &types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionService, Values: []string{want}}}, in.Filter, "%s: default mode is the bare SERVICE dimension (no single-operand And)", service)
		assert.Equal(t, []types.GroupDefinition{{Type: types.GroupDefinitionTypeDimension, Key: aws.String("LINKED_ACCOUNT")}}, in.GroupBy)
		assert.Equal(t, []string{"Hour"}, in.Metrics)
	}
	t.Run("explicit regions are one REGION expression with all of them", func(t *testing.T) {
		cfg := probeCfg()
		cfg.Regions = []string{"us-east-1", "eu-west-1"}
		fake := &fakeCoverage{t: t}
		require.NoError(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, cfg))
		require.Len(t, fake.inputs[0].Filter.And, 2)
		assert.Nil(t, fake.inputs[0].Filter.Dimensions)
		assert.Equal(t, &types.DimensionValues{Key: types.DimensionService, Values: []string{"Amazon Elastic Compute Cloud - Compute"}}, fake.inputs[0].Filter.And[0].Dimensions)
		assert.Equal(t, &types.DimensionValues{Key: types.DimensionRegion, Values: []string{"us-east-1", "eu-west-1"}}, fake.inputs[0].Filter.And[1].Dimensions)
	})
	t.Run("the window is the coverage lookback", func(t *testing.T) {
		for lookback, wantDays := range map[int]int{0: 30, 7: 7} {
			cfg := probeCfg()
			cfg.CoverageLookbackDays = lookback
			fake := &fakeCoverage{t: t}
			require.NoError(t, probeServiceAccounts(ctx, fake, common.ServiceEC2, cfg))
			start, err := time.Parse(time.DateOnly, aws.ToString(fake.inputs[0].TimePeriod.Start))
			require.NoError(t, err)
			end, err := time.Parse(time.DateOnly, aws.ToString(fake.inputs[0].TimePeriod.End))
			require.NoError(t, err)
			assert.Equal(t, time.Duration(wantDays)*24*time.Hour, end.Sub(start))
		}
	})
}

func TestGroupAccount(t *testing.T) {
	for name, tc := range map[string]struct {
		attrs   map[string]string
		want    string
		wantErr string
	}{
		"camelCase key":        {map[string]string{"linkedAccount": "111111111111"}, "111111111111", ""},
		"SNAKE_CASE key":       {map[string]string{"LINKED_ACCOUNT": "111111111111"}, "111111111111", ""},
		"other attributes":     {map[string]string{"instanceType": "m5.large", "linkedAccount": "111111111111"}, "111111111111", ""},
		"value is not trimmed": {map[string]string{"linkedAccount": " 111111111111 "}, " 111111111111 ", ""},
		"missing":              {map[string]string{"instanceType": "m5.large"}, "", "no linked-account attribute"},
		"nil attributes":       {nil, "", "no linked-account attribute"},
		"empty value":          {map[string]string{"linkedAccount": ""}, "", "empty linked-account attribute"},
		"duplicate keys":       {map[string]string{"linkedAccount": "111111111111", "LINKED_ACCOUNT": "222222222222"}, "", "duplicate linked-account attributes"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := groupAccount(tc.attrs)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
