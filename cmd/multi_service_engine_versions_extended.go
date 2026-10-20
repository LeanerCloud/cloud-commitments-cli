package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// isInExtendedSupport checks if a version is currently in extended support based on lifecycle dates.
// The second return value reports whether lifecycle data was available for the
// lookup: known is false when the major version cannot be extracted or the
// engine:majorVersion key is missing from versionInfo. Callers on money paths
// must not treat known=false as "not in extended support" (issue #1313).
// An extended-support entry with no start date is an anomalous payload: it is
// skipped with a warning rather than read as "already started", and if no
// other entry proves extended support is active, known is false (issue #1315).
func isInExtendedSupport(engine, fullVersion string, versionInfo map[string]MajorEngineVersionInfo) (extended, known bool) {
	majorVersion := extractMajorVersion(engine, fullVersion)
	if majorVersion == "" {
		return false, false
	}

	// Normalize engine name for lookup
	normalizedEngine := strings.ToLower(engine)
	normalizedEngine = strings.ReplaceAll(normalizedEngine, " ", "")

	// Look up the version info
	key := fmt.Sprintf("%s:%s", normalizedEngine, majorVersion)
	info, exists := versionInfo[key]
	if !exists {
		return false, false
	}

	// Check if current date falls within extended support period
	now := time.Now()
	missingStart := false
	for _, lifecycle := range info.SupportedEngineLifecycles {
		if lifecycle.LifecycleSupportName != string(rdstypes.LifecycleSupportNameOpenSourceRdsExtendedSupport) {
			continue
		}
		// A zero start date would pass the start gate below and claim the
		// window is open with no evidence; skip it loudly (issue #1315).
		if lifecycle.LifecycleSupportStartDate.IsZero() {
			missingStart = true
			log.Printf("⚠️  Extended-support lifecycle for %s %s has no start date: ignoring the entry, extended-support window unknown",
				normalizedEngine, majorVersion)
			continue
		}
		// Not in extended support before its start date
		if now.Before(lifecycle.LifecycleSupportStartDate) {
			continue
		}
		// Past the end date means extended support is over; a zero end date
		// is open-ended (issue #1182)
		if lifecycle.LifecycleSupportEndDate.IsZero() || now.Before(lifecycle.LifecycleSupportEndDate) {
			return true, true
		}
	}

	return false, !missingStart
}

// adjustRecommendationForExcludedVersions reduces the instance count in a recommendation
// by the number of instances running versions in extended support.
func adjustRecommendationForExcludedVersions(rec common.Recommendation, instanceVersions map[string][]InstanceEngineVersion, versionInfo map[string]MajorEngineVersionInfo) common.Recommendation {
	// Check if this instance type has any running instances
	versions, exists := instanceVersions[rec.ResourceType]
	if !exists {
		// No running instances of this type, return unchanged
		return rec
	}

	// Get the engine name from the recommendation. DatabaseDetails is
	// always a pointer (every producer constructs it that way; see
	// pkg/common/service_details_codec.go's package doc). A typed nil
	// pointer is treated like a non-RDS rec rather than panicking on the
	// field read.
	var recEngine string
	switch details := rec.Details.(type) {
	case *common.DatabaseDetails:
		if details == nil {
			return rec
		}
		recEngine = details.Engine
	default:
		return rec // Not RDS, no engine version filtering
	}

	// Count how many instances in this region are running versions in extended support
	excludedCount := 0

	// Warn once per engine:version when lifecycle data is missing instead of
	// silently treating the instance as not on extended support (issue #1313).
	unknownLogged := make(map[string]bool)

	for _, version := range versions {
		if shouldExcludeForExtendedSupport(version, rec, recEngine, versionInfo, unknownLogged) {
			excludedCount++
		}
	}

	// If we found excluded instances, reduce the recommendation count
	if excludedCount > 0 {
		originalCount := rec.Count
		newCount := max(0, rec.Count-excludedCount)

		if newCount != originalCount {
			log.Printf("📉 Adjusting recommendation for %s %s in %s: %d instances → %d instances (excluded %d extended support instances)",
				recEngine, rec.ResourceType, rec.Region, originalCount, newCount, excludedCount)
			return resizeAfterExclusion(rec, newCount)
		}
	}

	return rec
}

// resizeAfterExclusion sets rec to newCount instances with its money fields
// scaled to match. A count of zero is the caller's drop signal and carries no
// money. The caller derives newCount as max(0, rec.Count-excluded) with
// excluded > 0, so for newCount >= 1 it is always within [1, rec.Count] and
// common.ResizeCount cannot refuse it; a refusal means that invariant broke,
// which must not turn into a count with unscaled money.
func resizeAfterExclusion(rec common.Recommendation, newCount int) common.Recommendation {
	if newCount < 1 {
		rec.Count = 0
		return rec
	}
	resized, err := common.ResizeCount(rec, newCount)
	if err != nil {
		panic(fmt.Sprintf("extended-support resize invariant broken: %v", err))
	}
	return resized
}

// shouldExcludeForExtendedSupport reports whether a single running instance
// should be excluded from the recommendation because its engine version is in
// extended support. Instances in other regions or with other engines never
// match. When lifecycle data is missing the instance is kept (conservative
// skip) and a warning is logged once per engine:version via unknownLogged
// (issue #1313). Extracted from adjustRecommendationForExcludedVersions to
// keep both functions under the gocyclo cap.
func shouldExcludeForExtendedSupport(version InstanceEngineVersion, rec common.Recommendation, recEngine string, versionInfo map[string]MajorEngineVersionInfo, unknownLogged map[string]bool) bool {
	// Only count instances in the same region
	if version.Region != rec.Region {
		return false
	}

	// Match engine (normalize by removing spaces/hyphens and comparing lowercase)
	if normalizeEngineNameForVersion(version.Engine) != normalizeEngineNameForVersion(recEngine) {
		return false
	}

	extended, known := isInExtendedSupport(version.Engine, version.EngineVersion, versionInfo)
	if !known {
		// Leave the instance in the count but make the gap visible so
		// incomplete lifecycle data can't silently steer purchases (issue #1313).
		warnKey := version.Engine + ":" + version.EngineVersion
		if !unknownLogged[warnKey] {
			unknownLogged[warnKey] = true
			log.Printf("⚠️  No lifecycle data for %s %s in %s: extended-support status unknown, keeping instance in the count",
				version.Engine, version.EngineVersion, version.Region)
		}
		return false
	}

	if !extended {
		return false
	}

	majorVersion := extractMajorVersion(version.Engine, version.EngineVersion)
	log.Printf("🚫 Found extended support instance: %s %s in %s running version %s (major version %s is in extended support)",
		recEngine, rec.ResourceType, rec.Region, version.EngineVersion, majorVersion)
	return true
}
