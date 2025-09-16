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
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
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
		// Use the existing RDS purchase client with adapter
		rdsClient := purchase.NewClient(cfg)
		return &rdsPurchaseClientAdapter{client: rdsClient}
	case common.ServiceElastiCache:
		return elasticache.NewPurchaseClient(cfg)
	case common.ServiceEC2:
		return ec2.NewPurchaseClient(cfg)
	// TODO: Add other service clients when implemented
	default:
		return nil
	}
}

// rdsPurchaseClientAdapter adapts the existing RDS client to the new interface
type rdsPurchaseClientAdapter struct {
	client *purchase.Client
}

func (a *rdsPurchaseClientAdapter) PurchaseRI(ctx context.Context, rec common.Recommendation) common.PurchaseResult {
	// Convert common.Recommendation to recommendations.Recommendation
	oldRec := recommendations.Recommendation{
		Region:         rec.Region,
		InstanceType:   rec.InstanceType,
		PaymentOption:  rec.PaymentOption,
		Term:           int32(rec.Term),
		Count:          rec.Count,
		EstimatedCost:  rec.EstimatedCost,
		SavingsPercent: rec.SavingsPercent,
		Timestamp:      rec.Timestamp,
		Description:    rec.Description,
	}

	// Extract RDS-specific details
	if rdsDetails, ok := rec.ServiceDetails.(*common.RDSDetails); ok {
		oldRec.Engine = rdsDetails.Engine
		oldRec.AZConfig = rdsDetails.AZConfig
	}

	// Call the original method
	oldResult := a.client.PurchaseRI(ctx, oldRec)

	// Convert back to common.PurchaseResult
	return common.PurchaseResult{
		Config:        rec,
		Success:       oldResult.Success,
		PurchaseID:    oldResult.PurchaseID,
		ReservationID: oldResult.ReservationID,
		Message:       oldResult.Message,
		ActualCost:    oldResult.ActualCost,
		Timestamp:     oldResult.Timestamp,
	}
}

func (a *rdsPurchaseClientAdapter) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	oldRec := recommendations.Recommendation{
		Region:         rec.Region,
		InstanceType:   rec.InstanceType,
		PaymentOption:  rec.PaymentOption,
		Term:           int32(rec.Term),
		Count:          rec.Count,
	}
	if rdsDetails, ok := rec.ServiceDetails.(*common.RDSDetails); ok {
		oldRec.Engine = rdsDetails.Engine
		oldRec.AZConfig = rdsDetails.AZConfig
	}
	return a.client.ValidateOffering(ctx, oldRec)
}

func (a *rdsPurchaseClientAdapter) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	oldRec := recommendations.Recommendation{
		Region:         rec.Region,
		InstanceType:   rec.InstanceType,
		PaymentOption:  rec.PaymentOption,
		Term:           int32(rec.Term),
	}
	if rdsDetails, ok := rec.ServiceDetails.(*common.RDSDetails); ok {
		oldRec.Engine = rdsDetails.Engine
		oldRec.AZConfig = rdsDetails.AZConfig
	}

	oldDetails, err := a.client.GetOfferingDetails(ctx, oldRec)
	if err != nil {
		return nil, err
	}

	return &common.OfferingDetails{
		OfferingID:    oldDetails.OfferingID,
		InstanceType:  oldDetails.InstanceType,
		Engine:        oldDetails.Engine,
		Duration:      oldDetails.Duration,
		PaymentOption: oldDetails.PaymentOption,
		MultiAZ:       oldDetails.MultiAZ,
		FixedPrice:    oldDetails.FixedPrice,
		UsagePrice:    oldDetails.UsagePrice,
		CurrencyCode:  oldDetails.CurrencyCode,
		OfferingType:  oldDetails.OfferingType,
	}, nil
}

func (a *rdsPurchaseClientAdapter) BatchPurchase(ctx context.Context, recommendations []common.Recommendation, delayBetweenPurchases time.Duration) []common.PurchaseResult {
	results := make([]common.PurchaseResult, 0, len(recommendations))
	for i, rec := range recommendations {
		result := a.PurchaseRI(ctx, rec)
		results = append(results, result)
		if i < len(recommendations)-1 && delayBetweenPurchases > 0 {
			time.Sleep(delayBetweenPurchases)
		}
	}
	return results
}

// generatePurchaseID creates a descriptive purchase ID
func generatePurchaseID(rec interface{}, region string, index int, isDryRun bool) string {
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

