package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetermineCSVCoverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      Config
		expected float64
	}{
		{
			name: "No --coverage flag buys 100% for CSV",
			cfg: Config{
				Coverage:            80.0,
				CoverageFromDefault: true,
			},
			expected: 100.0,
		},
		{
			// #2236: an explicit value equal to the flag's default is honored.
			name: "Explicit 80% coverage preserved",
			cfg: Config{
				Coverage: 80.0,
			},
			expected: 80.0,
		},
		{
			name: "User-specified coverage preserved",
			cfg: Config{
				Coverage: 75.0,
			},
			expected: 75.0,
		},
		{
			name: "User-specified 100% coverage preserved",
			cfg: Config{
				Coverage: 100.0,
			},
			expected: 100.0,
		},
		{
			name: "User-specified 50% coverage preserved",
			cfg: Config{
				Coverage: 50.0,
			},
			expected: 50.0,
		},
		{
			name: "User-specified 0% coverage preserved",
			cfg: Config{
				Coverage: 0.0,
			},
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := determineCSVCoverage(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWriteMultiServiceCSVReport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		results  []common.PurchaseResult
		wantErr  bool
	}{
		{
			name: "RDS results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:           common.ServiceRDS,
						Region:            "us-east-1",
						ResourceType:      "db.t3.micro",
						Count:             2,
						Term:              "3yr",
						PaymentOption:     "partial-upfront",
						EstimatedSavings:  100,
						SavingsPercentage: 30,
						Timestamp:         time.Now(),
						Details: &common.DatabaseDetails{
							Engine:   "mysql",
							AZConfig: "multi-az",
						},
					},
					Success:      true,
					CommitmentID: "test-001",
					Timestamp:    time.Now(),
				},
			},
			filename: "test-rds.csv",
			wantErr:  false,
		},
		{
			name: "ElastiCache results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceElastiCache,
						Region:       "us-west-2",
						ResourceType: "cache.t3.micro",
						Count:        1,
						Term:         "1yr",
						Details: &common.CacheDetails{
							Engine:   "redis",
							NodeType: "cache.t3.micro",
						},
					},
					Success:      true,
					CommitmentID: "test-002",
					Timestamp:    time.Now(),
				},
			},
			filename: "test-cache.csv",
			wantErr:  false,
		},
		{
			name: "EC2 results",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceEC2,
						Region:       "eu-west-1",
						ResourceType: "t3.medium",
						Count:        5,
						Term:         "3yr",
						Details: common.ComputeDetails{
							Platform: "Linux/UNIX",
							Tenancy:  "shared",
							Scope:    "region",
						},
					},
					Success:      false,
					CommitmentID: "test-003",
					Error:        errors.New("Insufficient capacity"),
					Timestamp:    time.Now(),
				},
			},
			filename: "test-ec2.csv",
			wantErr:  false,
		},
		{
			name:     "Empty results",
			results:  []common.PurchaseResult{},
			filename: "test-empty.csv",
			wantErr:  false,
		},
		{
			name: "Unknown service type",
			results: []common.PurchaseResult{
				{
					Recommendation: common.Recommendation{
						Service:      common.ServiceType("unknown"),
						Region:       "us-east-1",
						ResourceType: "unknown.large",
						Count:        1,
						Term:         "3yr",
					},
					Success: true,
				},
			},
			filename: "test-unknown.csv",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			filepath := tmpDir + "/" + tt.filename

			err := writeMultiServiceCSVReport(tt.results, filepath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Tests for loadRecommendationsFromCSV function.
func TestLoadRecommendationsFromCSV(t *testing.T) {
	t.Parallel()
	tests := []struct {
		validate    func(t *testing.T, recs []common.Recommendation)
		name        string
		csvContent  string
		errContains string
		wantErr     bool
	}{
		{
			name: "Valid CSV with all fields",
			csvContent: `Service,Region,ResourceType,Count,Account,AccountName,Term,PaymentOption,EstimatedSavings
rds,us-east-1,db.t3.micro,5,123456789012,Production,3yr,partial-upfront,1500.50
ec2,us-west-2,t3.medium,10,123456789012,Development,1yr,all-upfront,2000.75`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 2)

				// Validate first recommendation
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "us-east-1", recs[0].Region)
				assert.Equal(t, "db.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 5, recs[0].Count)
				assert.Equal(t, "123456789012", recs[0].Account)
				assert.Equal(t, "Production", recs[0].AccountName)
				assert.Equal(t, "3yr", recs[0].Term)
				assert.Equal(t, "partial-upfront", recs[0].PaymentOption)
				assert.InDelta(t, 1500.50, recs[0].EstimatedSavings, 0.01)

				// Validate second recommendation
				assert.Equal(t, common.ServiceEC2, recs[1].Service)
				assert.Equal(t, "us-west-2", recs[1].Region)
				assert.Equal(t, "t3.medium", recs[1].ResourceType)
				assert.Equal(t, 10, recs[1].Count)
			},
		},
		{
			name: "Valid CSV with minimal fields",
			csvContent: `Service,Region,ResourceType,Count,Term,PaymentOption
elasticache,eu-west-1,cache.t3.micro,3,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceElastiCache, recs[0].Service)
				assert.Equal(t, "eu-west-1", recs[0].Region)
				assert.Equal(t, "cache.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 3, recs[0].Count)
			},
		},
		{
			name: "Valid CSV with empty optional Account field",
			csvContent: `Service,Region,ResourceType,Count,Account,Term,PaymentOption
rds,us-east-1,db.t3.micro,2,,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "us-east-1", recs[0].Region)
				assert.Equal(t, "db.t3.micro", recs[0].ResourceType)
				assert.Equal(t, 2, recs[0].Count)
				assert.Equal(t, "", recs[0].Account)
			},
		},
		{
			name: "Engine and Deployment reconstruct service Details",
			csvContent: `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption
rds,us-east-1,db.t4g.medium,Aurora MySQL,single-az,3,3yr,partial-upfront
rds,eu-west-2,db.r7g.large,MySQL,multi-az,2,3yr,partial-upfront
elasticache,us-east-1,cache.r6g.large,redis,,4,1yr,no-upfront
ec2,us-west-2,m5.large,Linux/UNIX,,5,1yr,all-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 4)

				// RDS Aurora MySQL, single-az -> DatabaseDetails
				rds0, ok := recs[0].Details.(*common.DatabaseDetails)
				require.True(t, ok, "RDS rec should carry *DatabaseDetails")
				assert.Equal(t, "Aurora MySQL", rds0.Engine)
				assert.Equal(t, "single-az", rds0.AZConfig)
				assert.Equal(t, "db.t4g.medium", rds0.InstanceClass)

				// RDS MySQL, multi-az -> AZConfig carried through for offering lookup
				rds1, ok := recs[1].Details.(*common.DatabaseDetails)
				require.True(t, ok)
				assert.Equal(t, "MySQL", rds1.Engine)
				assert.Equal(t, "multi-az", rds1.AZConfig)

				// ElastiCache -> CacheDetails (no Deployment column)
				cache, ok := recs[2].Details.(*common.CacheDetails)
				require.True(t, ok, "ElastiCache rec should carry *CacheDetails")
				assert.Equal(t, "redis", cache.Engine)
				assert.Equal(t, "cache.r6g.large", cache.NodeType)

				// EC2 -> ComputeDetails, platform from the Engine column
				ec2, ok := recs[3].Details.(*common.ComputeDetails)
				require.True(t, ok, "EC2 rec should carry *ComputeDetails")
				assert.Equal(t, "Linux/UNIX", ec2.Platform)
				assert.Equal(t, "m5.large", ec2.InstanceType)
			},
		},
		{
			name: "No Engine column leaves Details nil (Savings Plans / minimal CSV)",
			csvContent: `Service,Region,ResourceType,Count,Term,PaymentOption
rds,us-east-1,db.t3.micro,2,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Nil(t, recs[0].Details, "Details must stay nil when Engine column is absent")
			},
		},
		{
			name: "TOTAL summary row is skipped",
			csvContent: `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption
rds,us-east-1,db.t4g.medium,Aurora MySQL,single-az,3,3yr,partial-upfront
TOTAL,,,,,3,,`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1, "the trailing TOTAL row must not become a recommendation")
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
			},
		},
		{
			name: "CSV with different column order",
			csvContent: `Count,Service,Region,ResourceType,Term,PaymentOption
7,rds,ap-south-1,db.r5.large,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, "ap-south-1", recs[0].Region)
				assert.Equal(t, "db.r5.large", recs[0].ResourceType)
				assert.Equal(t, 7, recs[0].Count)
			},
		},
		{
			name: "Empty CSV file (only header)",
			csvContent: `Service,Region,ResourceType,Count,Account,AccountName,Term,PaymentOption,EstimatedSavings
`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				assert.Len(t, recs, 0)
			},
		},
		{
			name: "Invalid Count value - non-numeric",
			csvContent: `Service,Region,ResourceType,Count,Term,PaymentOption
rds,us-east-1,db.t3.micro,abc,3yr,partial-upfront`,
			wantErr:     true,
			errContains: `column 4 "Count": invalid integer`,
		},
		{
			name: "Invalid EstimatedSavings value - non-numeric",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption
rds,us-east-1,db.t3.micro,5,invalid,3yr,partial-upfront`,
			wantErr:     true,
			errContains: `column 5 "EstimatedSavings": invalid number`,
		},
		{
			name: "Multiple rows with various services",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption
rds,us-east-1,db.t3.micro,5,100.00,3yr,partial-upfront
ec2,us-west-2,t3.medium,10,200.50,3yr,partial-upfront
elasticache,eu-west-1,cache.t3.micro,3,50.25,3yr,partial-upfront
opensearch,ap-southeast-1,t3.small.search,2,75.00,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 4)
				assert.Equal(t, common.ServiceRDS, recs[0].Service)
				assert.Equal(t, common.ServiceEC2, recs[1].Service)
				assert.Equal(t, common.ServiceElastiCache, recs[2].Service)
				assert.Equal(t, common.ServiceOpenSearch, recs[3].Service)
			},
		},
		{
			name: "CSV with large Count values",
			csvContent: `Service,Region,ResourceType,Count,Term,PaymentOption
ec2,us-east-1,t3.large,1000,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, 1000, recs[0].Count)
			},
		},
		{
			name: "CSV with decimal savings",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption
rds,us-east-1,db.t3.micro,5,1234.5678,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.InDelta(t, 1234.5678, recs[0].EstimatedSavings, 0.0001)
			},
		},
		{
			name: "CSV with zero EstimatedSavings stays valid",
			csvContent: `Service,Region,ResourceType,Count,EstimatedSavings,Term,PaymentOption
rds,us-east-1,db.t3.micro,5,0,3yr,partial-upfront`,
			wantErr: false,
			validate: func(t *testing.T, recs []common.Recommendation) {
				require.Len(t, recs, 1)
				assert.Equal(t, 5, recs[0].Count)
				assert.Equal(t, float64(0), recs[0].EstimatedSavings)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary CSV file
			tmpDir := t.TempDir()
			csvPath := filepath.Join(tmpDir, "test.csv")
			err := os.WriteFile(csvPath, []byte(tt.csvContent), 0644)
			require.NoError(t, err)

			// Call function
			recs, err := loadRecommendationsFromCSV(csvPath)

			// Validate results
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, recs)
				}
			}
		})
	}
}
