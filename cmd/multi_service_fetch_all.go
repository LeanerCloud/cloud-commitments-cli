package main

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// checkDuplicates adjusts recommendations against already-owned RIs so the run
// does not double-purchase existing capacity.
//
// It deliberately does NOT apply --max-instances. This function runs once per
// (service, region), and capping here caps each region independently, which
// multiplies the operator's cap by the number of service/region pairs. The cap
// is applied once run-wide instead, after every region has been fetched
// (applyGlobalInstanceLimit in multi_service.go).
//
// The duplicate check is the only guard between a re-run and a double
// purchase, so a failed check (throttling, a describe-call AccessDenied, a
// transient 5xx) must not fall back to the un-deduplicated counts on a
// purchase run: that would buy reserved capacity the account already owns.
// isDryRun selects the behavior on error: a dry run logs a loud warning and
// continues (nothing is bought, so reporting fidelity wins), while a purchase
// run refuses to spend and drops these recommendations entirely: an
// over-purchase of reserved capacity is not reversible.
//
// drops accumulates per-reason drop counts for the end-of-run summary; pass nil to skip.
//
// A recommendation dropped because the check itself failed is counted under
// dropDuplicateCheckFailed, not common.DropDuplicateDedup: the latter means
// "an actual duplicate was found and subtracted", and reusing it for a failed
// check would report the failure as a successful dedup in the summary.
func checkDuplicates(
	ctx context.Context,
	filteredRecs []common.Recommendation,
	serviceClient provider.ServiceClient,
	isDryRun bool,
	drops *common.DropSummary,
) []common.Recommendation {
	// Check for duplicate RIs to avoid double purchasing
	duplicateChecker := NewDuplicateChecker(toolCfg.IdempotencyWindowHours)
	adjustedRecs, dedupedOut, err := duplicateChecker.AdjustRecommendationsForExistingRIs(ctx, filteredRecs, serviceClient)
	if err != nil {
		if ctx.Err() != nil {
			// Interrupted: terminal in dry and real runs alike, never
			// "continue with un-deduplicated counts". The caller stops on ctx.Err().
			return nil
		}
		if !isDryRun {
			AppLogger.Printf("  ❌ Refusing to purchase %d instance(s): could not check for existing RIs (%v). Dropping these recommendations rather than risking a duplicate purchase.\n", CalculateTotalInstances(filteredRecs), err)
			drops.Add(dropDuplicateCheckFailed, len(filteredRecs))
			return nil
		}
		AppLogger.Printf("  ⚠️  Warning: Could not check for existing RIs: %v (dry run; continuing with un-deduplicated counts)\n", err)
		// Continue with original filteredRecs on error; adjustedRecs is not used in this branch.
	} else {
		// Always use the adjusted recommendations (they might have different counts even if same length)
		originalInstances := CalculateTotalInstances(filteredRecs)
		adjustedInstances := CalculateTotalInstances(adjustedRecs)
		if originalInstances != adjustedInstances {
			AppLogger.Printf("  🔍 Adjusted recommendations: %d instances → %d instances to avoid duplicate purchases\n", originalInstances, adjustedInstances)
		}
		drops.Add(common.DropDuplicateDedup, len(dedupedOut))
		filteredRecs = mapRecs(adjustedRecs, refreshCountLinear)
	}

	return filteredRecs
}

// fetchAndFilterRegionRecs fetches, filters, applies coverage, and deduplicates
// recommendations for a single service+region. No purchases are made.
// coverageMap (when non-nil) feeds existing-pool coverage into the sizing
// step for --target-coverage. drops accumulates per-reason drop counts for
// the end-of-run summary; pass nil to skip tracking.
func fetchAndFilterRegionRecs(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	service common.ServiceType,
	region string,
	regionIndex, totalRegions int,
	engineData engineVersionData,
	cfg Config,
	coverageMap recommendations.PoolCoverageMap,
	drops *common.DropSummary,
) ([]common.Recommendation, error) {
	AppLogger.Printf("\n  📍 [%d/%d] Region: %s\n", regionIndex, totalRegions, region)

	recs, err := fetchRecommendationsForRegion(ctx, recClient, service, region, cfg)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations found\n")
		return nil, nil
	}
	AppLogger.Printf("  ✅ Found %d recommendations\n", len(recs))

	if err := populateRecommendationAccountNames(ctx, recs, accountCache, cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", errRecommendationFetch, err)
	}
	if err := requireRegionInventory(recs, cfg, engineData); err != nil {
		return nil, err
	}
	recs = applyRegionFilters(recs, engineData, region, cfg, drops)
	if len(recs) == 0 {
		AppLogger.Printf("  ℹ️  No recommendations after applying filters\n")
		return nil, nil
	}

	// Build the regional service client once and reuse for both expiry-aware
	// coverage adjustment and the downstream duplicate check.
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = region
	serviceClient := createServiceClient(service, regionalCfg)

	// Fetch existing commitments up front when --rebuy-window-days is set so
	// the sizing step can deduct expiring RIs from ExistingCoveragePct.
	// Best-effort: a failure here logs and continues; expiry adjustment
	// becomes a no-op for this region.
	var expiringCommitments []common.Commitment
	if cfg.RebuyWindowDays > 0 && serviceClient != nil {
		commits, err := serviceClient.GetExistingCommitments(ctx)
		if err != nil {
			AppLogger.Printf("  ⚠️  Could not fetch existing commitments for expiry check: %v\n", err)
		} else {
			expiringCommitments = commits
		}
	}

	recs = applyCoverageAndOverrides(recs, cfg, coverageMap, expiringCommitments, drops)

	// Deduplication: skip recs matching recently-purchased commitments.
	// --max-instances is NOT applied here; it is enforced once run-wide by the
	// caller so the cap covers every service and region together.
	if serviceClient != nil {
		recs = checkDuplicates(ctx, recs, serviceClient, effectiveDryRun(cfg), drops)
	}

	return recs, nil
}

// fetchAllRecs collects recommendations from all services and regions without
// purchasing. coverageMap (when non-nil) populates Recommendation.ExistingCoveragePct
// on each rec before sizing, so --target-coverage can subtract what's already
// owned in the same pool. The returned DropSummary accumulates per-reason
// drop counts across every service and region for the end-of-run summary.
func fetchAllRecs(
	ctx context.Context,
	awsCfg aws.Config,
	recClient provider.RecommendationsClient,
	accountCache *AccountAliasCache,
	servicesToProcess []common.ServiceType,
	engineData engineVersionData,
	cfg Config,
	coverageMap recommendations.PoolCoverageMap,
) ([]common.Recommendation, *common.DropSummary, error) {
	all := make([]common.Recommendation, 0)
	drops := common.NewDropSummary()
	for _, service := range servicesToProcess {
		AppLogger.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		AppLogger.Printf("🔍 Fetching %s recommendations\n", getServiceDisplayName(service))
		AppLogger.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		regions, err := determineRegionsForService(ctx, recClient, service, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return nil, drops, nil
			}
			return nil, nil, fmt.Errorf("%w: determining regions for %s: %w", errRecommendationFetch, getServiceDisplayName(service), err)
		}
		for i, region := range regions {
			if ctx.Err() != nil {
				return nil, drops, nil // partial data from an interrupted fetch is not a result
			}
			recs, err := fetchAndFilterRegionRecs(ctx, awsCfg, recClient, accountCache, service, region, i+1, len(regions), engineData, cfg, coverageMap, drops)
			if ctx.Err() != nil {
				return nil, drops, nil // interrupted mid-fetch: not a result, not a failure
			}
			if err != nil {
				return nil, nil, err
			}
			all = append(all, recs...)
		}
	}
	// Checked once over every account seen in any batch, including those the
	// filters dropped, so an exact account in another batch is not missed.
	if err := requireExactExcludes(accountCache.knownAccounts(), &cfg); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errRecommendationFetch, err)
	}
	return all, drops, nil
}
