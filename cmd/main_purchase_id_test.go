package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestGeneratePurchaseID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		region         string
		expectedPrefix string
		rec            common.Recommendation
		index          int
		coverage       float64
		isDryRun       bool
	}{
		{
			name: "RDS Recommendation - dry run",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.t3.micro",
				Count:        2,
				Details: &common.DatabaseDetails{
					Engine:   "mysql",
					AZConfig: "single-az",
				},
			},
			region:         "us-east-1",
			index:          1,
			isDryRun:       true,
			coverage:       80.0,
			expectedPrefix: "dryrun-rds-mysql-us-east-1-db-t3-micro-2x",
		},
		{
			name: "EC2 Recommendation - actual purchase",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "t3.large",
				Count:        5,
			},
			region:         "eu-west-1",
			index:          3,
			isDryRun:       false,
			coverage:       80.0,
			expectedPrefix: "ri-ec2-eu-west-1-t3-large-5x",
		},
		{
			name: "ElastiCache Recommendation - dry run",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.r5.large",
				Count:        1,
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			region:         "us-west-2",
			index:          2,
			isDryRun:       true,
			coverage:       80.0,
			expectedPrefix: "dryrun-elasticache-redis-us-west-2-cache-r5-large-1x",
		},
		{
			name: "RDS Recommendation - multi-AZ",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.m5.xlarge",
				Count:        3,
				Details: &common.DatabaseDetails{
					Engine:   "postgres",
					AZConfig: "multi-az",
				},
			},
			region:         "ap-southeast-1",
			index:          5,
			isDryRun:       false,
			coverage:       80.0,
			expectedPrefix: "ri-rds-postgres-ap-southeast-1-db-m5-xlarge-3x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun, tt.coverage)
			assert.Contains(t, result, tt.expectedPrefix)
			// Should contain timestamp (YYYYMMDD-HHMMSS) and UUID suffix (8 chars)
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, result)
		})
	}
}

func TestGeneratePurchaseIDEdgeCases(t *testing.T) {
	t.Parallel()
	testCoverage := 80.0

	// Test with recommendations that have special characters
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		ResourceType: "db.r5b.2xlarge",
		Count:        10,
		Details: &common.DatabaseDetails{
			Engine:   "MySQL 8.0",
			AZConfig: "single-az",
		},
	}

	id := generatePurchaseID(rec, "us-east-1", 999, false, testCoverage)
	assert.Contains(t, id, "rds")
	assert.Contains(t, id, "r5b-2xlarge")
	assert.Contains(t, id, "10x")
	// Index is no longer included due to UUID replacement

	// Test with empty region
	id = generatePurchaseID(rec, "", 1, true, testCoverage)
	assert.Contains(t, id, "dryrun")

	// Test with very long instance type
	rec.ResourceType = "db.x2gd.metal.16xlarge"
	id = generatePurchaseID(rec, "ap-south-1", 1, false, testCoverage)
	assert.Contains(t, id, "x2gd-metal")
}

func TestGeneratePurchaseIDCoverageVariations(t *testing.T) {
	t.Parallel()
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		ResourceType: "db.t3.small",
		Count:        1,
		Details: &common.DatabaseDetails{
			Engine: "mysql",
		},
	}

	tests := []struct {
		name             string
		expectedCoverage string
		coverage         float64
	}{
		{"Coverage 0%", "0pct", 0.0},
		{"Coverage 50%", "50pct", 50.0},
		{"Coverage 75.5%", "76pct", 75.5}, // Rounds to nearest integer
		{"Coverage 99%", "99pct", 99.0},
		{"Coverage 100%", "100pct", 100.0},
		{"Coverage 33.3%", "33pct", 33.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(rec, "us-east-1", 1, false, tt.coverage)
			assert.Contains(t, result, tt.expectedCoverage)
		})
	}
}

func TestGeneratePurchaseID_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		region   string
		rec      common.Recommendation
		index    int
		isDryRun bool
	}{
		{
			name: "RDS dry run",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.t3.micro",
				Count:        2,
			},
			region:   "us-east-1",
			index:    1,
			isDryRun: true,
		},
		{
			name: "EC2 actual purchase",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "t3.large",
				Count:        5,
			},
			region:   "eu-west-1",
			index:    99,
			isDryRun: false,
		},
		{
			name: "ElastiCache with dots in instance type",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.r6g.2xlarge",
				Count:        1,
			},
			region:   "ap-southeast-1",
			index:    1000,
			isDryRun: false,
		},
		{
			name: "Unknown service",
			rec: common.Recommendation{
				Service:      common.ServiceType("future-service"),
				ResourceType: "unknown.large",
				Count:        10,
			},
			region:   "us-west-2",
			index:    1,
			isDryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testCoverage := 80.0
			id := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun, testCoverage)

			// Verify ID contains expected parts
			if tt.isDryRun {
				assert.Contains(t, id, "dryrun")
			} else {
				assert.Contains(t, id, "ri")
			}

			assert.Contains(t, id, tt.region)
			assert.Contains(t, id, strings.ReplaceAll(tt.rec.ResourceType, ".", "-"))
			assert.Contains(t, id, fmt.Sprintf("%dx", tt.rec.Count))
			// Should contain timestamp (YYYYMMDD-HHMMSS) and UUID suffix (8 chars)
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, id)
		})
	}
}
