package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}

func TestFetchEngineVersionData_CancellationAndDeadlineAreTerminal(t *testing.T) {
	captureAppLog(t)
	for name, ctx := range map[string]context.Context{"canceled": canceledCtx(), "deadline": expiredCtx(t)} {
		_, err := fetchEngineVersionData(ctx, Config{}, true)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ctx.Err(), name)
	}
}

func TestFilterAndAdjustRecommendations_CancellationAndDeadlineAreTerminal(t *testing.T) {
	for name, ctx := range map[string]context.Context{"canceled": canceledCtx(), "deadline": expiredCtx(t)} {
		recs := []common.Recommendation{{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 2}}
		got, err := filterAndAdjustRecommendations(ctx, recs, 100, Config{})
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ctx.Err(), name)
		assert.Empty(t, got, name)
	}
}

func TestPrepareCSVPurchaseRun_CanceledContextStopsBeforeConfirmationAndPurchase(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "in.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("Service,Region,ResourceType,Count,Engine,Term,PaymentOption\nrds,us-east-1,db.t3.small,2,mysql,1yr,no-upfront\n"), 0o600))
	cfg := Config{CSVInput: csvPath, AuditLog: filepath.Join(dir, "audit.jsonl")}

	for _, dry := range []bool{true, false} {
		recs, _, _, err := prepareCSVPurchaseRun(canceledCtx(), cfg, 100, dry)
		require.ErrorIs(t, err, context.Canceled, "dryRun=%v", dry)
		assert.Empty(t, recs)
	}
}

func TestFetchExistingCoverage_CanceledContextIsTerminalEvenOnDryRun(t *testing.T) {
	realClient := awsprovider.NewRecommendationsClientDirect(aws.Config{Region: "us-east-1"})
	cfg := Config{TargetCoverage: 80, Regions: []string{"us-east-1"}, ActualPurchase: false}

	got, err := fetchExistingCoverage(canceledCtx(), aws.Config{Region: "us-east-1"}, realClient, cfg)

	require.ErrorIs(t, err, context.Canceled, "a dry run used to swallow this and size against an empty map")
	assert.Nil(t, got)
}

func TestDetermineRegionsForService_CanceledContextIsTerminal(t *testing.T) {
	captureAppLog(t)
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.Anything).Return(nil, canceledCtx().Err())

	regions, err := determineRegionsForService(canceledCtx(), client, common.ServiceRDS, Config{TermYears: 1})

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, regions)
}

func TestDetermineRegionsForService_DiscoversRegionsTheCallerHasNotOptedInto(t *testing.T) {
	captureAppLog(t)
	cfg := Config{TermYears: 1, PaymentOption: "no-upfront", Coverage: 100}
	rec := func(region string) common.Recommendation {
		return common.Recommendation{Service: common.ServiceEC2, Region: region, ResourceType: "m5.large", Count: 1}
	}
	client := &MockRecommendationsClient{}
	// aws.Config{} has no credentials, so any ec2:DescribeRegions call would
	// fail: the regions must come from the recommendations.
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "" })).
		Return([]common.Recommendation{rec("us-east-1"), rec("eu-central-2"), rec("eu-central-2")}, nil).Once()

	regions, err := determineRegionsForService(context.Background(), client, common.ServiceEC2, cfg)

	require.NoError(t, err)
	assert.Equal(t, []string{"eu-central-2", "us-east-1"}, regions)
}

func TestFetchAllRecs_DefaultRunKeepsRecommendationsInRegionsOutsideTheCallersOptIns(t *testing.T) {
	captureAppLog(t)
	cfg := Config{TermYears: 1, PaymentOption: "no-upfront", Coverage: 100}
	rec := func(region string) common.Recommendation {
		return common.Recommendation{Service: common.ServiceRDS, Region: region, ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	}
	client := &MockRecommendationsClient{}
	// Cost Explorer answers the same account-wide list for every call; the
	// per-region pass keeps the rows of its own region.
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Return([]common.Recommendation{rec("us-east-1"), rec("eu-central-2")}, nil)

	awsCfg, awsStub := newAWSFixtureConfig(t)
	recs, _, err := fetchAllRecs(context.Background(), awsCfg, client, NewAccountAliasCache(awsCfg), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	require.NoError(t, err)
	// One duplicate check per region: us-east-1 and eu-central-2.
	awsStub.requireRequests(t, map[string]int{
		"rds.us-east-1.amazonaws.com DescribeReservedDBInstances":    1,
		"rds.eu-central-2.amazonaws.com DescribeReservedDBInstances": 1,
	})
	regions := make([]string, 0, len(recs))
	for i := range recs {
		regions = append(regions, recs[i].Region)
	}
	assert.ElementsMatch(t, []string{"us-east-1", "eu-central-2"}, regions)
}

func TestFetchRecommendationsForRegion_InterruptIsNotReportedAsProviderFailure(t *testing.T) {
	out := captureAppLog(t)
	ctx := canceledCtx()
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.Anything).Return(nil, ctx.Err())

	got, err := fetchRecommendationsForRegion(ctx, client, common.ServiceRDS, "us-east-1", Config{TermYears: 1})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, got)
	assert.NotContains(t, out.String(), "Failed to fetch recommendations")
}

func TestFetchAllRecs_InterruptStopsFanOutAndDiscardsPartialData(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Regions: []string{"us-east-1", "us-west-2", "eu-west-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
		Run(func(mock.Arguments) { cancel() }). // Ctrl-C after the first region answered
		Return([]common.Recommendation{rec}, nil).Once()

	recs, _, _ := fetchAllRecs(ctx, aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	assert.Empty(t, recs, "partial data from an interrupted fetch is not a result")
	client.AssertNumberOfCalls(t, "GetRecommendations", 1) // later regions are never queried
}

func TestFetchAllRecs_ProviderErrorFailsTheRun(t *testing.T) {
	captureAppLog(t)
	cfg := Config{Regions: []string{"us-east-1", "us-west-2"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
		Return(nil, errors.New("expired SSO token")).Once()

	recs, _, err := fetchAllRecs(context.Background(), aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	require.Error(t, err, "a fetch failure must not read as 'no recommendations'")
	assert.Contains(t, err.Error(), "expired SSO token")
	assert.Empty(t, recs)
	client.AssertNumberOfCalls(t, "GetRecommendations", 1)
}

func TestFetchAllRecs_EmptyResultIsNotAnError(t *testing.T) {
	captureAppLog(t)
	cfg := Config{Regions: []string{"us-east-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.Anything).Return([]common.Recommendation{}, nil)

	recs, _, err := fetchAllRecs(context.Background(), aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestCheckDuplicates_InterruptedCheckIsTerminalInDryAndRealRuns(t *testing.T) {
	captureAppLog(t)
	recs := []common.Recommendation{{ResourceType: "db.t3.small", Count: 5}}
	for _, dry := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		client := &MockServiceClient{}
		client.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), context.Canceled).Run(func(mock.Arguments) { cancel() })

		drops := common.NewDropSummary()
		got := checkDuplicates(ctx, recs, client, dry, drops)

		assert.Nil(t, got, "dryRun=%v: never continue with un-deduplicated counts after an interrupt", dry)
		assert.Equal(t, 0, drops.Total(), "dryRun=%v: an interrupt is not a failed duplicate check", dry)
		cancel()
	}
}

func TestCheckDuplicatesForCSVRegion_InterruptedCheckStopsEvenOnDryRun(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &MockServiceClient{}
	client.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), context.Canceled).Run(func(mock.Arguments) { cancel() })

	got, ok := checkDuplicatesForCSVRegion(ctx, []common.Recommendation{{ResourceType: "db.t3.small", Count: 5}}, client, common.ServiceRDS, "us-east-1", true)

	assert.False(t, ok, "a dry run used to continue with un-deduplicated counts")
	assert.Nil(t, got)
}

func TestFetchAllRecs_InterruptDuringLastRegionDiscardsItsData(t *testing.T) {
	captureAppLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Regions: []string{"us-east-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	client := &MockRecommendationsClient{}
	rec := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).
		Return([]common.Recommendation{rec}, nil).Once()

	recs, _, _ := fetchAllRecs(ctx, aws.Config{}, client, NewAccountAliasCache(aws.Config{}), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	assert.Empty(t, recs)
}

// An --exclude-accounts name cannot be matched when the account's name lookup
// fails. The old code replaced the name with the ID, so the excluded account's
// recommendations passed the filter and could be purchased.
func TestFetchAllRecs_AccountFilters(t *testing.T) {
	captureAppLog(t)
	rec := func(account string) common.Recommendation {
		return common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", Account: account, ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10}
	}
	base := Config{Regions: []string{"us-east-1"}, Coverage: 100, TermYears: 1, PaymentOption: "partial-upfront"}
	// Member accounts cannot call DescribeAccount: every lookup is denied.
	dupChecks := 0
	run := func(t *testing.T, cfg Config, accounts ...string) (accountsOut []string, orgCalls int, err error) {
		t.Helper()
		found := make([]common.Recommendation, 0, len(accounts))
		for _, a := range accounts {
			found = append(found, rec(a))
		}
		client := &MockRecommendationsClient{}
		client.On("GetRecommendations", mock.Anything, mock.Anything).Return(found, nil)
		org := &MockOrganizationsClient{}
		org.On("DescribeAccount", mock.Anything, mock.Anything).Return(nil, errors.New("AccessDenied"))
		awsCfg, awsStub := newAWSFixtureConfig(t)
		recs, _, err := fetchAllRecs(context.Background(), awsCfg, client, NewAccountAliasCacheWithClient(org), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)
		for _, n := range awsStub.received {
			dupChecks += n
		}
		for i := range recs {
			accountsOut = append(accountsOut, recs[i].Account)
		}
		return accountsOut, len(org.Calls), err
	}

	t.Run("name-based exclude with a failed lookup fails the run", func(t *testing.T) {
		cfg := base
		cfg.ExcludeAccounts = []string{"sandbox"}
		got, _, err := run(t, cfg, testCallerAccount)
		require.Error(t, err)
		assert.Contains(t, err.Error(), testCallerAccount)
		assert.Empty(t, got)
	})
	t.Run("name-based include with a failed lookup fails the run", func(t *testing.T) {
		cfg := base
		cfg.IncludeAccounts = []string{"production"}
		_, _, err := run(t, cfg, testCallerAccount)
		require.Error(t, err)
	})
	t.Run("ID-based exclude works without names", func(t *testing.T) {
		cfg := base
		cfg.ExcludeAccounts = []string{testCallerAccount}
		got, _, err := run(t, cfg, testCallerAccount, otherAccount)
		require.NoError(t, err)
		assert.Equal(t, []string{otherAccount}, got)
	})
	t.Run("ID-based include works without names", func(t *testing.T) {
		cfg := base
		cfg.IncludeAccounts = []string{otherAccount}
		got, _, err := run(t, cfg, testCallerAccount, otherAccount)
		require.NoError(t, err)
		assert.Equal(t, []string{otherAccount}, got)
	})
	t.Run("no filter keeps the ID as the name", func(t *testing.T) {
		got, _, err := run(t, base, testCallerAccount)
		require.NoError(t, err)
		assert.Equal(t, []string{testCallerAccount}, got)
	})
	t.Run("a lasting lookup failure costs one call per account", func(t *testing.T) {
		cfg := base
		cfg.ExcludeAccounts = []string{testCallerAccount}
		accounts := make([]string, 20)
		for i := range accounts {
			accounts[i] = otherAccount
		}
		_, calls, err := run(t, cfg, accounts...)
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
	})
	assert.Equal(t, 4, dupChecks, "duplicate checks served by the fixture stub")
}

func TestRequireResolvedAccountNames_CSVRows(t *testing.T) {
	rows := []common.Recommendation{{Account: testCallerAccount, AccountName: testCallerAccount}}
	// The CSV wrote the ID where the name lookup had failed.
	require.Error(t, requireResolvedAccountNames(rows, &Config{ExcludeAccounts: []string{"sandbox"}}))
	require.NoError(t, requireResolvedAccountNames(rows, &Config{ExcludeAccounts: []string{testCallerAccount}}))
	require.NoError(t, requireResolvedAccountNames(rows, &Config{}))
	named := []common.Recommendation{{Account: testCallerAccount, AccountName: "sandbox"}}
	require.NoError(t, requireResolvedAccountNames(named, &Config{ExcludeAccounts: []string{"sandbox"}}))
	assert.False(t, shouldIncludeAccountIDOrName(testCallerAccount, "sandbox", &Config{ExcludeAccounts: []string{testCallerAccount}}), "an ID filter matches a named account")
}

func TestRequireNamesResolved_BlankFilterValueIsNotANameFilter(t *testing.T) {
	unresolved := []common.Recommendation{{Account: testCallerAccount, AccountName: testCallerAccount}}

	require.NoError(t, requireNamesResolved(unresolved, &Config{ExcludeAccounts: []string{" "}}))
	require.Error(t, requireNamesResolved(unresolved, &Config{ExcludeAccounts: []string{" ", "sandbox"}}))
}

func TestFilterAndAdjustRecommendations_NameExcludeOnCSVRowsWithoutNamesFails(t *testing.T) {
	captureAppLog(t)
	rows := []common.Recommendation{{Service: common.ServiceRDS, Region: "us-east-1", Account: testCallerAccount, AccountName: testCallerAccount, ResourceType: "db.t3.small", Count: 2}}

	recs, err := filterAndAdjustRecommendations(context.Background(), rows, 100, Config{ExcludeAccounts: []string{"sandbox"}, IncludeExtendedSupport: true})

	require.Error(t, err, "replay must not let an excluded account through because its name is missing")
	assert.Empty(t, recs)
}

func TestFetchAllRecs_ExcludeAccountsExactInAnotherBatchIsNotAmbiguous(t *testing.T) {
	captureAppLog(t)
	rec := func(region, account string) common.Recommendation {
		return common.Recommendation{Service: common.ServiceRDS, Region: region, ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10, Account: account}
	}
	client := &MockRecommendationsClient{}
	// Region discovery sees both; each per-region batch sees only its own account.
	usProd, euProd := rec("us-east-1", "123456789012"), rec("eu-central-2", "999888777666")
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "" })).
		Return([]common.Recommendation{usProd, euProd}, nil)
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
		Return([]common.Recommendation{usProd}, nil)
	client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "eu-central-2" })).
		Return([]common.Recommendation{euProd}, nil)
	org := &MockOrganizationsClient{}
	for id, name := range map[string]string{"123456789012": "prod", "999888777666": "prod-eu"} {
		org.On("DescribeAccount", mock.Anything, mock.MatchedBy(func(in *organizations.DescribeAccountInput) bool { return *in.AccountId == id })).
			Return(&organizations.DescribeAccountOutput{Account: &orgtypes.Account{Name: aws.String(name)}}, nil)
	}
	cfg := Config{TermYears: 1, PaymentOption: "no-upfront", Coverage: 100, ExcludeAccounts: []string{"prod"}}

	awsCfg, _ := newAWSFixtureConfig(t)
	recs, _, err := fetchAllRecs(context.Background(), awsCfg, client, NewAccountAliasCacheWithClient(org), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)

	require.NoError(t, err)
	for i := range recs {
		assert.NotEqual(t, "prod", recs[i].AccountName)
	}
}

func TestFetchAllRecs_ExcludeAccountsPartialOnlyIsRefused(t *testing.T) {
	captureAppLog(t)
	rec := func(region, account string) common.Recommendation {
		return common.Recommendation{Service: common.ServiceRDS, Region: region, ResourceType: "db.t3.small", Count: 3, EstimatedSavings: 10, SavingsPercentage: 10, Account: account}
	}
	usProd, euProd := rec("us-east-1", "123456789012"), rec("eu-central-2", "999888777666")
	run := func(t *testing.T, exclude string) ([]common.Recommendation, error) {
		t.Helper()
		client := &MockRecommendationsClient{}
		client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "" })).
			Return([]common.Recommendation{usProd, euProd}, nil)
		client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "us-east-1" })).
			Return([]common.Recommendation{usProd}, nil)
		client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool { return p.Region == "eu-central-2" })).
			Return([]common.Recommendation{euProd}, nil)
		org := &MockOrganizationsClient{}
		for id, name := range map[string]string{"123456789012": "prod-us", "999888777666": "prod-eu"} {
			org.On("DescribeAccount", mock.Anything, mock.MatchedBy(func(in *organizations.DescribeAccountInput) bool { return *in.AccountId == id })).
				Return(&organizations.DescribeAccountOutput{Account: &orgtypes.Account{Name: aws.String(name)}}, nil)
		}
		cfg := Config{TermYears: 1, PaymentOption: "no-upfront", Coverage: 100, ExcludeAccounts: []string{exclude}}
		awsCfg, _ := newAWSFixtureConfig(t)
		recs, _, err := fetchAllRecs(context.Background(), awsCfg, client, NewAccountAliasCacheWithClient(org), []common.ServiceType{common.ServiceRDS}, engineVersionData{}, cfg, nil)
		return recs, err
	}

	t.Run("a substring matching no account exactly is refused", func(t *testing.T) {
		recs, err := run(t, "prod")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "matches no account exactly")
		assert.Contains(t, err.Error(), "prod-us")
		assert.Contains(t, err.Error(), "prod-eu")
		assert.Empty(t, recs)
	})
	t.Run("an exact name in another case excludes only that account", func(t *testing.T) {
		recs, err := run(t, "PROD-US")
		require.NoError(t, err)
		require.Len(t, recs, 1)
		assert.Equal(t, "prod-eu", recs[0].AccountName)
	})
}
