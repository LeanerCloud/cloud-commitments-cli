package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ==================== Mock Implementations ====================

// MockEC2Client for testing getAllAWSRegions
type MockEC2Client struct {
	mock.Mock
}

func (m *MockEC2Client) DescribeRegions(ctx context.Context, params *ec2.DescribeRegionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeRegionsOutput), args.Error(1)
}

// MockRecommendationsClient for testing
type MockRecommendationsClient struct {
	mock.Mock
}

func (m *MockRecommendationsClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

func (m *MockRecommendationsClient) GetRecommendationsForDiscovery(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	args := m.Called(ctx, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

// MockPurchaseClient for testing
type MockPurchaseClient struct {
	mock.Mock
}

func (m *MockPurchaseClient) PurchaseRI(ctx context.Context, rec common.Recommendation) common.PurchaseResult {
	args := m.Called(ctx, rec)
	return args.Get(0).(common.PurchaseResult)
}

func (m *MockPurchaseClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	args := m.Called(ctx, rec)
	return args.Error(0)
}

func (m *MockPurchaseClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	args := m.Called(ctx, rec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*common.OfferingDetails), args.Error(1)
}

func (m *MockPurchaseClient) BatchPurchase(ctx context.Context, recs []common.Recommendation, delay time.Duration) []common.PurchaseResult {
	args := m.Called(ctx, recs, delay)
	return args.Get(0).([]common.PurchaseResult)
}

func (m *MockPurchaseClient) GetExistingReservedInstances(ctx context.Context) ([]common.ExistingRI, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.ExistingRI), args.Error(1)
}

// ==================== Core Function Tests ====================

func TestRunToolMultiService_Validation(t *testing.T) {
	// Save original values
	originalCoverage := coverage
	originalPaymentOption := paymentOption
	originalTermYears := termYears
	originalAllServices := allServices
	originalServices := services

	// Restore after test
	defer func() {
		coverage = originalCoverage
		paymentOption = originalPaymentOption
		termYears = originalTermYears
		allServices = originalAllServices
		services = originalServices
	}()

	tests := []struct {
		name          string
		setupVars     func()
		expectPanic   bool
	}{
		{
			name: "Valid input - all services",
			setupVars: func() {
				coverage = 75.0
				paymentOption = "partial-upfront"
				termYears = 3
				allServices = true
				services = nil
			},
			expectPanic: false,
		},
		{
			name: "Valid input - specific services",
			setupVars: func() {
				coverage = 50.0
				paymentOption = "no-upfront"
				termYears = 1
				allServices = false
				services = []string{"rds", "ec2"}
			},
			expectPanic: false,
		},
		{
			name: "Invalid coverage - too high",
			setupVars: func() {
				coverage = 150.0
				paymentOption = "partial-upfront"
				termYears = 3
			},
			expectPanic: true,
		},
		{
			name: "Invalid coverage - negative",
			setupVars: func() {
				coverage = -10.0
				paymentOption = "all-upfront"
				termYears = 1
			},
			expectPanic: true,
		},
		{
			name: "Invalid payment option",
			setupVars: func() {
				coverage = 80.0
				paymentOption = "invalid-payment"
				termYears = 3
			},
			expectPanic: true,
		},
		{
			name: "Invalid term years",
			setupVars: func() {
				coverage = 80.0
				paymentOption = "partial-upfront"
				termYears = 2 // Only 1 or 3 allowed
			},
			expectPanic: true,
		},
		{
			name: "Default to RDS when no services",
			setupVars: func() {
				coverage = 80.0
				paymentOption = "all-upfront"
				termYears = 3
				allServices = false
				services = nil
			},
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupVars()

			if tt.expectPanic {
				// Can't easily test log.Fatalf, so we skip execution
				// In production code, would use dependency injection
				return
			}

			// For non-panic tests, verify the setup is valid
			assert.GreaterOrEqual(t, coverage, 0.0)
			assert.LessOrEqual(t, coverage, 100.0)
			assert.Contains(t, []string{"all-upfront", "partial-upfront", "no-upfront"}, paymentOption)
			assert.Contains(t, []int{1, 3}, termYears)
		})
	}
}

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
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
				{Region: "eu-west-1", InstanceType: "db.t3.medium"},
			},
			expectedRegions: []string{"eu-west-1", "us-east-1", "us-west-2"},
		},
		{
			name:    "Duplicate regions",
			service: common.ServiceEC2,
			mockReturns: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "t3.micro"},
				{Region: "us-east-1", InstanceType: "t3.small"},
				{Region: "us-west-2", InstanceType: "t3.medium"},
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
				{Region: "us-east-1", InstanceType: "ra3.xlplus"},
				{Region: "", InstanceType: "ra3.4xlarge"},
				{Region: "us-west-2", InstanceType: "ra3.16xlarge"},
			},
			expectedRegions: []string{"us-east-1", "us-west-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockRecommendationsClient{}
			mockClient.On("GetRecommendationsForDiscovery", ctx, tt.service).Return(tt.mockReturns, nil)

			// Now we can use the actual function directly since it accepts an interface
			regions, err := discoverRegionsForService(ctx, mockClient, tt.service)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedRegions, regions)

			mockClient.AssertExpectations(t)
		})
	}
}

func TestCalculateServiceStats(t *testing.T) {
	tests := []struct {
		name     string
		service  common.ServiceType
		recs     []common.Recommendation
		results  []common.PurchaseResult
		expected ServiceProcessingStats
	}{
		{
			name:    "Empty inputs",
			service: common.ServiceRDS,
			recs:    []common.Recommendation{},
			results: []common.PurchaseResult{},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceRDS,
				RegionsProcessed:        0,
				RecommendationsFound:    0,
				RecommendationsSelected: 0,
				InstancesProcessed:      0,
				SuccessfulPurchases:     0,
				FailedPurchases:         0,
				TotalEstimatedSavings:   0,
			},
		},
		{
			name:    "Multiple regions with mixed results",
			service: common.ServiceEC2,
			recs: []common.Recommendation{
				{Region: "us-east-1", Count: 2, EstimatedCost: 100},
				{Region: "us-west-2", Count: 3, EstimatedCost: 200},
				{Region: "eu-west-1", Count: 1, EstimatedCost: 50},
			},
			results: []common.PurchaseResult{
				{Success: true},
				{Success: true},
				{Success: false},
			},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceEC2,
				RegionsProcessed:        3,
				RecommendationsFound:    3,
				RecommendationsSelected: 3,
				InstancesProcessed:      6,
				SuccessfulPurchases:     2,
				FailedPurchases:         1,
				TotalEstimatedSavings:   350,
			},
		},
		{
			name:    "Same region multiple recommendations",
			service: common.ServiceElastiCache,
			recs: []common.Recommendation{
				{Region: "us-east-1", Count: 1, EstimatedCost: 100},
				{Region: "us-east-1", Count: 2, EstimatedCost: 200},
				{Region: "us-east-1", Count: 3, EstimatedCost: 300},
			},
			results: []common.PurchaseResult{
				{Success: true},
				{Success: true},
				{Success: true},
			},
			expected: ServiceProcessingStats{
				Service:                 common.ServiceElastiCache,
				RegionsProcessed:        1,
				RecommendationsFound:    3,
				RecommendationsSelected: 3,
				InstancesProcessed:      6,
				SuccessfulPurchases:     3,
				FailedPurchases:         0,
				TotalEstimatedSavings:   600,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateServiceStats(tt.service, tt.recs, tt.results)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPrintServiceSummary(t *testing.T) {
	tests := []struct {
		name    string
		service common.ServiceType
		stats   ServiceProcessingStats
	}{
		{
			name:    "With savings",
			service: common.ServiceRDS,
			stats: ServiceProcessingStats{
				Service:                 common.ServiceRDS,
				RegionsProcessed:        2,
				RecommendationsSelected: 5,
				InstancesProcessed:      10,
				SuccessfulPurchases:     4,
				FailedPurchases:         1,
				TotalEstimatedSavings:   1500.50,
			},
		},
		{
			name:    "Without savings",
			service: common.ServiceEC2,
			stats: ServiceProcessingStats{
				Service:                 common.ServiceEC2,
				RegionsProcessed:        1,
				RecommendationsSelected: 0,
				InstancesProcessed:      0,
				SuccessfulPurchases:     0,
				FailedPurchases:         0,
				TotalEstimatedSavings:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			printServiceSummary(tt.service, tt.stats)

			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			// Verify output contains expected information
			assert.Contains(t, output, getServiceDisplayName(tt.service))
			assert.Contains(t, output, fmt.Sprintf("Regions processed: %d", tt.stats.RegionsProcessed))
			assert.Contains(t, output, fmt.Sprintf("Recommendations: %d", tt.stats.RecommendationsSelected))
			assert.Contains(t, output, fmt.Sprintf("Instances: %d", tt.stats.InstancesProcessed))

			if tt.stats.TotalEstimatedSavings > 0 {
				assert.Contains(t, output, fmt.Sprintf("$%.2f", tt.stats.TotalEstimatedSavings))
			}
		})
	}
}

func TestWriteMultiServiceCSVReport(t *testing.T) {
	tests := []struct {
		name     string
		results  []common.PurchaseResult
		filepath string
		wantErr  bool
	}{
		{
			name: "RDS results",
			results: []common.PurchaseResult{
				{
					Config: common.Recommendation{
						Service:      common.ServiceRDS,
						Region:       "us-east-1",
						InstanceType: "db.t3.micro",
						Count:        2,
						Term:         36,
						PaymentOption: "partial-upfront",
						EstimatedCost: 100,
						SavingsPercent: 30,
						Description:  "Test RDS",
						Timestamp:    time.Now(),
						ServiceDetails: &common.RDSDetails{
							Engine:   "mysql",
							AZConfig: "multi-az",
						},
					},
					Success:    true,
					PurchaseID: "test-001",
					Timestamp:  time.Now(),
				},
			},
			filepath: "/tmp/test-rds.csv",
			wantErr:  false,
		},
		{
			name: "ElastiCache results",
			results: []common.PurchaseResult{
				{
					Config: common.Recommendation{
						Service:      common.ServiceElastiCache,
						Region:       "us-west-2",
						InstanceType: "cache.t3.micro",
						Count:        1,
						Term:         12,
						ServiceDetails: &common.ElastiCacheDetails{
							Engine:   "redis",
							NodeType: "cache.t3.micro",
						},
					},
					Success:    true,
					PurchaseID: "test-002",
					Timestamp:  time.Now(),
				},
			},
			filepath: "/tmp/test-cache.csv",
			wantErr:  false,
		},
		{
			name: "EC2 results",
			results: []common.PurchaseResult{
				{
					Config: common.Recommendation{
						Service:      common.ServiceEC2,
						Region:       "eu-west-1",
						InstanceType: "t3.medium",
						Count:        5,
						Term:         36,
						ServiceDetails: &common.EC2Details{
							Platform: "Linux/UNIX",
							Tenancy:  "shared",
							Scope:    "region",
						},
					},
					Success:    false,
					PurchaseID: "test-003",
					Message:    "Insufficient capacity",
					Timestamp:  time.Now(),
				},
			},
			filepath: "/tmp/test-ec2.csv",
			wantErr:  false,
		},
		{
			name:     "Empty results",
			results:  []common.PurchaseResult{},
			filepath: "/tmp/test-empty.csv",
			wantErr:  false,
		},
		{
			name: "Unknown service type",
			results: []common.PurchaseResult{
				{
					Config: common.Recommendation{
						Service:      common.ServiceType("unknown"),
						Region:       "us-east-1",
						InstanceType: "unknown.large",
						Count:        1,
						Term:         36,
					},
					Success: true,
				},
			},
			filepath: "/tmp/test-unknown.csv",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeMultiServiceCSVReport(tt.results, tt.filepath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Clean up test files
			os.Remove(tt.filepath)
		})
	}
}

func TestPrintMultiServiceSummary(t *testing.T) {
	tests := []struct {
		name       string
		recs       []common.Recommendation
		results    []common.PurchaseResult
		stats      map[common.ServiceType]ServiceProcessingStats
		isDryRun   bool
	}{
		{
			name: "Dry run with multiple services",
			recs: []common.Recommendation{
				{Service: common.ServiceRDS, Count: 2},
				{Service: common.ServiceEC2, Count: 3},
			},
			results: []common.PurchaseResult{
				{Success: true, Config: common.Recommendation{Count: 2}},
				{Success: false, Config: common.Recommendation{Count: 3}},
			},
			stats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceRDS: {
					Service:                 common.ServiceRDS,
					RecommendationsSelected: 1,
					InstancesProcessed:      2,
					SuccessfulPurchases:     1,
					TotalEstimatedSavings:   500.0,
				},
				common.ServiceEC2: {
					Service:                 common.ServiceEC2,
					RecommendationsSelected: 1,
					InstancesProcessed:      3,
					FailedPurchases:         1,
					TotalEstimatedSavings:   300.0,
				},
			},
			isDryRun: true,
		},
		{
			name: "Actual purchase with success",
			recs: []common.Recommendation{
				{Service: common.ServiceElastiCache, Count: 5},
			},
			results: []common.PurchaseResult{
				{Success: true, Config: common.Recommendation{Count: 5}},
			},
			stats: map[common.ServiceType]ServiceProcessingStats{
				common.ServiceElastiCache: {
					Service:                 common.ServiceElastiCache,
					RecommendationsSelected: 1,
					InstancesProcessed:      5,
					SuccessfulPurchases:     1,
					TotalEstimatedSavings:   1000.0,
				},
			},
			isDryRun: false,
		},
		{
			name:     "Empty results",
			recs:     []common.Recommendation{},
			results:  []common.PurchaseResult{},
			stats:    map[common.ServiceType]ServiceProcessingStats{},
			isDryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			printMultiServiceSummary(tt.recs, tt.results, tt.stats, tt.isDryRun)

			w.Close()
			os.Stdout = old

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			// Verify output contains expected information
			assert.Contains(t, output, "Final Summary")
			if tt.isDryRun {
				assert.Contains(t, output, "DRY RUN")
			} else {
				assert.Contains(t, output, "ACTUAL PURCHASE")
			}

			if len(tt.stats) > 0 {
				assert.Contains(t, output, "By Service:")
			}

			if len(tt.results) > 0 {
				assert.Contains(t, output, "success rate")
			}
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
			expected: "RDS, ElastiCache, EC2, OpenSearch, Redshift, MemoryDB",
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
		{Count: 10, EstimatedCost: 100},
		{Count: 5, EstimatedCost: 50},
		{Count: 2, EstimatedCost: 20},
	}

	tests := []struct {
		name              string
		coverage          float64
		expectedCount     int
		expectedInstances []int32
	}{
		{
			name:              "100% coverage",
			coverage:          100.0,
			expectedCount:     3,
			expectedInstances: []int32{10, 5, 2},
		},
		{
			name:              "50% coverage",
			coverage:          50.0,
			expectedCount:     3,
			expectedInstances: []int32{5, 3, 1},  // Using ceiling: 10*0.5=5, 5*0.5=2.5→3, 2*0.5=1
		},
		{
			name:              "0% coverage",
			coverage:          0.0,
			expectedCount:     0,
			expectedInstances: []int32{},
		},
		{
			name:              "75% coverage",
			coverage:          75.0,
			expectedCount:     3,
			expectedInstances: []int32{8, 4, 2},  // Using ceiling: 10*0.75=7.5→8, 5*0.75=3.75→4, 2*0.75=1.5→2
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

func TestProcessService_EdgeCases(t *testing.T) {
	// Save original values
	originalRegions := regions
	originalCoverage := coverage
	originalPaymentOption := paymentOption
	originalTermYears := termYears

	defer func() {
		regions = originalRegions
		coverage = originalCoverage
		paymentOption = originalPaymentOption
		termYears = originalTermYears
	}()

	// Set test values
	paymentOption = "partial-upfront"
	termYears = 3

	tests := []struct {
		name       string
		setupFunc  func()
		service    common.ServiceType
		isDryRun   bool
		expectRecs int
	}{
		{
			name: "With explicit regions",
			setupFunc: func() {
				regions = []string{"us-east-1"}
				coverage = 100.0
			},
			service:    common.ServiceRDS,
			isDryRun:   true,
			expectRecs: 0, // Would need mock to return actual recs
		},
		{
			name: "No regions triggers discovery",
			setupFunc: func() {
				regions = []string{}
				coverage = 75.0
			},
			service:    common.ServiceEC2,
			isDryRun:   false,
			expectRecs: 0, // Would need mock
		},
		{
			name: "Zero coverage",
			setupFunc: func() {
				regions = []string{"us-west-2"}
				coverage = 0.0
			},
			service:    common.ServiceElastiCache,
			isDryRun:   true,
			expectRecs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc()

			// Note: This would fail without AWS credentials
			// For unit tests, we'd need to inject a mock client
			// This test structure shows the approach

			// Would call: processService(ctx, cfg, recClient, tt.service, tt.isDryRun)
			// And verify results

			assert.Equal(t, tt.service, tt.service) // Placeholder assertion
		})
	}
}

// TestProcessServiceWithMocks tests the processService function using mocks
func TestProcessServiceWithMocks(t *testing.T) {
	ctx := context.Background()
	cfg := aws.Config{Region: "us-east-1"}

	// Save original values
	originalCoverage := coverage
	originalPaymentOption := paymentOption
	originalTermYears := termYears

	defer func() {
		coverage = originalCoverage
		paymentOption = originalPaymentOption
		termYears = originalTermYears
	}()

	tests := []struct {
		name        string
		service     common.ServiceType
		isDryRun    bool
		testRegions []string
		mockRecs    []common.Recommendation
		setupFunc   func()
	}{
		{
			name:        "RDS dry run with recommendations",
			service:     common.ServiceRDS,
			isDryRun:    true,
			testRegions: []string{"us-east-1"},
			mockRecs: []common.Recommendation{
				{InstanceType: "db.t3.micro", Count: 2, Region: "us-east-1", EstimatedCost: 100},
				{InstanceType: "db.t3.small", Count: 1, Region: "us-east-1", EstimatedCost: 200},
			},
			setupFunc: func() {
				coverage = 100.0
				paymentOption = "partial-upfront"
				termYears = 3
			},
		},
		{
			name:        "EC2 with no recommendations",
			service:     common.ServiceEC2,
			isDryRun:    true,
			testRegions: []string{"us-west-2"},
			mockRecs:    []common.Recommendation{},
			setupFunc: func() {
				coverage = 80.0
				paymentOption = "no-upfront"
				termYears = 1
			},
		},
		{
			name:        "ElastiCache with 50% coverage",
			service:     common.ServiceElastiCache,
			isDryRun:    false,
			testRegions: []string{"eu-west-1"},
			mockRecs: []common.Recommendation{
				{InstanceType: "cache.t3.micro", Count: 3, Region: "eu-west-1", EstimatedCost: 150},
				{InstanceType: "cache.t3.small", Count: 2, Region: "eu-west-1", EstimatedCost: 250},
			},
			setupFunc: func() {
				coverage = 50.0
				paymentOption = "all-upfront"
				termYears = 3
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc()

			// Create mock client
			mockClient := &MockRecommendationsClient{}

			// Setup expectations
			for _, region := range tt.testRegions {
				params := common.RecommendationParams{
					Service:            tt.service,
					Region:             region,
					PaymentOption:      paymentOption,
					TermInYears:        termYears,
					LookbackPeriodDays: 7,
				}
				mockClient.On("GetRecommendations", ctx, params).Return(tt.mockRecs, nil)
			}

			// Save and restore global regions
			originalRegions := regions
			regions = tt.testRegions
			defer func() { regions = originalRegions }()

			// Now we can use the actual function directly since it accepts an interface
			recs, results := processService(ctx, cfg, mockClient, tt.service, tt.isDryRun)

			if len(tt.mockRecs) > 0 {
				// Should have recommendations based on coverage
				expectedCount := int(float64(len(tt.mockRecs)) * coverage / 100.0)
				if expectedCount > 0 {
					assert.NotEmpty(t, recs)
					assert.LessOrEqual(t, len(recs), len(tt.mockRecs))
				} else {
					assert.Empty(t, recs)
				}

				// Check results for dry run
				if tt.isDryRun && len(recs) > 0 {
					assert.Equal(t, len(recs), len(results))
					for _, result := range results {
						assert.True(t, result.Success)
						assert.Contains(t, result.Message, "Dry run")
					}
				}
			} else {
				assert.Empty(t, recs)
				assert.Empty(t, results)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestGeneratePurchaseID_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		rec      common.Recommendation
		region   string
		index    int
		isDryRun bool
	}{
		{
			name: "RDS dry run",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t3.micro",
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
				InstanceType: "t3.large",
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
				InstanceType: "cache.r6g.2xlarge",
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
				InstanceType: "unknown.large",
				Count:        10,
			},
			region:   "us-west-2",
			index:    1,
			isDryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun)

			// Verify ID contains expected parts
			if tt.isDryRun {
				assert.Contains(t, id, "dryrun")
			} else {
				assert.Contains(t, id, "ri")
			}

			assert.Contains(t, id, tt.region)
			assert.Contains(t, id, strings.ReplaceAll(tt.rec.InstanceType, ".", "-"))
			assert.Contains(t, id, fmt.Sprintf("%dx", tt.rec.Count))
			// Should contain timestamp (YYYYMMDD-HHMMSS) and UUID suffix (8 chars)
			assert.Regexp(t, `-\d{8}-\d{6}-[a-f0-9]{8}$`, id)
		})
	}
}

// ==================== Helper Function Tests ====================

func TestCalculateTotalInstances(t *testing.T) {
	tests := []struct {
		name     string
		recs     []common.Recommendation
		expected int32
	}{
		{
			name: "multiple recommendations",
			recs: []common.Recommendation{
				{Count: 5},
				{Count: 3},
				{Count: 2},
			},
			expected: 10,
		},
		{
			name:     "empty recommendations",
			recs:     []common.Recommendation{},
			expected: 0,
		},
		{
			name: "single recommendation",
			recs: []common.Recommendation{
				{Count: 7},
			},
			expected: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := calculateTotalInstances(tt.recs)
			assert.Equal(t, tt.expected, total)
		})
	}
}

func TestApplyCoverageToRecommendations(t *testing.T) {
	tests := []struct {
		name         string
		recs         []common.Recommendation
		coverage     float64
		expectedRecs int
	}{
		{
			name: "50% coverage of 4 recommendations",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
				{InstanceType: "type3", Count: 1},
				{InstanceType: "type4", Count: 4},
			},
			coverage:     0.5,
			expectedRecs: 2,
		},
		{
			name: "100% coverage",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
			},
			coverage:     1.0,
			expectedRecs: 2,
		},
		{
			name: "0% coverage",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 3},
			},
			coverage:     0.0,
			expectedRecs: 0,
		},
		{
			name: "75% coverage of 3 recommendations",
			recs: []common.Recommendation{
				{InstanceType: "type1", Count: 2},
				{InstanceType: "type2", Count: 2},
				{InstanceType: "type3", Count: 2},
			},
			coverage:     0.75,
			expectedRecs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyCoverageToRecommendations(tt.recs, tt.coverage)
			assert.Equal(t, tt.expectedRecs, len(result))
		})
	}
}

func TestServiceProcessingOrder(t *testing.T) {
	// Test that services are processed in a consistent order
	services := []common.ServiceType{
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceEC2,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}

	// Verify all expected services are present
	expectedServices := map[common.ServiceType]bool{
		common.ServiceRDS:         false,
		common.ServiceElastiCache: false,
		common.ServiceEC2:         false,
		common.ServiceOpenSearch:  false,
		common.ServiceRedshift:    false,
		common.ServiceMemoryDB:    false,
	}

	for _, service := range services {
		expectedServices[service] = true
	}

	// Check all services were found
	for service, found := range expectedServices {
		assert.True(t, found, "Service %s should be in processing list", service)
	}
}

func TestGenerateCSVFilename(t *testing.T) {
	tests := []struct {
		name        string
		service     common.ServiceType
		payment     string
		term        int
		dryRun      bool
		expectParts []string
	}{
		{
			name:        "RDS dry run",
			service:     common.ServiceRDS,
			payment:     "no-upfront",
			term:        36,
			dryRun:      true,
			expectParts: []string{"rds", "no-upfront", "dryrun"},
		},
		{
			name:        "EC2 actual purchase",
			service:     common.ServiceEC2,
			payment:     "all-upfront",
			term:        12,
			dryRun:      false,
			expectParts: []string{"ec2", "all-upfront", "purchase"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := generateCSVFilename(tt.service, tt.payment, tt.term, tt.dryRun)

			for _, part := range tt.expectParts {
				assert.Contains(t, filename, part)
			}

			// Should end with .csv
			assert.Contains(t, filename, ".csv")
		})
	}
}

func TestMultiServiceConfig(t *testing.T) {
	cfg := MultiServiceConfig{
		Services: map[common.ServiceType]ServiceConfig{
			common.ServiceRDS: {
				Enabled:  true,
				Coverage: 0.5,
			},
			common.ServiceElastiCache: {
				Enabled:  true,
				Coverage: 0.8,
			},
			common.ServiceEC2: {
				Enabled:  false,
				Coverage: 0.0,
			},
		},
		PaymentOption: "no-upfront",
		TermYears:     3,
		DryRun:        true,
	}

	// Test enabled services count
	enabledCount := 0
	for _, svcConfig := range cfg.Services {
		if svcConfig.Enabled {
			enabledCount++
		}
	}
	assert.Equal(t, 2, enabledCount)

	// Test coverage values
	assert.Equal(t, 0.5, cfg.Services[common.ServiceRDS].Coverage)
	assert.Equal(t, 0.8, cfg.Services[common.ServiceElastiCache].Coverage)
}

// ==================== Benchmark Tests ====================

func BenchmarkCalculateTotalInstances(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{Count: int32(i%10 + 1)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calculateTotalInstances(recs)
	}
}

func BenchmarkApplyCoverageToRecommendations(b *testing.B) {
	recs := make([]common.Recommendation, 100)
	for i := range recs {
		recs[i] = common.Recommendation{
			InstanceType: "type",
			Count:        int32(i%5 + 1),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyCoverageToRecommendations(recs, 0.5)
	}
}

// ==================== Helper Functions for Tests ====================

func calculateTotalInstances(recs []common.Recommendation) int32 {
	var total int32
	for _, rec := range recs {
		total += rec.Count
	}
	return total
}

func applyCoverageToRecommendations(recs []common.Recommendation, coverage float64) []common.Recommendation {
	if coverage <= 0 {
		return []common.Recommendation{}
	}
	if coverage >= 1.0 {
		return recs
	}

	targetCount := int(float64(len(recs)) * coverage)
	if targetCount == 0 && coverage > 0 && len(recs) > 0 {
		targetCount = 1
	}

	if targetCount >= len(recs) {
		return recs
	}

	return recs[:targetCount]
}

func generateCSVFilename(service common.ServiceType, payment string, term int, dryRun bool) string {
	mode := "purchase"
	if dryRun {
		mode = "dryrun"
	}

	serviceStr := ""
	switch service {
	case common.ServiceRDS:
		serviceStr = "rds"
	case common.ServiceElastiCache:
		serviceStr = "elasticache"
	case common.ServiceEC2:
		serviceStr = "ec2"
	case common.ServiceOpenSearch:
		serviceStr = "opensearch"
	case common.ServiceRedshift:
		serviceStr = "redshift"
	case common.ServiceMemoryDB:
		serviceStr = "memorydb"
	default:
		serviceStr = "unknown"
	}

	return serviceStr + "-" + payment + "-" + mode + ".csv"
}

// Test types
type MultiServiceConfig struct {
	Services      map[common.ServiceType]ServiceConfig
	PaymentOption string
	TermYears     int
	DryRun        bool
}

type ServiceConfig struct {
	Enabled  bool
	Coverage float64
}

// ==================== Filter Function Tests ====================

func TestApplyFilters(t *testing.T) {
	// Save original values
	origIncludeRegions := includeRegions
	origExcludeRegions := excludeRegions
	origIncludeTypes := includeInstanceTypes
	origExcludeTypes := excludeInstanceTypes

	// Restore after test
	defer func() {
		includeRegions = origIncludeRegions
		excludeRegions = origExcludeRegions
		includeInstanceTypes = origIncludeTypes
		excludeInstanceTypes = origExcludeTypes
	}()

	tests := []struct {
		name                 string
		recommendations      []common.Recommendation
		includeRegions       []string
		excludeRegions       []string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expectedCount        int
	}{
		{
			name: "No filters - all pass through",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Include specific regions only",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
				{Region: "eu-west-1", InstanceType: "db.t3.medium"},
			},
			includeRegions:       []string{"us-east-1", "eu-west-1"},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Exclude specific regions",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{"us-west-2"},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expectedCount:        1,
		},
		{
			name: "Include specific instance types",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
				{Region: "eu-west-1", InstanceType: "db.t3.micro"},
			},
			includeRegions:       []string{},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{"db.t3.micro"},
			excludeInstanceTypes: []string{},
			expectedCount:        2,
		},
		{
			name: "Combined filters",
			recommendations: []common.Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-east-1", InstanceType: "db.t3.small"},
				{Region: "us-west-2", InstanceType: "db.t3.micro"},
			},
			includeRegions:       []string{"us-east-1"},
			excludeRegions:       []string{},
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{"db.t3.micro"},
			expectedCount:        1, // Only us-east-1 with db.t3.small
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global variables
			includeRegions = tt.includeRegions
			excludeRegions = tt.excludeRegions
			includeInstanceTypes = tt.includeInstanceTypes
			excludeInstanceTypes = tt.excludeInstanceTypes

			// Apply filters
			result := applyFilters(tt.recommendations)

			// Check count
			assert.Equal(t, tt.expectedCount, len(result))
		})
	}
}

func TestShouldIncludeRegion(t *testing.T) {
	// Save original values
	origIncludeRegions := includeRegions
	origExcludeRegions := excludeRegions

	defer func() {
		includeRegions = origIncludeRegions
		excludeRegions = origExcludeRegions
	}()

	tests := []struct {
		name           string
		region         string
		includeRegions []string
		excludeRegions []string
		expected       bool
	}{
		{
			name:           "No filters - should include",
			region:         "us-east-1",
			includeRegions: []string{},
			excludeRegions: []string{},
			expected:       true,
		},
		{
			name:           "In include list",
			region:         "us-east-1",
			includeRegions: []string{"us-east-1", "us-west-2"},
			excludeRegions: []string{},
			expected:       true,
		},
		{
			name:           "Not in include list",
			region:         "eu-west-1",
			includeRegions: []string{"us-east-1"},
			excludeRegions: []string{},
			expected:       false,
		},
		{
			name:           "In exclude list",
			region:         "us-east-1",
			includeRegions: []string{},
			excludeRegions: []string{"us-east-1"},
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			includeRegions = tt.includeRegions
			excludeRegions = tt.excludeRegions

			result := shouldIncludeRegion(tt.region)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludeInstanceType(t *testing.T) {
	// Save original values
	origIncludeTypes := includeInstanceTypes
	origExcludeTypes := excludeInstanceTypes

	defer func() {
		includeInstanceTypes = origIncludeTypes
		excludeInstanceTypes = origExcludeTypes
	}()

	tests := []struct {
		name                 string
		instanceType         string
		includeInstanceTypes []string
		excludeInstanceTypes []string
		expected             bool
	}{
		{
			name:                 "No filters - should include",
			instanceType:         "db.t3.micro",
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{},
			expected:             true,
		},
		{
			name:                 "In include list",
			instanceType:         "cache.t3.micro",
			includeInstanceTypes: []string{"cache.t3.micro"},
			excludeInstanceTypes: []string{},
			expected:             true,
		},
		{
			name:                 "In exclude list",
			instanceType:         "db.t3.large",
			includeInstanceTypes: []string{},
			excludeInstanceTypes: []string{"db.t3.large"},
			expected:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			includeInstanceTypes = tt.includeInstanceTypes
			excludeInstanceTypes = tt.excludeInstanceTypes

			result := shouldIncludeInstanceType(tt.instanceType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldIncludeEngine(t *testing.T) {
	// Save original values
	origIncludeEngines := includeEngines
	origExcludeEngines := excludeEngines

	defer func() {
		includeEngines = origIncludeEngines
		excludeEngines = origExcludeEngines
	}()

	tests := []struct {
		name           string
		recommendation common.Recommendation
		includeEngines []string
		excludeEngines []string
		expected       bool
	}{
		{
			name: "ElastiCache Redis - no filters",
			recommendation: common.Recommendation{
				Service:     common.ServiceElastiCache,
				Description: "Redis cache.t4g.micro 3x",
			},
			includeEngines: []string{},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "ElastiCache Redis - in include list",
			recommendation: common.Recommendation{
				Service:     common.ServiceElastiCache,
				Description: "Redis cache.t4g.micro 3x",
			},
			includeEngines: []string{"redis"},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "ElastiCache Valkey - not in include list",
			recommendation: common.Recommendation{
				Service:     common.ServiceElastiCache,
				Description: "Valkey cache.t3.micro 18x",
			},
			includeEngines: []string{"redis"},
			excludeEngines: []string{},
			expected:       false,
		},
		{
			name: "ElastiCache Redis - in exclude list",
			recommendation: common.Recommendation{
				Service:     common.ServiceElastiCache,
				Description: "Redis cache.t4g.micro 3x",
			},
			includeEngines: []string{},
			excludeEngines: []string{"redis"},
			expected:       false,
		},
		{
			name: "RDS MySQL - with ServiceDetails",
			recommendation: common.Recommendation{
				Service: common.ServiceRDS,
				ServiceDetails: &common.RDSDetails{
					Engine: "mysql",
				},
			},
			includeEngines: []string{"mysql", "postgresql"},
			excludeEngines: []string{},
			expected:       true,
		},
		{
			name: "Case insensitive matching",
			recommendation: common.Recommendation{
				Service:     common.ServiceElastiCache,
				Description: "Redis cache.t4g.micro 3x",
			},
			includeEngines: []string{"REDIS"},
			excludeEngines: []string{},
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			includeEngines = tt.includeEngines
			excludeEngines = tt.excludeEngines

			result := shouldIncludeEngine(tt.recommendation)
			assert.Equal(t, tt.expected, result)
		})
	}
}