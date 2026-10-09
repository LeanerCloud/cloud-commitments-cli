package main

import (
	"context"
	"os"
	"path/filepath"
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
