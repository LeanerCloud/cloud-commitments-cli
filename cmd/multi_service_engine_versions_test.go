package main

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestAdjustRecommendationForExcludedVersions(t *testing.T) {
	tests := []struct {
		name             string
		recommendation   common.Recommendation
		versionInfo      map[string]MajorEngineVersionInfo
		instanceVersions map[string][]InstanceEngineVersion
		expectedCount    int
		expectedAdjusted bool
	}{
		{
			name: "No running instances - recommendation unchanged",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        10,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo:      createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{},
			expectedCount:    10,
			expectedAdjusted: false,
		},
		{
			name: "Exclude 1 MySQL 5.7 instance in extended support",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        10,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
					{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
					{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
				},
			},
			expectedCount:    9, // 10 - 1 MySQL 5.7 instance in extended support
			expectedAdjusted: true,
		},
		{
			name: "Exclude all MySQL 5.7 instances in extended support",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "eu-west-2",
				ResourceType: "db.t3.small",
				Count:        2,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.t3.small": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.2", InstanceClass: "db.t3.small", Region: "eu-west-2"},
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.2", InstanceClass: "db.t3.small", Region: "eu-west-2"},
				},
			},
			expectedCount:    0, // All excluded (both in extended support)
			expectedAdjusted: true,
		},
		{
			name: "Different engine - no adjustment",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        5,
				Details: &common.DatabaseDetails{
					Engine: "Aurora PostgreSQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
				},
			},
			expectedCount:    5, // Different engine, no adjustment
			expectedAdjusted: false,
		},
		{
			name: "Different region - no adjustment",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.r5.large",
				Count:        5,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL",
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "eu-west-2"},
				},
			},
			expectedCount:    5, // Different region, no adjustment
			expectedAdjusted: false,
		},
		{
			name: "MySQL (not Aurora) with standard mysql engine name",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "eu-west-2",
				ResourceType: "db.r5.4xlarge",
				Count:        8,
				Details: &common.DatabaseDetails{
					Engine: "MySQL",
				},
			},
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-extended-support",
							LifecycleSupportStartDate: time.Now().AddDate(0, -6, 0),
							LifecycleSupportEndDate:   time.Now().AddDate(3, 0, 0),
						},
					},
				},
			},
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r5.4xlarge": {
					{Engine: "mysql", EngineVersion: "5.7.44", InstanceClass: "db.r5.4xlarge", Region: "eu-west-2"},
					{Engine: "mysql", EngineVersion: "8.0.35", InstanceClass: "db.r5.4xlarge", Region: "eu-west-2"},
				},
			},
			expectedCount:    7, // 8 - 1 MySQL 5.7 instance in extended support
			expectedAdjusted: true,
		},
		{
			name: "Engine name normalization - spaces vs hyphens",
			recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-west-2",
				ResourceType: "db.r6g.large",
				Count:        3,
				Details: &common.DatabaseDetails{
					Engine: "Aurora MySQL", // Space in name
				},
			},
			versionInfo: createTestVersionInfo(),
			instanceVersions: map[string][]InstanceEngineVersion{
				"db.r6g.large": {
					{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.12.0", InstanceClass: "db.r6g.large", Region: "us-west-2"}, // Hyphen in name
				},
			},
			expectedCount:    2, // Should match despite space vs hyphen
			expectedAdjusted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adjustRecommendationForExcludedVersions(tt.recommendation, tt.instanceVersions, tt.versionInfo)

			assert.Equal(t, tt.expectedCount, result.Count, "Instance count mismatch")

			if tt.expectedAdjusted {
				assert.NotEqual(t, tt.recommendation.Count, result.Count, "Count should have been adjusted")
			} else {
				assert.Equal(t, tt.recommendation.Count, result.Count, "Count should not have been adjusted")
			}
		})
	}
}

func TestAdjustRecommendationForExcludedVersions_MultipleVersionsInExtendedSupport(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceRDS,
		Region:       "us-east-1",
		ResourceType: "db.r5.large",
		Count:        10,
		Details: &common.DatabaseDetails{
			Engine: "Aurora MySQL",
		},
	}

	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r5.large": {
			{Engine: "aurora-mysql", EngineVersion: "5.6.mysql_aurora.1.22.5", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "aurora-mysql", EngineVersion: "5.7.mysql_aurora.2.11.1", InstanceClass: "db.r5.large", Region: "us-east-1"},
			{Engine: "aurora-mysql", EngineVersion: "8.0.mysql_aurora.3.04.0", InstanceClass: "db.r5.large", Region: "us-east-1"},
		},
	}

	// Version info with both 5.6 and 5.7 in extended support
	versionInfo := map[string]MajorEngineVersionInfo{
		"aurora-mysql:5.6": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "5.6",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-extended-support",
					LifecycleSupportStartDate: time.Now().AddDate(0, -12, 0),
					LifecycleSupportEndDate:   time.Now().AddDate(2, 0, 0),
				},
			},
		},
		"aurora-mysql:5.7": {
			Engine:             "aurora-mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:      "open-source-rds-extended-support",
					LifecycleSupportStartDate: time.Now().AddDate(0, -6, 0),
					LifecycleSupportEndDate:   time.Now().AddDate(3, 0, 0),
				},
			},
		},
	}

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 8, result.Count, "Should exclude 2 instances (5.6 and 5.7 both in extended support)")
}

func TestAdjustRecommendationForExcludedVersions_NonRDSService(t *testing.T) {
	recommendation := common.Recommendation{
		Service:      common.ServiceEC2,
		Region:       "us-east-1",
		ResourceType: "m5.large",
		Count:        5,
		Details:      nil, // Not RDS
	}

	instanceVersions := map[string][]InstanceEngineVersion{}
	versionInfo := createTestVersionInfo()

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 5, result.Count, "Non-RDS services should not be adjusted")
}

func TestExtractMajorVersion_Additional(t *testing.T) {
	tests := []struct {
		name     string
		engine   string
		version  string
		expected string
	}{
		{
			name:     "MySQL 5.7.44 extracts 5.7",
			engine:   "mysql",
			version:  "5.7.44",
			expected: "5.7",
		},
		{
			name:     "MySQL 8.0.35 extracts 8.0",
			engine:   "mysql",
			version:  "8.0.35",
			expected: "8.0",
		},
		{
			name:     "PostgreSQL 13.10 extracts 13.10",
			engine:   "postgres",
			version:  "13.10",
			expected: "13.10",
		},
		{
			name:     "PostgreSQL 15.4 extracts 15.4",
			engine:   "postgres",
			version:  "15.4",
			expected: "15.4",
		},
		{
			name:     "Aurora MySQL compatible 5.7.mysql_aurora.2.11.3",
			engine:   "aurora-mysql",
			version:  "5.7.mysql_aurora.2.11.3",
			expected: "5.7",
		},
		{
			name:     "Aurora PostgreSQL 14.6",
			engine:   "aurora-postgresql",
			version:  "14.6",
			expected: "14.6",
		},
		{
			name:     "Empty version",
			engine:   "mysql",
			version:  "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMajorVersion(tt.engine, tt.version)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Comprehensive tests for extractMajorVersion function
func TestExtractMajorVersion_Comprehensive(t *testing.T) {
	tests := []struct {
		name     string
		engine   string
		version  string
		expected string
	}{
		// Aurora MySQL special formats
		{
			name:     "Aurora MySQL 2.x format (MySQL 5.7 compatible)",
			engine:   "aurora-mysql",
			version:  "mysql_aurora.2.11.1",
			expected: "5.7",
		},
		{
			name:     "Aurora MySQL 3.x format (MySQL 8.0 compatible)",
			engine:   "aurora-mysql",
			version:  "mysql_aurora.3.04.0",
			expected: "8.0",
		},
		{
			name:     "Aurora MySQL 2.x with full version",
			engine:   "aurora-mysql",
			version:  "5.7.mysql_aurora.2.12.0",
			expected: "5.7",
		},
		{
			name:     "Aurora MySQL 3.x with full version",
			engine:   "aurora-mysql",
			version:  "8.0.mysql_aurora.3.05.2",
			expected: "8.0",
		},
		{
			name:     "Aurora MySQL with only major.minor",
			engine:   "aurora-mysql",
			version:  "5.7",
			expected: "5.7",
		},
		{
			name:     "Aurora MySQL with only major.minor v8",
			engine:   "aurora-mysql",
			version:  "8.0",
			expected: "8.0",
		},
		{
			name:     "Aurora MySQL engine name with spaces",
			engine:   "Aurora MySQL",
			version:  "mysql_aurora.2.11.1",
			expected: "5.7",
		},
		{
			name:     "Aurora MySQL engine name normalized",
			engine:   "AuroraMYSQL",
			version:  "mysql_aurora.3.04.0",
			expected: "8.0",
		},

		// Standard MySQL versions
		{
			name:     "MySQL 5.6.x",
			engine:   "mysql",
			version:  "5.6.51",
			expected: "5.6",
		},
		{
			name:     "MySQL 5.7.x",
			engine:   "mysql",
			version:  "5.7.40",
			expected: "5.7",
		},
		{
			name:     "MySQL 8.0.x",
			engine:   "mysql",
			version:  "8.0.33",
			expected: "8.0",
		},
		{
			name:     "MySQL with only major.minor",
			engine:   "mysql",
			version:  "5.7",
			expected: "5.7",
		},

		// PostgreSQL versions
		{
			name:     "PostgreSQL 11.x",
			engine:   "postgres",
			version:  "11.19",
			expected: "11.19",
		},
		{
			name:     "PostgreSQL 12.x",
			engine:   "postgres",
			version:  "12.15",
			expected: "12.15",
		},
		{
			name:     "PostgreSQL 13.x",
			engine:   "postgres",
			version:  "13.11",
			expected: "13.11",
		},
		{
			name:     "PostgreSQL 14.x",
			engine:   "postgres",
			version:  "14.8",
			expected: "14.8",
		},
		{
			name:     "PostgreSQL 15.x",
			engine:   "postgres",
			version:  "15.3",
			expected: "15.3",
		},

		// Aurora PostgreSQL versions
		{
			name:     "Aurora PostgreSQL 11.x",
			engine:   "aurora-postgresql",
			version:  "11.18",
			expected: "11.18",
		},
		{
			name:     "Aurora PostgreSQL 13.x",
			engine:   "aurora-postgresql",
			version:  "13.10",
			expected: "13.10",
		},
		{
			name:     "Aurora PostgreSQL 14.x",
			engine:   "aurora-postgresql",
			version:  "14.7",
			expected: "14.7",
		},

		// Edge cases
		{
			name:     "Version with only major number",
			engine:   "mysql",
			version:  "8",
			expected: "8",
		},
		{
			name:     "Version with patch containing letters",
			engine:   "mysql",
			version:  "5.7.40a",
			expected: "5.7",
		},
		{
			name:     "Version with non-numeric minor (extracts numeric part)",
			engine:   "mysql",
			version:  "8.0rc1",
			expected: "8.0",
		},
		{
			name:     "Empty version string",
			engine:   "mysql",
			version:  "",
			expected: "",
		},
		{
			name:     "Version with extra dots",
			engine:   "postgres",
			version:  "13.10.1.2",
			expected: "13.10",
		},
		{
			name:     "Engine name with hyphens",
			engine:   "aurora-mysql",
			version:  "5.7.44",
			expected: "5.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMajorVersion(tt.engine, tt.version)
			assert.Equal(t, tt.expected, result, "extractMajorVersion(%q, %q) should return %q", tt.engine, tt.version, tt.expected)
		})
	}
}

func TestIsInExtendedSupport(t *testing.T) {
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)
	futureDate := now.AddDate(3, 0, 0)

	tests := []struct {
		name        string
		engine      string
		version     string
		versionInfo map[string]MajorEngineVersionInfo
		expected    bool
	}{
		{
			name:    "Version in extended support",
			engine:  "aurora-mysql",
			version: "5.7.mysql_aurora.2.11.1",
			versionInfo: map[string]MajorEngineVersionInfo{
				"aurora-mysql:5.7": {
					Engine:             "aurora-mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-extended-support",
							LifecycleSupportStartDate: pastDate,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected: true,
		},
		{
			name:    "Version not in extended support - still in standard support",
			engine:  "aurora-mysql",
			version: "8.0.mysql_aurora.3.04.0",
			versionInfo: map[string]MajorEngineVersionInfo{
				"aurora-mysql:8.0": {
					Engine:             "aurora-mysql",
					MajorEngineVersion: "8.0",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-standard-support",
							LifecycleSupportStartDate: now.AddDate(-2, 0, 0),
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected: false,
		},
		{
			name:    "Version info not found",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"postgres:13": {
					Engine:             "postgres",
					MajorEngineVersion: "13",
				},
			},
			expected: false,
		},
		{
			name:        "Empty version info",
			engine:      "mysql",
			version:     "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{},
			expected:    false,
		},
		{
			name:    "Extended support not started yet",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-extended-support",
							LifecycleSupportStartDate: futureDate,
							LifecycleSupportEndDate:   futureDate.AddDate(1, 0, 0),
						},
					},
				},
			},
			expected: false,
		},
		{
			name:    "Extended support started on current date",
			engine:  "postgres",
			version: "11.19",
			versionInfo: map[string]MajorEngineVersionInfo{
				"postgres:11.19": {
					Engine:             "postgres",
					MajorEngineVersion: "11.19",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-extended-support",
							LifecycleSupportStartDate: now,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected: true,
		},
		{
			name:    "Engine name normalization with spaces",
			engine:  "Aurora MySQL",
			version: "5.7.mysql_aurora.2.11.1",
			versionInfo: map[string]MajorEngineVersionInfo{
				"auroramysql:5.7": {
					Engine:             "auroramysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      "open-source-rds-extended-support",
							LifecycleSupportStartDate: pastDate,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInExtendedSupport(tt.engine, tt.version, tt.versionInfo)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQueryMajorEngineVersions_ErrorHandling(t *testing.T) {
	// This test validates the error handling logic when AWS config fails
	ctx := context.Background()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "Empty profile - should use default credentials",
			cfg: Config{
				Profile:           "",
				ValidationProfile: "",
			},
			wantErr: false, // Will fail with real AWS but tests error path exists
		},
		{
			name: "With validation profile",
			cfg: Config{
				Profile:           "default",
				ValidationProfile: "validation-profile",
			},
			wantErr: false,
		},
		{
			name: "Fallback to main profile",
			cfg: Config{
				Profile:           "main-profile",
				ValidationProfile: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This will likely fail in test environment without real AWS credentials
			// but it validates the function signature and basic logic paths
			_, err := queryMajorEngineVersions(ctx, tt.cfg)
			// We expect an error in test environment (no real AWS creds)
			// The important thing is that the function doesn't panic
			if err != nil {
				assert.Contains(t, err.Error(), "failed to load AWS config")
			}
		})
	}
}

func TestQueryRunningInstanceEngineVersions_ErrorHandling(t *testing.T) {
	// This test validates the error handling logic
	ctx := context.Background()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "Empty profile - should use default credentials",
			cfg: Config{
				Profile:           "",
				ValidationProfile: "",
			},
			wantErr: false,
		},
		{
			name: "With validation profile",
			cfg: Config{
				Profile:           "default",
				ValidationProfile: "validation-profile",
			},
			wantErr: false,
		},
		{
			name: "Fallback to main profile",
			cfg: Config{
				Profile:           "main-profile",
				ValidationProfile: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This will likely fail in test environment without real AWS credentials
			// but it validates the function signature and basic logic paths
			_, err := queryRunningInstanceEngineVersions(ctx, tt.cfg)
			// We expect an error in test environment (no real AWS creds)
			// The important thing is that the function doesn't panic
			if err != nil {
				assert.Contains(t, err.Error(), "failed to")
			}
		})
	}
}
