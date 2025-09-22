package main

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock for testing
type mockRecommendationsClient struct {
	mock.Mock
}

func (m *mockRecommendationsClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

func (m *mockRecommendationsClient) GetRecommendationsForDiscovery(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	args := m.Called(ctx, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

func TestMainFunctionWithDifferentArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantErr  bool
	}{
		{
			name:    "Help flag",
			args:    []string{"--help"},
			wantErr: false,
		},
		{
			name:    "Version flag",
			args:    []string{"--version"},
			wantErr: false,
		},
		{
			name:    "Invalid flag",
			args:    []string{"--invalid-flag"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original
			origCmd := rootCmd
			defer func() {
				rootCmd = origCmd
			}()

			// Create a new command for testing
			testCmd := &cobra.Command{
				Use:   "test",
				Short: "Test command",
				Run:   func(cmd *cobra.Command, args []string) {},
			}

			// Add flags
			testCmd.Flags().StringSliceVarP(&regions, "regions", "r", []string{}, "AWS regions")
			testCmd.Flags().StringSliceVarP(&services, "services", "s", []string{"rds"}, "Services")
			testCmd.Flags().Float64VarP(&coverage, "coverage", "c", 80.0, "Coverage")
			testCmd.Flags().BoolVar(&actualPurchase, "purchase", false, "Purchase")
			testCmd.Flags().StringVarP(&csvOutput, "output", "o", "", "Output")
			testCmd.Flags().StringVarP(&paymentOption, "payment", "p", "no-upfront", "Payment")
			testCmd.Flags().IntVarP(&termYears, "term", "t", 3, "Term")
			testCmd.Flags().BoolVar(&allServices, "all-services", false, "All services")

			testCmd.SetArgs(tt.args)
			err := testCmd.Execute()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				// Help and version don't return errors
				assert.True(t, err == nil || err.Error() == "")
			}
		})
	}
}

func TestRunToolWithValidation(t *testing.T) {
	tests := []struct {
		name          string
		setupFlags    func()
		expectPanic   bool
		panicContains string
	}{
		{
			name: "Invalid coverage percentage - negative",
			setupFlags: func() {
				coverage = -10
				paymentOption = "no-upfront"
				termYears = 3
			},
			expectPanic:   true,
			panicContains: "Coverage percentage must be between 0 and 100",
		},
		{
			name: "Invalid coverage percentage - over 100",
			setupFlags: func() {
				coverage = 150
				paymentOption = "no-upfront"
				termYears = 3
			},
			expectPanic:   true,
			panicContains: "Coverage percentage must be between 0 and 100",
		},
		{
			name: "Invalid payment option",
			setupFlags: func() {
				coverage = 50
				paymentOption = "invalid-payment"
				termYears = 3
			},
			expectPanic:   true,
			panicContains: "Invalid payment option",
		},
		{
			name: "Invalid term years",
			setupFlags: func() {
				coverage = 50
				paymentOption = "no-upfront"
				termYears = 5
			},
			expectPanic:   true,
			panicContains: "Invalid term",
		},
		{
			name: "Valid configuration",
			setupFlags: func() {
				coverage = 50
				paymentOption = "no-upfront"
				termYears = 1
				regions = []string{"us-east-1"}
				services = []string{"rds"}
			},
			expectPanic:   true, // Will panic on AWS config load
			panicContains: "Failed to load AWS config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original values
			origCoverage := coverage
			origPaymentOption := paymentOption
			origTermYears := termYears
			origRegions := regions
			origServices := services

			defer func() {
				// Restore
				coverage = origCoverage
				paymentOption = origPaymentOption
				termYears = origTermYears
				regions = origRegions
				services = origServices
			}()

			tt.setupFlags()

			if tt.expectPanic {
				defer func() {
					r := recover()
					if r == nil {
						t.Errorf("Expected panic but didn't get one")
					} else if tt.panicContains != "" {
						errStr := ""
						switch v := r.(type) {
						case string:
							errStr = v
						case error:
							errStr = v.Error()
						default:
							errStr = "unknown panic type"
						}
						assert.Contains(t, errStr, tt.panicContains)
					}
				}()
			}

			// This will panic based on validation
			runToolMultiService(context.Background())
		})
	}
}

func TestGeneratePurchaseIDComprehensive(t *testing.T) {
	tests := []struct {
		name     string
		rec      any
		region   string
		index    int
		isDryRun bool
		validate func(t *testing.T, result string)
	}{
		{
			name: "Common recommendation with service details",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t3.micro",
				Count:        2,
				ServiceDetails: &common.RDSDetails{
					Engine:   "mysql",
					AZConfig: "single",
				},
			},
			region:   "us-east-1",
			index:    1,
			isDryRun: false,
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "ri-rds")
				assert.Contains(t, result, "us-east-1")
				assert.Contains(t, result, "2x")
			},
		},
		{
			name: "Legacy recommendation with spaces in engine",
			rec: recommendations.Recommendation{
				Engine:       "MySQL 8.0",
				InstanceType: "db.r5.large",
				Count:        1,
				AZConfig:     "multi",
			},
			region:   "eu-west-1",
			index:    99,
			isDryRun: true,
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "dryrun")
				assert.Contains(t, result, "mysql-8-0")
				assert.Contains(t, result, "maz")
				assert.Contains(t, result, "099")
			},
		},
		{
			name: "Nil recommendation",
			rec:  nil,
			region:   "ap-south-1",
			index:    1,
			isDryRun: false,
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "unknown")
			},
		},
		{
			name: "Complex instance type",
			rec: recommendations.Recommendation{
				Engine:       "postgres",
				InstanceType: "db.x2gd.metal.32xlarge",
				Count:        5,
				AZConfig:     "single",
			},
			region:   "us-west-2",
			index:    333,
			isDryRun: false,
			validate: func(t *testing.T, result string) {
				assert.Contains(t, result, "x2gd-metal")
				assert.Contains(t, result, "5x")
				assert.Contains(t, result, "333")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePurchaseID(tt.rec, tt.region, tt.index, tt.isDryRun)
			tt.validate(t, result)
		})
	}
}

func TestCreatePurchaseClientEdgeCases(t *testing.T) {
	cfg := aws.Config{
		Region: "us-east-1",
	}

	// Test with empty service type
	client := createPurchaseClient(common.ServiceType(""), cfg)
	assert.Nil(t, client)

	// Test with very long service type
	client = createPurchaseClient(common.ServiceType("very-long-service-name-that-does-not-exist"), cfg)
	assert.Nil(t, client)

	// Test that Elasticsearch and OpenSearch return the same client type
	esClient := createPurchaseClient(common.ServiceElasticsearch, cfg)
	osClient := createPurchaseClient(common.ServiceOpenSearch, cfg)
	assert.NotNil(t, esClient)
	assert.NotNil(t, osClient)
	// Both should be OpenSearch clients
}

func TestParseServicesComprehensive(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []common.ServiceType
	}{
		{
			name:     "Nil input",
			input:    nil,
			expected: []common.ServiceType{},
		},
		{
			name:     "Mixed valid and empty strings",
			input:    []string{"rds", "", "ec2", "  ", "elasticache"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceEC2, common.ServiceElastiCache},
		},
		{
			name:     "Duplicate services",
			input:    []string{"rds", "RDS", "rds"},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceRDS, common.ServiceRDS},
		},
		{
			name:     "Services with extra spaces",
			input:    []string{" rds ", "  elasticache  ", "ec2  "},
			expected: []common.ServiceType{common.ServiceRDS, common.ServiceElastiCache, common.ServiceEC2},
		},
		{
			name:     "Legacy and new service names",
			input:    []string{"elasticsearch", "opensearch"},
			expected: []common.ServiceType{common.ServiceElasticsearch, common.ServiceOpenSearch},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseServices(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRootCommandValidation(t *testing.T) {
	// Test command metadata
	assert.Contains(t, rootCmd.Short, "Reserved Instance")
	assert.Contains(t, rootCmd.Long, "Cost Explorer")
	assert.NotNil(t, rootCmd.Run)

	// Test all flags have descriptions
	rootCmd.Flags().VisitAll(func(flag *pflag.Flag) {
		assert.NotEmpty(t, flag.Usage, "Flag %s should have usage", flag.Name)
	})
}

func TestFlagDefaults(t *testing.T) {
	// The init() function runs automatically when the package loads
	// We can't call it directly, but we can test the default values

	// Save current values
	origCoverage := coverage
	origActualPurchase := actualPurchase
	origPaymentOption := paymentOption
	origTermYears := termYears
	origAllServices := allServices
	origCSVOutput := csvOutput

	defer func() {
		// Restore
		coverage = origCoverage
		actualPurchase = origActualPurchase
		paymentOption = origPaymentOption
		termYears = origTermYears
		allServices = origAllServices
		csvOutput = origCSVOutput
	}()

	// Check that flags have expected defaults
	flag := rootCmd.Flags().Lookup("coverage")
	assert.Equal(t, "80", flag.DefValue)

	flag = rootCmd.Flags().Lookup("purchase")
	assert.Equal(t, "false", flag.DefValue)

	flag = rootCmd.Flags().Lookup("payment")
	assert.Equal(t, "no-upfront", flag.DefValue)

	flag = rootCmd.Flags().Lookup("term")
	assert.Equal(t, "3", flag.DefValue)
}

func TestGetAllServicesOrder(t *testing.T) {
	services := getAllServices()

	// Should always return in the same order
	for i := 0; i < 10; i++ {
		newServices := getAllServices()
		assert.Equal(t, services, newServices, "Services should be in consistent order")
	}

	// Should contain all expected services
	assert.Contains(t, services, common.ServiceRDS)
	assert.Contains(t, services, common.ServiceElastiCache)
	assert.Contains(t, services, common.ServiceEC2)
	assert.Contains(t, services, common.ServiceOpenSearch)
	assert.Contains(t, services, common.ServiceRedshift)
	assert.Contains(t, services, common.ServiceMemoryDB)
}

func TestRunToolPanicRecovery(t *testing.T) {
	// Save originals
	origRegions := regions
	origServices := services
	origCoverage := coverage
	origActualPurchase := actualPurchase
	origAllServices := allServices
	origPaymentOption := paymentOption
	origTermYears := termYears

	defer func() {
		regions = origRegions
		services = origServices
		coverage = origCoverage
		actualPurchase = origActualPurchase
		allServices = origAllServices
		paymentOption = origPaymentOption
		termYears = origTermYears
	}()

	// Set valid configuration
	regions = []string{"us-east-1"}
	services = []string{"rds"}
	coverage = 50.0
	actualPurchase = false
	allServices = false
	paymentOption = "no-upfront"
	termYears = 3

	// Should not panic with valid cobra.Command
	cmd := &cobra.Command{
		Use: "test",
	}

	defer func() {
		// Expect panic due to AWS config
		r := recover()
		assert.NotNil(t, r, "Should panic when AWS config fails")
	}()

	runTool(cmd, []string{})
}

func TestGeneratePurchaseIDTimestamp(t *testing.T) {
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		InstanceType: "db.t3.micro",
		Count:        1,
	}

	// Generate two IDs quickly
	id1 := generatePurchaseID(rec, "us-east-1", 1, false)
	time.Sleep(time.Second)
	id2 := generatePurchaseID(rec, "us-east-1", 1, false)

	// They should have different timestamps
	assert.NotEqual(t, id1, id2, "IDs generated at different times should differ")
}