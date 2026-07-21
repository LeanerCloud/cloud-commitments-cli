package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"golang.org/x/term"
)

// Constants for purchase processing.
const (
	// DefaultDuplicateCheckLookbackHours is the default lookback period for checking recent purchases.
	DefaultDuplicateCheckLookbackHours = 24

	// PurchaseDelaySeconds is the delay between consecutive purchases to avoid rate limiting.
	PurchaseDelaySeconds = 2
)

// AppLogger is a simple logger for application output.
var AppLogger = log.New(os.Stdout, "", 0)

// OrganizationsAPI interface for describing accounts.
type OrganizationsAPI interface {
	DescribeAccount(ctx context.Context, params *organizations.DescribeAccountInput, optFns ...func(*organizations.Options)) (*organizations.DescribeAccountOutput, error)
}

// AccountAliasGetter is an interface for getting account aliases.
type AccountAliasGetter interface {
	GetAccountAlias(ctx context.Context, accountID string) string
}

// AccountAliasCache caches account ID to alias mappings.
type AccountAliasCache struct {
	orgClient OrganizationsAPI
	cache     map[string]string
	mu        sync.RWMutex
}

// NewAccountAliasCache creates a new account alias cache.
func NewAccountAliasCache(cfg aws.Config) *AccountAliasCache {
	return &AccountAliasCache{
		cache:     make(map[string]string),
		orgClient: organizations.NewFromConfig(cfg),
	}
}

// NewAccountAliasCacheWithClient creates a new account alias cache with a custom client
// This is useful for testing with mocked clients.
func NewAccountAliasCacheWithClient(orgClient OrganizationsAPI) *AccountAliasCache {
	return &AccountAliasCache{
		cache:     make(map[string]string),
		orgClient: orgClient,
	}
}

// GetAccountAlias returns the account alias for an account ID.
func (c *AccountAliasCache) GetAccountAlias(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}

	c.mu.RLock()
	if alias, ok := c.cache[accountID]; ok {
		c.mu.RUnlock()
		return alias
	}
	c.mu.RUnlock()

	// Try to fetch from Organizations
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if alias, ok := c.cache[accountID]; ok {
		return alias
	}

	// Try to describe the account
	result, err := c.orgClient.DescribeAccount(ctx, &organizations.DescribeAccountInput{
		AccountId: aws.String(accountID),
	})
	if err != nil {
		c.cache[accountID] = accountID // Use ID as fallback
		return accountID
	}

	if result.Account != nil && result.Account.Name != nil {
		c.cache[accountID] = *result.Account.Name
		return *result.Account.Name
	}

	c.cache[accountID] = accountID
	return accountID
}

// CalculateTotalInstances calculates the total instance count across recommendations.
func CalculateTotalInstances(recs []common.Recommendation) int {
	total := 0
	for _rvc := range recs {
		rec := recs[_rvc]
		total += rec.Count
	}
	return total
}

// ApplyCoverage applies coverage percentage to recommendations.
//
// All cost-bearing fields (CommitmentCost, OnDemandCost, EstimatedSavings,
// and for SPs the SavingsPlanDetails.HourlyCommitment) scale by coverage/100
// so the returned Recommendation represents the sized purchase rather than
// AWS's pre-sized proposal. SavingsPercentage is invariant (savings vs
// on-demand ratio) and stays unscaled. Pre-sizing values can still be
// recovered: RecommendedCount holds AWS's pre-sized count for RIs.
func ApplyCoverage(recs []common.Recommendation, coverage float64) []common.Recommendation {
	return applyCoverage(recs, coverage, nil)
}

// applyCoverage applies legacy percentage sizing and optionally records
// recommendations whose discrete RI count is reduced to zero.
func applyCoverage(recs []common.Recommendation, coverage float64, drops *common.DropSummary) []common.Recommendation {
	if coverage >= 100 {
		return recs
	}
	if coverage <= 0 {
		return []common.Recommendation{}
	}

	ratio := coverage / 100.0
	result := make([]common.Recommendation, 0, len(recs))
	for _rvc := range recs {
		rec := recs[_rvc]
		adjusted := rec

		// For Savings Plans, reduce the hourly commitment instead of count.
		// If the type assertion fails (defensive — Details should always
		// be *SavingsPlanDetails for SP recs), preserve the recommendation
		// at its original values rather than silently dropping it. A
		// missing-Details record is a logged anomaly, not a reason to
		// erase coverage from the run.
		if common.IsSavingsPlan(rec.Service) {
			if details, ok := rec.Details.(*common.SavingsPlanDetails); ok {
				newDetails := *details // Copy the struct
				newDetails.HourlyCommitment *= ratio
				adjusted = common.ScaleRecommendationCosts(adjusted, ratio)
				adjusted.Details = &newDetails
			} else {
				AppLogger.Printf("WARNING: SP recommendation for service %q has unexpected Details type %T; passing through unscaled\n", rec.Service, rec.Details)
			}
			result = append(result, adjusted)
			continue
		}

		// For RIs, reduce the count and scale cost-bearing fields by the
		// DISCRETE count ratio (newCount / rec.Count) rather than the
		// requested ratio. Truncating newCount to an int then multiplying
		// costs by the unrounded ratio desynchronises Count and costs:
		// e.g. rec.Count=3 + ratio=0.5 yields newCount=1 (33% of instances)
		// but costs would scale to 50%, overstating the sized purchase
		// price by ~50%. Mirrors ApplyTargetCoverage / family-NU sizing.
		// rec.Count is guaranteed > 0 here because newCount > 0 implies
		// rec.Count >= 1 (int(0 * ratio) is 0 for any ratio).
		newCount := int(float64(rec.Count) * ratio)
		if newCount > 0 {
			sizedRatio := float64(newCount) / float64(rec.Count)
			adjusted = common.ScaleRecommendationCosts(adjusted, sizedRatio)
			adjusted.Count = newCount
			result = append(result, adjusted)
		} else if drops != nil {
			drops.Add(common.DropTargetSizedToZero, 1)
		}
	}
	return result
}

// ApplyTargetCoverage sizes RI/SP recommendations so that projected
// post-purchase COVERAGE lands near targetPct, leaving (100-targetPct)% of
// historical demand on-demand as headroom. See ApplyCoverage for the simpler
// rec.Count-scaled coverage flag; the two are dispatched via applySizing.
//
// AWS's recommendation count is sized for ~100% coverage of historical demand
// (average instances used per hour). --target-coverage is the lever the
// operator uses to deliberately under-buy that baseline, accepting more
// on-demand spend in exchange for less idle commitment when demand is bursty
// or trending down.
//
// The flag name says "utilization" because the original framing (issue #338)
// was a utilization floor. In practice operators set values like 70 or 80
// expecting coverage near that figure (with utilization staying ~100% on the
// commitments actually purchased), not the over-buy semantics that floor
// produces; see the #338 review discussion for the redirect.
//
// RIs (existing-aware, per-pool, strict-target):
//
//	gap            = targetPct - ExistingCoveragePct      (percentage points)
//	remaining_gap  = 100 - ExistingCoveragePct            (percentage points)
//	n_target       = floor(rec.Count * gap / remaining_gap)
//
//	The formula scales AWS's per-account-incremental rec.Count by the
//	fraction of the current-to-100% gap we want to fill. For example
//	with existing=50% and target=80%: gap=30, remaining_gap=50, so we
//	buy 30/50 = 60% of AWS's rec.Count. Anchoring to rec.Count (which
//	AWS computed per-linked-account) is more robust in multi-account
//	orgs than scaling against avg, since CE's ExistingCoveragePct is
//	org-wide averaged and mixes accounts together.
//
//	If gap <= 0 (existing already at/above target) → drop with INFO log.
//	If n_target == 0 (gap too small to fit one RI) → drop with INFO log.
//	If AverageInstancesUsedPerHour <= 0 → pass through (no signal); counted
//	in the per-run skip summary.
//	Projected coverage = ExistingCoveragePct + n_target/avg * 100 (total
//	coverage after the purchase, clamped to 100). Projected utilization =
//	avg/n_target * 100 clamped to 100.
//
//	ExistingCoveragePct is sourced from CE GetReservationCoverage in the
//	same pool; zero means "no signal" and the formula reduces to
//	floor(rec.Count * target/100) — i.e. plain target% of AWS's count.
//	For RDS the coverage lookup keys by (region, instance_type, engine).
//	Floor (rather than ceil or round) gives strict "at-most-target"
//	sizing. Pools too small to approximate the target meaningfully
//	should be filtered upstream via --min-pool-size; floor will drop
//	them as zero-count otherwise.
//
//	Pools where CE reports 100% existing coverage but AWS still recommends
//	new RIs (typical when existing RIs are near expiry) are dropped here —
//	the existing coverage is honored strictly. Use --rebuy-window-days to
//	surface those replacements before the cliff.
//
// SPs:
//
//	Scale SavingsPlanDetails.HourlyCommitment and EstimatedSavings by
//	targetPct/100 (the same lever ApplyCoverage's SP branch uses, but with
//	the explicit utilization-target framing). RecommendedUtilization is used
//	only as the no-signal guard: when AWS hasn't returned a projected
//	utilization figure, we pass the rec through unchanged and count it in
//	the skip summary, since we can't sanity-check what the scaled commitment
//	would mean.
//	If RecommendedUtilization <= 0 → pass through; counted in skip summary.
//
// Recs of any other CommitmentType are passed through unmodified (warned
// once per type per run).
// ApplyTargetCoverage applies the target coverage percentage to a slice of
// recommendations. drops accumulates per-reason drop counts for the
// end-of-run summary; pass nil to skip tracking.
func ApplyTargetCoverage(recs []common.Recommendation, targetPct float64, drops *common.DropSummary) []common.Recommendation {
	if targetPct <= 0 || targetPct > 100 {
		// Validation ensures we never get here in production, but be defensive
		// so a buggy caller doesn't divide by zero.
		AppLogger.Printf("WARNING: ApplyTargetCoverage called with targetPct=%.2f outside (0,100]; returning recs unchanged\n", targetPct)
		return recs
	}

	result := make([]common.Recommendation, 0, len(recs))
	var skipped int
	unsupportedSeen := make(map[common.CommitmentType]bool)

	for i := range recs {
		adjusted, kept, missingSignal, dropReason := applyTargetCoverageOne(recs[i], targetPct, unsupportedSeen)
		if missingSignal {
			skipped++
		}
		if kept {
			result = append(result, adjusted)
		} else if dropReason != "" {
			drops.Add(dropReason, 1)
		}
	}

	if skipped > 0 {
		AppLogger.Printf("INFO: --target-coverage=%.1f%% skipped %d of %d recommendations with no utilization signal (passed through unchanged)\n",
			targetPct, skipped, len(recs))
	}

	return result
}

// applyTargetCoverageOne dispatches a single recommendation through the
// appropriate branch. Returns (rec, kept, missingSignal, dropReason):
//   - kept=true → caller appends `rec` (the adjusted or pass-through value).
//   - kept=false → caller drops the rec (only the RI "target unreachable"
//     branches return this; an INFO log already fired).
//   - missingSignal=true → counted toward the end-of-run skip summary.
//   - dropReason is non-empty when kept=false and the drop has a named category.
//
// Split out of ApplyTargetCoverage to keep that function under gocyclo's
// complexity threshold.
func applyTargetCoverageOne(rec common.Recommendation, targetPct float64, unsupportedSeen map[common.CommitmentType]bool) (result common.Recommendation, kept, missingSignal bool, drop string) {
	switch {
	case common.IsSavingsPlan(rec.Service):
		adjusted, ok := applyTargetCoverageSP(rec, targetPct)
		if !ok {
			// SP no-signal: pass through unchanged.
			return rec, true, true, ""
		}
		return adjusted, true, false, ""
	case rec.CommitmentType == common.CommitmentReservedInstance:
		adjusted, ok, dropReason := applyTargetCoverageRI(rec, targetPct)
		if !ok {
			// Distinguish "no signal" (pass through, count in summary) from
			// "target unreachable" (drop with already-fired INFO log).
			if rec.AverageInstancesUsedPerHour <= 0 {
				return rec, true, true, ""
			}
			return rec, false, false, dropReason
		}
		return adjusted, true, false, ""
	default:
		if !unsupportedSeen[rec.CommitmentType] {
			AppLogger.Printf("WARNING: --target-coverage not supported for CommitmentType=%q; passing recommendations through unchanged\n", rec.CommitmentType)
			unsupportedSeen[rec.CommitmentType] = true
		}
		return rec, true, false, ""
	}
}

// applyTargetCoverageRI is the RI branch of ApplyTargetCoverage. Returns
// (adjusted, true, "") on success, (rec, false, dropReason) when the rec
// should be passed through unscaled (no signal) or dropped (target
// unreachable). Caller distinguishes no-signal from drop via
// rec.AverageInstancesUsedPerHour and uses dropReason for the summary.
func applyTargetCoverageRI(rec common.Recommendation, targetPct float64) (result common.Recommendation, ok bool, drop string) {
	if rec.AverageInstancesUsedPerHour <= 0 {
		// No signal — caller will pass through and count in the summary.
		return rec, false, ""
	}

	avg := rec.AverageInstancesUsedPerHour
	// Coverage-anchored under-buy: size linearly off the pool's avg demand
	// and the absolute gap to target. Both inputs come from
	// GetReservationCoverage (AvgInstancesPerHour from
	// TotalRunningHours/window; ExistingCoveragePct from
	// CoverageHoursPercentage) so the buy lines up with the AWS console's
	// reservations-coverage report: target%-existing% of avg instances.
	//
	// The previous formula anchored on AWS's rec.Count
	// (floor(rec.Count × gap / (100−existing))), which under-bought when
	// AWS sized rec.Count for less than full coverage (ROI-curated) and
	// when CE's org-wide existing% disagreed with rec.Count's per-account
	// derivation. Anchoring on coverage's own avg removes both mismatches.
	// rec.Count is retained only for the cost-scaling ratio further down.
	//
	// Keep the subtraction in percentage units (subtract first, divide
	// later) so whole-percent values don't lose precision to float
	// rounding at integer boundaries.
	gapPct := targetPct - rec.ExistingCoveragePct
	if gapPct <= 0 {
		// Existing commitments already meet or exceed the target; no purchase
		// needed in this pool. Drop with an info log so operators can see what
		// the flag did. Returning (_, false) with avg > 0 signals "drop, don't
		// pass through".
		AppLogger.Printf("INFO: --target-coverage=%.1f%% already met by existing coverage %.1f%% for %s/%s/%s; dropped recommendation\n",
			targetPct, rec.ExistingCoveragePct, rec.Service, rec.Region, rec.ResourceType)
		return rec, false, common.DropTargetAlreadyMet
	}
	// Floor so we never over-shoot the target on integer-arithmetic edges.
	// Strict-target semantics: 80% means "at most 80% coverage", not "at
	// least 80%". Floor under-covers small/odd pools (e.g. avg=2, target=80
	// gives 1 RI = 50% rather than 2 RIs = 100%); pools too small to
	// approximate target are best filtered out via --min-pool-size upstream.
	nTarget := int(math.Floor(avg * gapPct / 100.0))

	if nTarget == 0 {
		// Floor produces zero when avg × gap% < 100 (small pools or thin
		// gaps). Drop — buying 1 RI would over-shoot target and the
		// strict-target intent prefers under-cover (run on-demand) over
		// over-cover (idle commitment). Use --min-pool-size to filter
		// these out earlier so they don't show up as drops in the log.
		AppLogger.Printf("INFO: --target-coverage=%.1f%% sizes %s/%s/%s to 0 instances (avg=%.2f, gap=%.2f%% produces <1 RI); dropped recommendation\n",
			targetPct, rec.Service, rec.Region, rec.ResourceType, avg, gapPct)
		// Returning (_, false) with avg > 0 signals "drop, don't pass through".
		// applyTargetCoverageRI's caller branches on
		// rec.AverageInstancesUsedPerHour to distinguish drop vs no-signal.
		return rec, false, common.DropTargetSizedToZero
	}

	// Cost-bearing fields scale by the ratio of sized-to-original count, so the
	// returned rec represents the sized purchase rather than AWS's pre-sized
	// proposal. SavingsPercentage is invariant (savings vs on-demand ratio).
	// rec.Count is the AWS pre-sizing count at this point (parser sets Count
	// == RecommendedCount and we haven't mutated either yet). When the
	// coverage-anchored nTarget exceeds rec.Count (AWS sized below full
	// coverage), the ratio scales costs up linearly — accurate when per-RI
	// pricing is constant, which it is within a single pool/term/payment
	// combination. Guarded against rec.Count==0 (malformed rec) by falling
	// back to nTarget so a zero-cost rec stays zero-cost rather than NaN.
	var ratio float64
	if rec.Count > 0 {
		ratio = float64(nTarget) / float64(rec.Count)
	} else {
		ratio = float64(nTarget)
	}
	adjusted := common.ScaleRecommendationCosts(rec, ratio)
	adjusted.Count = nTarget

	// Projection metrics. ProjectedCoverage is TOTAL coverage (existing +
	// new) so operators can see the figure they actually targeted.
	// ProjectedUtilization stays at the per-purchase fill rate; under-buy
	// keeps nTarget <= avg so it always clamps to 100%.
	projUtil := avg / float64(nTarget) * 100.0
	if projUtil > 100 {
		projUtil = 100
	}
	projCov := rec.ExistingCoveragePct + float64(nTarget)/avg*100.0
	if projCov > 100 {
		projCov = 100
	}
	adjusted.ProjectedUtilization = projUtil
	adjusted.ProjectedCoverage = projCov
	return adjusted, true, ""
}

// applyTargetCoverageSP is the SP branch of ApplyTargetCoverage. Returns
// (adjusted, true) when the rec is kept, (rec, false) when it should be
// skipped (caller passes through unscaled and counts in the skip summary).
func applyTargetCoverageSP(rec common.Recommendation, targetPct float64) (common.Recommendation, bool) {
	if rec.RecommendedUtilization <= 0 {
		return rec, false
	}
	// Also treat a $0 HourlyCommitment as "no signal" — CE occasionally
	// returns placeholder recs with zero commitment. Sizing such a rec
	// would produce nonsense ($0 commitment * ratio = $0) while still
	// claiming the target coverage is achieved, which is incoherent.
	// Pass through unchanged and count in the skip summary.
	if details, ok := rec.Details.(*common.SavingsPlanDetails); ok && details.HourlyCommitment <= 0 {
		return rec, false
	}

	// Under-buy: scale all cost-bearing fields by target/100 against AWS's
	// recommended commitment. This deliberately spends less than AWS suggested,
	// leaving (100-target)% of the SP's projected workload on on-demand.
	// RecommendedUtilization is consulted only as a no-signal guard above (a
	// zero value means we can't sanity-check the result); the scaling itself
	// uses targetPct directly rather than a recUtil/target ratio so the flag's
	// intent is honored even when AWS already projects above target.
	//
	// If Details isn't a *SavingsPlanDetails (defensive — should always be
	// for SP recs), log a warning and pass through UNCHANGED — including
	// leaving ProjectedUtilization at zero. Setting projection fields on a
	// rec whose commitment fields couldn't be scaled would produce a
	// misleading row (projection=target%, savings=full-unscaled).
	details, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok {
		AppLogger.Printf("WARNING: SP recommendation for service %q has unexpected Details type %T; passing through unscaled\n", rec.Service, rec.Details)
		return rec, true
	}
	ratio := targetPct / 100.0
	newDetails := *details // copy
	newDetails.HourlyCommitment *= ratio
	adjusted := common.ScaleRecommendationCosts(rec, ratio)
	adjusted.Details = &newDetails
	// Shrinking commitment raises projected utilization by 1/ratio
	// (used is fixed = orig_commit * RecUtil, bought is orig_commit * ratio).
	// Clamp to 100 since utilization caps at full use.
	projUtil := rec.RecommendedUtilization / ratio
	if projUtil > 100 {
		projUtil = 100
	}
	adjusted.ProjectedUtilization = projUtil
	// ProjectedCoverage stays zero for SPs — CE doesn't expose total-demand-$
	// for a clean coverage figure (see field doc on Recommendation).
	return adjusted, true
}

// applySizing chooses target-coverage or coverage sizing.
//
// coverage is the effective % to apply when target-coverage is unset
// (the main path passes cfg.Coverage; the CSV path passes csvModeCoverage,
// which substitutes the default 80% with 100% so CSV-driven counts aren't
// silently dropped).
//
// drops accumulates per-reason drop counts for the end-of-run summary.
// Pass nil to skip tracking.
func applySizing(recs []common.Recommendation, cfg Config, coverage float64, drops *common.DropSummary) []common.Recommendation {
	if cfg.TargetCoverage > 0 {
		return ApplyTargetCoverage(recs, cfg.TargetCoverage, drops)
	}
	return applyCoverage(recs, coverage, drops)
}

// ApplyCountOverride overrides the count for all recommendations.
func ApplyCountOverride(recs []common.Recommendation, overrideCount int32) []common.Recommendation {
	if overrideCount <= 0 {
		return recs
	}
	result := make([]common.Recommendation, len(recs))
	for i := range recs {
		rec := recs[i]
		result[i] = rec
		result[i].Count = int(overrideCount)
	}
	return result
}

// ApplyInstanceLimit limits the total number of instances.
func ApplyInstanceLimit(recs []common.Recommendation, maxInstances int32) []common.Recommendation {
	if maxInstances <= 0 {
		return recs
	}

	result := make([]common.Recommendation, 0)
	remaining := int(maxInstances)

	for _rvc := range recs {
		rec := recs[_rvc]
		if remaining <= 0 {
			break
		}
		adjusted := rec
		if rec.Count > remaining {
			adjusted.Count = remaining
		}
		result = append(result, adjusted)
		remaining -= adjusted.Count
	}
	return result
}

// ConfirmPurchase asks the user for confirmation before proceeding.
// totalSavings is the estimated monthly savings from the purchase (not the purchase cost),
// matching the EstimatedSavings column and the "Estimated monthly savings" summary.
// Returns false without prompting if stdin is not a TTY and skipConfirmation is false.
func ConfirmPurchase(totalInstances int, totalSavings float64, skipConfirmation bool) bool {
	if skipConfirmation {
		return true
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // G115: uintptr->int for file descriptor; FD values are always small positive integers
		log.Printf("stdin is not a terminal and --yes was not set; skipping purchase")
		return false
	}

	fmt.Printf("\n⚠️  About to purchase %d instances with estimated monthly savings: $%.2f\n", totalInstances, totalSavings)
	fmt.Print("Do you want to proceed? (yes/no): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "yes" || response == "y"
}

// CheckAuditLogWritable opens the audit log file in append mode to verify it is writable.
// Returns an error if the path cannot be opened for writing.
func CheckAuditLogWritable(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304 -- audit log path is operator-configured; value is not reachable from user input
	if err != nil {
		return fmt.Errorf("audit log %q not writable: %w", path, err)
	}
	return f.Close()
}

// DuplicateChecker checks for existing commitments to avoid duplicates.
type DuplicateChecker struct {
	LookbackHours int // How many hours to look back for recent purchases
}

// NewDuplicateChecker creates a new duplicate checker. Pass 0 to use the default lookback period.
func NewDuplicateChecker(hours int) *DuplicateChecker {
	if hours <= 0 {
		hours = DefaultDuplicateCheckLookbackHours
	}
	return &DuplicateChecker{
		LookbackHours: hours,
	}
}

// AdjustRecommendationsForExisting adjusts recommendations based on existing commitments
// This checks for recently purchased RIs (within LookbackHours) to avoid duplicate purchases.
// Note: This is designed to prevent re-purchasing something you just bought, not to prevent
// purchasing RIs in other accounts that happen to have the same characteristics.
func (d *DuplicateChecker) AdjustRecommendationsForExisting(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) (passed, filtered []common.Recommendation, err error) {
	existing, err := client.GetExistingCommitments(ctx)
	if err != nil {
		return recs, nil, err
	}

	log.Printf("    [DuplicateChecker] Found %d total existing commitments", len(existing))

	recentExisting := d.filterRecentCommitments(existing)
	log.Printf("    [DuplicateChecker] Found %d recent commitments (purchased in last %d hours)", len(recentExisting), d.LookbackHours)

	if len(recentExisting) == 0 {
		return recs, nil, nil
	}

	existingMap := buildExistingCommitmentsMap(recentExisting)
	log.Printf("    [DuplicateChecker] Existing map has %d unique keys", len(existingMap))

	passed, filtered = adjustRecommendationsAgainstExisting(recs, existingMap)

	if len(filtered) > 0 {
		log.Printf("    [DuplicateChecker] Result: %d recommendations kept out of %d (avoided %d duplicates)",
			len(passed), len(recs), len(filtered))
	}
	return passed, filtered, nil
}

// filterRecentCommitments filters commitments to only recent purchases within the lookback window.
func (d *DuplicateChecker) filterRecentCommitments(existing []common.Commitment) []common.Commitment {
	cutoffTime := time.Now().Add(-time.Duration(d.LookbackHours) * time.Hour)
	recentExisting := make([]common.Commitment, 0)

	for _rvc := range existing {
		c := existing[_rvc]
		if isRecentActiveCommitment(c, cutoffTime) {
			recentExisting = append(recentExisting, c)
		}
	}

	return recentExisting
}

// isRecentActiveCommitment checks if a commitment is active and purchased after the cutoff time.
func isRecentActiveCommitment(c common.Commitment, cutoffTime time.Time) bool {
	return (c.State == "active" || c.State == "payment-pending") && c.StartDate.After(cutoffTime)
}

// buildExistingCommitmentsMap builds a map of commitments by resource type, region, and engine.
func buildExistingCommitmentsMap(commitments []common.Commitment) map[string]int {
	existingMap := make(map[string]int)

	for _rvc := range commitments {
		c := commitments[_rvc]
		normalizedEngine := normalizeEngineName(c.Engine)
		key := fmt.Sprintf("%s|%s|%s", c.ResourceType, c.Region, normalizedEngine)
		existingMap[key] += c.Count
		log.Printf("    [DuplicateChecker] Recent RI: key=%s count=%d startDate=%s (raw engine=%s)",
			key, c.Count, c.StartDate.Format("2006-01-02 15:04:05"), c.Engine)
	}

	return existingMap
}

// adjustRecommendationsAgainstExisting adjusts recommendations based on existing commitments.
// Returns (passed, filtered) where filtered contains recs whose count was reduced to zero.
func adjustRecommendationsAgainstExisting(recs []common.Recommendation, existingMap map[string]int) (passed, filtered []common.Recommendation) {
	passed = make([]common.Recommendation, 0, len(recs))
	filtered = make([]common.Recommendation, 0)

	for _rvc := range recs {
		rec := recs[_rvc]
		adjusted := adjustSingleRecommendation(rec, existingMap)
		if adjusted.Count > 0 {
			passed = append(passed, adjusted)
		} else {
			filtered = append(filtered, rec)
		}
	}

	return passed, filtered
}

// adjustSingleRecommendation adjusts a single recommendation based on existing commitments.
func adjustSingleRecommendation(rec common.Recommendation, existingMap map[string]int) common.Recommendation {
	engine := getEngineFromRecommendation(rec)
	key := fmt.Sprintf("%s|%s|%s", rec.ResourceType, rec.Region, engine)
	existingCount := existingMap[key]

	if existingCount >= rec.Count {
		// All of this recommendation is covered by recent RIs.
		// Return a zero-value Recommendation (Count=0) as a sentinel; the caller
		// (adjustRecommendationsAgainstExisting) filters out recommendations with Count <= 0.
		log.Printf("    [DuplicateChecker] SKIP %s: recent %d >= recommended %d", key, existingCount, rec.Count)
		existingMap[key] -= rec.Count
		return common.Recommendation{Count: 0}
	}

	// Partial or no coverage by recent RIs
	adjusted := rec
	if existingCount > 0 {
		adjusted.Count = rec.Count - existingCount
		existingMap[key] = 0
		log.Printf("    [DuplicateChecker] PARTIAL %s: adjusted count from %d to %d", key, rec.Count, adjusted.Count)
	}

	return adjusted
}

// getEngineFromRecommendation extracts the engine from recommendation details.
func getEngineFromRecommendation(rec common.Recommendation) string {
	if rec.Details == nil {
		return ""
	}
	var engine string
	switch details := rec.Details.(type) {
	case common.DatabaseDetails:
		engine = details.Engine
	case *common.DatabaseDetails:
		engine = details.Engine
	case common.CacheDetails:
		engine = details.Engine
	case *common.CacheDetails:
		engine = details.Engine
	default:
		return ""
	}
	return normalizeEngineName(engine)
}

// engineNameMap maps database engine names to a consistent normalized format.
// AWS RIs use: "aurora-postgresql", "aurora-mysql", "mysql", "postgres"
// Cost Explorer uses: "Aurora PostgreSQL", "Aurora MySQL", "MySQL", "PostgreSQL".
var engineNameMap = map[string]string{
	// Cost Explorer format -> normalized
	"Aurora PostgreSQL": "aurora-postgresql",
	"Aurora MySQL":      "aurora-mysql",
	"MySQL":             "mysql",
	"PostgreSQL":        "postgresql",
	"MariaDB":           "mariadb",
	"Oracle":            "oracle",
	"SQL Server":        "sqlserver",
	// Already normalized (from AWS RIs)
	"aurora-postgresql": "aurora-postgresql",
	"aurora-mysql":      "aurora-mysql",
	"mysql":             "mysql",
	"postgresql":        "postgresql",
	"postgres":          "postgresql",
	"mariadb":           "mariadb",
	"oracle-se":         "oracle",
	"oracle-se1":        "oracle",
	"oracle-se2":        "oracle",
	"oracle-ee":         "oracle",
	"sqlserver-se":      "sqlserver",
	"sqlserver-ee":      "sqlserver",
	"sqlserver-ex":      "sqlserver",
	"sqlserver-web":     "sqlserver",
}

// normalizeEngineName normalizes database engine names to a consistent format.
func normalizeEngineName(engine string) string {
	if normalized, ok := engineNameMap[engine]; ok {
		return normalized
	}
	// Return lowercase as fallback
	return strings.ToLower(engine)
}

// AdjustRecommendationsForExistingRIs is an alias for AdjustRecommendationsForExisting.
func (d *DuplicateChecker) AdjustRecommendationsForExistingRIs(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) (passed, filtered []common.Recommendation, err error) {
	return d.AdjustRecommendationsForExisting(ctx, recs, client)
}

// GetRecommendationDescription returns a human-readable description.
func GetRecommendationDescription(rec common.Recommendation) string {
	desc := fmt.Sprintf("%s %s", rec.Service, rec.ResourceType)
	if rec.Details != nil {
		desc += " " + rec.Details.GetDetailDescription()
	}
	return desc
}
