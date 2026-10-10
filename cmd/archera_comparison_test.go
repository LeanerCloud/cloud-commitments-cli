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
	"os"
	"path/filepath"
	"regexp"
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

// archeraComparisonFixture is a synthetic document shaped like the documented
// commitment-plans schema. Every money field carries a distinct value: the
// hundreds digit is the block (1 current totals, 2 and 6 hypotheticals, 3
// current offer, 4 and 5 candidates) and the units digit is the field (1 cost
// total, 2 cloud cost, 3 premium, 4 gross, 5 net, 6 covered on-demand, 7
// upfront, 8 and 9 deltas), so a swapped field mapping changes the output. The
// current-totals premium is a long decimal that float64 cannot hold exactly.
func archeraComparisonFixture() string {
	b, err := os.ReadFile("testdata/archera_comparison.json")
	if err != nil {
		panic(err)
	}
	return string(b)
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

var archeraFetchedRE = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`)

// checkArcheraGolden compares output with testdata/<name> after masking the
// wall-clock fetch time. ARCHERA_UPDATE_GOLDEN=1 rewrites the file.
func checkArcheraGolden(t *testing.T, name, got string) {
	t.Helper()
	got = archeraFetchedRE.ReplaceAllString(got, "<fetched>")
	path := filepath.Join("testdata", name)
	if os.Getenv("ARCHERA_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
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

	checkArcheraGolden(t, "archera_table.golden", out)
	assert.Contains(t, out, "never a bindable insurance quote")
	assert.Contains(t, out, "103.1234567890123456789", "long decimal kept exact, not float64")
	assert.NotContains(t, strings.ToLower(out), "insured quote")
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, common.ArcheraNonGatingDisclosure)
	assert.Contains(t, out, common.ArcheraSponsorshipDisclosure)
	assert.NotContains(t, out, common.ArcheraSignupURL)
	assert.NotContains(t, out+errOut, archeraTestKey)
}

func TestArcheraComparisonJSON(t *testing.T) {
	setupArchera(t, okHandler)
	out, errOut, err := runArchera(t, fullEnv(), "--format", "json")
	require.NoError(t, err)
	assert.Empty(t, errOut)
	dec := json.NewDecoder(strings.NewReader(out))
	var doc map[string]any
	require.NoError(t, dec.Decode(&doc), "stdout must be exactly one JSON document")
	assert.False(t, dec.More())
	assert.NotContains(t, doc, "disclosures")
	assert.NotContains(t, out, archeraTestKey)
	checkArcheraGolden(t, "archera_json.golden", out)
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
		{"401", 401, nil, `{"message":"nope"}`, "HTTP 401"},
		{"403 echoing key", 403, nil, `{"message":"bad key ` + archeraTestKey + `"}`, "bad key [redacted]"},
		{"429", 429, map[string]string{"Retry-After": "30"}, `{"message":"slow down"}`, "retry after 30s"},
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
	usages := strings.ToLower(archeraComparisonCmd.Flags().FlagUsages())
	for _, bad := range []string{"key", "token", "secret", "password"} {
		assert.NotContains(t, usages, bad, "a flag looks like a credential flag")
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
