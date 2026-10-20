package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetermineServicesToProcess_AllServices(t *testing.T) {
	cfg := Config{
		AllServices: true,
	}

	result, err := determineServicesToProcess(cfg)
	assert.NoError(t, err)

	// Should contain all supported services
	assert.Contains(t, result, common.ServiceRDS)
	assert.Contains(t, result, common.ServiceElastiCache)
	assert.Contains(t, result, common.ServiceEC2)
	assert.Contains(t, result, common.ServiceOpenSearch)
	assert.Contains(t, result, common.ServiceRedshift)
	assert.Contains(t, result, common.ServiceMemoryDB)
}

func TestDetermineServicesToProcess_SpecificServices(t *testing.T) {
	cfg := Config{
		AllServices: false,
		Services:    []string{"rds", "elasticache"},
	}

	result, err := determineServicesToProcess(cfg)
	assert.NoError(t, err)

	assert.Equal(t, 2, len(result))
	assert.Contains(t, result, common.ServiceRDS)
	assert.Contains(t, result, common.ServiceElastiCache)
}

func TestDetermineServicesToProcess_DefaultAndInvalid(t *testing.T) {
	for _, services := range [][]string{nil, {}} {
		result, err := determineServicesToProcess(Config{Services: services})
		assert.NoError(t, err)
		assert.Equal(t, []common.ServiceType{common.ServiceRDS}, result)
	}
	result, err := determineServicesToProcess(Config{Services: []string{"rds", "elasticahe"}})
	assert.ErrorContains(t, err, "elasticahe")
	assert.Nil(t, result)
}

func TestDetermineServicesToProcess_SavingsPlanTypeFilters(t *testing.T) {
	allSP := []common.ServiceType{
		common.ServiceSavingsPlansCompute, common.ServiceSavingsPlansEC2Instance,
		common.ServiceSavingsPlansSageMaker, common.ServiceSavingsPlansDatabase,
	}
	tests := []struct {
		name    string
		cfg     Config
		want    []common.ServiceType
		wantErr string
	}{
		{"no filter keeps all four", Config{Services: []string{"savingsplans"}}, allSP, ""},
		{"include narrows, case-insensitive", Config{Services: []string{"savingsplans"}, IncludeSPTypes: []string{"EC2Instance"}},
			[]common.ServiceType{common.ServiceSavingsPlansEC2Instance}, ""},
		{"exclude drops", Config{Services: []string{"savingsplans"}, ExcludeSPTypes: []string{"sagemaker", "Database"}},
			[]common.ServiceType{common.ServiceSavingsPlansCompute, common.ServiceSavingsPlansEC2Instance}, ""},
		{"include then exclude", Config{Services: []string{"savingsplans"}, IncludeSPTypes: []string{"compute", "ec2instance"}, ExcludeSPTypes: []string{"compute"}},
			[]common.ServiceType{common.ServiceSavingsPlansEC2Instance}, ""},
		{"non-SP services untouched", Config{Services: []string{"rds", "savingsplans"}, IncludeSPTypes: []string{"compute"}},
			[]common.ServiceType{common.ServiceRDS, common.ServiceSavingsPlansCompute}, ""},
		{"unknown include fails loud", Config{Services: []string{"savingsplans"}, IncludeSPTypes: []string{"ec2"}}, nil, `unknown Savings Plan type "ec2"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := determineServicesToProcess(tc.cfg)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
