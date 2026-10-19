package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformPlanRootSummary(t *testing.T) {
	platformPlanEnv(t)
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			h, b := platformPlanResponse(t, 200, platformPlanFixture)
			out, sinks, err := platformPlanGet(t, format, context.Background())
			require.NoError(t, err)
			assert.Equal(t, 1, h.count("/api/plans/"+platformPlanTestID))
			assert.Len(t, h.paths, 1)
			assert.True(t, b.closed)
			assert.Equal(t, len(platformPlanFixture), b.consumed)
			assert.Empty(t, sinks)
			assert.NotContains(t, out, "unlisted")
			if format == "json" {
				var got map[string]any
				require.NoError(t, json.Unmarshal([]byte(out), &got))
				assert.Equal(t, map[string]any{"id": platformPlanTestID, "name": "Weekly synthetic", "enabled": true, "auto_purchase": false, "ramp_type": "weekly", "current_step": float64(2), "total_steps": float64(7), "updated_at": "2026-10-01T12:34:56+02:00", "next_execution_date": "2026-11-02T03:04:05Z", "last_execution_date": nil}, got)
			} else {
				assert.Equal(t, "id\t"+platformPlanTestID+"\nname\tWeekly synthetic\nenabled\ttrue\nauto_purchase\tfalse\nramp_type\tweekly\ncurrent_step\t2\ntotal_steps\t7\nupdated_at\t2026-10-01T12:34:56+02:00\nnext_execution_date (next execution reported by platform)\t2026-11-02T03:04:05Z\nlast_execution_date\tunknown\n", out)
			}
			assert.NotContains(t, out, platformPlanTestKey)
		})
	}
}

func TestPlatformPlanZeroHTTP(t *testing.T) {
	platformPlanEnv(t)
	for _, args := range [][]string{{"--help"}, {"platform-plan", "--help"}, {"platform-plan", "get", "--help"}, {"archera-comparison", "--help"}, {"platform-plan", "get"}, {"platform-plan", "get", platformPlanTestID, "extra"}, {"platform-plan", "get", "urn:uuid:" + platformPlanTestID}, {"platform-plan", "get", platformPlanTestID + "?bad"}, {"platform-plan", "get", platformPlanTestID, "--format", "bad"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h, _ := platformPlanResponse(t, 200, platformPlanFixture)
			out, sinks, err := platformPlanRun(t, context.Background(), args...)
			if args[len(args)-1] == "--help" {
				require.NoError(t, err)
				assert.NotEmpty(t, out)
			} else {
				require.Error(t, err)
				assert.Empty(t, out)
			}
			assert.Empty(t, h.paths)
			assert.NotContains(t, sinks, platformPlanTestKey)
		})
	}
	for _, endpoint := range []string{"", "http://platform.example.invalid", "https://user:pass@platform.example.invalid", "https://platform.example.invalid/path", "https://platform.example.invalid/?x=1", "https://platform.example.invalid/#", "https://platform.example.invalid:bad", "https://platform.example.invalid:65536", "https:///", "https:opaque", "https://platform.example.invalid/%2f"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("CUDLY_PLATFORM_URL", endpoint)
			h, _ := platformPlanResponse(t, 200, platformPlanFixture)
			out, sinks, err := platformPlanGet(t, "json", context.Background())
			platformPlanRejected(t, out, sinks, err, "CUDLY_PLATFORM_URL")
			assert.Empty(t, h.paths)
		})
	}
	for _, key := range []string{"", "bad\rkey", "bad\tkey", "bad\nkey", "bad\x7fkey", "badékey"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("CUDLY_PLATFORM_USER_API_KEY", key)
			for _, name := range []string{"ARCHERA_API_KEY", "CUDLY_API_KEY", "AWS_SECRET_ACCESS_KEY", "SERVER_API_KEY"} {
				t.Setenv(name, platformPlanTestKey)
			}
			h, _ := platformPlanResponse(t, 200, platformPlanFixture)
			out, sinks, err := platformPlanGet(t, "table", context.Background())
			platformPlanRejected(t, out, sinks, err, "CUDLY_PLATFORM_USER_API_KEY")
			assert.Empty(t, h.paths)
		})
	}
}
