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

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"golang.org/x/term"
)

// Constants for purchase processing.
const (
	// PurchaseDelaySeconds is the delay between consecutive purchases to avoid rate limiting.
	PurchaseDelaySeconds = 2

	// DefaultDuplicateCheckLookbackHours is re-exported from pkg/recfilter.
	DefaultDuplicateCheckLookbackHours = recfilter.DefaultDuplicateCheckLookbackHours
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
	failed    map[string]error // lookups that failed this run, so a lasting failure costs one call per account
	mu        sync.RWMutex
}

// NewAccountAliasCache creates a new account alias cache.
func NewAccountAliasCache(cfg aws.Config) *AccountAliasCache {
	return &AccountAliasCache{
		cache:     make(map[string]string),
		failed:    make(map[string]error),
		orgClient: organizations.NewFromConfig(cfg),
	}
}

// NewAccountAliasCacheWithClient creates a new account alias cache with a custom client
// This is useful for testing with mocked clients.
func NewAccountAliasCacheWithClient(orgClient OrganizationsAPI) *AccountAliasCache {
	return &AccountAliasCache{
		cache:     make(map[string]string),
		failed:    make(map[string]error),
		orgClient: orgClient,
	}
}

// knownAccounts lists every account whose name resolved this run.
func (c *AccountAliasCache) knownAccounts() []common.Recommendation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]common.Recommendation, 0, len(c.cache))
	for id, name := range c.cache {
		out = append(out, common.Recommendation{Account: id, AccountName: name})
	}
	return out
}

// GetAccountAlias returns the account alias for an account ID, or the ID itself
// when the name cannot be resolved. For display only: a caller that filters on
// the name must use ResolveAccountName, because the ID is not the name.
func (c *AccountAliasCache) GetAccountAlias(ctx context.Context, accountID string) string {
	name, err := c.ResolveAccountName(ctx, accountID)
	if err != nil {
		return accountID
	}
	return name
}

// ResolveAccountName returns the Organizations name of an account. A failed
// lookup is returned, and remembered for the rest of the run so a lasting
// failure (member accounts cannot call DescribeAccount) costs one call per
// account. An account with no name resolves to its ID.
func (c *AccountAliasCache) ResolveAccountName(ctx context.Context, accountID string) (string, error) {
	if accountID == "" {
		return "", nil
	}

	c.mu.RLock()
	if alias, ok := c.cache[accountID]; ok {
		c.mu.RUnlock()
		return alias, nil
	}
	if err, ok := c.failed[accountID]; ok {
		c.mu.RUnlock()
		return "", err
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if alias, ok := c.cache[accountID]; ok {
		return alias, nil
	}
	if err, ok := c.failed[accountID]; ok {
		return "", err
	}

	result, err := c.orgClient.DescribeAccount(ctx, &organizations.DescribeAccountInput{
		AccountId: aws.String(accountID),
	})
	if err != nil {
		err = fmt.Errorf("describing account %s: %w", accountID, err)
		c.failed[accountID] = err
		return "", err
	}

	name := accountID
	if result.Account != nil && result.Account.Name != nil {
		name = *result.Account.Name
	}
	c.cache[accountID] = name
	return name, nil
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

// isSavingsPlan reports whether rec is a Savings Plan, whose Count is a
// placeholder 1 rather than a number of instances.
func isSavingsPlan(rec common.Recommendation) bool {
	return rec.CommitmentType == common.CommitmentSavingsPlan
}

// withoutSavingsPlans returns the instance-based recommendations, in order.
func withoutSavingsPlans(recs []common.Recommendation) []common.Recommendation {
	var others []common.Recommendation
	for i := range recs {
		if !isSavingsPlan(recs[i]) {
			others = append(others, recs[i])
		}
	}
	return others
}

// mergeKeepingOrder returns the entries of original that survive, in original
// order: every Savings Plan, plus the leading len(keptOthers) instance-based
// entries replaced by their (possibly reduced) kept versions. keptOthers must be
// a prefix-ordered result of the instance-based entries.
func mergeKeepingOrder(original, keptOthers []common.Recommendation) []common.Recommendation {
	out := make([]common.Recommendation, 0, len(original))
	next := 0
	for i := range original {
		switch {
		case isSavingsPlan(original[i]):
			out = append(out, original[i])
		case next < len(keptOthers):
			out = append(out, keptOthers[next])
			next++
		}
	}
	return out
}

// ApplyCoverage delegates to recfilter.ApplyCoverage, wiring AppLogger as the
// logging sink. Substantive documentation lives on recfilter.ApplyCoverage.
func ApplyCoverage(recs []common.Recommendation, coverage float64) []common.Recommendation {
	return mapRecs(recfilter.ApplyCoverage(recs, coverage, AppLogger.Printf, nil), syncDataWarehouseNodes)
}

// applyCoverage delegates to recfilter.ApplyCoverage, wiring AppLogger as the
// logging sink. Substantive documentation lives on recfilter.ApplyCoverage.
func applyCoverage(recs []common.Recommendation, coverage float64, drops *common.DropSummary) []common.Recommendation {
	return mapRecs(recfilter.ApplyCoverage(recs, coverage, AppLogger.Printf, drops), syncDataWarehouseNodes)
}

// ApplyTargetCoverage delegates to recfilter.ApplyTargetCoverage, wiring
// AppLogger as the logging sink. Substantive documentation (the RI/SP sizing
// formulas and the #338 flag-name history) lives on recfilter.ApplyTargetCoverage.
func ApplyTargetCoverage(recs []common.Recommendation, targetPct float64, drops *common.DropSummary) []common.Recommendation {
	return mapRecs(recfilter.ApplyTargetCoverage(recs, targetPct, AppLogger.Printf, drops), syncDataWarehouseNodes)
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

// ApplyInstanceLimit truncates recs so their total Count does not exceed
// maxInstances. It is a single-shot cap over whatever slice it is handed: the
// caller is responsible for handing it the complete run-wide set, because
// applying it to a subset (one service, one region) caps that subset only and
// multiplies the effective cap by the number of subsets. See
// applyGlobalInstanceLimit in multi_service.go for the run-wide call site.
//
// Recommendations are consumed in slice order, so the caller controls which
// ones survive by ordering the slice (the main path caps the scorer's
// savings-sorted output, keeping the highest-value commitments).
//
// A truncated recommendation has its extensive money fields scaled by the
// discrete count ratio, like every other sizing path (see
// common.ScaleRecommendationCosts). EstimatedSavings and friends are
// whole-row totals for the count the provider proposed, so cutting Count
// alone leaves the row claiming the savings of instances the run will not
// buy. The run summary and the purchase report then overstate the benefit
// of a capped run, which is the wrong direction to be wrong in on a money
// path (#1830). The count-linear fields that are not money (ProjectedCoverage,
// ProjectedUtilization, Redshift NumberOfNodes) are re-derived from the new
// Count by refreshCountLinear (#1845).
//
// Savings Plans are exempt: their Count is always 1 and is not an instance
// count, so they pass through unchanged, wherever they sit in recs, and consume
// no budget.
func ApplyInstanceLimit(recs []common.Recommendation, maxInstances int32) []common.Recommendation {
	if maxInstances <= 0 {
		return recs
	}

	result := make([]common.Recommendation, 0)
	remaining := int(maxInstances)

	for _rvc := range recs {
		rec := recs[_rvc]
		if isSavingsPlan(rec) {
			result = append(result, rec)
			continue
		}
		if remaining <= 0 {
			continue // keep scanning: later Savings Plans still pass through
		}
		adjusted := rec
		// rec.Count > remaining and remaining >= 1 together imply rec.Count
		// >= 2, so the denominator is always positive here. A non-positive
		// Count can never enter this branch and so is never rescaled: it
		// buys nothing, there is nothing to scale down to, and a zero or
		// negative denominator would produce NaN or a sign flip.
		if rec.Count > remaining {
			adjusted = common.ScaleRecommendationCosts(rec, float64(remaining)/float64(rec.Count))
			adjusted.Count = remaining
			adjusted = refreshCountLinear(adjusted)
		}
		result = append(result, adjusted)
		// Only a positive Count consumes budget. Subtracting a non-positive
		// Count would credit budget back and let later recommendations push
		// the run past the cap.
		if adjusted.Count > 0 {
			remaining -= adjusted.Count
		}
	}
	return result
}

// refreshCountLinear re-derives the count-linear, non-money fields after a
// caller outside the shared sizing code changed rec.Count (#1845): the
// projections the target-coverage sizer wrote, and the Redshift node count
// mirrored from Count at parse. It is idempotent on a row whose Count did not
// change, so callers may apply it to every row of a slice.
func refreshCountLinear(rec common.Recommendation) common.Recommendation {
	return syncDataWarehouseNodes(rederiveProjections(rec))
}

// mapRecs applies fn to every rec into a new slice, keeping a nil input nil.
func mapRecs(recs []common.Recommendation, fn func(common.Recommendation) common.Recommendation) []common.Recommendation {
	if recs == nil {
		return nil
	}
	out := make([]common.Recommendation, len(recs))
	for i := range recs {
		out[i] = fn(recs[i])
	}
	return out
}

// rederiveProjections mirrors the projection formulas of the shared sizer
// (recfilter applyTargetCoverageRI) for the rec's current Count. A projection
// that is zero ("not computed") stays zero. Savings Plans, rows without a
// usage signal (zero, negative, NaN or infinite avg) and rows without a positive
// Count are returned unchanged.
// RDS ProjectedCoverage is a family aggregate set by family-NU sizing, which a
// per-row formula would corrupt, so only RDS ProjectedUtilization is refreshed.
func rederiveProjections(rec common.Recommendation) common.Recommendation {
	avg := rec.AverageInstancesUsedPerHour
	if common.IsSavingsPlan(rec.Service) || rec.Count <= 0 || !(avg > 0) || math.IsInf(avg, 1) {
		return rec
	}
	n := float64(rec.Count)
	if rec.ProjectedUtilization != 0 {
		rec.ProjectedUtilization = math.Min(100, avg/n*100)
	}
	if rec.ProjectedCoverage != 0 && rec.ExistingCoverageKnown && rec.Service != common.ServiceRDS {
		rec.ProjectedCoverage = math.Min(100, rec.ExistingCoveragePct+n/avg*100)
	}
	return rec
}

// syncDataWarehouseNodes re-syncs NumberOfNodes (and the single-node/multi-node
// ClusterType derived from it) with rec.Count for AWS Redshift recs, the only
// parser that mirrors Count into NumberOfNodes; other users of
// DataWarehouseDetails keep their value. The details are copied, never written
// through the shared pointer.
func syncDataWarehouseNodes(rec common.Recommendation) common.Recommendation {
	if rec.Service != common.ServiceRedshift || rec.Count <= 0 {
		return rec
	}
	d, isPtr, ok := dataWarehouseDetails(rec.Details)
	if !ok || d.NumberOfNodes == rec.Count {
		return rec
	}
	d.NumberOfNodes = rec.Count
	if d.ClusterType == "single-node" || d.ClusterType == "multi-node" {
		d.ClusterType = "multi-node"
		if rec.Count == 1 {
			d.ClusterType = "single-node"
		}
	}
	if isPtr {
		rec.Details = &d
	} else {
		rec.Details = d
	}
	return rec
}

// dataWarehouseDetails returns a copy of a pointer or value DataWarehouseDetails.
func dataWarehouseDetails(d common.ServiceDetails) (cp common.DataWarehouseDetails, isPtr, ok bool) {
	switch v := d.(type) {
	case *common.DataWarehouseDetails:
		if v == nil {
			return cp, false, false
		}
		return *v, true, true
	case common.DataWarehouseDetails:
		return v, false, true
	}
	return cp, false, false
}

// ConfirmPurchase requires terminal input; totalSavings is monthly savings, not purchase cost.
func ConfirmPurchase(totalInstances int, totalSavings float64) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // G115: uintptr->int for file descriptor; FD values are always small positive integers
		log.Printf("stdin is not a terminal; interactive confirmation is required, skipping purchase")
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

// CheckAuditLogWritable reports whether the audit log and its immediate parent
// directories satisfy the read, append, and durability requirements.
// Thin wrapper over common.CheckAuditLogWritable; kept so cmd's existing call sites
// and tests are unchanged.
func CheckAuditLogWritable(path string) error { return common.CheckAuditLogWritable(path) }

// DuplicateChecker is re-exported from pkg/recfilter so cmd's existing call
// sites and tests are unchanged.
type DuplicateChecker = recfilter.DuplicateChecker

// NewDuplicateChecker creates a new duplicate checker. Pass 0 to use the
// default lookback period. Logf is wired to log.Printf so the CLI's
// decision trail keeps going to stderr exactly as it does today.
func NewDuplicateChecker(hours int) *DuplicateChecker {
	d := recfilter.NewDuplicateChecker(hours)
	d.Logf = log.Printf
	return d
}

// GetRecommendationDescription returns a human-readable description.
func GetRecommendationDescription(rec common.Recommendation) string {
	desc := fmt.Sprintf("%s %s", rec.Service, rec.ResourceType)
	if rec.Details != nil {
		desc += " " + rec.Details.GetDetailDescription()
	}
	return desc
}
