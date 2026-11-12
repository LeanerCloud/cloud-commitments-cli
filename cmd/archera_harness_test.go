package main

import (
	"log"
	"log/slog"
	"net/http"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snapshotArcheraTestState(t *testing.T) {
	t.Helper()
	savedClient, savedOpts := archeraHTTPClient, archeraOpts
	rootCmd.InitDefaultHelpFlag()
	archeraComparisonCmd.InitDefaultHelpFlag()
	configureGCPCmd.InitDefaultHelpFlag()
	restore := []func(){}
	for _, cmd := range []*cobra.Command{rootCmd, archeraComparisonCmd, configureGCPCmd} {
		for _, name := range []string{"org-id", "plan-id", "format", "help", "line-item-ids", "contract-terms", "payment-options"} {
			flag := cmd.Flags().Lookup(name)
			if flag == nil {
				continue
			}
			changed, value := flag.Changed, flag.Value.String()
			restore = append(restore, func() {
				if name == "org-id" || name == "plan-id" || name == "format" || name == "help" {
					require.True(t, flag.Value.Set(value) == nil, "restore flag value")
				}
				flag.Changed = changed
			})
		}
	}
	t.Cleanup(func() {
		for _, reset := range restore {
			reset()
		}
		archeraOpts, archeraHTTPClient = savedOpts, savedClient
	})
}

func captureArcheraTestLogs(t *testing.T) []*syncBuffer {
	t.Helper()
	sinks := []*syncBuffer{{}, {}, {}}
	savedLog, savedFlags, savedSlog := log.Writer(), log.Flags(), slog.Default()
	savedLevel := logging.GetLevel()
	slog.SetDefault(slog.New(slog.NewTextHandler(sinks[1], nil)))
	log.SetOutput(sinks[0])
	savedShared := logging.SetOutput(sinks[2])
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() {
		slog.SetDefault(savedSlog)
		log.SetOutput(savedLog)
		log.SetFlags(savedFlags)
		logging.SetOutput(savedShared)
		logging.SetLevelValue(savedLevel)
	})
	return sinks
}

func TestArcheraRunRestoresSeededState(t *testing.T) {
	snapshotArcheraTestState(t)
	archeraOpts.OrgID, archeraOpts.PlanID, archeraOpts.Format = archeraTestOrg, archeraTestPlan, "json"
	archeraOpts.LineItemIDs = []string{"90000000-0000-4000-8000-000000000099"}
	archeraOpts.ContractTerms = []string{"one_year_gris"}
	archeraOpts.PaymentOptions = []string{"all_upfront"}
	wantOpts := archeraOpts
	flags := []struct {
		cmd     *cobra.Command
		name    string
		value   string
		changed bool
	}{}
	for _, cmd := range []*cobra.Command{rootCmd, archeraComparisonCmd, configureGCPCmd} {
		cmd.InitDefaultHelpFlag()
		for i, name := range []string{"help", "org-id", "plan-id", "format", "line-item-ids", "contract-terms", "payment-options"} {
			flag := cmd.Flags().Lookup(name)
			if flag == nil {
				continue
			}
			if name == "help" {
				require.NoError(t, flag.Value.Set("true"))
			}
			flag.Changed = i%2 == 0
			flags = append(flags, struct {
				cmd     *cobra.Command
				name    string
				value   string
				changed bool
			}{cmd, name, flag.Value.String(), flag.Changed})
		}
	}
	transport := &hookTransport{handle: func(*http.Request) (int, string) {
		return http.StatusOK, archeraComparisonFixture()
	}}
	seededClient := &http.Client{Transport: transport}
	archeraHTTPClient = seededClient
	t.Run("real root dispatch", func(t *testing.T) {
		out, _, err := runArchera(t, fullEnv())
		require.NoError(t, err)
		require.NotEmpty(t, out)
	})
	assert.Equal(t, 1, transport.count("/comparison"))
	assert.Equal(t, wantOpts, archeraOpts, "restore all prior bound options including slices")
	assert.Same(t, seededClient, archeraHTTPClient)
	for _, want := range flags {
		flag := want.cmd.Flags().Lookup(want.name)
		assert.Equal(t, want.value, flag.Value.String(), want.name)
		assert.Equal(t, want.changed, flag.Changed, want.name)
	}
}

func TestArcheraLogCaptureRestoresSeededLoggers(t *testing.T) {
	var stdSeed, slogSeed, sharedSeed syncBuffer
	savedLog, savedFlags, savedSlog := log.Writer(), log.Flags(), slog.Default()
	savedShared, savedLevel := logging.SetOutput(&sharedSeed), logging.GetLevel()
	t.Cleanup(func() {
		slog.SetDefault(savedSlog)
		log.SetOutput(savedLog)
		log.SetFlags(savedFlags)
		logging.SetOutput(savedShared)
		logging.SetLevelValue(savedLevel)
	})
	seededSlog := slog.New(slog.NewTextHandler(&slogSeed, nil))
	slog.SetDefault(seededSlog)
	log.SetOutput(&stdSeed)
	wantFlags := log.Ldate | log.Ltime | log.Lshortfile
	log.SetFlags(wantFlags)
	logging.SetLevelValue(logging.LevelWarn)
	t.Run("capture three actual logger APIs", func(t *testing.T) {
		sinks := captureArcheraTestLogs(t)
		log.Print("child standard marker")
		slog.Info("child slog marker")
		logging.Info("child shared marker")
		assert.Contains(t, sinks[0].String(), "child standard marker")
		assert.Contains(t, sinks[1].String(), "child slog marker")
		assert.Contains(t, sinks[2].String(), "child shared marker")
	})
	assert.Same(t, seededSlog, slog.Default())
	assert.Same(t, &stdSeed, log.Writer())
	assert.Equal(t, wantFlags, log.Flags(), "nonzero flags restored after custom slog handler")
	assert.Same(t, &sharedSeed, logging.SetOutput(&sharedSeed))
	assert.Equal(t, logging.LevelWarn, logging.GetLevel())
}
