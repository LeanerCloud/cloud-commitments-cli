package main

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestIsInExtendedSupport(t *testing.T) {
	now := time.Now()
	pastDate := now.AddDate(0, -6, 0)
	futureDate := now.AddDate(3, 0, 0)

	tests := []struct {
		versionInfo   map[string]MajorEngineVersionInfo
		name          string
		engine        string
		version       string
		expected      bool
		expectedKnown bool
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
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: pastDate,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected:      true,
			expectedKnown: true,
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
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
							LifecycleSupportStartDate: now.AddDate(-2, 0, 0),
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected:      false,
			expectedKnown: true,
		},
		{
			name:    "Version info not found - unknown, not silently false",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"postgres:13": {
					Engine:             "postgres",
					MajorEngineVersion: "13",
				},
			},
			expected:      false,
			expectedKnown: false,
		},
		{
			name:          "Empty version info - unknown, not silently false",
			engine:        "mysql",
			version:       "5.7.44",
			versionInfo:   map[string]MajorEngineVersionInfo{},
			expected:      false,
			expectedKnown: false,
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
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: futureDate,
							LifecycleSupportEndDate:   futureDate.AddDate(1, 0, 0),
						},
					},
				},
			},
			expected:      false,
			expectedKnown: true,
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
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: now,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected:      true,
			expectedKnown: true,
		},
		{
			name:    "Extended support already ended",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: now.AddDate(-3, 0, 0),
							LifecycleSupportEndDate:   pastDate,
						},
					},
				},
			},
			expected:      false,
			expectedKnown: true,
		},
		{
			name:    "Zero end date treated as open-ended",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: pastDate,
						},
					},
				},
			},
			expected:      true,
			expectedKnown: true,
		},
		{
			// Issue #1315: both dates absent must not read as "in extended support".
			name:    "Zero start and zero end date - unknown, not silently true",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName: string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
						},
					},
				},
			},
			expected:      false,
			expectedKnown: false,
		},
		{
			// Issue #1315: a missing start date is skipped even with a future end date.
			name:    "Zero start date with future end date - unknown, not silently true",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:    string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportEndDate: futureDate,
						},
					},
				},
			},
			expected:      false,
			expectedKnown: false,
		},
		{
			// A well-formed active entry still proves extended support even
			// when another extended entry is missing its start date.
			name:    "Zero start entry alongside a valid active entry",
			engine:  "mysql",
			version: "5.7.44",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:5.7": {
					Engine:             "mysql",
					MajorEngineVersion: "5.7",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:    string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportEndDate: futureDate,
						},
						{
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: pastDate,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected:      true,
			expectedKnown: true,
		},
		{
			// A zero start date on a non-extended entry is irrelevant.
			name:    "Zero start date on standard support entry is ignored",
			engine:  "mysql",
			version: "8.0.35",
			versionInfo: map[string]MajorEngineVersionInfo{
				"mysql:8.0": {
					Engine:             "mysql",
					MajorEngineVersion: "8.0",
					SupportedEngineLifecycles: []EngineLifecycleInfo{
						{
							LifecycleSupportName:    string(rdstypes.LifecycleSupportNameOpenSourceRdsStandardSupport),
							LifecycleSupportEndDate: futureDate,
						},
					},
				},
			},
			expected:      false,
			expectedKnown: true,
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
							LifecycleSupportName:      string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
							LifecycleSupportStartDate: pastDate,
							LifecycleSupportEndDate:   futureDate,
						},
					},
				},
			},
			expected:      true,
			expectedKnown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extended, known := isInExtendedSupport(tt.engine, tt.version, tt.versionInfo)
			assert.Equal(t, tt.expected, extended)
			assert.Equal(t, tt.expectedKnown, known, "known flag mismatch")
		})
	}
}

// TestIsInExtendedSupport_ZeroStartDateWarns covers issue #1315: an
// extended-support entry with no start date is logged with the engine and
// major version, and the instance stays in the recommendation count.
func TestIsInExtendedSupport_ZeroStartDateWarns(t *testing.T) {
	versionInfo := map[string]MajorEngineVersionInfo{
		"mysql:5.7": {
			Engine:             "mysql",
			MajorEngineVersion: "5.7",
			SupportedEngineLifecycles: []EngineLifecycleInfo{
				{
					LifecycleSupportName:    string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport),
					LifecycleSupportEndDate: time.Now().AddDate(1, 0, 0),
				},
			},
		},
	}
	recommendation := common.Recommendation{
		Service:      common.ServiceRDS,
		Region:       "us-east-1",
		ResourceType: "db.r5.large",
		Count:        2,
		Details:      &common.DatabaseDetails{Engine: "MySQL"},
	}
	instanceVersions := map[string][]InstanceEngineVersion{
		"db.r5.large": {
			{Engine: "mysql", EngineVersion: "5.7.44", InstanceClass: "db.r5.large", Region: "us-east-1"},
		},
	}

	var logBuf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	result := adjustRecommendationForExcludedVersions(recommendation, instanceVersions, versionInfo)

	assert.Equal(t, 2, result.Count, "missing start date must not exclude the instance")
	logged := logBuf.String()
	assert.Contains(t, logged, "mysql 5.7 has no start date", "warning must name the engine and major version")
	assert.Contains(t, logged, "No lifecycle data for mysql 5.7.44", "caller must surface the unknown status")
}

// TestQueryRunningInstanceEngineVersions_ProfileSelection asserts the
// validation-profile precedence deterministically: the profiles name entries
// absent from the (redirected, empty) shared config, so config loading fails
// before any AWS call and the error names the profile actually selected.
// Engine-error and region-failure behavior is covered by the stubbed
// TestQueryMajorEngineVersionsWithClient_* and TestQueryRDSInstancesInRegions_*.
func TestQueryRunningInstanceEngineVersions_ProfileSelection(t *testing.T) {
	tests := []struct {
		name              string
		profile           string
		validationProfile string
		wantProfileInErr  string
	}{
		{
			name:              "validation profile takes precedence",
			profile:           "cudly-test-main-profile-does-not-exist",
			validationProfile: "cudly-test-validation-profile-does-not-exist",
			wantProfileInErr:  "cudly-test-validation-profile-does-not-exist",
		},
		{
			name:             "falls back to main profile",
			profile:          "cudly-test-main-profile-does-not-exist",
			wantProfileInErr: "cudly-test-main-profile-does-not-exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWSEnv(t)

			instances, failed, err := queryRunningInstanceEngineVersions(context.Background(), Config{
				Profile:           tt.profile,
				ValidationProfile: tt.validationProfile,
			})

			require.Error(t, err)
			assert.Nil(t, instances)
			assert.Nil(t, failed)
			assert.Contains(t, err.Error(), "failed to load validation AWS config")
			assert.Contains(t, err.Error(), tt.wantProfileInErr, "error must name the selected profile")
		})
	}
}
