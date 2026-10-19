package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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

func TestPlatformPlanResponseFailuresAndBoundary(t *testing.T) {
	platformPlanEnv(t)
	for status, message := range map[int]string{
		401: "platform plan authentication failed (HTTP 401)",
		403: "platform plan access denied (HTTP 403)",
		404: "platform plan not found (HTTP 404)",
		429: "platform plan rate limited (HTTP 429); not retried",
		500: "platform plan returned an unsuccessful HTTP status",
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h, b := platformPlanResponse(t, status, platformPlanTestKey)
			out, sinks, err := platformPlanGet(t, "json", context.Background())
			platformPlanRejected(t, out, sinks, err, message)
			assert.EqualError(t, err, message)
			assert.Len(t, h.paths, 1)
			assert.Zero(t, b.consumed)
			assert.True(t, b.closed)
		})
	}
	for _, body := range []string{"{", "null", `[]`, `{}`, strings.TrimSuffix(platformPlanFixture, "}"), platformPlanFixture + ` {}`, strings.Replace(platformPlanFixture, `"enabled":true`, `"enabled":"true"`, 1), strings.Replace(platformPlanFixture, `"enabled":true,`, "", 1), strings.Replace(platformPlanFixture, platformPlanTestID, "bad-id", 1), strings.Replace(platformPlanFixture, platformPlanTestID, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", 1), strings.Replace(platformPlanFixture, `"current_step":2`, `"current_step":2.5`, 1), strings.Replace(platformPlanFixture, `"total_steps":7`, `"total_steps":-1`, 1), strings.Replace(platformPlanFixture, `"type":"weekly"`, `"type":null`, 1), strings.Replace(platformPlanFixture, "2026-11-02T03:04:05Z", "bad-date", 1), platformPlanFixture + strings.Repeat(" ", (1<<20)+1-len(platformPlanFixture))} {
		t.Run("invalid response", func(t *testing.T) {
			h, b := platformPlanResponse(t, 200, body)
			out, sinks, err := platformPlanGet(t, "json", context.Background())
			platformPlanRejected(t, out, sinks, err, "platform plan response")
			assert.Len(t, h.paths, 1)
			assert.True(t, b.closed)
		})
	}
	for _, body := range []string{platformPlanFixture + strings.Repeat(" ", (1<<20)-len(platformPlanFixture)), strings.Replace(platformPlanFixture, `"last_execution_date":null,`, "", 1), strings.Replace(platformPlanFixture, `"current_step":2`, `"current_step":9`, 1)} {
		h, _ := platformPlanResponse(t, 200, body)
		out, _, err := platformPlanGet(t, "json", context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, out)
		assert.Len(t, h.paths, 1)
	}
	platformPlanHTTPClient = &http.Client{Transport: platformPlanTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New(platformPlanTestKey) })}
	out, sinks, err := platformPlanGet(t, "json", context.Background())
	platformPlanRejected(t, out, sinks, err, "platform plan request failed")
}

func TestPlatformPlanSuccessfulFinalReadCancellation(t *testing.T) {
	platformPlanEnv(t)
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			_, live := platformPlanResponse(t, 200, platformPlanFixture)
			control, _, err := platformPlanGet(t, format, context.Background())
			require.NoError(t, err)
			require.NotEmpty(t, control)
			require.Equal(t, len(platformPlanFixture), live.consumed)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h, b := platformPlanResponse(t, 200, platformPlanFixture)
			b.cancel = cancel
			out, sinks, err := platformPlanGet(t, format, ctx)
			require.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, out)
			assert.Empty(t, sinks)
			assert.Len(t, h.paths, 1)
			assert.Equal(t, len(platformPlanFixture), b.consumed)
			assert.True(t, b.closed)
		})
	}
}

func TestPlatformPlanContextControlsAndRedirects(t *testing.T) {
	platformPlanEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, _ := platformPlanResponse(t, 200, platformPlanFixture)
	out, sinks, err := platformPlanGet(t, "json", ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, out+sinks)
	assert.Empty(t, h.paths)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	platformPlanHTTPClient = &http.Client{Transport: platformPlanTransport(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		require.True(t, ok)
		assert.Equal(t, deadline, got)
		<-r.Context().Done()
		return nil, errors.New(platformPlanTestKey)
	})}
	out, sinks, err = platformPlanGet(t, "json", ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, out+sinks)
	platformPlanHTTPClient = &http.Client{Transport: platformPlanTransport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	out, sinks, err = platformPlanGet(t, "json", context.Background())
	platformPlanRejected(t, out, sinks, err, "platform plan request failed")
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
	for _, location := range []string{"https://other.example.invalid/", "https://platform.example.invalid/else"} {
		calls := 0
		callback := false
		original := &http.Client{Timeout: time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { callback = true; return nil }, Transport: platformPlanTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		platformPlanHTTPClient = original
		out, sinks, err = platformPlanGet(t, "json", context.Background())
		platformPlanRejected(t, out, sinks, err, "platform plan request failed")
		assert.Equal(t, 1, calls)
		assert.False(t, callback)
		assert.Equal(t, time.Hour, original.Timeout)
		require.NotNil(t, original.CheckRedirect)
	}
}

func TestPlatformPlanAcceptedSuccessPrivacy(t *testing.T) {
	platformPlanEnv(t)
	for _, format := range []string{"table", "json"} {
		for _, name := range []string{platformPlanTestKey, strings.Repeat("x", 700) + platformPlanTestKey, strings.Join(strings.Split(platformPlanTestKey, ""), "\u200b"), strings.Join(strings.Split(platformPlanTestKey, ""), "\x01"), strings.Join(strings.Split(platformPlanTestKey, ""), "\u0301"), strings.Join(strings.Split(platformPlanTestKey, ""), " * "), "ＳＹＮＴＨＥＴＩＣ-zqKey-VX98"} {
			t.Run(format+" name", func(t *testing.T) {
				encoded, _ := json.Marshal(name)
				body := strings.Replace(platformPlanFixture, `"Weekly synthetic"`, string(encoded), 1)
				h, _ := platformPlanResponse(t, 200, body)
				t.Setenv("CUDLY_PLATFORM_USER_API_KEY", "unrelated-control-credential-ZZW")
				control, _, err := platformPlanGet(t, format, context.Background())
				require.NoError(t, err)
				require.NotEmpty(t, control)
				assert.Len(t, h.paths, 1)
				t.Setenv("CUDLY_PLATFORM_USER_API_KEY", platformPlanTestKey)
				h, _ = platformPlanResponse(t, 200, body)
				out, sinks, err := platformPlanGet(t, format, context.Background())
				platformPlanRejected(t, out, sinks, err, "result withheld")
				assert.Len(t, h.paths, 1)
			})
		}
		for _, key := range []string{platformPlanTestID, "2026-11-02T03:04:05Z", "2", "7", "a"} {
			t.Run(format+" typed collision", func(t *testing.T) {
				t.Setenv("CUDLY_PLATFORM_USER_API_KEY", key)
				h, _ := platformPlanResponse(t, 200, platformPlanFixture)
				out, sinks, err := platformPlanGet(t, format, context.Background())
				platformPlanRejected(t, out, sinks, err, "result withheld")
				assert.Len(t, h.paths, 1)
			})
		}
		for _, name := range []string{"SYNTHETIC-zqKey-VX97", "SYNTHETIC-zqKeey-VX98", "ordinary\u001b[31m\u202e name"} {
			encoded, _ := json.Marshal(name)
			body := strings.Replace(platformPlanFixture, `"Weekly synthetic"`, string(encoded), 1)
			platformPlanResponse(t, 200, body)
			out, sinks, err := platformPlanGet(t, format, context.Background())
			require.NoError(t, err)
			assert.NotEmpty(t, out)
			assert.Empty(t, sinks)
			assert.NotContains(t, out, "\u001b")
			assert.NotContains(t, out, "\u202e")
		}
	}
}
