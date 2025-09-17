package main

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/purchase"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)


func TestGeneratePurchaseID(t *testing.T) {
	tests := []struct {
		name      string
		rec       interface{}
		region    string
		index     int
		isDryRun  bool
		contains  []string
	}{
		{
			name: "RDS recommendation dry run",
			rec: recommendations.Recommendation{
				Engine:       "mysql",
				InstanceType: "db.t3.medium",
				Count:        2,
			},
			region:   "us-east-1",
			index:    1,
			isDryRun: true,
			contains: []string{"dryrun", "mysql", "t3-medium", "2x", "us-east-1", "001"},
		},
		{
			name: "RDS recommendation actual purchase",
			rec: recommendations.Recommendation{
				Engine:       "postgres",
				InstanceType: "db.r6g.large",
				Count:        3,
			},
			region:   "eu-west-1",
			index:    5,
			isDryRun: false,
			contains: []string{"ri", "postgres", "r6g-large", "3x", "eu-west-1", "005"},
		},
		{
			name: "Common recommendation dry run",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				InstanceType: "m5.large",
				Count:        4,
			},
			region:   "ap-south-1",
			index:    10,
			isDryRun: true,
			contains: []string{"dryrun", "ec2", "ap-south-1", "m5-large", "4x", "010"},
		},
		{
			name:     "Unknown type",
			rec:      struct{}{},
			region:   "us-west-2",
			index:    0,
			isDryRun: true,
			contains: []string{"dryrun", "unknown", "us-west-2", "000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun)

			for _, expected := range tt.contains {
				assert.Contains(t, result, expected)
			}
		})
	}
}


func TestRDSPurchaseClientAdapter_ValidateOffering(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	client := purchase.NewClient(cfg)
	adapter := &rdsPurchaseClientAdapter{client: client}

	ctx := context.Background()
	rec := common.Recommendation{
		Region:        "us-east-1",
		InstanceType:  "db.t3.micro",
		PaymentOption: "no-upfront",
		Term:          12,
		ServiceDetails: &common.RDSDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}

	// This will error because we're not connected to AWS, but it validates the adapter
	err := adapter.ValidateOffering(ctx, rec)
	assert.Error(t, err) // Expected to fail without AWS connection
}

func TestRDSPurchaseClientAdapter_GetOfferingDetails(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	client := purchase.NewClient(cfg)
	adapter := &rdsPurchaseClientAdapter{client: client}

	ctx := context.Background()
	rec := common.Recommendation{
		Region:        "us-east-1",
		InstanceType:  "db.t3.micro",
		PaymentOption: "partial-upfront",
		Term:          36,
		ServiceDetails: &common.RDSDetails{
			Engine:   "postgres",
			AZConfig: "multi-az",
		},
	}

	// This will error because we're not connected to AWS, but it validates the adapter
	_, err := adapter.GetOfferingDetails(ctx, rec)
	assert.Error(t, err) // Expected to fail without AWS connection
}

func TestRDSPurchaseClientAdapter_BatchPurchase(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	client := purchase.NewClient(cfg)
	adapter := &rdsPurchaseClientAdapter{client: client}

	ctx := context.Background()
	recommendations := []common.Recommendation{
		{
			Region:        "us-east-1",
			InstanceType:  "db.t3.small",
			PaymentOption: "no-upfront",
			Term:          12,
			Count:         1,
			ServiceDetails: &common.RDSDetails{
				Engine:   "mysql",
				AZConfig: "single-az",
			},
		},
		{
			Region:        "us-east-1",
			InstanceType:  "db.r6g.large",
			PaymentOption: "all-upfront",
			Term:          36,
			Count:         2,
			ServiceDetails: &common.RDSDetails{
				Engine:   "postgres",
				AZConfig: "multi-az",
			},
		},
	}

	// Test with no delay
	results := adapter.BatchPurchase(ctx, recommendations, 0)
	assert.Len(t, results, 2)

	// Test with delay
	start := time.Now()
	results = adapter.BatchPurchase(ctx, recommendations, 100*time.Millisecond)
	duration := time.Since(start)

	assert.Len(t, results, 2)
	// Should have at least one delay between purchases
	assert.GreaterOrEqual(t, duration, 100*time.Millisecond)
}

func TestRootCommandConfiguration(t *testing.T) {
	// Test that rootCmd is properly configured
	assert.NotNil(t, rootCmd)
	assert.Equal(t, "ri-helper", rootCmd.Use)
	assert.Contains(t, rootCmd.Short, "Reserved Instance")

	// Test that all flags are registered
	assert.NotNil(t, rootCmd.Flags().Lookup("regions"))
	assert.NotNil(t, rootCmd.Flags().Lookup("services"))
	assert.NotNil(t, rootCmd.Flags().Lookup("all-services"))
	assert.NotNil(t, rootCmd.Flags().Lookup("coverage"))
	assert.NotNil(t, rootCmd.Flags().Lookup("purchase"))
	assert.NotNil(t, rootCmd.Flags().Lookup("output"))
	assert.NotNil(t, rootCmd.Flags().Lookup("payment"))
	assert.NotNil(t, rootCmd.Flags().Lookup("term"))
}

func BenchmarkGeneratePurchaseID(b *testing.B) {
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		InstanceType: "db.t3.medium",
		Count:        2,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generatePurchaseID(rec, "us-east-1", i, true)
	}
}