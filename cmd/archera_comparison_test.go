package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	archeraTestKey  = "test-key-SYNTHETIC-0000"
	archeraTestOrg  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	archeraTestPlan = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// Synthetic fixtures shaped like the documented commitment-plans schema.
const archeraFinancialsFixture = `{
	"commitment_cost": {"total": 110, "breakdown": {"cloud_provider_cost": {"total": 100}, "archera_premium": 10.125}},
	"commitment_savings": {"net": null, "gross": 4.5},
	"covered_ondemand_cost": 104.5
}`

func archeraOfferFixture(isCurrent bool, offerType, lease, term, payment, name string) string {
	return fmt.Sprintf(`{
		"is_current": %t,
		"offer_id": "11111111-1111-4111-8111-111111111111",
		"offer_org_id": "public",
		"offer": {"provider": "aws", "type": %q, "region": "us-east-1", "guaranteed_display_name": %s},
		"lease_menu_item_id": %s,
		"selected_amount": 3,
		"commitment_type": %q,
		"contract_term": %s,
		"payment_option": %s,
		"discount_rate": 0.3,
		"breakeven_days": null,
		"commitment_upfront_cost": 1200,
		"commitment_financials_monthly_rate": %s,
		"delta_vs_current": {"monthly_net_savings": 0, "upfront_cost": 0, "discount_rate": 0, "breakeven_days": null}
	}`, isCurrent, offerType, name, lease, offerType, term, payment, archeraFinancialsFixture)
}

func archeraComparisonFixture() string {
	return `{
		"current_totals": {"commitment_financials_monthly_rate": ` + archeraFinancialsFixture + `, "commitment_upfront_cost": 1200},
		"hypothetical_totals": [{
			"contract_term": null,
			"payment_option": "no_upfront",
			"commitment_financials_monthly_rate": {},
			"commitment_upfront_cost": 0,
			"delta_vs_current": {"monthly_net_savings": 7.25, "monthly_commitment_cost": -1, "upfront_cost": -1200},
			"line_items": [{
				"line_item_id": "22222222-2222-4222-8222-222222222222",
				"actual_term": "one_year_gris", "actual_payment_option": "no_upfront",
				"actual_commitment_type": "aws/AmazonEC2",
				"actual_term_reason": "fallback_closest_shorter"
			}]
		}],
		"data": [{
			"line_item_id": "22222222-2222-4222-8222-222222222222",
			"current": ` + archeraOfferFixture(true, "aws/AmazonEC2", "null", "null", "null", "null") + `,
			"candidates": [` +
		archeraOfferFixture(false, "aws/AmazonEC2", `"33333333-3333-4333-8333-333333333333"`, `"one_year_gris"`, `"no_upfront"`, `"Offer\u001b[31m RED"`) + `,` +
		archeraOfferFixture(false, "aws/SomethingNew", "null", `"one_year_gris"`, `"no_upfront"`, "null") + `]
		}]
	}`
}

type archeraRecorder struct {
	mu       sync.Mutex
	requests []*http.Request
	hits     map[string]int
}

// archeraTestTransport asserts the production origin before rewriting the
// request to the httptest server, so only the fixed origin is ever exercised.
type archeraTestTransport struct {
	t      *testing.T
	target *url.URL
	rec    *archeraRecorder
}

func (a *archeraTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	require.Equal(a.t, "https", r.URL.Scheme)
	require.Equal(a.t, "api.archera.ai", r.URL.Host)
	a.rec.mu.Lock()
	a.rec.requests = append(a.rec.requests, r.Clone(context.Background()))
	a.rec.mu.Unlock()
	rewritten := r.Clone(r.Context())
	rewritten.URL.Scheme, rewritten.URL.Host = a.target.Scheme, a.target.Host
	rewritten.Host = a.target.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

func setupArchera(t *testing.T, handler http.HandlerFunc) *archeraRecorder {
	t.Helper()
	rec := &archeraRecorder{hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.hits[r.URL.Path]++
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	archeraHTTPClient = &http.Client{Transport: &archeraTestTransport{t: t, target: target, rec: rec}}
	t.Cleanup(func() { archeraHTTPClient = nil })
	return rec
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(archeraComparisonFixture()))
}

// resetHelpFlags clears the sticky --help flag cobra leaves set after a help run.
func resetHelpFlags() {
	for _, c := range []*cobra.Command{rootCmd, archeraComparisonCmd, configureGCPCmd} {
		if f := c.Flags().Lookup("help"); f != nil {
			_ = f.Value.Set("false")
			f.Changed = false
		}
	}
}

func runArchera(t *testing.T, env map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	for _, k := range []string{archeraEnvAPIKey, archeraEnvOrgID, archeraEnvPlanID} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	resetHelpFlags()
	archeraOpts.OrgID, archeraOpts.PlanID, archeraOpts.Format = "", "", "table"
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(append([]string{"archera-comparison"}, args...))
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	err = rootCmd.Execute()
	return out.String(), errb.String(), err
}

func fullEnv() map[string]string {
	return map[string]string{archeraEnvAPIKey: archeraTestKey, archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}
}

func TestArcheraComparisonDefaultOffMakesNoRequest(t *testing.T) {
	rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected Archera request") })
	for _, args := range [][]string{{"--help"}} {
		_, _, err := runArchera(t, fullEnv(), args...)
		require.NoError(t, err)
	}
	rootCmd.SetArgs([]string{"configure-gcp", "--help"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	require.NoError(t, rootCmd.Execute())
	resetHelpFlags()
	assert.Empty(t, rec.requests)
}

func TestArcheraComparisonIncompleteConfig(t *testing.T) {
	rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	cases := []struct {
		name    string
		env     map[string]string
		wantMsg string
	}{
		{"no key", map[string]string{archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}, archeraEnvAPIKey},
		{"no org", map[string]string{archeraEnvAPIKey: archeraTestKey, archeraEnvPlanID: archeraTestPlan}, "org ID"},
		{"no plan", map[string]string{archeraEnvAPIKey: archeraTestKey, archeraEnvOrgID: archeraTestOrg}, "plan ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := runArchera(t, tc.env)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
		})
	}
	assert.Empty(t, rec.requests)
}

func TestArcheraComparisonBadFormat(t *testing.T) {
	setupArchera(t, okHandler)
	_, _, err := runArchera(t, fullEnv(), "--format", "xml")
	require.ErrorContains(t, err, "--format")
}

func TestArcheraComparisonTable(t *testing.T) {
	rec := setupArchera(t, okHandler)
	out, errOut, err := runArchera(t, fullEnv())
	require.NoError(t, err)

	require.Len(t, rec.requests, 1)
	req := rec.requests[0]
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/comparison", req.URL.Path)
	assert.Empty(t, req.URL.RawQuery)
	assert.Equal(t, archeraTestKey, req.Header.Get("x-api-key"))

	assert.Contains(t, out, archeraTitle)
	assert.NotContains(t, strings.ToLower(out), "insured quote")
	assert.Contains(t, out, "premium 10.125", "premium kept exact")
	assert.Contains(t, out, "net savings unknown", "null money is unknown, not 0")
	assert.Contains(t, out, "730-hour monthly rate")
	assert.Contains(t, out, "one-time")
	assert.Contains(t, out, "The API reports no currency")
	assert.Contains(t, out, "reason fallback_closest_shorter")
	assert.Contains(t, out, "lease attached")
	assert.Contains(t, out, "Archera offer name: Offer[31m RED", "control characters stripped, text kept")
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, "Archera product support: supported (")
	assert.Contains(t, out, "Archera product support: unknown")
	assert.Contains(t, out, "discount rate 0.3 (0-1 basis)")
	assert.Contains(t, out, common.ArcheraNonGatingDisclosure)
	assert.Contains(t, out, common.ArcheraSponsorshipDisclosure)
	assert.NotContains(t, out, common.ArcheraSignupURL)
	assert.Contains(t, out, "not mapped onto CUDly recommendation rows")
	assert.NotContains(t, out+errOut, archeraTestKey)
}

func TestArcheraComparisonJSON(t *testing.T) {
	setupArchera(t, okHandler)
	out, errOut, err := runArchera(t, fullEnv(), "--format", "json")
	require.NoError(t, err)
	assert.Empty(t, errOut)
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	require.NoError(t, dec.Decode(&doc), "stdout must be exactly one JSON document")
	assert.False(t, dec.More())
	assert.Nil(t, doc["currency"])
	assert.Equal(t, true, doc["premium_included"])
	assert.Equal(t, common.ArcheraNonGatingDisclosure, doc["non_gating_disclosure"])
	assert.Equal(t, common.ArcheraSponsorshipDisclosure, doc["sponsorship_disclosure"])
	assert.NotContains(t, doc, "disclosures")
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, `\u001b`)
	assert.NotContains(t, out, archeraTestKey)

	cur := doc["current_totals"].(map[string]any)["monthly_rate_730h"].(map[string]any)
	assert.Equal(t, "10.125", cur["premium"])
	assert.Nil(t, cur["net_savings"])
	rows := doc["rows"].([]any)
	cand := rows[0].(map[string]any)["candidates"].([]any)
	support := cand[0].(map[string]any)["archera_product_support"].(map[string]any)
	assert.Equal(t, "supported", support["status"])
	assert.NotEmpty(t, support["source"])
	unknown := cand[1].(map[string]any)["archera_product_support"].(map[string]any)
	assert.Equal(t, "unknown", unknown["status"])
	assert.NotContains(t, unknown, "source")
}

func TestArcheraMoneyExact(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "7.25": "7.25", "10.125": "10.125", "1200": "1200", "0.00001": "0.00001"} {
		r, ok := new(big.Rat).SetString(in)
		require.True(t, ok)
		got := archeraMoney(r)
		require.NotNil(t, got)
		assert.Equal(t, want, *got, in)
	}
	assert.Nil(t, archeraMoney(nil))
}

func TestArcheraSanitize(t *testing.T) {
	got := archeraSanitize("a\x1bb\u009bc\nd")
	assert.Equal(t, "abcd", got)
	long := archeraSanitize(strings.Repeat("é", 300))
	assert.LessOrEqual(t, len(long), archeraMaxFieldSize)
}

func TestArcheraVendorErrorsAndRedaction(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		want   string
	}{
		{"401", 401, nil, `{"detail":"nope"}`, "HTTP 401"},
		{"403 echoing key", 403, nil, `{"detail":"bad key ` + archeraTestKey + `"}`, "HTTP 403"},
		{"429", 429, map[string]string{"Retry-After": "30"}, `{"detail":"slow down"}`, "retry after 30s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			out, errOut, err := runArchera(t, fullEnv())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
			assert.Len(t, rec.requests, 1, "no automatic retry")
		})
	}
	t.Run("invalid uuid", func(t *testing.T) {
		setupArchera(t, okHandler)
		env := fullEnv()
		env[archeraEnvOrgID] = "not-a-uuid"
		out, errOut, err := runArchera(t, env)
		require.Error(t, err)
		assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
		assert.NotContains(t, err.Error(), "not-a-uuid")
	})
}

func TestArcheraRedirectNotFollowed(t *testing.T) {
	rec := setupArchera(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/second") {
			t.Error("redirect target was requested")
			return
		}
		http.Redirect(w, r, "/second", http.StatusFound)
	})
	out, errOut, err := runArchera(t, fullEnv())
	require.Error(t, err)
	assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
	assert.Zero(t, rec.hits["/second"])
}

func TestArcheraNoKeyFlagAndStructsRedacted(t *testing.T) {
	for _, name := range []string{"api-key", "key", "token", "secret", "archera-api-key"} {
		assert.Nil(t, archeraComparisonCmd.Flags().Lookup(name), name)
	}
	check := func(label string) {
		comparison, err := insurance.DecodeComparison(strings.NewReader(archeraComparisonFixture()), archeraTestOrg, archeraTestPlan, time.Unix(0, 0))
		require.NoError(t, err)
		dto := buildArcheraDTO(comparison)
		for _, v := range []any{archeraOpts, &archeraOpts, dto, &dto} {
			for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
				assert.NotContains(t, fmt.Sprintf(f, v), archeraTestKey, label+" "+f)
			}
		}
	}
	setupArchera(t, okHandler)
	check("before")
	_, _, err := runArchera(t, fullEnv())
	require.NoError(t, err)
	check("after")
}
