package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LeanerCloud/CUDly/internal/common"
	"github.com/LeanerCloud/CUDly/internal/ec2"
	"github.com/LeanerCloud/CUDly/internal/elasticache"
	"github.com/LeanerCloud/CUDly/internal/memorydb"
	"github.com/LeanerCloud/CUDly/internal/opensearch"
	"github.com/LeanerCloud/CUDly/internal/rds"
	"github.com/LeanerCloud/CUDly/internal/recommendations"
	"github.com/LeanerCloud/CUDly/internal/redshift"
	"github.com/LeanerCloud/CUDly/providers/aws/services/savingsplans"
	_ "github.com/LeanerCloud/CUDly/providers/aws"
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
	Providers            []string
	Regions              []string
	Services             []string
	Coverage             float64
	ActualPurchase       bool
	CSVOutput            string
	CSVInput             string
	AllServices          bool
	PaymentOption        string
	TermYears            int
	IncludeRegions       []string
	ExcludeRegions       []string
	IncludeInstanceTypes []string
	ExcludeInstanceTypes []string
	IncludeEngines       []string
	ExcludeEngines       []string
	IncludeAccounts      []string
	ExcludeAccounts      []string
	SkipConfirmation      bool
	MaxInstances          int32
	OverrideCount         int32
	Profile               string
	ValidationProfile     string
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
}

// Package-level Config that cobra flags bind to
var toolCfg = Config{}

// validateFlags performs validation on command line flags before execution
func validateFlags(cmd *cobra.Command, args []string) error {
	// Validate coverage percentage
	if toolCfg.Coverage < 0 || toolCfg.Coverage > 100 {
		return fmt.Errorf("coverage percentage must be between 0 and 100, got: %.2f", toolCfg.Coverage)
	}

	// Validate max instances
	if toolCfg.MaxInstances < 0 {
		return fmt.Errorf("max-instances must be 0 (no limit) or a positive number, got: %d", toolCfg.MaxInstances)
	}

	// Validate max instances doesn't exceed reasonable limit
	if toolCfg.MaxInstances > MaxReasonableInstances {
		return fmt.Errorf("max-instances (%d) exceeds reasonable limit of %d", toolCfg.MaxInstances, MaxReasonableInstances)
	}

	// Validate override count
	if toolCfg.OverrideCount < 0 {
		return fmt.Errorf("override-count must be 0 (disabled) or a positive number, got: %d", toolCfg.OverrideCount)
	}

	// Validate override count doesn't exceed reasonable limit
	if toolCfg.OverrideCount > MaxReasonableInstances {
		return fmt.Errorf("override-count (%d) exceeds reasonable limit of %d", toolCfg.OverrideCount, MaxReasonableInstances)
	}

	// Validate payment option
	validPaymentOptions := map[string]bool{
		"all-upfront":     true,
		"partial-upfront": true,
		"no-upfront":      true,
	}
	if !validPaymentOptions[toolCfg.PaymentOption] {
		return fmt.Errorf("invalid payment option: %s. Must be one of: all-upfront, partial-upfront, no-upfront", toolCfg.PaymentOption)
	}

	// Validate term years
	if toolCfg.TermYears != 1 && toolCfg.TermYears != 3 {
		return fmt.Errorf("invalid term: %d years. Must be 1 or 3", toolCfg.TermYears)
	}

	// Warn about RDS 3-year no-upfront limitation
	if toolCfg.PaymentOption == "no-upfront" && toolCfg.TermYears == 3 {
		services := determineServicesToProcess(toolCfg)
		hasRDS := false
		for _, svc := range services {
			if svc == common.ServiceRDS {
				hasRDS = true
				break
			}
		}
		if hasRDS || toolCfg.AllServices {
			log.Println("⚠️  WARNING: AWS does not offer 3-year no-upfront Reserved Instances for RDS.")
			log.Println("    RDS 3-year RIs only support: all-upfront, partial-upfront")
			log.Println("    No RDS recommendations will be found with this combination.")
		}
	}

	// Validate CSV output path if provided
	if toolCfg.CSVOutput != "" {
		// Check if the directory exists
		dir := filepath.Dir(toolCfg.CSVOutput)
		if dir != "." && dir != "" {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return fmt.Errorf("output directory does not exist: %s", dir)
			}
		}
	}

	// Validate CSV input path if provided
	if toolCfg.CSVInput != "" {
		if _, err := os.Stat(toolCfg.CSVInput); os.IsNotExist(err) {
			return fmt.Errorf("input CSV file does not exist: %s", toolCfg.CSVInput)
		}
		if !strings.HasSuffix(strings.ToLower(toolCfg.CSVInput), ".csv") {
			return fmt.Errorf("input file must have .csv extension: %s", toolCfg.CSVInput)
		}
	}

	// Validate filter flags
	if len(toolCfg.IncludeRegions) > 0 && len(toolCfg.ExcludeRegions) > 0 {
		// Check for conflicts
		for _, inc := range toolCfg.IncludeRegions {
			for _, exc := range toolCfg.ExcludeRegions {
				if inc == exc {
					return fmt.Errorf("region '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	if len(toolCfg.IncludeInstanceTypes) > 0 && len(toolCfg.ExcludeInstanceTypes) > 0 {
		// Check for conflicts
		for _, inc := range toolCfg.IncludeInstanceTypes {
			for _, exc := range toolCfg.ExcludeInstanceTypes {
				if inc == exc {
					return fmt.Errorf("instance type '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	if len(toolCfg.IncludeEngines) > 0 && len(toolCfg.ExcludeEngines) > 0 {
		// Check for conflicts
		for _, inc := range toolCfg.IncludeEngines {
			for _, exc := range toolCfg.ExcludeEngines {
				if inc == exc {
					return fmt.Errorf("engine '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	// Validate instance types
	if err := common.ValidateInstanceTypes(toolCfg.IncludeInstanceTypes); err != nil {
		return fmt.Errorf("invalid include-instance-types: %w", err)
	}
	if err := common.ValidateInstanceTypes(toolCfg.ExcludeInstanceTypes); err != nil {
		return fmt.Errorf("invalid exclude-instance-types: %w", err)
	}

	return nil
}

// parseServices converts service names to ServiceType
func parseServices(serviceNames []string) []common.ServiceType {
	var result []common.ServiceType
	serviceMap := map[string]common.ServiceType{
		"rds":           common.ServiceRDS,
		"elasticache":   common.ServiceElastiCache,
		"ec2":           common.ServiceEC2,
		"opensearch":    common.ServiceOpenSearch,
		"elasticsearch": common.ServiceElasticsearch, // Legacy alias
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

// createPurchaseClient creates the appropriate purchase client for a service
func createPurchaseClient(service common.ServiceType, cfg aws.Config) common.PurchaseClient {
	switch service {
	case common.ServiceRDS:
		return rds.NewPurchaseClient(cfg)
	case common.ServiceElastiCache:
		return elasticache.NewPurchaseClient(cfg)
	case common.ServiceEC2:
		return ec2.NewPurchaseClient(cfg)
	case common.ServiceOpenSearch, common.ServiceElasticsearch:
		// OpenSearch client handles both service names
		return opensearch.NewPurchaseClient(cfg)
	case common.ServiceRedshift:
		return redshift.NewPurchaseClient(cfg)
	case common.ServiceMemoryDB:
		return memorydb.NewPurchaseClient(cfg)
	case common.ServiceSavingsPlans:
		return savingsplans.NewPurchaseClient(cfg)
	default:
		return nil
	}
}


// generatePurchaseID creates a descriptive purchase ID with UUID for uniqueness
func generatePurchaseID(rec any, region string, _ int, isDryRun bool, coverage float64) string {
	// Generate a short UUID suffix (first 8 characters) for uniqueness
	uuidSuffix := uuid.New().String()[:8]
	timestamp := time.Now().Format("20060102-150405")
	prefix := "ri"
	if isDryRun {
		prefix = "dryrun"
	}

	// Handle both old and new recommendation types
	switch r := rec.(type) {
	case recommendations.Recommendation:
		cleanEngine := strings.ReplaceAll(strings.ToLower(r.Engine), " ", "-")
		cleanEngine = strings.ReplaceAll(cleanEngine, "_", "-")

		instanceParts := strings.Split(r.InstanceType, ".")
		instanceSize := "unknown"
		if len(instanceParts) >= 3 {
			instanceSize = fmt.Sprintf("%s-%s", instanceParts[1], instanceParts[2])
		}

		deployment := "saz"
		if r.GetMultiAZ() {
			deployment = "maz"
		}

		// Add account name if available
		accountName := sanitizeAccountName(r.AccountName)
		coveragePct := fmt.Sprintf("%.0fpct", coverage)
		if accountName != "" {
			return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%s-%s-%s-%s",
				prefix, accountName, cleanEngine, instanceSize, r.Count, coveragePct, deployment, region, timestamp, uuidSuffix)
		}

		return fmt.Sprintf("%s-%s-%s-%dx-%s-%s-%s-%s-%s",
			prefix, cleanEngine, instanceSize, r.Count, coveragePct, deployment, region, timestamp, uuidSuffix)

	case common.Recommendation:
		service := strings.ToLower(r.GetServiceName())
		instanceType := strings.ReplaceAll(r.InstanceType, ".", "-")

		// Extract engine information from service details
		engine := ""
		switch details := r.ServiceDetails.(type) {
		case *common.RDSDetails:
			engine = strings.ToLower(details.Engine)
			engine = strings.ReplaceAll(engine, " ", "-")
			engine = strings.ReplaceAll(engine, "_", "-")
		case *common.ElastiCacheDetails:
			engine = strings.ToLower(details.Engine)
		case *common.MemoryDBDetails:
			engine = "memorydb"
		case *common.EC2Details:
			engine = strings.ToLower(details.Platform)
			engine = strings.ReplaceAll(engine, " ", "-")
			engine = strings.ReplaceAll(engine, "/", "-")
		}

		// Add account name if available
		accountName := sanitizeAccountName(r.AccountName)
		coveragePct := fmt.Sprintf("%.0fpct", coverage)
		if accountName != "" {
			if engine != "" {
				return fmt.Sprintf("%s-%s-%s-%s-%s-%s-%dx-%s-%s-%s",
					prefix, accountName, service, engine, region, instanceType, r.Count, coveragePct, timestamp, uuidSuffix)
			}
			return fmt.Sprintf("%s-%s-%s-%s-%s-%dx-%s-%s-%s",
				prefix, accountName, service, region, instanceType, r.Count, coveragePct, timestamp, uuidSuffix)
		}

		// Fallback without account name
		if engine != "" {
			return fmt.Sprintf("%s-%s-%s-%s-%s-%dx-%s-%s-%s",
				prefix, service, engine, region, instanceType, r.Count, coveragePct, timestamp, uuidSuffix)
		}
		return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%s-%s",
			prefix, service, region, instanceType, r.Count, coveragePct, timestamp, uuidSuffix)

	default:
		return fmt.Sprintf("%s-unknown-%s-%s-%s", prefix, region, timestamp, uuidSuffix)
	}
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
	result := ""
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result += string(r)
		}
	}

	// Remove leading/trailing hyphens and collapse multiple hyphens
	result = strings.Trim(result, "-")
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}

	return result
}

func runTool(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Always use the multi-service implementation
	runToolMultiService(ctx, toolCfg)
}
