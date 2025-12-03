package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
)

// AppLogger is a simple logger for application output
var AppLogger = log.New(os.Stdout, "", 0)

// AccountAliasCache caches account ID to alias mappings
type AccountAliasCache struct {
	mu      sync.RWMutex
	cache   map[string]string
	orgClient *organizations.Client
}

// NewAccountAliasCache creates a new account alias cache
func NewAccountAliasCache(cfg aws.Config) *AccountAliasCache {
	return &AccountAliasCache{
		cache:     make(map[string]string),
		orgClient: organizations.NewFromConfig(cfg),
	}
}

// GetAccountAlias returns the account alias for an account ID
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

// CalculateTotalInstances calculates the total instance count across recommendations
func CalculateTotalInstances(recs []common.Recommendation) int {
	total := 0
	for _, rec := range recs {
		total += rec.Count
	}
	return total
}

// ApplyCoverage applies coverage percentage to recommendations
func ApplyCoverage(recs []common.Recommendation, coverage float64) []common.Recommendation {
	if coverage >= 100 {
		return recs
	}
	if coverage <= 0 {
		return []common.Recommendation{}
	}

	// Apply coverage by reducing counts (for RIs) or hourly commitment (for Savings Plans)
	result := make([]common.Recommendation, 0, len(recs))
	for _, rec := range recs {
		adjusted := rec

		// For Savings Plans, reduce the hourly commitment instead of count
		if rec.Service == common.ServiceSavingsPlans {
			if details, ok := rec.Details.(*common.SavingsPlanDetails); ok {
				newDetails := *details // Copy the struct
				newDetails.HourlyCommitment = newDetails.HourlyCommitment * coverage / 100
				adjusted.Details = &newDetails
				// Also adjust the estimated savings proportionally
				adjusted.EstimatedSavings = rec.EstimatedSavings * coverage / 100
				result = append(result, adjusted)
			}
			continue
		}

		// For RIs, reduce the count
		newCount := int(float64(rec.Count) * coverage / 100)
		if newCount > 0 {
			adjusted.Count = newCount
			result = append(result, adjusted)
		}
	}
	return result
}

// ApplyCountOverride overrides the count for all recommendations
func ApplyCountOverride(recs []common.Recommendation, overrideCount int32) []common.Recommendation {
	if overrideCount <= 0 {
		return recs
	}
	result := make([]common.Recommendation, len(recs))
	for i, rec := range recs {
		result[i] = rec
		result[i].Count = int(overrideCount)
	}
	return result
}

// ApplyInstanceLimit limits the total number of instances
func ApplyInstanceLimit(recs []common.Recommendation, maxInstances int32) []common.Recommendation {
	if maxInstances <= 0 {
		return recs
	}

	result := make([]common.Recommendation, 0)
	remaining := int(maxInstances)

	for _, rec := range recs {
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

// ConfirmPurchase asks the user for confirmation before proceeding
func ConfirmPurchase(totalInstances int, totalCost float64, skipConfirmation bool) bool {
	if skipConfirmation {
		return true
	}

	fmt.Printf("\n⚠️  About to purchase %d instances with estimated total cost: $%.2f\n", totalInstances, totalCost)
	fmt.Print("Do you want to proceed? (yes/no): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "yes" || response == "y"
}

// DuplicateChecker checks for existing commitments to avoid duplicates
type DuplicateChecker struct {
	LookbackHours int // How many hours to look back for recent purchases
}

// NewDuplicateChecker creates a new duplicate checker with default 24-hour lookback
func NewDuplicateChecker() *DuplicateChecker {
	return &DuplicateChecker{
		LookbackHours: 24,
	}
}

// AdjustRecommendationsForExisting adjusts recommendations based on existing commitments
// This checks for recently purchased RIs (within LookbackHours) to avoid duplicate purchases.
// Note: This is designed to prevent re-purchasing something you just bought, not to prevent
// purchasing RIs in other accounts that happen to have the same characteristics.
func (d *DuplicateChecker) AdjustRecommendationsForExisting(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) ([]common.Recommendation, error) {
	existing, err := client.GetExistingCommitments(ctx)
	if err != nil {
		return recs, err
	}

	log.Printf("    [DuplicateChecker] Found %d total existing commitments", len(existing))

	// Filter to recent purchases only (within LookbackHours)
	// This is the key filter that prevents cross-account matching issues:
	// - The API returns RIs from the current account only
	// - But recommendations come from all org accounts
	// - By only checking RECENT purchases, we avoid incorrectly matching old RIs
	//   from the payer account against recommendations for member accounts
	cutoffTime := time.Now().Add(-time.Duration(d.LookbackHours) * time.Hour)
	recentExisting := make([]common.Commitment, 0)
	for _, c := range existing {
		// Only include active or payment-pending RIs purchased after cutoff
		if (c.State == "active" || c.State == "payment-pending") && c.StartDate.After(cutoffTime) {
			recentExisting = append(recentExisting, c)
		}
	}

	log.Printf("    [DuplicateChecker] Found %d recent commitments (purchased in last %d hours)", len(recentExisting), d.LookbackHours)

	if len(recentExisting) == 0 {
		// No recent purchases, return all recommendations as-is
		return recs, nil
	}

	// Build a map of recent commitments by resource type, region, and engine (for RDS/ElastiCache)
	// Key format: resourceType|region|engine (engine may be empty for non-database services)
	existingMap := make(map[string]int)
	for _, c := range recentExisting {
		normalizedEngine := normalizeEngineName(c.Engine)
		key := fmt.Sprintf("%s|%s|%s", c.ResourceType, c.Region, normalizedEngine)
		existingMap[key] += c.Count
		log.Printf("    [DuplicateChecker] Recent RI: key=%s count=%d startDate=%s (raw engine=%s)",
			key, c.Count, c.StartDate.Format("2006-01-02 15:04:05"), c.Engine)
	}

	log.Printf("    [DuplicateChecker] Existing map has %d unique keys", len(existingMap))

	// Adjust recommendations - decrement existing count as we "use up" existing RIs
	result := make([]common.Recommendation, 0, len(recs))
	for _, rec := range recs {
		// Get engine from recommendation details if available
		engine := getEngineFromRecommendation(rec)
		key := fmt.Sprintf("%s|%s|%s", rec.ResourceType, rec.Region, engine)
		existingCount := existingMap[key]

		if existingCount >= rec.Count {
			// All of this recommendation is covered by recent RIs
			log.Printf("    [DuplicateChecker] SKIP %s: recent %d >= recommended %d", key, existingCount, rec.Count)
			existingMap[key] -= rec.Count // Use up these existing RIs
			continue
		}
		// Partial or no coverage by recent RIs
		adjusted := rec
		if existingCount > 0 {
			adjusted.Count = rec.Count - existingCount
			existingMap[key] = 0 // Use up all remaining existing RIs for this key
			log.Printf("    [DuplicateChecker] PARTIAL %s: adjusted count from %d to %d", key, rec.Count, adjusted.Count)
		}
		if adjusted.Count > 0 {
			result = append(result, adjusted)
		}
	}

	if len(result) < len(recs) {
		log.Printf("    [DuplicateChecker] Result: %d recommendations kept out of %d (avoided %d duplicates)",
			len(result), len(recs), len(recs)-len(result))
	}
	return result, nil
}

// getEngineFromRecommendation extracts the engine from recommendation details
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

// normalizeEngineName normalizes database engine names to a consistent format
// AWS RIs use: "aurora-postgresql", "aurora-mysql", "mysql", "postgres"
// Cost Explorer uses: "Aurora PostgreSQL", "Aurora MySQL", "MySQL", "PostgreSQL"
func normalizeEngineName(engine string) string {
	engineMap := map[string]string{
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
	if normalized, ok := engineMap[engine]; ok {
		return normalized
	}
	// Return lowercase as fallback
	return strings.ToLower(engine)
}

// AdjustRecommendationsForExistingRIs is an alias for AdjustRecommendationsForExisting
func (d *DuplicateChecker) AdjustRecommendationsForExistingRIs(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) ([]common.Recommendation, error) {
	return d.AdjustRecommendationsForExisting(ctx, recs, client)
}

// GetRecommendationDescription returns a human-readable description
func GetRecommendationDescription(rec common.Recommendation) string {
	desc := fmt.Sprintf("%s %s", rec.Service, rec.ResourceType)
	if rec.Details != nil {
		desc += " " + rec.Details.GetDetailDescription()
	}
	return desc
}
