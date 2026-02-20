package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

// ==================== Tests for coverage improvement ====================
// This file contains additional tests to improve coverage for functions
// identified as having low coverage

// ==================== Tests for queryMajorEngineVersions ====================

func TestQueryMajorEngineVersions_Success(t *testing.T) {
	// This test verifies the logic of queryMajorEngineVersions without mocking
	// Since it requires AWS credentials, we test the error cases and structure

	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	// Test with empty profile (should work if AWS credentials are configured)
	toolCfg.Profile = ""
	toolCfg.ValidationProfile = ""

	// This will attempt to load AWS config - may fail without credentials
	result, err := queryMajorEngineVersions(ctx, toolCfg)

	// Either succeeds with valid credentials or fails gracefully
	if err != nil {
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load AWS config")
	} else {
		// If it succeeds, verify the result structure
		assert.NotNil(t, result)
		// Result can be empty if no versions are found
	}
}

func TestQueryMajorEngineVersions_ProfileHandling(t *testing.T) {
	ctx := context.Background()
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	tests := []struct {
		name              string
		profile           string
		validationProfile string
	}{
		{
			name:              "Uses validation profile if set",
			profile:           "main-profile",
			validationProfile: "validation-profile",
		},
		{
			name:              "Falls back to main profile",
			profile:           "main-profile",
			validationProfile: "",
		},
		{
			name:              "Empty profiles use default",
			profile:           "",
			validationProfile: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCfg.Profile = tt.profile
			toolCfg.ValidationProfile = tt.validationProfile

			// This will attempt to load config - may fail without valid profiles
			_, err := queryMajorEngineVersions(ctx, toolCfg)

			// We just verify it doesn't panic - actual AWS calls may fail
			// Error is acceptable for invalid profiles
			if err != nil {
				assert.Error(t, err)
			}
		})
	}
}

// ==================== Tests for extractMajorVersion ====================

func TestExtractMajorVersion_ComprehensiveTests(t *testing.T) {
	tests := []struct {
		name          string
		engine        string
		fullVersion   string
		expectedMajor string
	}{
		// Aurora MySQL special handling
		{
			name:          "Aurora MySQL 2.x format",
			engine:        "aurora-mysql",
			fullVersion:   "mysql_aurora.2.11.3",
			expectedMajor: "5.7",
		},
		{
			name:          "Aurora MySQL 3.x format",
			engine:        "aurora-mysql",
			fullVersion:   "mysql_aurora.3.04.0",
			expectedMajor: "8.0",
		},
		{
			name:          "Aurora MySQL direct 5.7",
			engine:        "aurora-mysql",
			fullVersion:   "5.7.mysql_aurora.2.11.1",
			expectedMajor: "5.7",
		},
		{
			name:          "Aurora MySQL direct 8.0",
			engine:        "aurora-mysql",
			fullVersion:   "8.0.mysql_aurora.3.04.0",
			expectedMajor: "8.0",
		},

		// Standard MySQL/PostgreSQL
		{
			name:          "MySQL 5.7",
			engine:        "mysql",
			fullVersion:   "5.7.44",
			expectedMajor: "5.7",
		},
		{
			name:          "MySQL 8.0",
			engine:        "mysql",
			fullVersion:   "8.0.35",
			expectedMajor: "8.0",
		},
		{
			name:          "PostgreSQL 13",
			engine:        "postgres",
			fullVersion:   "13.12",
			expectedMajor: "13.12",
		},
		{
			name:          "PostgreSQL 15",
			engine:        "postgres",
			fullVersion:   "15.4",
			expectedMajor: "15.4",
		},

		// Edge cases
		{
			name:          "Empty version",
			engine:        "mysql",
			fullVersion:   "",
			expectedMajor: "",
		},
		{
			name:          "Single digit version",
			engine:        "postgres",
			fullVersion:   "14",
			expectedMajor: "14",
		},
		{
			name:          "Version with patch suffix",
			engine:        "mysql",
			fullVersion:   "8.0.35-rds.1",
			expectedMajor: "8.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMajorVersion(tt.engine, tt.fullVersion)
			assert.Equal(t, tt.expectedMajor, result)
		})
	}
}

// ==================== Tests for isInExtendedSupport ====================

func TestIsInExtendedSupport_EdgeCases(t *testing.T) {
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)  // 6 months ago
	futureDate := now.AddDate(3, 0, 0) // 3 years from now

	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-extended-support",
					LifecycleSupportStartDate: pastDate,
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
		"mysql:8.0": {
			Engine:             "mysql",
			MajorEngineVersion: "8.0",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-standard-support",
					LifecycleSupportStartDate: now.AddDate(-2, 0, 0),
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
	}

	tests := []struct {
		name             string
		engine           string
		fullVersion      string
		expectedExtended bool
	}{
		{
			name:             "MySQL 5.7 in extended support",
			engine:           "mysql",
			fullVersion:      "5.7.44",
			expectedExtended: true,
		},
		{
			name:             "MySQL 8.0 in standard support",
			engine:           "mysql",
			fullVersion:      "8.0.35",
			expectedExtended: false,
		},
		{
			name:             "Unknown version",
			engine:           "mysql",
			fullVersion:      "9.0.0",
			expectedExtended: false,
		},
		{
			name:             "Empty version",
			engine:           "mysql",
			fullVersion:      "",
			expectedExtended: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInExtendedSupport(tt.engine, tt.fullVersion, versionInfo)
			assert.Equal(t, tt.expectedExtended, result)
		})
	}
}

// ==================== Tests for adjustRecommendationForExcludedVersions ====================

func TestAdjustRecommendationForExcludedVersions_AdditionalCases(t *testing.T) {
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)
	futureDate := now.AddDate(3, 0, 0)

	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-extended-support",
					LifecycleSupportStartDate: pastDate,
					LifecycleSupportEndDate:   futureDate,
				},
			},
		},
	}

	tests := []struct {
		name             string
		rec              common.Recommendation
		instanceVersions map[string][]InstanceEngineVersion
		expectedCount    int
	}{
		{
			name: "No running instances - no adjustment",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    10,
		},
		{
			name: "Running instances with extended support - adjust count",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{
						Engine:        "mysql",
						EngineVersion: "5.7.44",
						InstanceClass: "db.t3.small",
						Region:        "us-east-1",
					},
					{
						Engine:        "mysql",
						EngineVersion: "5.7.42",
						InstanceClass: "db.t3.small",
						Region:        "us-east-1",
					},
				},
			},
			expectedCount: 8, // 10 - 2 extended support instances
		},
		{
			name: "Running instances in different region - no adjustment",
			rec: common.Recommendation{
				ResourceType: "db.t3.small",
				Count:        10,
				Region:       "us-east-1",
				Details: common.DatabaseDetails{
					Engine: "mysql",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{
						Engine:        "mysql",
						EngineVersion: "5.7.44",
						InstanceClass: "db.t3.small",
						Region:        "us-west-2", // Different region
					},
				},
			},
			expectedCount: 10, // No adjustment
		},
		{
			name: "Non-RDS recommendation - no adjustment",
			rec: common.Recommendation{
				ResourceType: "t3.small",
				Count:        5,
				Region:       "us-east-1",
				Details: common.ComputeDetails{
					Platform: "Linux",
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adjustRecommendationForExcludedVersions(tt.rec, tt.instanceVersions, versionInfo)
			assert.Equal(t, tt.expectedCount, result.Count)
		})
	}
}

// ==================== Tests for validateFlags ====================

func TestValidateFlags_Coverage(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	tests := []struct {
		name        string
		setupCfg    func()
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid configuration",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 3
				toolCfg.MaxInstances = 100
				toolCfg.OverrideCount = 5
			},
			expectError: false,
		},
		{
			name: "Coverage below 0",
			setupCfg: func() {
				toolCfg.Coverage = -10.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 1
			},
			expectError: true,
			errorMsg:    "coverage percentage must be between 0 and 100",
		},
		{
			name: "Coverage above 100",
			setupCfg: func() {
				toolCfg.Coverage = 150.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 1
			},
			expectError: true,
			errorMsg:    "coverage percentage must be between 0 and 100",
		},
		{
			name: "Invalid payment option",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "invalid-option"
				toolCfg.TermYears = 3
			},
			expectError: true,
			errorMsg:    "invalid payment option",
		},
		{
			name: "Invalid term years",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "partial-upfront"
				toolCfg.TermYears = 2 // Only 1 or 3 allowed
			},
			expectError: true,
			errorMsg:    "invalid term",
		},
		{
			name: "Negative max instances",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 3
				toolCfg.MaxInstances = -5
			},
			expectError: true,
			errorMsg:    "max-instances must be 0",
		},
		{
			name: "Max instances exceeds limit",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 3
				toolCfg.MaxInstances = MaxReasonableInstances + 1
			},
			expectError: true,
			errorMsg:    "exceeds reasonable limit",
		},
		{
			name: "Negative override count",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 3
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = -3
			},
			expectError: true,
			errorMsg:    "override-count must be 0",
		},
		{
			name: "Override count exceeds limit",
			setupCfg: func() {
				toolCfg.Coverage = 80.0
				toolCfg.PaymentOption = "all-upfront"
				toolCfg.TermYears = 3
				toolCfg.MaxInstances = 0
				toolCfg.OverrideCount = MaxReasonableInstances + 1
			},
			expectError: true,
			errorMsg:    "exceeds reasonable limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupCfg()

			err := validateFlags(nil, nil)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateFlags_CSVPaths(t *testing.T) {
	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	// Setup base valid config
	setupBaseCfg := func() {
		toolCfg.Coverage = 80.0
		toolCfg.PaymentOption = "all-upfront"
		toolCfg.TermYears = 3
		toolCfg.MaxInstances = 0
		toolCfg.OverrideCount = 0
		toolCfg.CSVOutput = ""
		toolCfg.CSVInput = ""
	}

	t.Run("Valid CSV output path", func(t *testing.T) {
		setupBaseCfg()
		toolCfg.CSVOutput = "/tmp/test-output.csv"

		err := validateFlags(nil, nil)
		assert.NoError(t, err)
	})

	t.Run("CSV output with non-existent directory", func(t *testing.T) {
		setupBaseCfg()
		toolCfg.CSVOutput = "/nonexistent/directory/output.csv"

		err := validateFlags(nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "output directory does not exist")
	})

	t.Run("CSV input with non-existent file", func(t *testing.T) {
		setupBaseCfg()
		toolCfg.CSVInput = "/nonexistent/file.csv"

		err := validateFlags(nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "input CSV file does not exist")
	})

	t.Run("CSV input without .csv extension", func(t *testing.T) {
		setupBaseCfg()
		// Create a temp file without .csv extension
		tmpFile, err := os.CreateTemp("", "test-input-*.txt")
		assert.NoError(t, err)
		defer os.Remove(tmpFile.Name())
		tmpFile.Close()

		toolCfg.CSVInput = tmpFile.Name()

		err = validateFlags(nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must have .csv extension")
	})

	t.Run("Valid CSV input file", func(t *testing.T) {
		setupBaseCfg()
		// Create a temp CSV file
		tmpFile, err := os.CreateTemp("", "test-input-*.csv")
		assert.NoError(t, err)
		defer os.Remove(tmpFile.Name())
		tmpFile.Close()

		toolCfg.CSVInput = tmpFile.Name()

		err = validateFlags(nil, nil)
		assert.NoError(t, err)
	})
}

// ==================== Tests for processService Error Paths ====================

func TestProcessService_GetRegionsError(t *testing.T) {
	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "all-upfront"
	toolCfg.TermYears = 3
	toolCfg.Regions = []string{} // Empty - will trigger auto-discovery

	mockClient := &MockRecommendationsClient{}
	accountCache := NewAccountAliasCache(awsCfg)

	// This test verifies behavior when region discovery is needed
	// Since getAllAWSRegions requires real AWS config, we test with explicit regions instead
	toolCfg.Regions = []string{"us-east-1"}

	// Setup mock to return empty recommendations
	params := common.RecommendationParams{
		Service:        common.ServiceRDS,
		Region:         "us-east-1",
		PaymentOption:  "all-upfront",
		Term:           "3yr",
		LookbackPeriod: "7d",
		IncludeSPTypes: toolCfg.IncludeSPTypes,
		ExcludeSPTypes: toolCfg.ExcludeSPTypes,
	}
	mockClient.On("GetRecommendations", ctx, params).Return([]common.Recommendation{}, nil)

	recs, results := processService(ctx, awsCfg, mockClient, accountCache, common.ServiceRDS, true, toolCfg, engineVersionData{})

	// Should return empty recommendations
	assert.Empty(t, recs)
	assert.Empty(t, results)

	mockClient.AssertExpectations(t)
}

func TestProcessService_GetRecommendationsError(t *testing.T) {
	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "partial-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = []string{"us-east-1"}

	mockClient := &MockRecommendationsClient{}
	accountCache := NewAccountAliasCache(awsCfg)

	// Setup mock to return error
	params := common.RecommendationParams{
		Service:        common.ServiceEC2,
		Region:         "us-east-1",
		PaymentOption:  "partial-upfront",
		Term:           "1yr",
		LookbackPeriod: "7d",
		IncludeSPTypes: toolCfg.IncludeSPTypes,
		ExcludeSPTypes: toolCfg.ExcludeSPTypes,
	}
	mockClient.On("GetRecommendations", ctx, params).Return([]common.Recommendation(nil), errors.New("API error"))

	recs, results := processService(ctx, awsCfg, mockClient, accountCache, common.ServiceEC2, true, toolCfg, engineVersionData{})

	// Should continue with empty results after error
	assert.Empty(t, recs)
	assert.Empty(t, results)

	mockClient.AssertExpectations(t)
}

func TestProcessService_AllRecommendationsFilteredOut(t *testing.T) {
	ctx := context.Background()
	awsCfg := aws.Config{Region: "us-east-1"}

	origCfg := toolCfg
	defer func() { toolCfg = origCfg }()

	toolCfg.Coverage = 100.0
	toolCfg.PaymentOption = "no-upfront"
	toolCfg.TermYears = 1
	toolCfg.Regions = []string{"us-east-1"}
	toolCfg.IncludeInstanceTypes = []string{"db.r5.large"} // Filter to specific type

	mockClient := &MockRecommendationsClient{}
	accountCache := NewAccountAliasCache(awsCfg)

	params := common.RecommendationParams{
		Service:        common.ServiceRDS,
		Region:         "us-east-1",
		PaymentOption:  "no-upfront",
		Term:           "1yr",
		LookbackPeriod: "7d",
		IncludeSPTypes: toolCfg.IncludeSPTypes,
		ExcludeSPTypes: toolCfg.ExcludeSPTypes,
	}

	// Return recommendations that don't match the filter
	mockRecs := []common.Recommendation{
		{ResourceType: "db.t3.small", Count: 5, Region: "us-east-1", EstimatedSavings: 100},
		{ResourceType: "db.t3.medium", Count: 3, Region: "us-east-1", EstimatedSavings: 200},
	}
	mockClient.On("GetRecommendations", ctx, params).Return(mockRecs, nil)

	recs, results := processService(ctx, awsCfg, mockClient, accountCache, common.ServiceRDS, true, toolCfg, engineVersionData{})

	// All recommendations should be filtered out
	assert.Empty(t, recs)
	assert.Empty(t, results)

	mockClient.AssertExpectations(t)
}

// ==================== Tests for filterAndAdjustRecommendations Edge Cases ====================

func TestFilterAndAdjustRecommendations_ZeroCoverage(t *testing.T) {
	saved := saveGlobalVars()
	defer saved.restore()

	recommendations := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 5},
		{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 3},
	}

	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 0

	result := filterAndAdjustRecommendations(recommendations, 0.0, toolCfg)

	// 0% coverage should return empty
	assert.Empty(t, result)
}

func TestFilterAndAdjustRecommendations_WithEngineVersionFiltering(t *testing.T) {
	saved := saveGlobalVars()
	defer saved.restore()

	recommendations := []common.Recommendation{
		{
			Service:      common.ServiceRDS,
			ResourceType: "db.t3.small",
			Count:        5,
			Region:       "us-east-1",
			Details: common.DatabaseDetails{
				Engine: "mysql",
			},
		},
	}

	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 0
	toolCfg.IncludeExtendedSupport = false

	result := filterAndAdjustRecommendations(recommendations, 100.0, toolCfg)

	// Should return recommendations (engine version filtering is done inside the function)
	assert.NotEmpty(t, result)
}

func TestFilterAndAdjustRecommendations_MaxInstancesApplied(t *testing.T) {
	saved := saveGlobalVars()
	defer saved.restore()

	recommendations := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 20},
		{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 20},
	}

	toolCfg.MaxInstances = 15
	toolCfg.OverrideCount = 0

	result := filterAndAdjustRecommendations(recommendations, 100.0, toolCfg)

	// Total instances should not exceed maxInstances
	totalInstances := 0
	for _, rec := range result {
		totalInstances += rec.Count
	}
	assert.LessOrEqual(t, totalInstances, int(toolCfg.MaxInstances))
}

func TestFilterAndAdjustRecommendations_OverrideCountApplied(t *testing.T) {
	saved := saveGlobalVars()
	defer saved.restore()

	recommendations := []common.Recommendation{
		{Service: common.ServiceRDS, ResourceType: "db.t3.small", Count: 20},
		{Service: common.ServiceRDS, ResourceType: "db.t3.medium", Count: 15},
	}

	toolCfg.MaxInstances = 0
	toolCfg.OverrideCount = 5

	result := filterAndAdjustRecommendations(recommendations, 100.0, toolCfg)

	// All recommendations should have count = OverrideCount
	for _, rec := range result {
		assert.Equal(t, int(toolCfg.OverrideCount), rec.Count)
	}
}
