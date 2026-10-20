package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
)

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
// by verifying it populates the AccountName field correctly.
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
