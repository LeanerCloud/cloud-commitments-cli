package main

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
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

	t.Run("SDK client path against the fixture endpoint", func(t *testing.T) {
		cfg, awsStub := newAWSFixtureConfig(t)
		regions, err := getAllAWSRegions(ctx, cfg)

		require.NoError(t, err)
		assert.Equal(t, []string{"eu-west-1", "us-west-2"}, regions, "regions are sorted")
		awsStub.requireRequests(t, map[string]int{"ec2.us-east-1.amazonaws.com DescribeRegions": 1})
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
			mockClient.On("GetRecommendations", ctx, mock.MatchedBy(func(p *common.RecommendationParams) bool {
				return p.Service == tt.service && p.Region == ""
			})).Return(tt.mockReturns, nil)

			regions, err := discoverRegionsForService(ctx, mockClient, tt.service, Config{TermYears: 1})

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedRegions, regions)

			mockClient.AssertExpectations(t)
		})
	}
}
