package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestGeneratePurchaseIDComprehensive(t *testing.T) {
	// Use a test coverage value
	testCoverage := 75.0

	tests := []struct {
		name                string
		region              string
		rec                 common.Recommendation
		expectedContains    []string
		expectedNotContains []string
		isDryRun            bool
	}{
		{
			name: "RDS with account name and engine",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.r5.large",
				Count:        3,
				AccountName:  "Production Account",
				Details: &common.DatabaseDetails{
					Engine: "PostgreSQL",
				},
			},
			region:   "eu-west-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "production-account", "rds", "postgresql", "eu-west-1",
				"db-r5-large", "3x", "75pct",
			},
		},
		{
			name: "ElastiCache with Redis engine",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.r5.xlarge",
				Count:        5,
				Details: &common.CacheDetails{
					Engine: "Redis",
				},
			},
			region:   "us-west-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "elasticache", "redis", "us-west-2",
				"cache-r5-xlarge", "5x", "75pct",
			},
		},
		{
			name: "EC2 with platform",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "m5.2xlarge",
				Count:        10,
				Details: common.ComputeDetails{
					Platform: "Linux/UNIX",
				},
			},
			region:   "ap-southeast-1",
			isDryRun: true,
			expectedContains: []string{
				"dryrun-", "ec2", "linux-unix", "ap-southeast-1",
				"m5-2xlarge", "10x", "75pct",
			},
		},
		{
			name: "MemoryDB recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceMemoryDB,
				ResourceType: "db.r6g.large",
				Count:        2,
				Details:      &common.CacheDetails{Engine: "redis"},
			},
			region:   "us-east-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "memorydb", "memorydb", "us-east-1",
				"db-r6g-large", "2x", "75pct",
			},
		},
		{
			name: "OpenSearch without engine",
			rec: common.Recommendation{
				Service:      common.ServiceOpenSearch,
				ResourceType: "r5.large.search",
				Count:        4,
			},
			region:   "eu-central-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "opensearch", "eu-central-1",
				"r5-large-search", "4x", "75pct",
			},
		},
		{
			name: "Elasticsearch alias (same as OpenSearch)",
			rec: common.Recommendation{
				Service:      common.ServiceElasticsearch, // Should work as alias for OpenSearch
				ResourceType: "m5.xlarge.elasticsearch",
				Count:        3,
			},
			region:   "us-west-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "opensearch", "us-west-1",
				"m5-xlarge-elasticsearch", "3x", "75pct",
			},
		},
		{
			name: "Redshift without engine",
			rec: common.Recommendation{
				Service:      common.ServiceRedshift,
				ResourceType: "dc2.large",
				Count:        8,
			},
			region:   "us-east-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "redshift", "us-east-2",
				"dc2-large", "8x", "75pct",
			},
		},
		{
			name: "RDS recommendation with account",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.r6g.xlarge",
				Count:        15,
				AccountName:  "Staging",
				Details: &common.DatabaseDetails{
					Engine:   "aurora-mysql",
					AZConfig: "multi-az",
				},
			},
			region:   "ca-central-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "staging", "aurora-mysql", "r6g-xlarge",
				"15x", "75pct", "ca-central-1",
			},
		},
		{
			name: "ElastiCache single-AZ recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.m5.large",
				Count:        1,
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			region:   "ap-northeast-1",
			isDryRun: true,
			expectedContains: []string{
				"dryrun-", "redis", "m5-large",
				"1x", "75pct", "ap-northeast-1",
			},
		},
		{
			name: "Recommendation with special characters in engine",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.t3.micro",
				Count:        20,
				Details: &common.DatabaseDetails{
					Engine: "MySQL_8.0_Community",
				},
			},
			region:   "us-west-1",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "rds", "mysql-8.0-community",
				"db-t3-micro", "20x", "75pct",
			},
		},
		{
			name: "Large count recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "t3.nano",
				Count:        999,
			},
			region:   "eu-west-2",
			isDryRun: false,
			expectedContains: []string{
				"ri-", "ec2", "eu-west-2",
				"t3-nano", "999x", "75pct",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, 1, tt.isDryRun, testCoverage)

			// Check expected contains
			for _, expected := range tt.expectedContains {
				assert.Contains(t, result, expected, "Expected ID to contain '%s'", expected)
			}

			// Check expected not contains
			for _, notExpected := range tt.expectedNotContains {
				assert.NotContains(t, result, notExpected, "Expected ID not to contain '%s'", notExpected)
			}

			// Should always contain timestamp and UUID
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, result)
		})
	}
}
