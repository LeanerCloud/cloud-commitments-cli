package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	_ "github.com/LeanerCloud/CUDly/providers/aws"
	"github.com/LeanerCloud/CUDly/providers/aws/services/ec2"
	"github.com/LeanerCloud/CUDly/providers/aws/services/elasticache"
	"github.com/LeanerCloud/CUDly/providers/aws/services/memorydb"
	"github.com/LeanerCloud/CUDly/providers/aws/services/opensearch"
	"github.com/LeanerCloud/CUDly/providers/aws/services/rds"
	"github.com/LeanerCloud/CUDly/providers/aws/services/redshift"
	"github.com/LeanerCloud/CUDly/providers/aws/services/savingsplans"
	_ "github.com/LeanerCloud/CUDly/providers/azure"
	_ "github.com/LeanerCloud/CUDly/providers/gcp"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	// MaxReasonableInstances is the maximum number of instances that can be processed
	// This is a safety limit to prevent accidental large purchases
	MaxReasonableInstances = 10000
)

// Config holds all configuration for the RI helper tool
type Config struct {
	Providers              []string
	Regions                []string
	Services               []string
	Coverage               float64
	ActualPurchase         bool
	CSVOutput              string
	CSVInput               string
	AllServices            bool
	PaymentOption          string
	TermYears              int
	IncludeRegions         []string
	ExcludeRegions         []string
	IncludeInstanceTypes   []string
	ExcludeInstanceTypes   []string
	IncludeEngines         []string
	ExcludeEngines         []string
	IncludeAccounts        []string
	ExcludeAccounts        []string
	SkipConfirmation       bool
	MaxInstances           int32
	OverrideCount          int32
	Profile                string
	ValidationProfile      string
	IncludeExtendedSupport bool
	// Savings Plans specific filters
	IncludeSPTypes []string
	ExcludeSPTypes []string
	// Purchase pipeline settings
	AuditLog           string
	DryRun             bool
	IdempotencyWindow  string
	MinSavingsPct      float64
	MaxBreakEvenMonths int
	MinCount           int
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error executing command: %v", err)
	}
}

var rootCmd = &cobra.Command{
	Use:   "ri-helper",
	Short: "AWS Reserved Instance purchase tool based on Cost Explorer recommendations",
	Long: `A tool that fetches Reserved Instance recommendations from AWS Cost Explorer
for multiple services (RDS, ElastiCache, EC2, OpenSearch, Redshift, MemoryDB) and
purchases them based on specified coverage percentage. Supports multiple regions.`,
	PreRunE: validateFlags,
	Run:     runTool,
}

func init() {
	// Note: We still bind to package-level variables here for cobra's flag system
	// These will be copied into a ToolConfig in runTool
	rootCmd.Flags().StringSliceVarP(&toolCfg.Regions, "regions", "r", []string{}, "AWS regions (comma-separated or multiple flags). If empty, auto-discovers regions from recommendations")
	rootCmd.Flags().StringSliceVarP(&toolCfg.Services, "services", "s", []string{"rds"}, "Services to process (rds, elasticache, ec2, opensearch, redshift, memorydb, savingsplans)")
	rootCmd.Flags().BoolVar(&toolCfg.AllServices, "all-services", false, "Process all supported services")
	rootCmd.Flags().Float64VarP(&toolCfg.Coverage, "coverage", "c", 80.0, "Percentage of recommendations to purchase (0-100)")
	rootCmd.Flags().BoolVar(&toolCfg.ActualPurchase, "purchase", false, "Actually purchase RIs instead of just printing the data")
	rootCmd.Flags().StringVarP(&toolCfg.CSVOutput, "output", "o", "", "Output CSV file path (if not specified, auto-generates filename)")
	rootCmd.Flags().StringVarP(&toolCfg.CSVInput, "input-csv", "i", "", "Input CSV file with recommendations to purchase")
	rootCmd.Flags().StringVarP(&toolCfg.PaymentOption, "payment", "p", "no-upfront", "Payment option (all-upfront, partial-upfront, no-upfront)")
	rootCmd.Flags().IntVarP(&toolCfg.TermYears, "term", "t", 3, "Term in years (1 or 3)")
	rootCmd.Flags().StringVar(&toolCfg.Profile, "profile", "", "AWS profile to use (defaults to AWS_PROFILE env var or default profile)")

	// Filter flags
	rootCmd.Flags().StringSliceVar(&toolCfg.IncludeRegions, "include-regions", []string{}, "Only include recommendations for these regions (comma-separated)")
	rootCmd.Flags().StringSliceVar(&toolCfg.ExcludeRegions, "exclude-regions", []string{}, "Exclude recommendations for these regions (comma-separated)")
	rootCmd.Flags().StringSliceVar(&toolCfg.IncludeInstanceTypes, "include-instance-types", []string{}, "Only include these instance types (comma-separated, e.g., 'db.t3.micro,cache.t3.small')")
	rootCmd.Flags().StringSliceVar(&toolCfg.ExcludeInstanceTypes, "exclude-instance-types", []string{}, "Exclude these instance types (comma-separated)")
	rootCmd.Flags().StringSliceVar(&toolCfg.IncludeEngines, "include-engines", []string{}, "Only include these engines (comma-separated, e.g., 'redis,mysql,postgresql')")
	rootCmd.Flags().StringSliceVar(&toolCfg.ExcludeEngines, "exclude-engines", []string{}, "Exclude these engines (comma-separated)")
	rootCmd.Flags().StringSliceVar(&toolCfg.IncludeAccounts, "include-accounts", []string{}, "Only include recommendations for these account names (comma-separated)")
	rootCmd.Flags().StringSliceVar(&toolCfg.ExcludeAccounts, "exclude-accounts", []string{}, "Exclude recommendations for these account names (comma-separated)")
	rootCmd.Flags().BoolVar(&toolCfg.SkipConfirmation, "yes", false, "Skip confirmation prompt for purchases (use with caution)")
	rootCmd.Flags().Int32Var(&toolCfg.MaxInstances, "max-instances", 0, "Maximum total number of instances to purchase (0 = no limit)")
	rootCmd.Flags().Int32Var(&toolCfg.OverrideCount, "override-count", 0, "Override recommendation count with fixed number for all selected RIs (0 = use recommendation or coverage)")
	rootCmd.Flags().StringVar(&toolCfg.ValidationProfile, "validation-profile", "", "AWS profile to use for validating running instances (if different from main profile)")
	rootCmd.Flags().BoolVar(&toolCfg.IncludeExtendedSupport, "include-extended-support", false, "Include instances running on extended support engine versions (by default they are excluded)")

	// Savings Plans specific filters
	rootCmd.Flags().StringSliceVar(&toolCfg.IncludeSPTypes, "include-sp-types", []string{}, "Only include these Savings Plan types (comma-separated: Compute, EC2Instance, SageMaker, Database)")
	rootCmd.Flags().StringSliceVar(&toolCfg.ExcludeSPTypes, "exclude-sp-types", []string{}, "Exclude these Savings Plan types (comma-separated: Compute, EC2Instance, SageMaker, Database)")

	// Purchase pipeline flags
	rootCmd.Flags().StringVar(&toolCfg.AuditLog, "audit-log", "./cudly-audit.jsonl", "Path to JSONL audit log file")
	rootCmd.Flags().BoolVar(&toolCfg.DryRun, "dry-run", true, "Dry-run mode: show what would be purchased without actually buying")
	rootCmd.Flags().StringVar(&toolCfg.IdempotencyWindow, "idempotency-window", "24h", "Lookback window for duplicate purchase detection")
	rootCmd.Flags().Float64Var(&toolCfg.MinSavingsPct, "min-savings-pct", 0, "Minimum savings percentage to include a recommendation (0 = no filter)")
	rootCmd.Flags().IntVar(&toolCfg.MaxBreakEvenMonths, "max-break-even-months", 0, "Maximum break-even period in months (0 = no filter)")
	rootCmd.Flags().IntVar(&toolCfg.MinCount, "min-count", 0, "Minimum instance count to include a recommendation (0 = no filter)")
}

// Package-level Config that cobra flags bind to
var toolCfg = Config{}

// validateFlags is now defined in validators.go

// parseServices converts service names to ServiceType
func parseServices(serviceNames []string) []common.ServiceType {
	var result []common.ServiceType
	serviceMap := map[string]common.ServiceType{
		"rds":           common.ServiceRDS,
		"elasticache":   common.ServiceElastiCache,
		"ec2":           common.ServiceEC2,
		"opensearch":    common.ServiceOpenSearch,
		"elasticsearch": common.ServiceOpenSearch, // Legacy alias maps to OpenSearch
		"redshift":      common.ServiceRedshift,
		"memorydb":      common.ServiceMemoryDB,
		"savingsplans":  common.ServiceSavingsPlans,
		"sp":            common.ServiceSavingsPlans, // Short alias
	}

	for _, name := range serviceNames {
		if service, ok := serviceMap[strings.ToLower(name)]; ok {
			result = append(result, service)
		} else {
			log.Printf("Warning: Unknown service '%s', skipping", name)
		}
	}

	return result
}

// getAllServices returns all supported services
func getAllServices() []common.ServiceType {
	return []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
		common.ServiceSavingsPlans,
	}
}

// createServiceClient creates the appropriate service client for a service
func createServiceClient(service common.ServiceType, cfg aws.Config) provider.ServiceClient {
	switch service {
	case common.ServiceRDS:
		return rds.NewClient(cfg)
	case common.ServiceElastiCache:
		return elasticache.NewClient(cfg)
	case common.ServiceEC2:
		return ec2.NewClient(cfg)
	case common.ServiceOpenSearch:
		return opensearch.NewClient(cfg)
	case common.ServiceRedshift:
		return redshift.NewClient(cfg)
	case common.ServiceMemoryDB:
		return memorydb.NewClient(cfg)
	case common.ServiceSavingsPlans:
		return savingsplans.NewClient(cfg)
	default:
		return nil
	}
}

// generatePurchaseID creates a descriptive purchase ID with UUID for uniqueness
func generatePurchaseID(rec common.Recommendation, region string, _ int, isDryRun bool, coverage float64) string {
	// Generate a short UUID suffix (first 8 characters) for uniqueness
	uuidSuffix := uuid.New().String()[:8]
	timestamp := time.Now().Format("20060102-150405")
	prefix := "ri"
	if isDryRun {
		prefix = "dryrun"
	}

	service := strings.ToLower(string(rec.Service))
	instanceType := strings.ReplaceAll(rec.ResourceType, ".", "-")

	// Extract engine information from service details
	engine := ""
	switch details := rec.Details.(type) {
	case common.DatabaseDetails:
		engine = strings.ToLower(details.Engine)
		engine = strings.ReplaceAll(engine, " ", "-")
		engine = strings.ReplaceAll(engine, "_", "-")
	case common.CacheDetails:
		engine = strings.ToLower(details.Engine)
	case common.ComputeDetails:
		engine = strings.ToLower(details.Platform)
		engine = strings.ReplaceAll(engine, " ", "-")
		engine = strings.ReplaceAll(engine, "/", "-")
	}

	// Add account name if available
	accountName := sanitizeAccountName(rec.AccountName)
	coveragePct := fmt.Sprintf("%.0fpct", coverage)
	if accountName != "" {
		if engine != "" {
			return fmt.Sprintf("%s-%s-%s-%s-%s-%s-%dx-%s-%s-%s",
				prefix, accountName, service, engine, region, instanceType, rec.Count, coveragePct, timestamp, uuidSuffix)
		}
		return fmt.Sprintf("%s-%s-%s-%s-%s-%dx-%s-%s-%s",
			prefix, accountName, service, region, instanceType, rec.Count, coveragePct, timestamp, uuidSuffix)
	}

	// Fallback without account name
	if engine != "" {
		return fmt.Sprintf("%s-%s-%s-%s-%s-%dx-%s-%s-%s",
			prefix, service, engine, region, instanceType, rec.Count, coveragePct, timestamp, uuidSuffix)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%s-%s",
		prefix, service, region, instanceType, rec.Count, coveragePct, timestamp, uuidSuffix)
}

// sanitizeAccountName converts account name to a filesystem/ID-safe format
func sanitizeAccountName(accountName string) string {
	if accountName == "" {
		return ""
	}

	// Convert to lowercase
	clean := strings.ToLower(accountName)

	// Replace spaces and special chars with hyphens
	clean = strings.ReplaceAll(clean, " ", "-")
	clean = strings.ReplaceAll(clean, "_", "-")
	clean = strings.ReplaceAll(clean, ".", "-")

	// Remove any characters that aren't alphanumeric or hyphens
	var b strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	result := b.String()

	// Remove leading/trailing hyphens and collapse multiple hyphens
	result = strings.Trim(result, "-")
	result = regexp.MustCompile("-{2,}").ReplaceAllString(result, "-")

	return result
}

func runTool(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Always use the multi-service implementation
	runToolMultiService(ctx, toolCfg)
}
