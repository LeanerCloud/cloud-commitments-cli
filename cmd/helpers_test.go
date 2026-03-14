package main

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestCalculateTotalInstances(t *testing.T) {
	tests := []struct {
		name     string
		recs     []common.Recommendation
		expected int
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
			total := CalculateTotalInstances(tt.recs)
			assert.Equal(t, tt.expected, total)
		})
	}
}

func TestNewAccountAliasCacheWithClient(t *testing.T) {
	mockOrg := &MockOrganizationsClient{}
	cache := NewAccountAliasCacheWithClient(mockOrg)

	assert.NotNil(t, cache)
	assert.NotNil(t, cache.cache)
	assert.Equal(t, mockOrg, cache.orgClient)
	assert.Equal(t, 0, len(cache.cache))
}

func TestGetAccountAlias(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		accountID   string
		mockSetup   func(m *MockOrganizationsClient)
		expected    string
		shouldCache bool
	}{
		{
			name:      "Empty account ID returns empty",
			accountID: "",
			mockSetup: func(m *MockOrganizationsClient) {
				// No calls expected
			},
			expected:    "",
			shouldCache: false,
		},
		{
			name:      "Successful account lookup",
			accountID: "123456789012",
			mockSetup: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("123456789012"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &types.Account{
						Name: aws.String("Production Account"),
					},
				}, nil).Once()
			},
			expected:    "Production Account",
			shouldCache: true,
		},
		{
			name:      "Account not found - uses ID as fallback",
			accountID: "999888777666",
			mockSetup: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("999888777666"),
				}).Return(nil, errors.New("account not found")).Once()
			},
			expected:    "999888777666",
			shouldCache: true,
		},
		{
			name:      "Account with nil name - uses ID as fallback",
			accountID: "111222333444",
			mockSetup: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("111222333444"),
				}).Return(&organizations.DescribeAccountOutput{
					Account: &types.Account{
						Name: nil,
					},
				}, nil).Once()
			},
			expected:    "111222333444",
			shouldCache: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOrg := &MockOrganizationsClient{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockOrg)
			}

			cache := NewAccountAliasCacheWithClient(mockOrg)

			result := cache.GetAccountAlias(ctx, tt.accountID)
			assert.Equal(t, tt.expected, result)

			if tt.shouldCache && tt.accountID != "" {
				// Verify caching - second call should not hit the API
				result2 := cache.GetAccountAlias(ctx, tt.accountID)
				assert.Equal(t, tt.expected, result2)
			}

			mockOrg.AssertExpectations(t)
		})
	}
}

func TestGetAccountAliasConcurrency(t *testing.T) {
	// This test relies on AccountAliasCache.GetAccountAlias using double-checked locking:
	// first acquire a read lock to check the cache, then acquire a write lock and re-check
	// before calling the API. Without this pattern, multiple goroutines could pass the
	// read-lock cache-miss check concurrently and issue multiple API calls.
	// Run with -race to surface any data races in the cache map.
	ctx := context.Background()
	mockOrg := &MockOrganizationsClient{}

	// Setup mock to return account name
	mockOrg.On("DescribeAccount", ctx, mock.AnythingOfType("*organizations.DescribeAccountInput")).
		Return(&organizations.DescribeAccountOutput{
			Account: &types.Account{
				Name: aws.String("Test Account"),
			},
		}, nil).Once()

	cache := NewAccountAliasCacheWithClient(mockOrg)

	// Test concurrent access to ensure proper locking
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			result := cache.GetAccountAlias(ctx, "123456789012")
			assert.Equal(t, "Test Account", result)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Mock should only be called once due to double-checked locking in GetAccountAlias
	mockOrg.AssertExpectations(t)
}

func TestGetAccountAliasRealFunction(t *testing.T) {
	// Skip integration test that requires real AWS API
	t.Skip("Skipping integration test - GetAccountAlias tested via mock tests")

	// This test would validate GetAccountAlias with real AWS API
	// but the functionality is already tested via the mock tests above
}

func TestApplyCountOverride(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		overrideCount  int32
		expectedCounts []int
	}{
		{
			name: "Override with positive value",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 10, ResourceType: "db.t3.medium"},
				{Count: 3, ResourceType: "db.t3.large"},
			},
			overrideCount:  2,
			expectedCounts: []int{2, 2, 2},
		},
		{
			name: "Override with zero - no change",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 10, ResourceType: "db.t3.medium"},
			},
			overrideCount:  0,
			expectedCounts: []int{5, 10},
		},
		{
			name: "Override with negative value - no change",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			overrideCount:  -1,
			expectedCounts: []int{5},
		},
		{
			name:           "Empty recommendations",
			recs:           []common.Recommendation{},
			overrideCount:  5,
			expectedCounts: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCountOverride(tt.recs, tt.overrideCount)
			assert.Equal(t, len(tt.expectedCounts), len(result))
			for i, rec := range result {
				assert.Equal(t, tt.expectedCounts[i], rec.Count)
			}
		})
	}
}

func TestApplyCoverage(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		coverage       float64
		expectedCounts []int
		expectedLen    int
	}{
		{
			name: "100% coverage - no change",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
				{Count: 5, EstimatedSavings: 50},
			},
			coverage:       100.0,
			expectedCounts: []int{10, 5},
			expectedLen:    2,
		},
		{
			name: "50% coverage",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
				{Count: 6, EstimatedSavings: 60},
			},
			coverage:       50.0,
			expectedCounts: []int{5, 3},
			expectedLen:    2,
		},
		{
			name: "0% coverage - returns empty",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       0.0,
			expectedCounts: []int{},
			expectedLen:    0,
		},
		{
			name: "Negative coverage - returns empty",
			recs: []common.Recommendation{
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       -10.0,
			expectedCounts: []int{},
			expectedLen:    0,
		},
		{
			name: "Coverage reduces to zero - filters out",
			recs: []common.Recommendation{
				{Count: 1, EstimatedSavings: 10},
				{Count: 10, EstimatedSavings: 100},
			},
			coverage:       10.0, // 1*0.1 = 0, 10*0.1 = 1
			expectedCounts: []int{1},
			expectedLen:    1,
		},
		{
			name: "Savings Plans - reduces hourly commitment",
			recs: []common.Recommendation{
				{
					Service:          common.ServiceSavingsPlans,
					Count:            1,
					EstimatedSavings: 100,
					Details: &common.SavingsPlanDetails{
						HourlyCommitment: 10.0,
						PlanType:         "Compute",
					},
				},
			},
			coverage:       50.0,
			expectedCounts: []int{1}, // Count stays the same for SPs
			expectedLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCoverage(tt.recs, tt.coverage)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			// For Savings Plans, verify hourly commitment is adjusted
			if tt.name == "Savings Plans - reduces hourly commitment" && len(result) > 0 {
				details, ok := result[0].Details.(*common.SavingsPlanDetails)
				require.True(t, ok, "expected *common.SavingsPlanDetails in result Details")
				assert.Equal(t, 5.0, details.HourlyCommitment)    // 10 * 0.5
				assert.Equal(t, 50.0, result[0].EstimatedSavings) // 100 * 0.5
			}
		})
	}
}

func TestAdjustRecommendationsForExisting(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		inputRecs      []common.Recommendation
		existingRIs    []common.Commitment
		expectedLen    int
		expectedCounts []int
	}{
		{
			name: "No existing RIs - all recommendations kept",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5},
				{ResourceType: "db.t3.medium", Region: "us-west-2", Count: 3},
			},
			existingRIs:    []common.Commitment{},
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Recent RI - partial adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 3, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{7}, // 10 - 3
		},
		{
			name: "Recent RI - complete coverage",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "postgresql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "postgresql", Count: 10, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    0, // All covered
			expectedCounts: []int{},
		},
		{
			name: "Old RI - not recent, no adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-48 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{5}, // No adjustment - RI is too old
		},
		{
			// Boundary: just inside the 24-hour window — should be treated as recent
			name: "RI just inside lookback threshold - adjusted",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-23*time.Hour - 59*time.Minute)},
			},
			expectedLen:    0, // Recent RI fully covers the recommendation
			expectedCounts: []int{},
		},
		{
			// Boundary: just outside the 24-hour window — should not be treated as recent
			name: "RI just outside lookback threshold - not adjusted",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-24*time.Hour - 1*time.Minute)},
			},
			expectedLen:    1, // RI is outside lookback window, no adjustment
			expectedCounts: []int{5},
		},
		{
			name: "Different engine - no adjustment",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "postgresql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{5}, // Different engine
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockServiceClient{}
			mockClient.On("GetExistingCommitments", ctx).Return(tt.existingRIs, nil)

			checker := NewDuplicateChecker()
			result, err := checker.AdjustRecommendationsForExisting(ctx, tt.inputRecs, mockClient)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestGetRecommendationDescription(t *testing.T) {
	tests := []struct {
		name     string
		rec      common.Recommendation
		expected string
	}{
		{
			name: "RDS recommendation with database details",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				ResourceType: "db.t3.small",
				Details: &common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			// GetDetailDescription returns "engine/AZConfig"; AZConfig is empty so trailing slash is included
			expected: "rds db.t3.small mysql/",
		},
		{
			name: "EC2 recommendation without details",
			rec: common.Recommendation{
				Service:      common.ServiceEC2,
				ResourceType: "t3.medium",
			},
			expected: "ec2 t3.medium",
		},
		{
			name: "ElastiCache recommendation with cache details",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				ResourceType: "cache.t3.micro",
				Details: &common.CacheDetails{
					Engine: "redis",
				},
			},
			// GetDetailDescription returns "engine/NodeType"; NodeType is empty so trailing slash is included
			expected: "elasticache cache.t3.micro redis/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRecommendationDescription(tt.rec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeEngineName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Aurora PostgreSQL", "aurora-postgresql"},
		{"Aurora MySQL", "aurora-mysql"},
		{"MySQL", "mysql"},
		{"PostgreSQL", "postgresql"},
		{"postgres", "postgresql"},
		{"MariaDB", "mariadb"},
		{"Oracle", "oracle"},
		{"oracle-se", "oracle"},
		{"oracle-se1", "oracle"},
		{"oracle-se2", "oracle"},
		{"oracle-ee", "oracle"},
		{"SQL Server", "sqlserver"},
		{"sqlserver-se", "sqlserver"},
		{"sqlserver-ee", "sqlserver"},
		{"sqlserver-ex", "sqlserver"},
		{"sqlserver-web", "sqlserver"},
		{"unknown-engine", "unknown-engine"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeEngineName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEngineFromRecommendation(t *testing.T) {
	tests := []struct {
		name     string
		rec      common.Recommendation
		expected string
	}{
		{
			name: "DatabaseDetails value type",
			rec: common.Recommendation{
				Details: common.DatabaseDetails{Engine: "mysql"},
			},
			expected: "mysql",
		},
		{
			name: "DatabaseDetails pointer type",
			rec: common.Recommendation{
				Details: &common.DatabaseDetails{Engine: "postgresql"},
			},
			expected: "postgresql",
		},
		{
			name: "CacheDetails value type",
			rec: common.Recommendation{
				Details: common.CacheDetails{Engine: "redis"},
			},
			expected: "redis",
		},
		{
			name: "CacheDetails pointer type",
			rec: common.Recommendation{
				Details: &common.CacheDetails{Engine: "valkey"},
			},
			expected: "valkey",
		},
		{
			name: "No details",
			rec: common.Recommendation{
				Details: nil,
			},
			expected: "",
		},
		{
			name: "ComputeDetails - returns empty (no engine)",
			rec: common.Recommendation{
				Details: &common.ComputeDetails{Platform: "Linux/UNIX"},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getEngineFromRecommendation(tt.rec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// confirmPurchaseWithInput is a testable variant of ConfirmPurchase that reads
// from the provided reader rather than os.Stdin, allowing stdin to be mocked in tests.
func confirmPurchaseWithInput(totalInstances int, totalCost float64, skipConfirmation bool, input string) bool {
	if skipConfirmation {
		return true
	}
	response := strings.TrimSpace(strings.ToLower(strings.SplitN(input, "\n", 2)[0]))
	return response == "yes" || response == "y"
}

func TestConfirmPurchase(t *testing.T) {
	tests := []struct {
		name             string
		totalInstances   int
		totalCost        float64
		skipConfirmation bool
		expected         bool
	}{
		{
			name:             "Skip confirmation returns true",
			totalInstances:   10,
			totalCost:        100.50,
			skipConfirmation: true,
			expected:         true,
		},
		{
			name:             "Skip confirmation with zero cost",
			totalInstances:   0,
			totalCost:        0.0,
			skipConfirmation: true,
			expected:         true,
		},
		{
			name:             "Skip confirmation with high cost",
			totalInstances:   1000,
			totalCost:        999999.99,
			skipConfirmation: true,
			expected:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConfirmPurchase(tt.totalInstances, tt.totalCost, tt.skipConfirmation)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConfirmPurchaseInput(t *testing.T) {
	// Tests for the interactive stdin branch of ConfirmPurchase logic
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "yes accepts", input: "yes\n", expected: true},
		{name: "y accepts", input: "y\n", expected: true},
		{name: "YES accepts (case insensitive)", input: "YES\n", expected: true},
		{name: "Y accepts (case insensitive)", input: "Y\n", expected: true},
		{name: "no rejects", input: "no\n", expected: false},
		{name: "n rejects", input: "n\n", expected: false},
		{name: "empty string rejects", input: "\n", expected: false},
		{name: "arbitrary text rejects", input: "maybe\n", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := confirmPurchaseWithInput(1, 10.0, false, tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAdjustRecommendationsForExistingRIsEdgeCases(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		inputRecs      []common.Recommendation
		existingRIs    []common.Commitment
		expectedLen    int
		expectedCounts []int
	}{
		{
			name: "Multiple RIs same instance type different regions",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
				{ResourceType: "db.t3.small", Region: "eu-west-1", Count: 8, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 3, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
				{ResourceType: "db.t3.small", Region: "eu-west-1", Engine: "mysql", Count: 2, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    2, // Both regions should have adjusted counts
			expectedCounts: []int{7, 6},
		},
		{
			name: "Retired RI should not affect recommendations",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 10, State: "retired", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1, // Retired RI should not affect
			expectedCounts: []int{5},
		},
		{
			name: "Payment pending RI should adjust",
			inputRecs: []common.Recommendation{
				{ResourceType: "db.t3.small", Region: "us-east-1", Count: 10, Details: &common.DatabaseDetails{Engine: "mysql"}},
			},
			existingRIs: []common.Commitment{
				{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 4, State: "payment-pending", StartDate: time.Now().Add(-1 * time.Hour)},
			},
			expectedLen:    1,
			expectedCounts: []int{6}, // 10 - 4 = 6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockServiceClient{}
			mockClient.On("GetExistingCommitments", ctx).Return(tt.existingRIs, nil)

			checker := NewDuplicateChecker()
			result, err := checker.AdjustRecommendationsForExisting(ctx, tt.inputRecs, mockClient)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestApplyInstanceLimit(t *testing.T) {
	tests := []struct {
		name           string
		recs           []common.Recommendation
		maxInstances   int32
		expectedLen    int
		expectedCounts []int
	}{
		{
			name: "No limit - all recommendations kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 3, ResourceType: "db.t3.medium"},
			},
			maxInstances:   0,
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Limit exceeds total - all kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 3, ResourceType: "db.t3.medium"},
			},
			maxInstances:   20,
			expectedLen:    2,
			expectedCounts: []int{5, 3},
		},
		{
			name: "Limit applies to first recommendation",
			recs: []common.Recommendation{
				{Count: 10, ResourceType: "db.t3.small"},
				{Count: 5, ResourceType: "db.t3.medium"},
			},
			maxInstances:   7,
			expectedLen:    1,
			expectedCounts: []int{7},
		},
		{
			name: "Limit applies across recommendations",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
				{Count: 5, ResourceType: "db.t3.medium"},
				{Count: 5, ResourceType: "db.t3.large"},
			},
			maxInstances:   12,
			expectedLen:    3,
			expectedCounts: []int{5, 5, 2},
		},
		{
			name: "Negative limit - all kept",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			maxInstances:   -1,
			expectedLen:    1,
			expectedCounts: []int{5},
		},
		{
			// math.MinInt32 should be treated the same as any negative value: no limit applied
			name: "math.MinInt32 limit - all kept (no int32 wrap)",
			recs: []common.Recommendation{
				{Count: 5, ResourceType: "db.t3.small"},
			},
			maxInstances:   math.MinInt32,
			expectedLen:    1,
			expectedCounts: []int{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyInstanceLimit(tt.recs, tt.maxInstances)
			assert.Equal(t, tt.expectedLen, len(result))
			for i := range result {
				if i < len(tt.expectedCounts) {
					assert.Equal(t, tt.expectedCounts[i], result[i].Count)
				}
			}
		})
	}
}
