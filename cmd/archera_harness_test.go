package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var archeraHarnessEnv = []string{archeraEnvAPIKey, archeraEnvOrgID, archeraEnvPlanID}

type archeraCtxKey struct{}

type archeraFlagState struct {
	flag    *pflag.Flag
	value   string
	slice   []string
	changed bool
}

// snapshotArcheraCommandState records the shared command tree state a runner
// mutates (bound options, every flag value and Changed bit, the injected HTTP
// client, the Archera environment, cobra out/err) and restores it on cleanup.
// Cobra keeps the args slice private and the tree is always run with
// SetArgs, so args are reset to nil, the untouched default.
func snapshotArcheraCommandState(t *testing.T) {
	t.Helper()
	cmds := []*cobra.Command{rootCmd, archeraComparisonCmd, archeraLineItemsCmd, configureGCPCmd}
	var flags []archeraFlagState
	for _, c := range cmds {
		c.InitDefaultHelpFlag()
		visit := func(f *pflag.Flag) {
			st := archeraFlagState{flag: f, value: f.Value.String(), changed: f.Changed}
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				st.slice = slices.Clone(sv.GetSlice())
			}
			flags = append(flags, st)
		}
		c.Flags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
	}
	ctxs := make([]context.Context, len(cmds))
	for i, c := range cmds {
		ctxs[i] = c.Context()
	}
	opts, listing, client := archeraOpts, archeraListingOpts, archeraHTTPClient
	opts.LineItemIDs = slices.Clone(opts.LineItemIDs)
	opts.ContractTerms = slices.Clone(opts.ContractTerms)
	opts.PaymentOptions = slices.Clone(opts.PaymentOptions)
	type envVar struct {
		val string
		set bool
	}
	env := map[string]envVar{}
	for _, k := range archeraHarnessEnv {
		v, ok := os.LookupEnv(k)
		env[k] = envVar{v, ok}
	}
	t.Cleanup(func() {
		for _, st := range flags {
			if sv, ok := st.flag.Value.(pflag.SliceValue); ok {
				require.NoError(t, sv.Replace(st.slice))
			} else {
				require.NoError(t, st.flag.Value.Set(st.value))
			}
			st.flag.Changed = st.changed
		}
		for i, c := range cmds {
			c.SetContext(ctxs[i])
		}
		archeraOpts, archeraListingOpts, archeraHTTPClient = opts, listing, client
		for k, e := range env {
			if e.set {
				require.NoError(t, os.Setenv(k, e.val))
			} else {
				require.NoError(t, os.Unsetenv(k))
			}
		}
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
}

type archeraLogSinks struct{ std, slog, shared syncBuffer }

func (s *archeraLogSinks) all() string { return s.std.String() + s.slog.String() + s.shared.String() }

// captureArcheraLogs routes the standard, slog and shared logging packages to
// separate sinks and restores each one, including log flags and level.
func captureArcheraLogs(t *testing.T) *archeraLogSinks {
	t.Helper()
	s := &archeraLogSinks{}
	savedLog, savedFlags, savedSlog, savedLevel := log.Writer(), log.Flags(), slog.Default(), logging.GetLevel()
	slog.SetDefault(slog.New(slog.NewTextHandler(&s.slog, nil)))
	log.SetOutput(&s.std) // after slog.SetDefault, which re-routes the log package
	savedShared := logging.SetOutput(&s.shared)
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() {
		logging.SetOutput(savedShared)
		logging.SetLevelValue(savedLevel)
		slog.SetDefault(savedSlog)
		log.SetOutput(savedLog)
		log.SetFlags(savedFlags)
	})
	return s
}

func TestArcheraLogCaptureReachesEverySinkAndRestores(t *testing.T) {
	var stdSeed, slogSeed, sharedSeed syncBuffer
	savedLog, savedFlags, savedSlog := log.Writer(), log.Flags(), slog.Default()
	savedShared, savedLevel := logging.SetOutput(&sharedSeed), logging.GetLevel()
	t.Cleanup(func() {
		logging.SetOutput(savedShared)
		logging.SetLevelValue(savedLevel)
		slog.SetDefault(savedSlog)
		log.SetOutput(savedLog)
		log.SetFlags(savedFlags)
	})
	seededSlog := slog.New(slog.NewTextHandler(&slogSeed, nil))
	slog.SetDefault(seededSlog)
	log.SetOutput(&stdSeed)
	wantFlags := log.Ldate | log.Lshortfile
	log.SetFlags(wantFlags)
	logging.SetLevelValue(logging.LevelWarn)
	t.Run("capture", func(t *testing.T) {
		s := captureArcheraLogs(t)
		log.Print("std-marker")
		slog.Info("slog-marker")
		logging.Info("shared-marker")
		assert.Contains(t, s.std.String(), "std-marker")
		assert.Contains(t, s.slog.String(), "slog-marker")
		assert.Contains(t, s.shared.String(), "shared-marker")
	})
	assert.Same(t, seededSlog, slog.Default())
	assert.Same(t, &stdSeed, log.Writer())
	assert.Equal(t, wantFlags, log.Flags())
	assert.Same(t, &sharedSeed, logging.SetOutput(&sharedSeed))
	assert.Equal(t, logging.LevelWarn, logging.GetLevel())
	assert.Empty(t, stdSeed.String()+slogSeed.String()+sharedSeed.String())
}

func TestArcheraRunRestoresSeededState(t *testing.T) {
	t.Setenv(archeraEnvOrgID, "seed-org")
	os.Unsetenv(archeraEnvPlanID)
	archeraOpts.OrgID, archeraOpts.Format = "seed-org-flag", "json"
	archeraOpts.LineItemIDs = []string{"seed-line"}
	archeraListingOpts.Limit = 7
	archeraListingOpts.Offset = 3
	f := archeraComparisonCmd.Flags().Lookup("org-id")
	require.NoError(t, f.Value.Set("seed-org-flag"))
	f.Changed = true
	seeded := &http.Client{}
	archeraHTTPClient = seeded
	wantOpts, wantListing := archeraOpts, archeraListingOpts
	wantOpts.LineItemIDs = slices.Clone(wantOpts.LineItemIDs)
	t.Cleanup(func() { archeraOpts.OrgID, archeraOpts.Format, archeraOpts.LineItemIDs = "", "table", nil })
	t.Cleanup(func() { archeraListingOpts.Limit, archeraListingOpts.Offset = 100, 0 })
	t.Cleanup(func() { archeraHTTPClient = nil; f.Changed = false; _ = f.Value.Set("") })
	lineCtx := context.WithValue(context.Background(), archeraCtxKey{}, "seed")
	prevCtx := archeraLineItemsCmd.Context()
	archeraLineItemsCmd.SetContext(lineCtx)
	t.Cleanup(func() { archeraLineItemsCmd.SetContext(prevCtx) })
	t.Run("run", func(t *testing.T) {
		rec := setupArchera(t, okHandler)
		_, _, err := runArchera(t, fullEnv(), "--org-id", archeraTestOrg, "--line-item-ids", "90000000-0000-4000-8000-000000000099")
		require.NoError(t, err)
		require.NotEmpty(t, rec.requests)
	})
	assert.Same(t, seeded, archeraHTTPClient)
	assert.Equal(t, lineCtx, archeraLineItemsCmd.Context())
	assert.Equal(t, wantOpts, archeraOpts)
	assert.Equal(t, wantListing, archeraListingOpts)
	assert.True(t, f.Changed)
	assert.Equal(t, "seed-org-flag", f.Value.String())
	assert.Equal(t, "seed-org", os.Getenv(archeraEnvOrgID))
	_, planSet := os.LookupEnv(archeraEnvPlanID)
	assert.False(t, planSet)
	assert.Empty(t, os.Getenv(archeraEnvAPIKey))
}

func assertArcheraLogSinksReceiveMarkers(t *testing.T, s *archeraLogSinks) {
	t.Helper()
	log.Print("archera-fixture-std")
	slog.Info("archera-fixture-slog")
	logging.Info("archera-fixture-shared")
	assert.Contains(t, s.std.String(), "archera-fixture-std")
	assert.Contains(t, s.slog.String(), "archera-fixture-slog")
	assert.Contains(t, s.shared.String(), "archera-fixture-shared")
}
