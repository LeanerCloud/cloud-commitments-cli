package main

import (
	"context"
	"sync"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
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

func (m *MockRecommendationsClient) GetRecommendationsForService(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	args := m.Called(ctx, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

func (m *MockRecommendationsClient) GetAllRecommendations(ctx context.Context) ([]common.Recommendation, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

// MockServiceClient implements provider.ServiceClient for testing
type MockServiceClient struct {
	mock.Mock
}

func (m *MockServiceClient) GetServiceType() common.ServiceType {
	args := m.Called()
	return args.Get(0).(common.ServiceType)
}

func (m *MockServiceClient) GetRegion() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockServiceClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Recommendation), args.Error(1)
}

func (m *MockServiceClient) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	args := m.Called(ctx, rec, opts)
	return args.Get(0).(common.PurchaseResult), args.Error(1)
}

func (m *MockServiceClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	args := m.Called(ctx, rec)
	return args.Error(0)
}

func (m *MockServiceClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	args := m.Called(ctx, rec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*common.OfferingDetails), args.Error(1)
}

func (m *MockServiceClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]common.Commitment), args.Error(1)
}

func (m *MockServiceClient) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

// ==================== Test Helpers ====================

// globalVarsSnapshot captures the toolCfg for tests
type globalVarsSnapshot struct {
	cfg Config
}

// saveGlobalVars captures current toolCfg state
func saveGlobalVars() *globalVarsSnapshot {
	return &globalVarsSnapshot{
		cfg: toolCfg,
	}
}

// restoreGlobalVars restores toolCfg state from snapshot
func (s *globalVarsSnapshot) restore() {
	toolCfg = s.cfg
}

// OrganizationsClientAPI is an interface for organizations client operations
type OrganizationsClientAPI interface {
	DescribeAccount(ctx context.Context, params *organizations.DescribeAccountInput, optFns ...func(*organizations.Options)) (*organizations.DescribeAccountOutput, error)
}

// MockOrganizationsClient for testing account alias cache
type MockOrganizationsClient struct {
	mock.Mock
}

func (m *MockOrganizationsClient) DescribeAccount(ctx context.Context, params *organizations.DescribeAccountInput, optFns ...func(*organizations.Options)) (*organizations.DescribeAccountOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*organizations.DescribeAccountOutput), args.Error(1)
}

// TestAccountAliasCache is a test-friendly version of AccountAliasCache
type TestAccountAliasCache struct {
	mu        sync.RWMutex
	cache     map[string]string
	orgClient OrganizationsClientAPI
}

// GetAccountAlias returns the account alias for an account ID (same logic as production)
func (c *TestAccountAliasCache) GetAccountAlias(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}

	c.mu.RLock()
	if alias, ok := c.cache[accountID]; ok {
		c.mu.RUnlock()
		return alias
	}
	c.mu.RUnlock()

	// Try to fetch from Organizations
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if alias, ok := c.cache[accountID]; ok {
		return alias
	}

	// Try to describe the account
	result, err := c.orgClient.DescribeAccount(ctx, &organizations.DescribeAccountInput{
		AccountId: aws.String(accountID),
	})
	if err != nil {
		c.cache[accountID] = accountID // Use ID as fallback
		return accountID
	}

	if result.Account != nil && result.Account.Name != nil {
		c.cache[accountID] = *result.Account.Name
		return *result.Account.Name
	}

	c.cache[accountID] = accountID
	return accountID
}

// ==================== Core Function Tests ====================
