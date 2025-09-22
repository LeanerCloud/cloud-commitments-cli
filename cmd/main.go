package main

import (
	"context"
	"fmt"
	"log"
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
	"github.com/spf13/cobra"
)

var (
	regions        []string
	services       []string
	coverage       float64
	actualPurchase bool
	csvOutput      string
	allServices    bool
	paymentOption  string
	termYears      int
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


// generatePurchaseID creates a descriptive purchase ID
func generatePurchaseID(rec any, region string, index int, isDryRun bool) string {
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

		return fmt.Sprintf("%s-%s-%s-%dx-%s-%s-%s-%03d",
			prefix, cleanEngine, instanceSize, r.Count, deployment, region, timestamp, index)

	case common.Recommendation:
		service := strings.ToLower(r.GetServiceName())
		instanceType := strings.ReplaceAll(r.InstanceType, ".", "-")

		return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%03d",
			prefix, service, region, instanceType, r.Count, timestamp, index)

	default:
		return fmt.Sprintf("%s-unknown-%s-%s-%03d", prefix, region, timestamp, index)
	}
}

func runTool(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Always use the multi-service implementation
	runToolMultiService(ctx)
}

