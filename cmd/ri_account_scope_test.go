package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidateRIAccountScope(t *testing.T) {
	orig := toolCfg.RIAccountScope
	t.Cleanup(func() { toolCfg.RIAccountScope = orig })
	for scope, wantErr := range map[string]bool{"": false, "linked": false, "payer": false, "PAYER": false, "org": true} {
		toolCfg.RIAccountScope = scope
		err := validateRIAccountScope()
		if wantErr {
			require.Error(t, err, scope)
			assert.Contains(t, err.Error(), "invalid ri-account-scope")
		} else {
			require.NoError(t, err, scope)
		}
	}
}

func TestRIAccountScopeFlagDefault(t *testing.T) {
	f := rootCmd.Flags().Lookup("ri-account-scope")
	require.NotNil(t, f)
	assert.Equal(t, "linked", f.DefValue)
}

func TestFetchRecommendationsForRegion_PassesRIAccountScope(t *testing.T) {
	captureAppLog(t)
	client := &MockRecommendationsClient{}
	var got *common.RecommendationParams
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { got = args.Get(1).(*common.RecommendationParams) }).
		Return([]common.Recommendation{}, nil)

	_, err := fetchRecommendationsForRegion(context.Background(), client, common.ServiceRDS, "us-east-1",
		Config{TermYears: 1, RIAccountScope: "payer"})
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Equal(t, "payer", got.RIAccountScope)
}
