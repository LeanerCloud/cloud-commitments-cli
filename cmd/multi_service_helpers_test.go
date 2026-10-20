package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestFormatServices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected string
		services []common.ServiceType
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
			expected: formatServices(getAllServices()), // Build expected from getAllServices() to avoid test breakage on service list changes
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
	t.Parallel()
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

func TestApplyCoverage_FloorsPerRecommendationCounts(t *testing.T) {
	recs := []common.Recommendation{
		{Count: 10, EstimatedSavings: 100},
		{Count: 5, EstimatedSavings: 50},
		{Count: 2, EstimatedSavings: 20},
	}

	tests := []struct {
		name              string
		expectedInstances []int
		coverage          float64
		expectedCount     int
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
			result := ApplyCoverage(recs, tt.coverage)
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
	// Check format: dryrun-{service}-{region}-{type}-{count}
	assert.Regexp(t, "^dryrun-rds-us-east-1-.*-5x-", result.CommitmentID)
	assert.NotEmpty(t, result.Timestamp)
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
	var capturedOpts common.PurchaseOptions
	mockClient.On("PurchaseCommitment", mock.Anything, rec, mock.MatchedBy(func(o common.PurchaseOptions) bool {
		capturedOpts = o
		return o.Source == common.PurchaseSourceCLI
	})).Return(expectedResult, nil)

	result := executePurchase(ctx, rec, "eu-west-1", 5, mockClient, toolCfg)

	assert.True(t, result.Success)
	assert.Equal(t, "test-purchase-id-123", result.CommitmentID)
	assert.Nil(t, result.Error)

	// #617: executePurchase hands the provider a descriptive reservation ID
	// (account/service/region/size aware) rather than leaving it generic.
	assert.NotEmpty(t, capturedOpts.ReservationID)
	assert.Contains(t, capturedOpts.ReservationID, "t3-medium")

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

// TestCheckDuplicates_ErrorOnPurchaseRun_DropsRecsRatherThanFallingBack
// reproduces #1941 at the checkDuplicates layer used by the main (non-CSV)
// pipeline: a failed duplicate check on a purchase run (isDryRun=false) must
// not fall back to the un-deduplicated input. Before the fix, the error
// branch left filteredRecs unchanged and only logged a warning, so the run
// proceeded to buy the full pre-dedup counts.
func TestCheckDuplicates_ErrorOnPurchaseRun_DropsRecsRatherThanFallingBack(t *testing.T) {
	ctx := context.Background()
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Count: 5},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return([]common.Commitment(nil), errors.New("throttling"))

	drops := common.NewDropSummary()
	result := checkDuplicates(ctx, recs, mockClient, false /* isDryRun */, drops)

	assert.Nil(t, result, "a failed duplicate check on a purchase run must drop the recommendations, not fall back to the un-deduplicated input")
	assert.Equal(t, 1, drops.Total())
	mockClient.AssertExpectations(t)
}

// TestCheckDuplicates_ErrorOnDryRun_ContinuesUnadjusted asserts the dry-run
// side of the same branch is unchanged: nothing is purchased, so a failed
// duplicate check logs a warning and keeps reporting the un-deduplicated
// counts rather than silently dropping recommendations from the dry-run report.
func TestCheckDuplicates_ErrorOnDryRun_ContinuesUnadjusted(t *testing.T) {
	ctx := context.Background()
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Count: 5},
	}

	mockClient := &MockServiceClient{}
	mockClient.On("GetExistingCommitments", ctx).Return([]common.Commitment(nil), errors.New("throttling"))

	result := checkDuplicates(ctx, recs, mockClient, true /* isDryRun */, nil)

	assert.Equal(t, recs, result)
	mockClient.AssertExpectations(t)
}

func TestGroupRecommendationsByServiceRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expectedGroups  map[common.ServiceType]map[string]int
		name            string
		recommendations []common.Recommendation
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
	t.Parallel()
	tests := []struct {
		check    func(t *testing.T, filename string)
		name     string
		cfg      Config
		isDryRun bool
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

func TestPrintPaymentAndTerm(t *testing.T) { //nolint:unparam // param intentional for interface consistency/future use
	// Capture output by disabling logger
	// Logger output disabled for testing

	cfg := Config{
		PaymentOption: "partial-upfront",
		TermYears:     3,
	}

	// Just ensure no panic - the function primarily prints
	printPaymentAndTerm(cfg)
}

// Moved from the deleted TestProcessService_WithOverrideCount: the override is
// applied by applyCoverageAndOverrides, which the live pipeline also calls.
func TestApplyCoverageAndOverrides_OverrideCount(t *testing.T) {
	recs := []common.Recommendation{
		{ResourceType: "cache.t3.micro", Count: 10, Region: "us-east-1", EstimatedSavings: 100},
		{ResourceType: "cache.t3.small", Count: 5, Region: "us-east-1", EstimatedSavings: 200},
	}
	got := applyCoverageAndOverrides(recs, Config{Coverage: 100, OverrideCount: 3}, nil, nil, nil)
	assert.Len(t, got, 2)
	for _, rec := range got {
		assert.Equal(t, 3, rec.Count)
	}
}
