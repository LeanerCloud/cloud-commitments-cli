package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

// TestApplyFilters_SavingsPlansRegionFilters covers the CLI half of #1582.
// Savings Plans recommendations never populate the top-level rec.Region
// (parser_sp.go stores the CE-supplied region in Details.Region instead), so
// filtering on the bare field dropped every SP recommendation whenever
// --include-regions was set, and leaked region-scoped EC2Instance SPs past
// --exclude-regions. The provider-side filters were fixed first; these cases
// pin the same semantics on the CLI path.
func TestApplyFilters_SavingsPlansRegionFilters(t *testing.T) {
	ec2SP := func(region string) common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderAWS,
			Service:        common.ServiceSavingsPlansEC2Instance,
			CommitmentType: common.CommitmentSavingsPlan,
			Count:          1,
			Details: &common.SavingsPlanDetails{
				PlanType:       "EC2Instance",
				InstanceFamily: "m5",
				Region:         region,
			},
		}
	}
	computeSP := func() common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderAWS,
			Service:        common.ServiceSavingsPlansCompute,
			CommitmentType: common.CommitmentSavingsPlan,
			Count:          1,
			Details:        &common.SavingsPlanDetails{PlanType: "Compute"},
		}
	}

	tests := []struct {
		name           string
		rec            common.Recommendation
		includeRegions []string
		excludeRegions []string
		wantKept       bool
	}{
		{
			name:           "EC2Instance SP in the included region survives",
			rec:            ec2SP("us-east-1"),
			includeRegions: []string{"us-east-1"},
			wantKept:       true,
		},
		{
			name:           "EC2Instance SP outside the included region is dropped",
			rec:            ec2SP("eu-west-1"),
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
		{
			name:           "region-agnostic Compute SP survives an include filter",
			rec:            computeSP(),
			includeRegions: []string{"us-east-1"},
			wantKept:       true,
		},
		{
			name:           "EC2Instance SP in an excluded region is dropped",
			rec:            ec2SP("eu-west-1"),
			excludeRegions: []string{"eu-west-1"},
			wantKept:       false,
		},
		{
			name:           "region-agnostic Compute SP survives an exclude filter",
			rec:            computeSP(),
			excludeRegions: []string{"eu-west-1"},
			wantKept:       true,
		},
		{
			// Cost Explorer omitted SavingsPlansDetails.Region. An EC2Instance
			// SP is region-scoped, so an unknown region must not be treated as
			// region-agnostic and waved past an explicit region filter.
			name:           "EC2Instance SP with an unknown region is not over-included",
			rec:            ec2SP(""),
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
		{
			name:           "reservation rec with an unknown region is still dropped",
			rec:            common.Recommendation{Service: common.ServiceRDS, CommitmentType: common.CommitmentReservedInstance, Count: 1},
			includeRegions: []string{"us-east-1"},
			wantKept:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				IncludeRegions: tt.includeRegions,
				ExcludeRegions: tt.excludeRegions,
			}
			want := 0
			if tt.wantKept {
				want = 1
			}
			got := applyFilters([]common.Recommendation{tt.rec}, &cfg, nil, nil, "", common.NewDropSummary())
			assert.Len(t, got, want, "region filter kept the wrong number of recommendations")
		})
	}
}

// TestApplyFilters_SavingsPlansRegionsFlag pins that --regions narrows
// region-scoped (EC2 Instance) Savings Plans by Details.Region while
// account-level plans stay unfiltered, and that it ANDs with
// --include-regions / --exclude-regions.
func TestApplyFilters_SavingsPlansRegionsFlag(t *testing.T) {
	sp := func(svc common.ServiceType, planType, region string) common.Recommendation {
		return common.Recommendation{
			Provider:       common.ProviderAWS,
			Service:        svc,
			CommitmentType: common.CommitmentSavingsPlan,
			Count:          1,
			Details:        &common.SavingsPlanDetails{PlanType: planType, Region: region},
		}
	}
	ec2 := func(region string) common.Recommendation {
		return sp(common.ServiceSavingsPlansEC2Instance, "EC2Instance", region)
	}

	tests := []struct {
		name    string
		rec     common.Recommendation
		regions []string
		include []string
		exclude []string
		want    bool
	}{
		{"EC2 SP in another region dropped", ec2("us-east-1"), []string{"eu-west-2"}, nil, nil, false},
		{"EC2 SP in listed region kept", ec2("eu-west-2"), []string{"eu-west-2"}, nil, nil, true},
		{"Compute SP kept", sp(common.ServiceSavingsPlansCompute, "Compute", ""), []string{"eu-west-2"}, nil, nil, true},
		{"SageMaker SP kept", sp(common.ServiceSavingsPlansSageMaker, "SageMaker", ""), []string{"eu-west-2"}, nil, nil, true},
		{"Database SP kept", sp(common.ServiceSavingsPlansDatabase, "Database", ""), []string{"eu-west-2"}, nil, nil, true},
		{"EC2 SP with unknown region dropped", ec2(""), []string{"eu-west-2"}, nil, nil, false},
		{"empty --regions leaves EC2 SP alone", ec2("us-east-1"), nil, nil, nil, true},
		{"regions AND include: outside include dropped", ec2("eu-west-2"), []string{"eu-west-2"}, []string{"us-east-1"}, nil, false},
		{"regions AND include: both match kept", ec2("eu-west-2"), []string{"eu-west-2", "us-east-1"}, []string{"eu-west-2"}, nil, true},
		{"regions AND exclude: excluded dropped", ec2("us-east-1"), []string{"eu-west-2", "us-east-1"}, nil, []string{"us-east-1"}, false},
		{"regions AND exclude: other kept", ec2("eu-west-2"), []string{"eu-west-2", "us-east-1"}, nil, []string{"us-east-1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Regions: tt.regions, IncludeRegions: tt.include, ExcludeRegions: tt.exclude}
			got := applyFilters([]common.Recommendation{tt.rec}, &cfg, nil, nil, "", common.NewDropSummary())
			assert.Equal(t, tt.want, len(got) == 1)
		})
	}

	t.Run("reservation rec is not narrowed by --regions here", func(t *testing.T) {
		ri := common.Recommendation{Service: common.ServiceRDS, CommitmentType: common.CommitmentReservedInstance, Region: "us-east-1", Count: 1}
		got := applyFilters([]common.Recommendation{ri}, &Config{Regions: []string{"eu-west-2"}}, nil, nil, "", common.NewDropSummary())
		assert.Len(t, got, 1)
	})

	t.Run("eu-west-2 scenario", func(t *testing.T) {
		recs := []common.Recommendation{ec2("us-east-1"), ec2("eu-west-2"), sp(common.ServiceSavingsPlansCompute, "Compute", "")}
		got := applyFilters(recs, &Config{Regions: []string{"eu-west-2"}}, nil, nil, "", common.NewDropSummary())
		if assert.Len(t, got, 2) {
			assert.Equal(t, common.ServiceSavingsPlansEC2Instance, got[0].Service)
			assert.Equal(t, "eu-west-2", got[0].Details.(*common.SavingsPlanDetails).Region)
			assert.Equal(t, common.ServiceSavingsPlansCompute, got[1].Service)
		}
	})
}
