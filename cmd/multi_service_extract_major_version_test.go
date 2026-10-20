package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Comprehensive tests for extractMajorVersion function.
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
