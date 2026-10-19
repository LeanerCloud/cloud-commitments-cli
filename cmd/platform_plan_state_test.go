package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformPlanRootStateAndStickyContexts(t *testing.T) {
	platformPlanEnv(t)
	restore := platformPlanBoundary(t)
	defer restore()
	group, _, err := rootCmd.Find([]string{"platform-plan"})
	require.NoError(t, err)
	leaf, _, err := rootCmd.Find([]string{"platform-plan", "get"})
	require.NoError(t, err)
	for _, seeded := range []bool{false, true} {
		var seed context.Context
		if seeded {
			c, cancel := context.WithCancel(context.WithValue(context.Background(), platformPlanCtxKey{}, "marker"))
			cancel()
			seed = c
		}
		for _, c := range []*cobra.Command{rootCmd, group, leaf} {
			c.SetContext(seed)
		}
		var seededOut, seededErr bytes.Buffer
		rootCmd.SetOut(&seededOut)
		rootCmd.SetErr(&seededErr)
		var seededArgs []string
		if seeded {
			seededArgs = []string{"archera-comparison", "--help"}
		}
		rootCmd.SetArgs(seededArgs)
		seededClient := &http.Client{}
		platformPlanHTTPClient = seededClient
		var stdSink, appSink, sharedSink, slogSink syncBuffer
		for _, sink := range []*syncBuffer{&stdSink, &appSink, &sharedSink, &slogSink} {
			_, werr := sink.Write([]byte("untouched sentinel"))
			require.NoError(t, werr)
		}
		seededSlog := slog.New(slog.NewTextHandler(&slogSink, &slog.HandlerOptions{Level: slog.LevelWarn}))
		slog.SetDefault(seededSlog)
		log.SetOutput(&stdSink)
		log.SetFlags(log.Llongfile)
		log.SetPrefix("seed-standard:")
		AppLogger.SetOutput(&appSink)
		AppLogger.SetFlags(log.Lshortfile)
		AppLogger.SetPrefix("seed-app:")
		logging.SetOutput(&sharedSink)
		logging.SetLevelValue(logging.LevelWarn)
		toolCfg.ActualPurchase = true
		for _, c := range []*cobra.Command{rootCmd, group, leaf} {
			c.InitDefaultHelpFlag()
			f := c.Flags().Lookup("help")
			require.NoError(t, f.Value.Set("true"))
			f.Changed = true
		}
		f := leaf.Flags().Lookup("format")
		require.NoError(t, f.Value.Set("table"))
		f.Changed = true
		for _, step := range []struct {
			name          string
			args          []string
			missingConfig bool
		}{
			{"help", []string{"platform-plan", "get", "--help"}, false},
			{"valid JSON", []string{"platform-plan", "get", platformPlanTestID, "--format", "json"}, false},
			{"invalid", []string{"platform-plan", "get", "bad"}, false},
			{"missing configuration", []string{"platform-plan", "get", platformPlanTestID}, true},
			{"valid table", []string{"platform-plan", "get", platformPlanTestID, "--format", "table"}, false},
		} {
			t.Run(step.name, func(t *testing.T) {
				restore := platformPlanBoundary(t)
				defer restore()
				if step.missingConfig {
					t.Setenv("CUDLY_PLATFORM_URL", "")
				}
				h, _ := platformPlanResponse(t, 200, platformPlanFixture)
				out, sinks, runErr := platformPlanRun(t, context.Background(), step.args...)
				if step.name == "invalid" || step.missingConfig {
					require.Error(t, runErr)
					assert.Empty(t, out)
				} else {
					require.NoError(t, runErr)
					assert.NotEmpty(t, out)
				}
				assert.Empty(t, sinks)
				if step.name == "help" || step.name == "invalid" || step.missingConfig {
					assert.Empty(t, h.paths)
				} else {
					assert.Len(t, h.paths, 1)
				}
			})
			for _, c := range []*cobra.Command{rootCmd, group, leaf} {
				assert.True(t, seed == c.Context())
				assert.Equal(t, "true", c.Flags().Lookup("help").Value.String())
				assert.True(t, c.Flags().Lookup("help").Changed)
			}
			assert.Same(t, &seededOut, rootCmd.OutOrStdout())
			assert.Same(t, &seededErr, rootCmd.ErrOrStderr())
			rawArgs := reflect.ValueOf(rootCmd).Elem().FieldByName("args")
			assert.Equal(t, seededArgs == nil, rawArgs.IsNil())
			require.Equal(t, len(seededArgs), rawArgs.Len())
			for i, want := range seededArgs {
				assert.Equal(t, want, rawArgs.Index(i).String())
			}
			assert.Same(t, seededClient, platformPlanHTTPClient)
			assert.Same(t, &stdSink, log.Writer())
			assert.Equal(t, log.Llongfile, log.Flags())
			assert.Equal(t, "seed-standard:", log.Prefix())
			assert.Same(t, &appSink, AppLogger.Writer())
			assert.Equal(t, log.Lshortfile, AppLogger.Flags())
			assert.Equal(t, "seed-app:", AppLogger.Prefix())
			assert.Same(t, seededSlog, slog.Default())
			assert.Equal(t, logging.LevelWarn, logging.GetLevel())
			sharedWriter := logging.SetOutput(&sharedSink)
			assert.Same(t, &sharedSink, sharedWriter)
			logging.SetOutput(sharedWriter)
			for _, sink := range []*syncBuffer{&stdSink, &appSink, &sharedSink, &slogSink} {
				assert.Equal(t, "untouched sentinel", sink.String())
			}
			assert.True(t, toolCfg.ActualPurchase)
			assert.Equal(t, "table", f.Value.String())
			assert.True(t, f.Changed)
		}
	}
	for _, c := range []*cobra.Command{rootCmd, group, leaf} {
		c.SetContext(nil) //nolint:staticcheck // restore the pristine nil context
		require.NoError(t, c.Flags().Lookup("help").Value.Set("false"))
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	h, _ := platformPlanResponse(t, 200, platformPlanFixture)
	out, sinks, err := platformPlanExecute(t, canceled, "platform-plan", "get", platformPlanTestID, "--format", "json")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, out+sinks)
	assert.Empty(t, h.paths)
	sticky := leaf.Context()
	require.ErrorIs(t, sticky.Err(), context.Canceled)
	h, _ = platformPlanResponse(t, 200, platformPlanFixture)
	out, sinks, err = platformPlanExecute(t, context.Background(), "platform-plan", "get", platformPlanTestID, "--format", "table")
	require.NoError(t, err)
	assert.NotEmpty(t, out)
	assert.Empty(t, sinks)
	assert.Len(t, h.paths, 1)
	assert.Same(t, sticky, leaf.Context())
}

func TestPlatformPlanFreshHelpRestoration(t *testing.T) {
	const marker = "CUDLY_PLATFORM_PLAN_HELP_TEST_CHILD"
	if os.Getenv(marker) != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, executable, "-test.run=^TestPlatformPlanFreshHelpRestoration$", "-test.timeout=8s", "-test.count=1")
		child.Env = append(os.Environ(), marker+"=1")
		child.WaitDelay = time.Second
		diagnostics, err := child.CombinedOutput()
		require.NoError(t, ctx.Err(), "fresh help child exceeded its deadline: %s", diagnostics)
		require.NoError(t, err, "fresh help child failed: %s", diagnostics)
		return
	}
	platformPlanEnv(t)
	group, _, err := rootCmd.Find([]string{"platform-plan"})
	require.NoError(t, err)
	leaf, _, err := rootCmd.Find([]string{"platform-plan", "get"})
	require.NoError(t, err)
	// Standard lazy help catalog initialization is permitted; its state must be reset immediately.
	for _, c := range []*cobra.Command{rootCmd, group, leaf} {
		require.Nil(t, c.Flags().Lookup("help"))
	}
	helpRequests := 0
	platformPlanHTTPClient = &http.Client{Transport: platformPlanTransport(func(*http.Request) (*http.Response, error) {
		helpRequests++
		return nil, errors.New("unexpected platform help request")
	})}
	for _, args := range [][]string{{"--help"}, {"platform-plan", "--help"}, {"platform-plan", "get", "--help"}} {
		var out, sinks string
		out, sinks, err = platformPlanRun(t, context.Background(), args...)
		require.Zero(t, helpRequests)
		require.NoError(t, err)
		assert.NotEmpty(t, out)
		assert.Empty(t, sinks)
		var c *cobra.Command
		c, _, err = rootCmd.Find(args[:len(args)-1])
		require.NoError(t, err)
		f := c.Flags().Lookup("help")
		require.NotNil(t, f)
		assert.Equal(t, "false", f.Value.String())
		assert.False(t, f.Changed)
	}
	h, _ := platformPlanResponse(t, 200, platformPlanFixture)
	client := platformPlanHTTPClient
	out, sinks, err := platformPlanGet(t, "json", context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, out)
	assert.Empty(t, sinks)
	assert.Len(t, h.paths, 1)
	assert.Same(t, client, platformPlanHTTPClient)
	for _, c := range []*cobra.Command{rootCmd, group, leaf} {
		assert.Nil(t, c.Context())
		f := c.Flags().Lookup("help")
		assert.Equal(t, "false", f.Value.String())
		assert.False(t, f.Changed)
	}
	assert.True(t, reflect.ValueOf(rootCmd).Elem().FieldByName("args").IsNil())
	assert.Equal(t, "table", leaf.Flags().Lookup("format").Value.String())
	assert.False(t, leaf.Flags().Lookup("format").Changed)
}

func TestPlatformPlanPresentHelpRestoration(t *testing.T) {
	platformPlanEnv(t)
	restore := platformPlanBoundary(t)
	defer restore()
	group, _, err := rootCmd.Find([]string{"platform-plan"})
	require.NoError(t, err)
	leaf, _, err := rootCmd.Find([]string{"platform-plan", "get"})
	require.NoError(t, err)
	for _, value := range []string{"false", "true"} {
		for _, changed := range []bool{false, true} {
			for _, c := range []*cobra.Command{rootCmd, group, leaf} {
				c.InitDefaultHelpFlag()
				f := c.Flags().Lookup("help")
				require.NoError(t, f.Value.Set(value))
				f.Changed = changed
			}
			h, _ := platformPlanResponse(t, 200, platformPlanFixture)
			out, sinks, err := platformPlanGet(t, "json", context.Background())
			require.NoError(t, err)
			assert.NotEmpty(t, out)
			assert.Empty(t, sinks)
			assert.Len(t, h.paths, 1)
			for _, c := range []*cobra.Command{rootCmd, group, leaf} {
				f := c.Flags().Lookup("help")
				assert.Equal(t, value, f.Value.String())
				assert.Equal(t, changed, f.Changed)
			}
		}
	}
}
