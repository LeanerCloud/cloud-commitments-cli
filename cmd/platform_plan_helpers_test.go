package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const platformPlanTestID = "abcdefab-1234-5678-9abc-abcdefabcdef"
const platformPlanTestKey = "SYNTHETIC-zqKey-VX98"
const platformPlanFixture = `{"id":"abcdefab-1234-5678-9abc-abcdefabcdef","name":"Weekly synthetic","enabled":true,"auto_purchase":false,"ramp_schedule":{"type":"weekly","current_step":2,"total_steps":7},"updated_at":"2026-10-01T12:34:56+02:00","next_execution_date":"2026-11-02T03:04:05Z","last_execution_date":null,"services":{"secret":"unlisted"},"unknown":"unlisted"}`

type platformPlanTransport func(*http.Request) (*http.Response, error)

func (f platformPlanTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type platformPlanBody struct {
	data     string
	consumed int
	closed   bool
	cancel   context.CancelFunc
}

func (b *platformPlanBody) Read(p []byte) (int, error) {
	n := copy(p, b.data[b.consumed:])
	b.consumed += n
	if b.consumed == len(b.data) {
		if b.cancel != nil {
			b.cancel()
		}
		return n, io.EOF
	}
	return n, nil
}
func (b *platformPlanBody) Close() error { b.closed = true; return nil }

func platformPlanEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CUDLY_PLATFORM_URL", " https://platform.example.invalid/ ")
	t.Setenv("CUDLY_PLATFORM_USER_API_KEY", " "+platformPlanTestKey+" \n")
	old := platformPlanHTTPClient
	t.Cleanup(func() { platformPlanHTTPClient = old })
}

func platformPlanResponse(t *testing.T, status int, body string) (*hookTransport, *platformPlanBody) {
	t.Helper()
	b := &platformPlanBody{data: body}
	h := &hookTransport{handle: func(r *http.Request) (int, string) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "https://platform.example.invalid/api/plans/"+platformPlanTestID, r.URL.String())
		assert.Equal(t, strings.TrimSpace(os.Getenv("CUDLY_PLATFORM_USER_API_KEY")), r.Header.Get("X-API-Key"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Len(t, r.Header, 2)
		deadline, ok := r.Context().Deadline()
		assert.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 30*time.Second)
		return status, body
	}}
	platformPlanHTTPClient = &http.Client{Transport: platformPlanTransport(func(r *http.Request) (*http.Response, error) {
		response, err := h.RoundTrip(r)
		response.Header.Set("Content-Type", "application/json")
		response.Body = b
		return response, err
	})}
	return h, b
}

func platformPlanGet(t *testing.T, format string, ctx context.Context) (string, string, error) {
	t.Helper()
	return platformPlanRun(t, ctx, "platform-plan", "get", platformPlanTestID, "--format", format)
}

func platformPlanRejected(t *testing.T, out, sinks string, err error, expected string) {
	t.Helper()
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Contains(t, err.Error(), expected)
	assert.Empty(t, sinks)
	assert.NotContains(t, out+sinks+err.Error(), platformPlanTestKey)
}

// Reflect reads Cobra's private nil/argument state; restoration uses public setters.
type platformPlanCtxKey struct{}

func platformPlanBoundary(t *testing.T) func() {
	t.Helper()
	var restore []func()
	var visit func(*cobra.Command)
	visit = func(c *cobra.Command) {
		v := reflect.ValueOf(c).Elem()
		ctx := c.Context()
		out, errOut := c.OutOrStdout(), c.ErrOrStderr()
		if v.FieldByName("outWriter").IsNil() {
			out = nil
		}
		if v.FieldByName("errWriter").IsNil() {
			errOut = nil
		}
		var args []string
		if a := v.FieldByName("args"); !a.IsNil() {
			args = make([]string, a.Len())
			for i := range args {
				args[i] = a.Index(i).String()
			}
		}
		restore = append(restore, func() { c.SetContext(ctx); c.SetOut(out); c.SetErr(errOut); c.SetArgs(args) })
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" || f.Name == "format" {
				value, changed := f.Value.String(), f.Changed
				restore = append(restore, func() { require.NoError(t, f.Value.Set(value)); f.Changed = changed })
			}
		})
		if c.Flags().Lookup("help") == nil {
			// Cobra may initialize its standard help catalog; restore absent help semantically.
			restore = append(restore, func() {
				if f := c.Flags().Lookup("help"); f != nil {
					require.NoError(t, f.Value.Set("false"))
					f.Changed = false
				}
			})
		}
		for _, child := range c.Commands() {
			visit(child)
		}
	}
	visit(rootCmd)
	oldConfig, oldClient := toolCfg, platformPlanHTTPClient
	oldLog, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	oldApp, appFlags, appPrefix := AppLogger.Writer(), AppLogger.Flags(), AppLogger.Prefix()
	oldSlog, oldLevel := slog.Default(), logging.GetLevel()
	var logs syncBuffer
	oldShared := logging.SetOutput(&logs)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&logs)
	AppLogger.SetOutput(&logs)
	return func() {
		for _, fn := range restore {
			fn()
		}
		toolCfg, platformPlanHTTPClient = oldConfig, oldClient
		slog.SetDefault(oldSlog)
		log.SetOutput(oldLog)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
		AppLogger.SetOutput(oldApp)
		AppLogger.SetFlags(appFlags)
		AppLogger.SetPrefix(appPrefix)
		logging.SetOutput(oldShared)
		logging.SetLevelValue(oldLevel)
	}
}

func platformPlanExecute(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	var out, diagnostics syncBuffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&diagnostics)
	rootCmd.SetArgs(args)
	log.SetFlags(0)
	log.SetPrefix("")
	AppLogger.SetFlags(0)
	AppLogger.SetPrefix("")
	logging.SetLevelValue(logging.LevelDebug)
	log.SetOutput(&diagnostics)
	AppLogger.SetOutput(&diagnostics)
	logging.SetOutput(&diagnostics)
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug})))
	slog.Debug("platform-plan debug capture fixture")
	fixture := diagnostics.String()
	require.Contains(t, fixture, "level=DEBUG")
	require.Contains(t, fixture, "platform-plan debug capture fixture")
	var err error
	realOut, realErr := captureRealStd(t, func() { err = rootCmd.ExecuteContext(ctx) })
	return out.String(), diagnostics.String()[len(fixture):] + realOut + realErr, err
}

func platformPlanRun(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	restore := platformPlanBoundary(t)
	defer restore()
	var clearHelp func(*cobra.Command)
	clearHelp = func(c *cobra.Command) {
		if f := c.Flags().Lookup("help"); f != nil {
			require.NoError(t, f.Value.Set("false"))
			f.Changed = false
		}
		for _, child := range c.Commands() {
			clearHelp(child)
		}
	}
	clearHelp(rootCmd)
	return platformPlanExecute(t, ctx, args...)
}
