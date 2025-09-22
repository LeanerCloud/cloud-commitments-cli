package main

import (
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
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