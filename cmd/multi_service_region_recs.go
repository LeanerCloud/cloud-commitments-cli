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

// applyCoverageAndOverrides applies sizing (coverage % or target-coverage)
// and count overrides. Sizing mode is selected by cfg.TargetCoverage: > 0
// routes to ApplyTargetCoverage; otherwise the legacy ApplyCoverage path.
// coverageMap (when non-nil) populates Recommendation.ExistingCoveragePct
// before sizing so the under-buy formula can subtract pool coverage already
// owned. Nil map = no-op; recs keep their default zero values and the
// sizing path falls back to the no-existing-commitments formula.
//
// expiringCommitments (when non-empty and cfg.RebuyWindowDays > 0) reduces
// ExistingCoveragePct further by the share of pool demand attributable to
// RIs expiring within the window, net of replacement RIs started inside the
// coverage lookback (so a re-run does not buy them again), so --target-coverage
// recommends replacements before the cliff. Doesn't run if either input is empty.
// RDS recs sized by the family-NU pass (target-coverage mode) ignore that
// per-rec adjustment and instead get the same commitments and window through
// ApplyFamilyNUSizingRDSWithExpiryOptions, which deducts expiring normalized units
// from the family's coverage and credits replacement RIs bought inside the
// coverage lookback (so a re-run does not buy them again); this deliberately
// raises RDS counts and costs when RIs are about to expire (cli#2132). Callers passing nil commitments
// get the plain ApplyFamilyNUSizingRDS behavior.
//
// drops accumulates per-reason drop counts for the end-of-run summary.
// Pass nil to skip tracking.
func applyCoverageAndOverrides(recs []common.Recommendation, cfg Config, coverageMap recommendations.PoolCoverageMap, expiringCommitments []common.Commitment, drops *common.DropSummary) []common.Recommendation {
	recommendations.ApplyCoverageMapToRecommendations(recs, coverageMap)
	if cfg.RebuyWindowDays > 0 && len(expiringCommitments) > 0 {
		n, missingDemand := recommendations.AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(
			recs, expiringCommitments, coverageMap,
			recommendations.PoolExpiryOptions{WindowDays: cfg.RebuyWindowDays, CoverageLookbackDays: coverageLookbackDays(cfg)})
		if n > 0 {
			AppLogger.Printf("  ⏰ Treating %d recs as partially uncovered (RIs expiring within %d days)\n", n, cfg.RebuyWindowDays)
		}
		if missingDemand > 0 {
			AppLogger.Printf("  ⚠️  Skipped expiry adjustment for %d recommendations because pool demand was unavailable; coverage was left unchanged\n", missingDemand)
		}
	}
	// Family-NU sizing for RDS recs: AWS rec API already bundles size-flex
	// demand within a family into one rec at one size, so per-pool sizing
	// under-buys. Run the family-NU pass first (RDS only, target-coverage
	// mode only); non-RDS recs flow through the per-pool path unchanged.
	// When TargetCoverage isn't set (legacy --coverage path) the per-pool
	// flow handles everything as before.
	var sizedRDS []common.Recommendation
	rest := recs
	if cfg.TargetCoverage > 0 {
		var familyDrops recommendations.FamilyDropCounts
		var expiry recommendations.FamilyExpiryReport
		lookbackDays := coverageLookbackDays(cfg)
		sizedRDS, rest, familyDrops, expiry = recommendations.ApplyFamilyNUSizingRDSWithExpiryOptions(
			recs, coverageMap, cfg.TargetCoverage, expiringCommitments,
			recommendations.FamilyExpiryOptions{WindowDays: cfg.RebuyWindowDays, CoverageLookbackDays: lookbackDays})
		logFamilyExpiry(expiry, cfg.RebuyWindowDays, lookbackDays)
		drops.Add(common.DropFamilyAlreadyAtTarget, familyDrops.AlreadyAtTarget)
		drops.Add(common.DropFamilyNoNUSignal, familyDrops.NoNUSignal)
		drops.Add(common.DropFamilySizedToZero, familyDrops.SizedToZero)
	}
	filteredRecs := applySizing(rest, cfg, cfg.Coverage, drops)
	filteredRecs = append(filteredRecs, sizedRDS...)
	if cfg.TargetCoverage > 0 {
		AppLogger.Printf("  🎯 Applying %.1f%% target-coverage: %d recommendations selected (%d via family-NU, %d via per-pool)\n",
			cfg.TargetCoverage, len(filteredRecs), len(sizedRDS), len(filteredRecs)-len(sizedRDS))
	} else {
		AppLogger.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", cfg.Coverage, len(filteredRecs))
	}

	// Apply count override if specified
	if cfg.OverrideCount > 0 {
		filteredRecs = ApplyCountOverride(filteredRecs, cfg.OverrideCount)
	}

	return filteredRecs
}

// logFamilyExpiry reports how rebuy-window expiry was folded into the RDS
// family-NU sizing. Unusable RIs (unparsable instance class, or an engine
// spelled differently from the coverage report) were NOT deducted, so they are
// warned about rather than read as "no expiry".
func logFamilyExpiry(expiry recommendations.FamilyExpiryReport, windowDays, lookbackDays int) {
	if expiry.ExpiringNU > 0 {
		// ExpiringNU is the deduction actually applied after the per-family
		// covered/active caps (go#342, go#533), so nothing is logged when it is 0.
		AppLogger.Printf("  ⏰ RDS family sizing: %.0f normalized units of RIs expiring within %d days considered against family coverage; %.0f units of replacement RIs started in the last %d days credited back\n", expiry.ExpiringNU, windowDays, expiry.ReplacementNU, lookbackDays)
	}
	if expiry.Unusable > 0 {
		AppLogger.Printf("  ⚠️  %d RDS reserved instances expiring within %d days could not be applied to family sizing (unparsable instance class or no matching coverage family); their expiry was NOT deducted\n", expiry.Unusable, windowDays)
	}
}
