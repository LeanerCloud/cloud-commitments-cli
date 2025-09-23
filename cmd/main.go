package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/ec2"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/elasticache"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/memorydb"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/opensearch"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/rds"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/redshift"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	regions              []string
	services             []string
	coverage             float64
	actualPurchase       bool
	csvOutput            string
	allServices          bool
	paymentOption        string
	termYears            int
	includeRegions       []string
	excludeRegions       []string
	includeInstanceTypes []string
	excludeInstanceTypes []string
	includeEngines       []string
	excludeEngines       []string
	skipConfirmation     bool
	maxInstances         int32
)

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
	Run: runTool,
}

func init() {
	rootCmd.Flags().StringSliceVarP(&regions, "regions", "r", []string{}, "AWS regions (comma-separated or multiple flags). If empty, auto-discovers regions from recommendations")
	rootCmd.Flags().StringSliceVarP(&services, "services", "s", []string{"rds"}, "Services to process (rds, elasticache, ec2, opensearch, redshift, memorydb)")
	rootCmd.Flags().BoolVar(&allServices, "all-services", false, "Process all supported services")
	rootCmd.Flags().Float64VarP(&coverage, "coverage", "c", 80.0, "Percentage of recommendations to purchase (0-100)")
	rootCmd.Flags().BoolVar(&actualPurchase, "purchase", false, "Actually purchase RIs instead of just printing the data")
	rootCmd.Flags().StringVarP(&csvOutput, "output", "o", "", "Output CSV file path (if not specified, auto-generates filename)")
	rootCmd.Flags().StringVarP(&paymentOption, "payment", "p", "no-upfront", "Payment option (all-upfront, partial-upfront, no-upfront)")
	rootCmd.Flags().IntVarP(&termYears, "term", "t", 3, "Term in years (1 or 3)")

	// Filter flags
	rootCmd.Flags().StringSliceVar(&includeRegions, "include-regions", []string{}, "Only include recommendations for these regions (comma-separated)")
	rootCmd.Flags().StringSliceVar(&excludeRegions, "exclude-regions", []string{}, "Exclude recommendations for these regions (comma-separated)")
	rootCmd.Flags().StringSliceVar(&includeInstanceTypes, "include-instance-types", []string{}, "Only include these instance types (comma-separated, e.g., 'db.t3.micro,cache.t3.small')")
	rootCmd.Flags().StringSliceVar(&excludeInstanceTypes, "exclude-instance-types", []string{}, "Exclude these instance types (comma-separated)")
	rootCmd.Flags().StringSliceVar(&includeEngines, "include-engines", []string{}, "Only include these engines (comma-separated, e.g., 'redis,mysql,postgresql')")
	rootCmd.Flags().StringSliceVar(&excludeEngines, "exclude-engines", []string{}, "Exclude these engines (comma-separated)")
	rootCmd.Flags().BoolVar(&skipConfirmation, "yes", false, "Skip confirmation prompt for purchases (use with caution)")
	rootCmd.Flags().Int32Var(&maxInstances, "max-instances", 0, "Maximum total number of instances to purchase (0 = no limit)")

	// Add validation for flags
	rootCmd.PreRunE = validateFlags
}

// validateFlags performs validation on command line flags before execution
func validateFlags(cmd *cobra.Command, args []string) error {
	// Validate coverage percentage
	if coverage < 0 || coverage > 100 {
		return fmt.Errorf("coverage percentage must be between 0 and 100, got: %.2f", coverage)
	}

	// Validate max instances
	if maxInstances < 0 {
		return fmt.Errorf("max-instances must be 0 (no limit) or a positive number, got: %d", maxInstances)
	}

	// Validate payment option
	validPaymentOptions := map[string]bool{
		"all-upfront":     true,
		"partial-upfront": true,
		"no-upfront":      true,
	}
	if !validPaymentOptions[paymentOption] {
		return fmt.Errorf("invalid payment option: %s. Must be one of: all-upfront, partial-upfront, no-upfront", paymentOption)
	}

	// Validate term years
	if termYears != 1 && termYears != 3 {
		return fmt.Errorf("invalid term: %d years. Must be 1 or 3", termYears)
	}

	// Validate CSV output path if provided
	if csvOutput != "" {
		// Check if the directory exists
		dir := filepath.Dir(csvOutput)
		if dir != "." && dir != "" {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return fmt.Errorf("output directory does not exist: %s", dir)
			}
		}
	}

	// Validate filter flags
	if len(includeRegions) > 0 && len(excludeRegions) > 0 {
		// Check for conflicts
		for _, inc := range includeRegions {
			for _, exc := range excludeRegions {
				if inc == exc {
					return fmt.Errorf("region '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	if len(includeInstanceTypes) > 0 && len(excludeInstanceTypes) > 0 {
		// Check for conflicts
		for _, inc := range includeInstanceTypes {
			for _, exc := range excludeInstanceTypes {
				if inc == exc {
					return fmt.Errorf("instance type '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	if len(includeEngines) > 0 && len(excludeEngines) > 0 {
		// Check for conflicts
		for _, inc := range includeEngines {
			for _, exc := range excludeEngines {
				if inc == exc {
					return fmt.Errorf("engine '%s' cannot be both included and excluded", inc)
				}
			}
		}
	}

	return nil
}

// parseServices converts service names to ServiceType
func parseServices(serviceNames []string) []common.ServiceType {
	var result []common.ServiceType
	serviceMap := map[string]common.ServiceType{
		"rds":         common.ServiceRDS,
		"elasticache": common.ServiceElastiCache,
		"ec2":         common.ServiceEC2,
		"opensearch":  common.ServiceOpenSearch,
		"elasticsearch": common.ServiceElasticsearch, // Legacy alias
		"redshift":    common.ServiceRedshift,
		"memorydb":    common.ServiceMemoryDB,
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
	default:
		return nil
	}
}


// generatePurchaseID creates a descriptive purchase ID with UUID for uniqueness
func generatePurchaseID(rec any, region string, _ int, isDryRun bool) string {
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

		return fmt.Sprintf("%s-%s-%s-%dx-%s-%s-%s-%s",
			prefix, cleanEngine, instanceSize, r.Count, deployment, region, timestamp, uuidSuffix)

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

		if engine != "" {
			return fmt.Sprintf("%s-%s-%s-%s-%s-%dx-%s-%s",
				prefix, service, engine, region, instanceType, r.Count, timestamp, uuidSuffix)
		}
		return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%s",
			prefix, service, region, instanceType, r.Count, timestamp, uuidSuffix)

	default:
		return fmt.Sprintf("%s-unknown-%s-%s-%s", prefix, region, timestamp, uuidSuffix)
	}
}

func runTool(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Always use the multi-service implementation
	runToolMultiService(ctx)
}

