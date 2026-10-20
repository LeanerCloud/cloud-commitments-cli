package main

import (
	"context"
	"errors"
	"fmt"

	azureprovider "github.com/LeanerCloud/cloud-commitments-go/providers/azure"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

// errRecommendationFetch marks a failure to obtain recommendations from the
// provider, as opposed to a successful but empty answer.
var errRecommendationFetch = errors.New("recommendation fetch failed")

// fetchRecommendationsForRegion fetches recommendations from AWS for a specific region.
// A provider failure is returned, not logged away: an empty slice with a nil
// error means the provider answered and had nothing to recommend. An
// incomplete-but-usable answer (partial sweep) is returned with a warning.
func fetchRecommendationsForRegion(
	ctx context.Context,
	recClient provider.RecommendationsClient,
	service common.ServiceType,
	region string,
	cfg Config,
) ([]common.Recommendation, error) {
	termStr := "1yr"
	if cfg.TermYears == 3 {
		termStr = "3yr"
	}

	lookback := cfg.RecLookbackPeriod
	if lookback == "" {
		lookback = recommendations.DefaultRecLookbackPeriod
	}
	params := common.RecommendationParams{
		Service:        service,
		PaymentOption:  cfg.PaymentOption,
		Term:           termStr,
		LookbackPeriod: lookback,
		// Savings Plans specific filters
		IncludeSPTypes: cfg.IncludeSPTypes,
		ExcludeSPTypes: cfg.ExcludeSPTypes,
		SPAccountScope: cfg.SPAccountScope,
		RIAccountScope: cfg.RIAccountScope,
	}
	// For Savings Plans, region is only the endpoint the account-level call is
	// signed in (spAPIRegion), not a filter. The provider folds params.Region
	// into its include-region set, which would drop every EC2 Instance plan
	// recommended outside that endpoint region.
	if !common.IsSavingsPlan(service) {
		params.Region = region
	}

	recs, err := recClient.GetRecommendations(ctx, &params)
	stampSavingsPlanRegions(recs)
	var incomplete *recommendations.IncompleteRecommendationsError
	if errors.As(err, &incomplete) {
		AppLogger.Printf("  ⚠️  %v\n", incomplete)
		return recs, nil
	}
	if partial := azureprovider.AsPartialSubscriptionFailure(err); partial != nil {
		// Keep the subscriptions that did answer, but say plainly that the
		// sweep was incomplete: without this the operator would read a short
		// list as "little to buy here" rather than "some subscriptions were
		// never queried".
		AppLogger.Printf("  ⚠️  Incomplete: %d of %d Azure subscriptions succeeded; %d could not be queried\n",
			partial.Succeeded, partial.Attempted, len(partial.Failed))
		for _, f := range partial.Failed {
			AppLogger.Printf("      subscription %s: %v\n", f.SubscriptionID, f.Err)
		}
		return recs, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Interrupted, not a provider failure: the caller stops on ctx.Err().
		return nil, ctxErr
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s in %s: %w", errRecommendationFetch, getServiceDisplayName(service), region, err)
	}

	return recs, nil
}

// stampSavingsPlanRegions copies the region Cost Explorer reports for an EC2
// Instance Savings Plan (kept in Details) into Recommendation.Region, which is
// what the table, CSV and audit log print. Account-level plans have no region
// and are left empty.
func stampSavingsPlanRegions(recs []common.Recommendation) {
	for i := range recs {
		if recs[i].CommitmentType == common.CommitmentSavingsPlan {
			recs[i].Region = awsprovider.EffectiveRegion(recs[i])
		}
	}
}

// populateRecommendationAccountNames populates account names from account IDs.
// An account whose name cannot be resolved gets its ID as the name (member
// accounts cannot call organizations:DescribeAccount). That is fine for
// ID-based account filters; requireNamesResolved fails the run when a
// name-based --include-accounts / --exclude-accounts value needs the name.
func populateRecommendationAccountNames(ctx context.Context, recs []common.Recommendation, accountCache *AccountAliasCache, cfg Config) error {
	for i := range recs {
		if recs[i].Account != "" {
			recs[i].AccountName = accountCache.GetAccountAlias(ctx, recs[i].Account)
		}
	}
	return requireNamesResolved(recs, &cfg)
}

// applyRegionFilters applies region and instance type filters to recommendations.
func applyRegionFilters(
	recs []common.Recommendation,
	engineData engineVersionData,
	region string,
	cfg Config,
	drops *common.DropSummary,
) []common.Recommendation {
	originalCount := len(recs)
	recs = applyFilters(recs, &cfg, engineData.instanceVersions, engineData.versionInfo, region, drops)

	if len(recs) < originalCount {
		AppLogger.Printf("  🔍 After filters: %d recommendations (filtered out %d)\n", len(recs), originalCount-len(recs))
	}

	return recs
}
