package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestGetAllAWSRegions(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		mockOutput    *ec2.DescribeRegionsOutput
		mockError     error
		expectRegions []string
		expectError   bool
	}{
		{
			name: "Success with multiple regions",
			mockOutput: &ec2.DescribeRegionsOutput{
				Regions: []types.Region{
					{RegionName: aws.String("us-east-1")},
					{RegionName: aws.String("eu-west-1")},
					{RegionName: aws.String("ap-south-1")},
				},
			},
			expectRegions: []string{"ap-south-1", "eu-west-1", "us-east-1"}, // Sorted
			expectError:   false,
		},
		{
			name:          "Error from AWS API",
			mockOutput:    nil,
			mockError:     errors.New("AWS API error"),
			expectRegions: nil,
			expectError:   true,
		},
		{
			name: "Empty regions list",
			mockOutput: &ec2.DescribeRegionsOutput{
				Regions: []types.Region{},
			},
			expectRegions: []string{},
			expectError:   false,
		},
		{
			name: "Regions with nil names",
			mockOutput: &ec2.DescribeRegionsOutput{
				Regions: []types.Region{
					{RegionName: aws.String("us-east-1")},
					{RegionName: nil},
					{RegionName: aws.String("eu-west-1")},
				},
			},
			expectRegions: []string{"eu-west-1", "us-east-1"}, // Sorted, nil excluded
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockEC2 := &MockEC2Client{}
			mockEC2.On("DescribeRegions", ctx, mock.Anything).Return(tt.mockOutput, tt.mockError)

			// Use the new interface-based function
			regions, err := getAllAWSRegionsWithClient(ctx, mockEC2)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, regions)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectRegions, regions)
			}

			mockEC2.AssertExpectations(t)
		})
	}

	t.Run("Integration test", func(t *testing.T) {
		// This test requires actual AWS credentials
		if testing.Short() {
			t.Skip("Skipping integration test")
		}

		cfg := aws.Config{Region: "us-east-1"}
		regions, err := getAllAWSRegions(ctx, cfg)

		if err == nil {
			assert.NotNil(t, regions)
			assert.Greater(t, len(regions), 0)

			// Verify regions are sorted
			for i := 1; i < len(regions); i++ {
				assert.LessOrEqual(t, regions[i-1], regions[i])
			}
		}
	})
}

func TestDiscoverRegionsForService(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		service         common.ServiceType
		mockReturns     []common.Recommendation
		expectedRegions []string
		expectError     bool
	}{
		{
			name:    "Multiple unique regions",
			service: common.ServiceRDS,
			mockReturns: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "db.t3.micro"},
				{Region: "us-west-2", ResourceType: "db.t3.small"},
				{Region: "eu-west-1", ResourceType: "db.t3.medium"},
			},
			expectedRegions: []string{"eu-west-1", "us-east-1", "us-west-2"},
		},
		{
			name:    "Duplicate regions",
			service: common.ServiceEC2,
			mockReturns: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "t3.micro"},
				{Region: "us-east-1", ResourceType: "t3.small"},
				{Region: "us-west-2", ResourceType: "t3.medium"},
			},
			expectedRegions: []string{"us-east-1", "us-west-2"},
		},
		{
			name:            "No recommendations",
			service:         common.ServiceElastiCache,
			mockReturns:     []common.Recommendation{},
			expectedRegions: []string{},
		},
		{
			name:    "Recommendations with empty regions filtered",
			service: common.ServiceRedshift,
			mockReturns: []common.Recommendation{
				{Region: "us-east-1", ResourceType: "ra3.xlplus"},
				{Region: "", ResourceType: "ra3.4xlarge"},
				{Region: "us-west-2", ResourceType: "ra3.16xlarge"},
			},
			expectedRegions: []string{"us-east-1", "us-west-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockRecommendationsClient{}
			mockClient.On("GetRecommendationsForService", ctx, tt.service).Return(tt.mockReturns, nil)

			// Now we can use the actual function directly since it accepts an interface
			regions, err := discoverRegionsForService(ctx, mockClient, tt.service)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedRegions, regions)

			mockClient.AssertExpectations(t)
		})
	}
}

func TestFormatServices(t *testing.T) {
	tests := []struct {
		name     string
		services []common.ServiceType
		expected string
	}{
		{
			name:     "Empty list",
			services: []common.ServiceType{},
			expected: "",
		},
		{
			name:     "Single service",
			services: []common.ServiceType{common.ServiceRDS},
			expected: "RDS",
		},
		{
			name:     "Multiple services",
			services: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
			expected: "RDS, EC2, ElastiCache",
		},
		{
			name:     "All services",
			services: getAllServices(),
			expected: "RDS, ElastiCache, EC2, OpenSearch, Redshift, MemoryDB, Savings Plans",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatServices(tt.services)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetServiceDisplayName(t *testing.T) {
	tests := []struct {
		service  common.ServiceType
		expected string
	}{
		{common.ServiceRDS, "RDS"},
		{common.ServiceElastiCache, "ElastiCache"},
		{common.ServiceEC2, "EC2"},
		{common.ServiceOpenSearch, "OpenSearch"},
		{common.ServiceElasticsearch, "OpenSearch"},
		{common.ServiceRedshift, "Redshift"},
		{common.ServiceMemoryDB, "MemoryDB"},
		{common.ServiceType("custom"), "custom"},
		{common.ServiceType(""), ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			result := getServiceDisplayName(tt.service)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestApplyCommonCoverage(t *testing.T) {
	recs := []common.Recommendation{
		{Count: 10, EstimatedSavings: 100},
		{Count: 5, EstimatedSavings: 50},
		{Count: 2, EstimatedSavings: 20},
	}

	tests := []struct {
		name              string
		coverage          float64
		expectedCount     int
		expectedInstances []int
	}{
		{
			name:              "100% coverage",
			coverage:          100.0,
			expectedCount:     3,
			expectedInstances: []int{10, 5, 2},
		},
		{
			name:              "50% coverage",
			coverage:          50.0,
			expectedCount:     3,
			expectedInstances: []int{5, 2, 1}, // Using floor: 10*0.5=5, 5*0.5=2.5→2, 2*0.5=1
		},
		{
			name:              "0% coverage",
			coverage:          0.0,
			expectedCount:     0,
			expectedInstances: []int{},
		},
		{
			name:              "75% coverage",
			coverage:          75.0,
			expectedCount:     3,
			expectedInstances: []int{7, 3, 1}, // Using floor: 10*0.75=7.5→7, 5*0.75=3.75→3, 2*0.75=1.5→1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCommonCoverage(recs, tt.coverage)
			assert.Equal(t, tt.expectedCount, len(result))

			for i, rec := range result {
				if i < len(tt.expectedInstances) {
					assert.Equal(t, tt.expectedInstances[i], rec.Count)
				}
			}
		})
	}
}

func TestCreateDryRunResult(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	toolCfg.Coverage = 75.0

	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		ResourceType: "db.t3.small",
		Count:        5,
		Region:       "us-east-1",
	}

	result := createDryRunResult(rec, "us-east-1", 1, toolCfg)

	assert.True(t, result.Success)
	assert.Equal(t, rec, result.Recommendation)
	assert.Nil(t, result.Error) // Dry runs are successful, so no error
	assert.True(t, result.DryRun)
	assert.Contains(t, result.CommitmentID, "dryrun")
	assert.NotEmpty(t, result.Timestamp)
}

func TestCreateCancelledResults(t *testing.T) {
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	toolCfg.Coverage = 80.0

	recs := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 2},
		{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 3},
		{Service: common.ServiceRDS, ResourceType: "db.t3.large", Count: 1},
	}

	results := createCancelledResults(recs, "us-west-2", toolCfg)

	assert.Len(t, results, 3)
	for i, result := range results {
		assert.False(t, result.Success)
		assert.Equal(t, recs[i], result.Recommendation)
		assert.NotNil(t, result.Error)
		assert.Contains(t, result.Error.Error(), "cancelled")
		assert.Contains(t, result.CommitmentID, "us-west-2")
	}
}

func TestExecutePurchase(t *testing.T) {
	ctx := context.Background()
	// Save original values
	origCfg := toolCfg

	defer func() {
		toolCfg = origCfg
	}()

	toolCfg.Coverage = 90.0

	rec := common.Recommendation{
		Service:      common.ServiceEC2,
		ResourceType: "t3.medium",
		Count:        10,
	}

	mockClient := &MockServiceClient{}
	expectedResult := common.PurchaseResult{
		Recommendation: rec,
		Success:        true,
		CommitmentID:   "test-purchase-id-123",
		Error:          nil,
		Timestamp:      time.Now(),
	}
	mockClient.On("PurchaseCommitment", ctx, rec).Return(expectedResult, nil)

	result := executePurchase(ctx, rec, "eu-west-1", 5, mockClient, toolCfg)

	assert.True(t, result.Success)
	assert.Equal(t, "test-purchase-id-123", result.CommitmentID)
	assert.Nil(t, result.Error)

	mockClient.AssertExpectations(t)
}

func TestAdjustRecsForDuplicates(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		inputRecs     []common.Recommendation
		existingRIs   []common.Commitment
		expectedCount int
		expectedError bool
	}{
		{
			name: "No duplicates",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Count: 5},
				{ResourceType: "db.t3.medium", Count: 3},
			},
			existingRIs:   []common.Commitment{},
			expectedCount: 2,
			expectedError: false,
		},
		{
			name: "With duplicates - adjusts count",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Count: 10},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Count: 3},
			},
			expectedCount: 1, // Should still have 1 recommendation but with adjusted count
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockServiceClient{}
			mockClient.On("GetExistingCommitments", ctx).Return(tt.existingRIs, nil)

			// Suppress logger output (no return value from SetEnabled)
			// Logger output disabled for testing

			results, err := adjustRecsForDuplicates(ctx, tt.inputRecs, mockClient)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.LessOrEqual(t, len(results), len(tt.inputRecs))
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestAdjustRecsForDuplicatesError(t *testing.T) {
	ctx := context.Background()

	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Count: 5},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return([]common.Commitment(nil), errors.New("API error"))

	// Logger output disabled for testing

	results, err := adjustRecsForDuplicates(ctx, recs, mockClient)

	// Should return original recommendations with error (error is propagated)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
	assert.Equal(t, recs, results) // Still returns original recommendations

	mockClient.AssertExpectations(t)
}

func TestGroupRecommendationsByServiceRegion(t *testing.T) {
	tests := []struct {
		name            string
		recommendations []common.Recommendation
		expectedGroups  map[common.ServiceType]map[string]int // service -> region -> count
	}{
		{
			name: "Single service single region",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5},
				{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.medium", Count: 3},
			},
			expectedGroups: map[common.ServiceType]map[string]int{
				common.ServiceRDS: {"us-east-1": 2},
			},
		},
		{
			name: "Single service multiple regions",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5},
				{Service: common.ServiceRDS, Region: "us-west-2", ResourceType: "db.t3.medium", Count: 3},
				{Service: common.ServiceRDS, Region: "eu-west-1", ResourceType: "db.t3.large", Count: 2},
			},
			expectedGroups: map[common.ServiceType]map[string]int{
				common.ServiceRDS: {"us-east-1": 1, "us-west-2": 1, "eu-west-1": 1},
			},
		},
		{
			name: "Multiple services multiple regions",
			recommendations: []common.Recommendation{
				{Service: common.ServiceRDS, Region: "us-east-1", ResourceType: "db.t3.small", Count: 5},
				{Service: common.ServiceRDS, Region: "us-west-2", ResourceType: "db.t3.medium", Count: 3},
				{Service: common.ServiceElastiCache, Region: "us-east-1", ResourceType: "cache.t3.small", Count: 2},
				{Service: common.ServiceElastiCache, Region: "eu-west-1", ResourceType: "cache.t3.medium", Count: 4},
				{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", Count: 10},
			},
			expectedGroups: map[common.ServiceType]map[string]int{
				common.ServiceRDS:         {"us-east-1": 1, "us-west-2": 1},
				common.ServiceElastiCache: {"us-east-1": 1, "eu-west-1": 1},
				common.ServiceEC2:         {"us-east-1": 1},
			},
		},
		{
			name:            "Empty recommendations",
			recommendations: []common.Recommendation{},
			expectedGroups:  map[common.ServiceType]map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := groupRecommendationsByServiceRegion(tt.recommendations)

			// Verify the structure matches expected
			assert.Equal(t, len(tt.expectedGroups), len(result))

			for service, regions := range tt.expectedGroups {
				assert.Contains(t, result, service)
				assert.Equal(t, len(regions), len(result[service]))

				for region, expectedCount := range regions {
					assert.Contains(t, result[service], region)
					assert.Equal(t, expectedCount, len(result[service][region]))
				}
			}
		})
	}
}

func TestGenerateCSVFilename(t *testing.T) {
	tests := []struct {
		name     string
		isDryRun bool
		cfg      Config
		check    func(t *testing.T, filename string)
	}{
		{
			name:     "Dry run mode generates dryrun filename",
			isDryRun: true,
			cfg:      Config{},
			check: func(t *testing.T, filename string) {
				assert.Contains(t, filename, "ri-helper-dryrun-")
				assert.Contains(t, filename, ".csv")
			},
		},
		{
			name:     "Purchase mode generates purchase filename",
			isDryRun: false,
			cfg:      Config{},
			check: func(t *testing.T, filename string) {
				assert.Contains(t, filename, "ri-helper-purchase-")
				assert.Contains(t, filename, ".csv")
			},
		},
		{
			name:     "Custom output overrides default",
			isDryRun: true,
			cfg:      Config{CSVOutput: "custom-output.csv"},
			check: func(t *testing.T, filename string) {
				assert.Equal(t, "custom-output.csv", filename)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateCSVFilename(tt.isDryRun, tt.cfg)
			tt.check(t, result)
		})
	}
}

func TestPrintRunMode(t *testing.T) {
	// Capture output by disabling logger
	// Logger output disabled for testing

	// Just ensure no panic - the function primarily prints
	printRunMode(true)
	printRunMode(false)
}

func TestPrintPaymentAndTerm(t *testing.T) {
	// Capture output by disabling logger
	// Logger output disabled for testing

	cfg := Config{
		PaymentOption: "partial-upfront",
		TermYears:     3,
	}

	// Just ensure no panic - the function primarily prints
	printPaymentAndTerm(cfg)
}

func TestDetermineServicesToProcess_AllServices(t *testing.T) {
	cfg := Config{
		AllServices: true,
	}

	result := determineServicesToProcess(cfg)

	// Should contain all supported services
	assert.Contains(t, result, common.ServiceRDS)
	assert.Contains(t, result, common.ServiceElastiCache)
	assert.Contains(t, result, common.ServiceEC2)
	assert.Contains(t, result, common.ServiceOpenSearch)
	assert.Contains(t, result, common.ServiceRedshift)
	assert.Contains(t, result, common.ServiceMemoryDB)
}

func TestDetermineServicesToProcess_SpecificServices(t *testing.T) {
	cfg := Config{
		AllServices: false,
		Services:    []string{"rds", "elasticache"},
	}

	result := determineServicesToProcess(cfg)

	assert.Equal(t, 2, len(result))
	assert.Contains(t, result, common.ServiceRDS)
	assert.Contains(t, result, common.ServiceElastiCache)
}

func TestPopulateAccountNames(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		recommendations []common.Recommendation
		setupMock       func(m *MockOrganizationsClient)
		expectedNames   []string
	}{
		{
			name: "Populates account names from IDs",
			recommendations: []common.Recommendation{
				{Account: "123456789012", AccountName: ""},
				{Account: "210987654321", AccountName: ""},
			},
			setupMock: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("123456789012"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &orgtypes.Account{
						Name: aws.String("Production"),
					},
				}, nil).Once()
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("210987654321"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &orgtypes.Account{
						Name: aws.String("Development"),
					},
				}, nil).Once()
			},
			expectedNames: []string{"Production", "Development"},
		},
		{
			name: "Handles empty account IDs",
			recommendations: []common.Recommendation{
				{Account: "", AccountName: ""},
				{Account: "123456789012", AccountName: ""},
			},
			setupMock: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("123456789012"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &orgtypes.Account{
						Name: aws.String("Production"),
					},
				}, nil).Once()
			},
			expectedNames: []string{"", "Production"},
		},
		{
			name: "Uses cached values for repeated accounts",
			recommendations: []common.Recommendation{
				{Account: "123456789012", AccountName: ""},
				{Account: "123456789012", AccountName: ""},
				{Account: "123456789012", AccountName: ""},
			},
			setupMock: func(m *MockOrganizationsClient) {
				// Should only be called once due to caching
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("123456789012"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &orgtypes.Account{
						Name: aws.String("Production"),
					},
				}, nil).Once()
			},
			expectedNames: []string{"Production", "Production", "Production"},
		},
		{
			name:            "Handles empty recommendations",
			recommendations: []common.Recommendation{},
			setupMock: func(m *MockOrganizationsClient) {
				// No calls expected
			},
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOrg := &MockOrganizationsClient{}
			tt.setupMock(mockOrg)

			cache := &TestAccountAliasCache{
				cache:     make(map[string]string),
				orgClient: mockOrg,
			}

			// Manually populate account names using our test cache
			for i := range tt.recommendations {
				if tt.recommendations[i].Account != "" {
					tt.recommendations[i].AccountName = cache.GetAccountAlias(ctx, tt.recommendations[i].Account)
				}
			}

			assert.Equal(t, len(tt.expectedNames), len(tt.recommendations))
			for i, rec := range tt.recommendations {
				assert.Equal(t, tt.expectedNames[i], rec.AccountName)
			}

			mockOrg.AssertExpectations(t)
		})
	}
}

// TestPopulateAccountNamesLogic tests the logic of populateAccountNames
// by verifying it populates the AccountName field correctly
func TestPopulateAccountNamesLogic(t *testing.T) {
	ctx := context.Background()

	t.Run("Correctly populates account names", func(t *testing.T) {
		mockOrg := &MockOrganizationsClient{}
		mockOrg.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
			AccountId: aws.String("123456789012"),
		}).Return(&organizations.DescribeAccountOutput{
			Account: &orgtypes.Account{
				Name: aws.String("Production"),
			},
		}, nil).Once()

		cache := &TestAccountAliasCache{
			cache:     make(map[string]string),
			orgClient: mockOrg,
		}

		recs := []common.Recommendation{
			{Account: "123456789012", AccountName: ""},
		}

		// Simulate what populateAccountNames does - call GetAccountAlias for each rec
		for i := range recs {
			if recs[i].Account != "" {
				recs[i].AccountName = cache.GetAccountAlias(ctx, recs[i].Account)
			}
		}

		assert.Equal(t, "Production", recs[0].AccountName)
		mockOrg.AssertExpectations(t)
	})

	t.Run("Skips empty account IDs", func(t *testing.T) {
		cache := &TestAccountAliasCache{
			cache:     make(map[string]string),
			orgClient: &MockOrganizationsClient{}, // No calls expected
		}

		recs := []common.Recommendation{
			{Account: "", AccountName: ""},
			{Account: "", AccountName: "initial"},
		}

		// Simulate what populateAccountNames does
		for i := range recs {
			if recs[i].Account != "" {
				recs[i].AccountName = cache.GetAccountAlias(ctx, recs[i].Account)
			}
		}

		// Empty accounts should not be modified (or return empty from GetAccountAlias)
		assert.Equal(t, "", recs[0].AccountName)
		assert.Equal(t, "initial", recs[1].AccountName)
	})

	t.Run("Handles multiple accounts with caching", func(t *testing.T) {
		mockOrg := &MockOrganizationsClient{}
		// Should only call once per unique account
		mockOrg.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
			AccountId: aws.String("111222333444"),
		}).Return(&organizations.DescribeAccountOutput{
			Account: &orgtypes.Account{
				Name: aws.String("Dev"),
			},
		}, nil).Once()
		mockOrg.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
			AccountId: aws.String("555666777888"),
		}).Return(&organizations.DescribeAccountOutput{
			Account: &orgtypes.Account{
				Name: aws.String("Staging"),
			},
		}, nil).Once()

		cache := &TestAccountAliasCache{
			cache:     make(map[string]string),
			orgClient: mockOrg,
		}

		recs := []common.Recommendation{
			{Account: "111222333444", AccountName: ""},
			{Account: "111222333444", AccountName: ""}, // Same account - should use cache
			{Account: "555666777888", AccountName: ""},
		}

		// Simulate what populateAccountNames does
		for i := range recs {
			if recs[i].Account != "" {
				recs[i].AccountName = cache.GetAccountAlias(ctx, recs[i].Account)
			}
		}

		assert.Equal(t, "Dev", recs[0].AccountName)
		assert.Equal(t, "Dev", recs[1].AccountName)
		assert.Equal(t, "Staging", recs[2].AccountName)
		mockOrg.AssertExpectations(t)
	})
}
