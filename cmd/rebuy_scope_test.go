package main

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	riServices = []common.ServiceType{common.ServiceEC2}
	spServices = []common.ServiceType{common.ServiceSavingsPlansCompute}
)

func gatedCfg() Config { return Config{RebuyWindowDays: 30, TargetCoverage: 80} }

// stubCaller replaces the STS seam for one test and counts its calls.
func stubCaller(t *testing.T, account string, err error) *int {
	t.Helper()
	orig := callerAccountFetcher
	t.Cleanup(func() { callerAccountFetcher = orig })
	calls := 0
	callerAccountFetcher = func(context.Context, aws.Config) (string, error) {
		calls++
		return account, err
	}
	stubProbe(t, &fakeCoverage{t: t, noCalls: true})
	stubOrg(t, managementOrg(account))
	return &calls
}

// stubProbe replaces the Cost Explorer seam for one test.
func stubProbe(t *testing.T, fake *fakeCoverage) {
	t.Helper()
	orig := rebuyCoverageClient
	t.Cleanup(func() { rebuyCoverageClient = orig })
	rebuyCoverageClient = func(aws.Config) coverageAPI { return fake }
}

func TestEnforceRebuyScope(t *testing.T) {
	t.Run("gate on resolves the caller", func(t *testing.T) {
		calls := stubCaller(t, "111111111111", nil)
		probe := &fakeCoverage{t: t}
		stubProbe(t, probe)
		out, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.NoError(t, err)
		assert.Equal(t, "111111111111", out.CallerAccountID)
		assert.Equal(t, 1, *calls)
		require.Len(t, probe.inputs, 1, "one probe call for one RI service")
	})
	t.Run("a mixed RI and SP list is gated", func(t *testing.T) {
		calls := stubCaller(t, "111111111111", nil)
		probe := &fakeCoverage{t: t}
		stubProbe(t, probe)
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), append(spServices, riServices...))
		require.NoError(t, err)
		assert.Equal(t, 1, *calls)
		require.Len(t, probe.inputs, 1, "the SP service is not probed")
	})
	for name, tc := range map[string]struct {
		cfg      Config
		services []common.ServiceType
	}{
		"rebuy without a target":    {Config{RebuyWindowDays: 30}, riServices},
		"target without rebuy":      {Config{TargetCoverage: 80}, riServices},
		"Savings Plans only":        {gatedCfg(), spServices},
		"no services":               {gatedCfg(), nil},
		"legacy coverage, no gates": {Config{Coverage: 80}, riServices},
	} {
		t.Run("gate off: "+name, func(t *testing.T) {
			calls := stubCaller(t, "", errors.New("must not be called"))
			out, err := enforceRebuyScope(context.Background(), aws.Config{}, tc.cfg, tc.services)
			require.NoError(t, err)
			assert.Equal(t, tc.cfg, out)
			assert.Zero(t, *calls, "no STS call when the gate is off")
			// stubCaller's probe fake fails the test if it is called.
		})
	}
	t.Run("STS failure refuses", func(t *testing.T) {
		stubCaller(t, "", errors.New("expired token"))
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "the scope check could not run (")
		assert.Contains(t, err.Error(), "cannot determine the AWS account of the credentials: expired token")
		assert.NotContains(t, err.Error(), "needed to check purchases")
		assert.Contains(t, err.Error(), "fix access or retry, or omit --rebuy-window-days")
	})
	t.Run("empty caller refuses", func(t *testing.T) {
		stubCaller(t, "", nil)
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "the AWS account of the credentials is unknown")
	})
}

func TestEnforceRebuyScopeProbe(t *testing.T) {
	t.Run("foreign usage refuses", func(t *testing.T) {
		stubCaller(t, "111111111111", nil)
		stubProbe(t, &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("", "111111111111", "222222222222")}})
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "222222222222")
	})
	t.Run("probe failure refuses", func(t *testing.T) {
		stubCaller(t, "111111111111", nil)
		stubProbe(t, &fakeCoverage{t: t, err: errors.New("throttled")})
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "the scope check could not run (throttled)")
	})
	t.Run("STS failure never reaches the probe", func(t *testing.T) {
		stubCaller(t, "", errors.New("expired token"))
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		require.ErrorIs(t, err, errRebuyScope)
	})
}

func TestRebuyScopeActive(t *testing.T) {
	assert.True(t, rebuyScopeActive(gatedCfg(), riServices))
	assert.False(t, rebuyScopeActive(gatedCfg(), spServices))
	assert.False(t, rebuyScopeActive(Config{RebuyWindowDays: 30}, riServices))
	assert.False(t, rebuyScopeActive(Config{TargetCoverage: 80}, riServices))
}

func ec2AccountRec(account string) common.Recommendation {
	return common.Recommendation{Service: common.ServiceEC2, Account: account, Region: "us-east-1"}
}

func TestRequireRebuyRecScope(t *testing.T) {
	cfg := gatedCfg()
	cfg.CallerAccountID = "111111111111"
	t.Run("own and empty accounts pass", func(t *testing.T) {
		require.NoError(t, requireRebuyRecScope([]common.Recommendation{ec2AccountRec("111111111111"), ec2AccountRec("")}, cfg))
	})
	t.Run("foreign RI rec refuses with account, service, region and remedy", func(t *testing.T) {
		err := requireRebuyRecScope([]common.Recommendation{ec2AccountRec("111111111111"), ec2AccountRec("222222222222")}, cfg)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Equal(t, "ec2 recommendation for account 222222222222 in us-east-1, but the credentials are for account 111111111111; gated rebuy needs a standalone account, or a management account whose member accounts have no in-scope usage; otherwise omit --rebuy-window-days", err.Error())
	})
	t.Run("foreign SP rec is ignored", func(t *testing.T) {
		sp := ec2AccountRec("222222222222")
		sp.Service = common.ServiceSavingsPlansCompute
		require.NoError(t, requireRebuyRecScope([]common.Recommendation{sp}, cfg))
	})
	t.Run("gate off ignores foreign RI recs", func(t *testing.T) {
		off := cfg
		off.TargetCoverage = 0
		require.NoError(t, requireRebuyRecScope([]common.Recommendation{ec2AccountRec("222222222222")}, off))
		off = cfg
		off.RebuyWindowDays = 0
		require.NoError(t, requireRebuyRecScope([]common.Recommendation{ec2AccountRec("222222222222")}, off))
	})
	t.Run("empty caller refuses as a check failure", func(t *testing.T) {
		noCaller := gatedCfg()
		err := requireRebuyRecScope([]common.Recommendation{ec2AccountRec("")}, noCaller)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "the scope check could not run (")
	})
}

func TestFetchRebuyInventory(t *testing.T) {
	ctx := context.Background()
	owned := []common.Commitment{{CommitmentID: "ri-1"}}
	t.Run("returns the inventory", func(t *testing.T) {
		client := &MockServiceClient{}
		client.On("GetExistingCommitments", ctx).Return(owned, nil)
		got, err := fetchRebuyInventory(ctx, client, common.ServiceEC2, gatedCfg())
		require.NoError(t, err)
		assert.Equal(t, owned, got)
	})
	t.Run("RI failure under the gate refuses", func(t *testing.T) {
		client := &MockServiceClient{}
		client.On("GetExistingCommitments", ctx).Return(nil, errors.New("throttled"))
		got, err := fetchRebuyInventory(ctx, client, common.ServiceEC2, gatedCfg())
		require.ErrorIs(t, err, errRebuyScope)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "the scope check could not run (reading existing ec2 commitments: throttled); fix access or retry, or omit --rebuy-window-days")
	})
	t.Run("nil client for an RI service under the gate refuses", func(t *testing.T) {
		_, err := fetchRebuyInventory(ctx, nil, common.ServiceEC2, gatedCfg())
		require.ErrorIs(t, err, errRebuyScope)
	})
	t.Run("SP failure warns", func(t *testing.T) {
		client := &MockServiceClient{}
		client.On("GetExistingCommitments", ctx).Return(nil, errors.New("throttled"))
		got, err := fetchRebuyInventory(ctx, client, common.ServiceSavingsPlansCompute, gatedCfg())
		require.NoError(t, err)
		assert.Nil(t, got)
	})
	t.Run("gate off warns", func(t *testing.T) {
		for _, cfg := range []Config{{RebuyWindowDays: 30}, {RebuyWindowDays: 30, TargetCoverage: 0}} {
			client := &MockServiceClient{}
			client.On("GetExistingCommitments", ctx).Return(nil, errors.New("throttled"))
			got, err := fetchRebuyInventory(ctx, client, common.ServiceEC2, cfg)
			require.NoError(t, err)
			assert.Nil(t, got)
		}
		_, err := fetchRebuyInventory(ctx, nil, common.ServiceEC2, Config{RebuyWindowDays: 30})
		require.NoError(t, err, "nil client with the gate off is not a refusal")
	})
	t.Run("rebuy off never reads", func(t *testing.T) {
		client := &MockServiceClient{}
		got, err := fetchRebuyInventory(ctx, client, common.ServiceEC2, Config{TargetCoverage: 80})
		require.NoError(t, err)
		assert.Nil(t, got)
		client.AssertNotCalled(t, "GetExistingCommitments", ctx)
	})
}
