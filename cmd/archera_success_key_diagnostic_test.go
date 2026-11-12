package main

import (
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func successKeyDiagnosticState(t *testing.T) {
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

func successKeyDiagnosticLogs(t *testing.T) []*syncBuffer {
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

func successKeyDiagnosticWire(t *testing.T, candidate bool, field, value string) string {
	t.Helper()
	var doc map[string]any
	decoder := json.NewDecoder(strings.NewReader(archeraComparisonFixture()))
	decoder.UseNumber()
	require.True(t, decoder.Decode(&doc) == nil, "decode fixture without float rounding")
	row := doc["data"].([]any)[0].(map[string]any)
	entry := row["current"].(map[string]any)
	if candidate {
		entry = row["candidates"].([]any)[0].(map[string]any)
	}
	offer := entry["offer"].(map[string]any)
	offer["guaranteed_display_name"] = "ordinary display"
	offer["region"] = "ordinary-region"
	entry["commitment_type"] = "ordinary/type"
	switch field {
	case "guaranteed_display_name", "region":
		offer[field] = value
	case "commitment_type", "contract_term":
		entry[field] = value
	}
	wire, err := json.Marshal(doc)
	require.True(t, err == nil, "encode exact-number fixture")
	return string(wire)
}

func successKeyDiagnosticTransport(t *testing.T, status int, wire string) *hookTransport {
	t.Helper()
	transport := &hookTransport{handle: func(req *http.Request) (int, string) {
		assert.True(t, req.Method == http.MethodGet, "expected GET")
		assert.True(t, req.URL.Scheme == "https" && req.URL.Host == "api.archera.ai", "fixed HTTPS origin")
		assert.True(t, req.URL.Path == "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/comparison", "comparison path")
		assert.True(t, req.URL.RawQuery == "", "no query")
		assert.True(t, req.Header.Get("x-api-key") == archeraTestKey, "expected outgoing synthetic authentication header")
		for name, values := range req.Header {
			if !strings.EqualFold(name, "x-api-key") {
				assert.False(t, strings.Contains(strings.Join(values, ""), archeraTestKey), "credential outside auth header")
			}
		}
		return status, wire
	}}
	archeraHTTPClient = &http.Client{Transport: transport}
	return transport
}

func successKeyDiagnosticField(t *testing.T, entry insurance.OfferEntry, field string) string {
	t.Helper()
	switch field {
	case "region":
		require.NotNil(t, entry.Region)
		return *entry.Region
	case "commitment_type":
		return entry.CommitmentType
	default:
		require.NotNil(t, entry.GuaranteedDisplayName)
		return *entry.GuaranteedDisplayName
	}
}

func successKeyDiagnosticOutputField(t *testing.T, entry archeraOfferDTO, field string) string {
	t.Helper()
	switch field {
	case "region":
		require.NotNil(t, entry.Region)
		return *entry.Region
	case "commitment_type":
		return entry.CommitmentType
	default:
		require.NotNil(t, entry.ArcheraOfferName)
		return *entry.ArcheraOfferName
	}
}

func successKeyDiagnosticSinks(t *testing.T, format, out, stderr, parsed string, logs []*syncBuffer) {
	t.Helper()
	sinks := []struct{ name, value string }{
		{"public_" + format + "_stdout", out}, {"cobra_process_stderr", stderr},
		{"standard_log", logs[0].String()}, {"slog", logs[1].String()}, {"shared_logging", logs[2].String()},
	}
	if format == "json" {
		sinks = append(sinks, struct{ name, value string }{"parsed_remarshaled_json_view", parsed})
	}
	for _, sink := range sinks {
		retained := strings.Contains(sink.value, archeraTestKey)
		t.Logf("sink=%s retained_synthetic_key=%t", sink.name, retained)
		if retained {
			t.Errorf("credential absence assertion failed in %s", sink.name)
		}
	}
}

func TestArcheraSuccessKeyDiagnostic(t *testing.T) {
	successKeyDiagnosticState(t)
	for _, candidate := range []bool{false, true} {
		role := "current"
		if candidate {
			role = "candidate"
		}
		for _, field := range []string{"control", "guaranteed_display_name", "region", "commitment_type"} {
			for _, format := range []string{"table", "json"} {
				t.Run(role+"/"+field+"/"+format, func(t *testing.T) {
					logs := successKeyDiagnosticLogs(t)
					want := "ordinary display"
					if field != "control" {
						want = "prefix " + archeraTestKey + " suffix"
					}
					wire := successKeyDiagnosticWire(t, candidate, field, want)
					decoded, err := insurance.DecodeComparison(strings.NewReader(wire), archeraTestOrg, archeraTestPlan, time.Unix(0, 0))
					require.True(t, err == nil, "same payload accepted by actual pinned decoder")
					require.NotNil(t, decoded)
					require.True(t, len(decoded.Rows) == 2, "decoded row count")
					require.True(t, len(decoded.Rows[0].Candidates) == 2, "decoded candidate count")
					require.True(t, decoded.Rows[0].LineItemID == "70000000-0000-4000-8000-0000000000c1\x1b[31m\u0085", "raw fixture line ID preserved")
					require.True(t, decoded.Rows[0].Current.OfferID == "30000000-0000-4000-8000-000000000001\x1b[31m\u0085", "raw current offer ID preserved")
					require.True(t, decoded.Rows[0].Candidates[0].OfferID == "40000000-0000-4000-8000-000000000002", "raw candidate ID preserved")
					entry := decoded.Rows[0].Current
					if candidate {
						entry = decoded.Rows[0].Candidates[0]
					}
					require.True(t, successKeyDiagnosticField(t, entry, field) == want, "accepted selected field preserved exactly")
					require.NotNil(t, entry.SelectedAmount)
					require.True(t, entry.SelectedAmount.Cmp(big.NewRat(3, 1)) == 0, "selected_amount retained")
					require.True(t, decoded.Current.Monthly.Premium.FloatString(19) == "103.1234567890123456789", "exact fixture money retained")
					transport := successKeyDiagnosticTransport(t, http.StatusOK, wire)
					out, stderr, err := runArchera(t, fullEnv(), "--format", format)
					require.True(t, err == nil, "actual root command accepted successful response")
					require.True(t, out != "", "successful output exists")
					require.False(t, t.Failed(), "request prerequisites must pass before sink inspection")
					require.True(t, transport.count("/comparison") == 1, "exactly one direct intercepted GET")
					parsed := ""
					if format == "json" {
						var dto archeraComparisonDTO
						decoder := json.NewDecoder(strings.NewReader(out))
						require.True(t, decoder.Decode(&dto) == nil, "one public JSON document")
						var trailing any
						require.True(t, decoder.Decode(&trailing) == io.EOF, "no second JSON document")
						require.True(t, len(dto.Rows) == 2, "public JSON row count")
						require.True(t, len(dto.Rows[0].Candidates) == 2, "public JSON candidate count")
						require.True(t, dto.Rows[0].LineItemID == "70000000-0000-4000-8000-0000000000c1[31m", "literal cleaned line identity")
						require.True(t, dto.Rows[0].Current.OfferID == "30000000-0000-4000-8000-000000000001[31m", "literal cleaned current offer identity")
						require.True(t, dto.Rows[0].Candidates[0].OfferID == "40000000-0000-4000-8000-000000000002", "literal candidate identity")
						outputEntry := dto.Rows[0].Current
						if candidate {
							outputEntry = dto.Rows[0].Candidates[0]
						}
						require.True(t, successKeyDiagnosticOutputField(t, outputEntry, field) == want, "exact selected public JSON field")
						require.True(t, *dto.Current.Monthly.Premium == "103.1234567890123456789", "exact public premium")
						require.True(t, *dto.Current.Monthly.CommitmentCostTotal == "101" && *dto.Current.UpfrontCost == "107", "public monthly and upfront values")
						require.True(t, *dto.Rows[0].Current.Totals.Monthly.CommitmentCostTotal == "301" && *dto.Rows[0].Current.Totals.UpfrontCost == "307", "current offer amounts")
						require.True(t, *dto.Rows[0].Candidates[0].Totals.Monthly.CommitmentCostTotal == "401" && *dto.Rows[0].Candidates[0].Totals.UpfrontCost == "407", "candidate offer amounts")
						require.Nil(t, dto.Currency)
						require.Nil(t, dto.Rows[0].Current.ContractTerm)
						require.Nil(t, dto.Rows[0].Candidates[1].Totals.Monthly.NetSavings)
						require.True(t, dto.NonGatingDisclosure != "" && dto.SponsorshipDisclosure != "", "both disclosures present")
						encoded, encodeErr := json.Marshal(dto)
						require.True(t, encodeErr == nil, "parsed JSON re-encoded")
						parsed = string(encoded)
					} else {
						for _, literal := range []string{want, "70000000-0000-4000-8000-0000000000c1[31m", "30000000-0000-4000-8000-000000000001[31m", "40000000-0000-4000-8000-000000000002", "103.1234567890123456789", "unknown", "107", "301", "307", "401", "407"} {
							require.True(t, strings.Contains(out, literal), "expected selected table value/identity/money/control visible")
						}
						require.True(t, strings.Contains(out, common.ArcheraNonGatingDisclosure) && strings.Contains(out, common.ArcheraSponsorshipDisclosure), "both table disclosures")
					}
					t.Log("prerequisites accepted_decode=true selected_amount_exact=true money_exact=true root_dispatch_success=true selected_field_exact=true identity_oracle=true")
					successKeyDiagnosticSinks(t, format, out, stderr, parsed, logs)
				})
			}
		}
	}
	for _, kind := range []string{"missing_key", "malformed_term", "http401"} {
		for _, format := range []string{"table", "json"} {
			t.Run("negative/"+kind+"/"+format, func(t *testing.T) {
				logs := successKeyDiagnosticLogs(t)
				wire := successKeyDiagnosticWire(t, false, "control", "ordinary display")
				env := fullEnv()
				status := http.StatusOK
				if kind == "missing_key" {
					delete(env, archeraEnvAPIKey)
				}
				if kind == "malformed_term" {
					wire = successKeyDiagnosticWire(t, false, "contract_term", "prefix "+archeraTestKey+" suffix")
					decoded, err := insurance.DecodeComparison(strings.NewReader(wire), archeraTestOrg, archeraTestPlan, time.Unix(0, 0))
					require.True(t, err != nil && decoded == nil, "malformed enum rejected before public dispatch")
				}
				if kind == "http401" {
					status = http.StatusUnauthorized
					wire = archeraTestKey
				}
				transport := successKeyDiagnosticTransport(t, status, wire)
				out, stderr, err := runArchera(t, env, "--format", format)
				require.True(t, err != nil, "negative control must return error")
				require.True(t, out == "", "negative control has no public rows")
				require.False(t, t.Failed(), "negative request prerequisites")
				calls := 1
				if kind == "missing_key" {
					calls = 0
				}
				require.True(t, transport.count("/comparison") == calls, "negative control call count/no retry")
				require.False(t, strings.Contains(err.Error(), archeraTestKey), "returned error credential absent")
				successKeyDiagnosticSinks(t, format, out, stderr, "", logs)
			})
		}
	}
}
