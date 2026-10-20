package main

import (
	"strings"
)

// extractMajorVersion extracts the major version from a full engine version string
// Handles special cases like Aurora MySQL version mapping.
func extractMajorVersion(engine, fullVersion string) string {
	if fullVersion == "" {
		return ""
	}

	normalizedEngine := normalizeEngineNameForVersion(engine)

	// Handle Aurora MySQL special format
	if normalizedEngine == "auroramysql" {
		if auroraVersion := extractAuroraMySQLVersion(fullVersion); auroraVersion != "" {
			return auroraVersion
		}
	}

	// For standard versions, extract "X.Y" or "X"
	return extractStandardVersion(fullVersion)
}

// normalizeEngineNameForVersion normalizes an engine name by removing spaces and hyphens.
func normalizeEngineNameForVersion(engine string) string {
	normalized := strings.ToLower(engine)
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return normalized
}

// extractAuroraMySQLVersion extracts the MySQL-compatible version from Aurora MySQL.
func extractAuroraMySQLVersion(fullVersion string) string {
	// Aurora MySQL 2.x is compatible with MySQL 5.7
	if strings.Contains(fullVersion, "mysql_aurora.2.") {
		return "5.7"
	}
	// Aurora MySQL 3.x is compatible with MySQL 8.0
	if strings.Contains(fullVersion, "mysql_aurora.3.") {
		return "8.0"
	}
	// Check if it starts with a version number
	if strings.HasPrefix(fullVersion, "5.7") {
		return "5.7"
	}
	if strings.HasPrefix(fullVersion, "8.0") {
		return "8.0"
	}
	return ""
}

// extractStandardVersion extracts major.minor version from a standard version string.
func extractStandardVersion(fullVersion string) string {
	parts := strings.Split(fullVersion, ".")
	if len(parts) >= 2 {
		return extractMajorMinorVersion(parts[0], parts[1])
	}
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

// extractMajorMinorVersion combines major and minor version parts.
func extractMajorMinorVersion(major, minor string) string {
	// Filter out non-numeric parts in minor version
	numericMinor := extractNumericPrefix(minor)
	if numericMinor != "" {
		return major + "." + numericMinor
	}
	return major
}

// extractNumericPrefix extracts the numeric prefix from a string.
func extractNumericPrefix(s string) string {
	numericPrefix := ""
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			numericPrefix += string(ch)
		} else {
			break
		}
	}
	return numericPrefix
}
