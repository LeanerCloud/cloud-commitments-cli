package main

import (
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func successKeyState(t *testing.T) {
	t.Helper()
	savedClient, savedOpts := archeraHTTPClient, archeraOpts
	rootCmd.InitDefaultHelpFlag()
	archeraComparisonCmd.InitDefaultHelpFlag()
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

func successKeyLogs(t *testing.T) []*syncBuffer {
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

func successKeyWire(t *testing.T, path, value string) string {
	t.Helper()
	var doc any
	decoder := json.NewDecoder(strings.NewReader(archeraComparisonFixture()))
	decoder.UseNumber()
	require.True(t, decoder.Decode(&doc) == nil, "exact-number fixture")
	if path != "" {
		parts := strings.Split(path, "/")
		parent := doc
		for _, part := range parts[:len(parts)-1] {
			if array, ok := parent.([]any); ok {
				index, err := strconv.Atoi(part)
				require.NoError(t, err)
				parent = array[index]
			} else {
				parent = parent.(map[string]any)[part]
			}
		}
		parent.(map[string]any)[parts[len(parts)-1]] = value
	}
	wire, err := json.Marshal(doc)
	require.True(t, err == nil, "fixture marshal")
	return string(wire)
}

func successKeyTransport(t *testing.T, key string, status int, wire string) *hookTransport {
	t.Helper()
	transport := &hookTransport{handle: func(req *http.Request) (int, string) {
		assert.True(t, req.Method == http.MethodGet, "GET")
		assert.True(t, req.URL.Scheme == "https" && req.URL.Host == "api.archera.ai", "fixed origin")
		assert.True(t, req.URL.Path == "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/comparison", "exact path")
		assert.True(t, req.URL.RawQuery == "", "no query")
		assert.True(t, req.Header.Get("x-api-key") == key, "expected synthetic authentication")
		for name, values := range req.Header {
			if !strings.EqualFold(name, "x-api-key") {
				assert.False(t, strings.Contains(strings.Join(values, ""), key), "no credential outside authentication")
			}
		}
		return status, wire
	}}
	archeraHTTPClient = &http.Client{Transport: transport}
	return transport
}

func TestArcheraSuccessWithholdsCredentials(t *testing.T) {
	successKeyState(t)
	cases := []struct{ name, path, value, key string }{{"benign", "", "", archeraTestKey}}
	for _, role := range []string{"current", "candidates/0"} {
		for _, field := range []string{"offer/guaranteed_display_name", "offer/region", "commitment_type"} {
			cases = append(cases, struct{ name, path, value, key string }{role + "/" + field, "data/0/" + role + "/" + field, "prefix " + archeraTestKey + " suffix", archeraTestKey})
		}
	}
	for _, split := range []struct{ name, separator string }{
		{"control", "\x01"}, {"format", "\u200b"}, {"combining", "\u0301"}, {"space", " "},
		{"filler", "\u115f"}, {"replacement", "\ufffd"},
	} {
		cases = append(cases, struct{ name, path, value, key string }{split.name, "data/0/current/offer/guaranteed_display_name", archeraSplitEverywhere(archeraTestKey, split.separator), archeraTestKey})
	}
	for _, field := range []string{"offer/guaranteed_display_name", "commitment_type"} {
		cases = append(cases, struct{ name, path, value, key string }{"cap/" + field, "data/0/current/" + field, strings.Repeat("p", 250) + archeraTestKey, archeraTestKey})
	}
	uuid := "90000000-0000-4000-8000-000000000099"
	cases = append(cases, []struct{ name, path, value, key string }{
		{"hypothetical/type", "hypothetical_totals/0/line_items/0/actual_commitment_type", archeraTestKey, archeraTestKey},
		{"uuid/row", "data/0/line_item_id", uuid, uuid},
		{"uuid/current", "data/0/current/offer_id", uuid, uuid},
		{"uuid/candidate", "data/0/candidates/0/offer_id", uuid, uuid},
		{"equal/plan", "", "", archeraTestPlan},
		{"equal/row", "", "", "71000000-0000-4000-8000-0000000000c2"},
		{"equal/offer", "", "", "40000000-0000-4000-8000-000000000002"},
		{"equal/enum", "", "", "no_upfront"},
		{"equal/decimal", "", "", "103.1234567890123456789"},
		{"short/numeric", "", "", "1"},
	}...)
	for _, tc := range cases {
		for _, format := range []string{"table", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				logs := successKeyLogs(t)
				wire := successKeyWire(t, tc.path, tc.value)
				decoded, err := insurance.DecodeComparison(strings.NewReader(wire), archeraTestOrg, archeraTestPlan, time.Unix(0, 0))
				require.True(t, err == nil && decoded != nil, "accepted decoder prerequisite")
				require.True(t, decoded.Rows[0].Current.SelectedAmount.Cmp(big.NewRat(3, 1)) == 0, "selected amount exact")
				require.True(t, decoded.Current.Monthly.Premium.FloatString(19) == "103.1234567890123456789", "premium exact")
				if strings.HasPrefix(tc.path, "data/0/current/") || strings.HasPrefix(tc.path, "data/0/candidates/0/") {
					entry := decoded.Rows[0].Current
					if strings.Contains(tc.path, "candidates/0/") {
						entry = decoded.Rows[0].Candidates[0]
					}
					var selected string
					switch {
					case strings.HasSuffix(tc.path, "/guaranteed_display_name"):
						selected = *entry.GuaranteedDisplayName
					case strings.HasSuffix(tc.path, "/region"):
						selected = *entry.Region
					case strings.HasSuffix(tc.path, "/commitment_type"):
						selected = entry.CommitmentType
					case strings.HasSuffix(tc.path, "/offer_id"):
						selected = entry.OfferID
					}
					require.True(t, selected == tc.value, "selected raw decoded field exact")
					require.True(t, entry.SelectedAmount.Cmp(big.NewRat(3, 1)) == 0, "selected role amount exact")
				}
				transport := successKeyTransport(t, tc.key, http.StatusOK, wire)
				env := fullEnv()
				env[archeraEnvAPIKey] = tc.key
				out, stderr, err := runArchera(t, env, "--format", format)
				require.False(t, t.Failed(), "transport prerequisites")
				require.True(t, transport.count("/comparison") == 1, "one intercepted GET")
				if tc.name == "benign" {
					require.True(t, err == nil, "ordinary comparison accepted")
					checkArcheraGolden(t, "archera_"+format+".golden", out)
				} else {
					if err == nil {
						if format == "json" {
							var dto archeraComparisonDTO
							require.True(t, json.Unmarshal([]byte(out), &dto) == nil, "parent public JSON prerequisite")
							if strings.HasPrefix(tc.path, "data/0/current/") || strings.HasPrefix(tc.path, "data/0/candidates/0/") {
								entry := dto.Rows[0].Current
								if strings.Contains(tc.path, "candidates/0/") {
									entry = dto.Rows[0].Candidates[0]
								}
								selected := entry.CommitmentType
								switch {
								case strings.HasSuffix(tc.path, "/guaranteed_display_name"):
									selected = *entry.ArcheraOfferName
								case strings.HasSuffix(tc.path, "/region"):
									selected = *entry.Region
								case strings.HasSuffix(tc.path, "/offer_id"):
									selected = entry.OfferID
								}
								want := tc.value
								if tc.name == "control" || tc.name == "format" {
									want = archeraTestKey
								}
								if strings.HasPrefix(tc.name, "cap/") {
									want = strings.Repeat("p", 250) + "test-k"
								}
								require.True(t, selected == want, "parent selected JSON field exact")
							}
						}
						require.True(t, out != "", "parent rendered successful comparison")
						if tc.value != "" {
							want := tc.value
							if tc.name == "control" || tc.name == "format" {
								want = archeraTestKey
							}
							if strings.HasPrefix(tc.name, "cap/") {
								want = strings.Repeat("p", 250) + "test-k"
							}
							require.True(t, strings.Contains(out, want), "parent selected output prerequisite")
						}
					}
					assert.True(t, err != nil, "credential-bearing success must be withheld")
					assert.True(t, out == "", "whole financial output must be withheld")
				}
				if len(tc.key) > 1 {
					errText := ""
					if err != nil {
						errText = err.Error()
					}
					for _, sink := range []string{out, stderr, errText, logs[0].String(), logs[1].String(), logs[2].String()} {
						assert.False(t, strings.Contains(sink, tc.key), "synthetic credential absent from public/log sinks")
					}
				}
			})
		}
	}
}

func TestArcheraSuccessNegativeControls(t *testing.T) {
	successKeyState(t)
	for _, kind := range []string{"missing_key", "malformed_term", "http401"} {
		for _, format := range []string{"table", "json"} {
			t.Run(kind+"/"+format, func(t *testing.T) {
				logs := successKeyLogs(t)
				wire, status := successKeyWire(t, "", ""), http.StatusOK
				env := fullEnv()
				calls := 1
				if kind == "missing_key" {
					delete(env, archeraEnvAPIKey)
					calls = 0
				}
				if kind == "malformed_term" {
					wire = successKeyWire(t, "data/0/current/contract_term", archeraTestKey)
					decoded, err := insurance.DecodeComparison(strings.NewReader(wire), archeraTestOrg, archeraTestPlan, time.Unix(0, 0))
					require.True(t, err != nil && decoded == nil, "malformed enum direct rejection")
				}
				if kind == "http401" {
					wire, status = archeraTestKey, http.StatusUnauthorized
				}
				transport := successKeyTransport(t, archeraTestKey, status, wire)
				out, stderr, err := runArchera(t, env, "--format", format)
				require.True(t, err != nil, "negative control rejected")
				require.False(t, t.Failed(), "negative transport prerequisites")
				assert.True(t, out == "", "negative control has no financial output")
				assert.True(t, transport.count("/comparison") == calls, "exact call count without retry")
				for _, sink := range []string{out, stderr, err.Error(), logs[0].String(), logs[1].String(), logs[2].String()} {
					assert.False(t, strings.Contains(sink, archeraTestKey), "negative sink credential absent")
				}
			})
		}
	}
}

type successKeyWriter struct{ err error }

func (w successKeyWriter) Write([]byte) (int, error) { return 0, w.err }

func TestArcheraRendererWriterErrorIdentity(t *testing.T) {
	want := errors.New("synthetic unrelated writer failure")
	for _, render := range []func(successKeyWriter) error{
		func(w successKeyWriter) error { return renderArcheraJSON(w, archeraComparisonDTO{}) },
		func(w successKeyWriter) error { return renderArcheraTable(w, archeraComparisonDTO{}) },
	} {
		require.True(t, render(successKeyWriter{want}) == want, "renderer preserves error identity")
	}
}
