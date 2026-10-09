package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetermineRegionsForService_SavingsPlanCollectedOnceWithConfiguredRegions(t *testing.T) {
	for _, svc := range []common.ServiceType{
		common.ServiceSavingsPlansCompute, common.ServiceSavingsPlansEC2Instance,
		common.ServiceSavingsPlansSageMaker, common.ServiceSavingsPlansDatabase,
	} {
		regions, err := determineRegionsForService(context.Background(), aws.Config{}, nil, svc, []string{"eu-west-1", "us-west-2", "ap-south-1"})
		require.NoError(t, err)
		assert.Equal(t, []string{spAPIRegion}, regions, svc)
	}
}

func TestDetermineRegionsForService_ConfiguredRegionsKeptForRegionalServices(t *testing.T) {
	regions, err := determineRegionsForService(context.Background(), aws.Config{}, nil, common.ServiceRDS, []string{"eu-west-1", "us-west-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"eu-west-1", "us-west-2"}, regions)
}

func spRow(commit float64) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceSavingsPlansCompute, Account: "111111111111", Term: "1yr", PaymentOption: "no-upfront",
		Details: &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: commit},
	}
}

func TestRejectDuplicateSavingsPlanRows(t *testing.T) {
	t.Run("identical rows rejected", func(t *testing.T) {
		err := rejectDuplicateSavingsPlanRows([]common.Recommendation{spRow(1.5), spRow(1.5)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate Savings Plan rows 1 and 2")
	})
	t.Run("differing commitment accepted", func(t *testing.T) {
		assert.NoError(t, rejectDuplicateSavingsPlanRows([]common.Recommendation{spRow(1.5), spRow(1.6)}))
	})
	t.Run("each key field distinguishes rows", func(t *testing.T) {
		base := spRow(1.5)
		mutate := map[string]func(*common.Recommendation){
			"account": func(r *common.Recommendation) { r.Account = "222222222222" },
			"term":    func(r *common.Recommendation) { r.Term = "3yr" },
			"payment": func(r *common.Recommendation) { r.PaymentOption = "all-upfront" },
			"service": func(r *common.Recommendation) { r.Service = common.ServiceSavingsPlansDatabase },
			"plan": func(r *common.Recommendation) {
				r.Details = &common.SavingsPlanDetails{PlanType: "EC2Instance", HourlyCommitment: 1.5}
			},
			"family": func(r *common.Recommendation) {
				r.Details = &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.5, InstanceFamily: "m5"}
			},
			"region": func(r *common.Recommendation) {
				r.Details = &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.5, Region: "eu-west-1"}
			},
			"offer": func(r *common.Recommendation) {
				r.Details = &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.5, OfferingID: "o-1"}
			},
		}
		for name, m := range mutate {
			other := base
			m(&other)
			assert.NoError(t, rejectDuplicateSavingsPlanRows([]common.Recommendation{base, other}), name)
		}
	})
	t.Run("non Savings Plan repeats ignored", func(t *testing.T) {
		r := common.Recommendation{Service: common.ServiceRDS, Region: "us-east-1", Account: "1"}
		assert.NoError(t, rejectDuplicateSavingsPlanRows([]common.Recommendation{r, r}))
	})
}

func TestPrepareCSVPurchaseRun_DuplicateSavingsPlanRowsRejectedBeforeConfirmation(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "in.csv")
	content := "Service,Region,ResourceType,Count,Account,Term,PaymentOption\n" +
		"savings-plans-compute,,,1,111111111111,1yr,no-upfront\n" +
		"savings-plans-compute,,,1,111111111111,1yr,no-upfront\n"
	require.NoError(t, os.WriteFile(csvPath, []byte(content), 0o600))

	for _, dry := range []bool{true, false} {
		_, _, _, err := prepareCSVPurchaseRun(context.Background(), Config{CSVInput: csvPath, AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}, 100, dry)
		require.Error(t, err, "dryRun=%v", dry)
		assert.Contains(t, err.Error(), "duplicate Savings Plan rows")
	}
}

// spStubTransport is an offline AWS RoundTripper (the library clones an
// *http.Client and keeps its Transport, so it must be wrapped in one): it records every request and
// answers DescribeSavingsPlans with an empty list and everything else with a
// non-retryable 400, so no call ever leaves the machine.
type spStubTransport struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (s *spStubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()
	status, body := http.StatusBadRequest, `{"__type":"ValidationException","message":"stubbed"}`
	if strings.HasSuffix(r.URL.Path, "/DescribeSavingsPlans") {
		status, body = http.StatusOK, `{"savingsPlans":[]}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (s *spStubTransport) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.requests))
	for _, r := range s.requests {
		out = append(out, r.URL.Path)
	}
	return out
}

func (s *spStubTransport) signedInRegion(region string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if !strings.Contains(r.Header.Get("Authorization"), "/"+region+"/") {
			return false
		}
	}
	return len(s.requests) > 0
}

func stubAWSConfig(t *testing.T) (aws.Config, *spStubTransport) {
	t.Helper()
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	stub := &spStubTransport{}
	return aws.Config{
		HTTPClient: &http.Client{Transport: stub},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, nil
		}),
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}, stub
}

// parserShapedSP is a Savings Plan recommendation as the library parser builds
// it: provider set, top-level Region empty.
func parserShapedSP() common.Recommendation {
	return common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceSavingsPlansCompute, CommitmentType: common.CommitmentSavingsPlan,
		Count: 1, Account: "111111111111", Term: "1yr", PaymentOption: "no-upfront",
		Details: &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.5},
	}
}

func TestPurchaseSingleRec_SavingsPlanFromParserReachesAPIInSPRegion(t *testing.T) {
	awsCfg, stub := stubAWSConfig(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}

	result, status := purchaseSingleRec(context.Background(), awsCfg, parserShapedSP(), 1, false, cfg)

	require.Error(t, result.Error)
	assert.NotContains(t, result.Error.Error(), "Region", "client must have a region")
	assert.Equal(t, "error", status)
	assert.NotEmpty(t, stub.paths(), "the purchase must reach the API")
	assert.True(t, stub.signedInRegion(spAPIRegion), "requests must be signed for %s", spAPIRegion)
	assert.Contains(t, result.CommitmentID, "global")
}

func TestProcessCSVRegionPurchases_SavingsPlanRowWithEmptyRegionAndProvider(t *testing.T) {
	awsCfg, stub := stubAWSConfig(t)
	cfg := Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}
	// The real loader shape: no Provider, no Region (group key "").
	rec := common.Recommendation{
		Service: common.ServiceSavingsPlansCompute, Count: 1, Account: "111111111111", Term: "1yr", PaymentOption: "no-upfront",
		Details: &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.5},
	}

	_, results, ok := processCSVRegionPurchases(context.Background(), awsCfg, rec.Service, "", []common.Recommendation{rec}, false, cfg, "run-1")

	require.True(t, ok)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.NotContains(t, results[0].Error.Error(), "Region")
	assert.Contains(t, stub.paths(), "/DescribeSavingsPlansOfferings")
	assert.True(t, stub.signedInRegion(spAPIRegion))
	assert.Contains(t, results[0].CommitmentID, "global")
}

func TestClientRegionFor(t *testing.T) {
	for _, svc := range []common.ServiceType{
		common.ServiceSavingsPlansCompute, common.ServiceSavingsPlansEC2Instance,
		common.ServiceSavingsPlansSageMaker, common.ServiceSavingsPlansDatabase,
	} {
		for _, recRegion := range []string{"", "eu-west-1"} {
			got, err := clientRegionFor(svc, recRegion)
			require.NoError(t, err)
			assert.Equal(t, spAPIRegion, got, "%s rec region %q", svc, recRegion)
		}
	}
	got, err := clientRegionFor(common.ServiceRDS, "eu-west-1")
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", got)

	_, err = clientRegionFor(common.ServiceRDS, "")
	assert.Error(t, err, "non Savings Plan service with no region fails loud")
}

func TestClientRegionFor_SavingsPlansAllNeverGetsARegion(t *testing.T) {
	for _, recRegion := range []string{"", "us-east-1"} {
		_, err := clientRegionFor(common.ServiceSavingsPlansAll, recRegion)
		assert.Error(t, err, "rec region %q", recRegion)
	}
	assert.Nil(t, createServiceClient(common.ServiceSavingsPlansAll, aws.Config{Region: spAPIRegion}))
}

func TestPurchaseRegionLabel(t *testing.T) {
	ec2sp := parserShapedSP()
	ec2sp.Details = &common.SavingsPlanDetails{PlanType: "EC2Instance", Region: "eu-west-1"}
	assert.Equal(t, "eu-west-1", purchaseRegionLabel(ec2sp, ""))
	assert.Equal(t, "global", purchaseRegionLabel(parserShapedSP(), ""))
	assert.Equal(t, "us-west-2", purchaseRegionLabel(common.Recommendation{Service: common.ServiceRDS}, "us-west-2"))
}
