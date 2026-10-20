package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

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
			name:      "Lookup error - GetAccountAlias shows the ID, uncached",
			accountID: "999888777666",
			mockSetup: func(m *MockOrganizationsClient) {
				m.On("DescribeAccount", ctx, &organizations.DescribeAccountInput{
					AccountId: aws.String("999888777666"),
				}).Return(nil, errors.New("account not found")).Once()
			},
			expected:    "999888777666",
			shouldCache: false, // a failed lookup is not remembered
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

	// Test concurrent access to ensure proper locking. Worker goroutines must
	// not call assert/require directly; funnel results back and assert on the
	// test goroutine after wg.Wait().
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	results := make(chan string, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			results <- cache.GetAccountAlias(ctx, "123456789012")
		}()
	}

	// Wait for all goroutines to complete, then assert on the test goroutine.
	wg.Wait()
	close(results)
	for result := range results {
		assert.Equal(t, "Test Account", result)
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
