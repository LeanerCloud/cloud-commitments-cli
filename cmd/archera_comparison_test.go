package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	"unicode"

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
	archeraOpts.LineItemIDs, archeraOpts.ContractTerms, archeraOpts.PaymentOptions = nil, nil, nil
	for _, name := range []string{"org-id", "plan-id", "format", "line-item-ids", "contract-terms", "payment-options"} {
		archeraComparisonCmd.Flags().Lookup(name).Changed = false
	}
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(append([]string{"archera-comparison"}, args...))
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	realOut, realErr := captureRealStd(t, func() { err = rootCmd.Execute() })
	// Anything the command wrote to the real process streams is returned with
	// the cobra stderr so every caller's key grep covers it.
	return out.String(), errb.String() + realOut + realErr, err
}

// captureRealStd swaps os.Stdout and os.Stderr for pipes while fn runs, so a
// write that bypasses cobra's buffers is still observed.
func captureRealStd(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	savedOut, savedErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
		fn()
	}()
	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())
	wg.Wait()
	return outBuf.String(), errBuf.String()
}

// The test binary runs in a non-UTC zone from the start, so a fetch time
// printed in local time but labeled Z is visible. Set here, before any
// goroutine exists, because time.Local is read concurrently by net/http.
func init() { time.Local = time.FixedZone("archera-test", 5*3600+1800) }

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
		{"no key", map[string]string{archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}, "ARCHERA_API_KEY is not set (read from the environment only)"},
		{"no org", map[string]string{archeraEnvAPIKey: archeraTestKey, archeraEnvPlanID: archeraTestPlan}, "archera org ID is not set (--org-id or ARCHERA_ORG_ID)"},
		{"no plan", map[string]string{archeraEnvAPIKey: archeraTestKey, archeraEnvOrgID: archeraTestOrg}, "archera plan ID is not set (--plan-id or ARCHERA_PLAN_ID)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := runArchera(t, tc.env)
			require.Error(t, err)
			assert.Equal(t, tc.wantMsg, err.Error())
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

// assertArcheraFetchedUTC proves the printed fetch time is the current instant
// in UTC. runArchera shifts time.Local, so a local-zone time mislabeled "Z"
// would be hours off.
func assertArcheraFetchedUTC(t *testing.T, got string) {
	t.Helper()
	stamp := archeraFetchedRE.FindString(got)
	require.NotEmpty(t, stamp)
	require.True(t, strings.HasSuffix(stamp, "Z"))
	parsed, err := time.Parse("2006-01-02T15:04:05Z", stamp)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().UTC(), parsed, 2*time.Minute)
}

// assertArcheraNoControlChars fails if any control character survives, in raw
// form or as a JSON escape. The fixture injects ESC and C1 characters into the
// vendor string fields.
func assertArcheraNoControlChars(t *testing.T, out string) {
	t.Helper()
	for _, r := range out {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)) {
			t.Fatalf("control or format character %U in output", r)
		}
	}
	for _, esc := range []string{`\u001b`, `\u0085`, `\u009b`} {
		assert.NotContains(t, out, esc)
	}
}

// checkArcheraGolden compares output with testdata/<name> after masking the
// wall-clock fetch time. ARCHERA_UPDATE_GOLDEN=1 rewrites the file.
func checkArcheraGolden(t *testing.T, name, got string) {
	t.Helper()
	assertArcheraFetchedUTC(t, got)
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
	assertArcheraNoControlChars(t, out)
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
	assertArcheraNoControlChars(t, out)
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

// TestArcheraDTOSanitizesEveryVendorString builds the comparison directly, so
// fields the decoder would reject (enums, UUIDs, terms) still carry control
// characters into the DTO.
func TestArcheraDTOSanitizesEveryVendorString(t *testing.T) {
	dirty := func(s string) string { return s + "\x1b[31m\u0085\u009b\u202e\u200b\u2028\u2029" }
	str := func(s string) *string { v := dirty(s); return &v }
	pay := insurance.PaymentOption(dirty("no_upfront"))
	entry := func(id string) insurance.OfferEntry {
		return insurance.OfferEntry{
			OfferID: dirty(id), CommitmentType: dirty("type" + id), Provider: common.ProviderType(dirty("aws")),
			ContractTerm: str("term"), PaymentOption: &pay, Region: str("region"), GuaranteedDisplayName: str("name"),
			LeaseMenuItemID: str("lease"),
		}
	}
	c := &insurance.Comparison{
		PlanID:   dirty("plan"),
		Currency: str("usd"),
		Hypotheticals: []insurance.Hypothetical{{
			ContractTerm: str("hterm"), PaymentOption: pay,
			LineItems: []insurance.HypotheticalLineItem{{
				LineItemID: dirty("li"), ActualTerm: str("aterm"), ActualPaymentOption: &pay,
				ActualCommitmentType: str("atype"), Reason: insurance.TermReason(dirty("exact_match")),
			}},
		}},
		Rows: []insurance.ComparisonRow{{LineItemID: dirty("row"), Current: entry("c"), Candidates: []insurance.OfferEntry{entry("k")}}},
	}
	var buf bytes.Buffer
	dto := buildArcheraDTO(c)
	require.NoError(t, renderArcheraJSON(&buf, dto))
	assertArcheraNoControlChars(t, buf.String())
	buf.Reset()
	require.NoError(t, renderArcheraTable(&buf, dto))
	assertArcheraNoControlChars(t, buf.String())
	assert.Equal(t, "plan[31m", dto.PlanID)
	require.NotNil(t, dto.Currency)
	assert.Equal(t, "usd[31m", *dto.Currency)
	buf.Reset()
	require.NoError(t, renderArcheraJSON(&buf, dto))
	assert.Contains(t, buf.String(), `"currency": "usd[31m"`)
}

func TestArcheraFlagsOverrideEnvironment(t *testing.T) {
	rec := setupArchera(t, okHandler)
	flagOrg := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	flagPlan := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	_, _, err := runArchera(t, fullEnv(), "--org-id", flagOrg, "--plan-id", flagPlan)
	require.NoError(t, err)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, "/v1/org/"+flagOrg+"/commitment-plans/"+flagPlan+"/comparison", rec.requests[0].URL.Path)
}

// Decode errors from pkg/insurance echo the rejected vendor value, so the
// command must bound and clean them.
func TestArcheraDecodeErrorsAreBoundedAndClean(t *testing.T) {
	hostile := strings.Repeat("X", 5000) + `\u001b[31m\u202e`
	paths := map[string]string{
		"hypothetical payment_option":  `"payment_option":"no_upfront"`,
		"hypothetical contract_term":   `"contract_term":"one_year_gris"`,
		"line item actual_term":        `"actual_term":"six_month_gris"`,
		"line item actual payment":     `"actual_payment_option":"partial_upfront"`,
		"line item actual_term_reason": `"actual_term_reason":"fallback_closest_shorter"`,
		"offer contract_term":          `"contract_term":"six_month_gris"`,
		"offer payment_option":         `"payment_option":"all_upfront"`,
	}
	for name, field := range paths {
		t.Run(name, func(t *testing.T) {
			key := field[:strings.Index(field, `":`)+2]
			body := strings.Replace(archeraComparisonFixture(), field, key+`"`+hostile+`"`, 1)
			require.NotEqual(t, archeraComparisonFixture(), body)
			setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			out, errOut, err := runArchera(t, fullEnv())
			require.Error(t, err)
			assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
			assert.NotContains(t, err.Error(), strings.Repeat("X", 600))
			assertArcheraNoControlChars(t, err.Error())
			assert.Empty(t, out)
			assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
		})
	}
}

func TestArcheraErrorsMaskObfuscatedKey(t *testing.T) {
	k := archeraTestKey
	split := func(sep string) string { return k[:5] + sep + k[5:] }
	pad := strings.Repeat("p", archeraMaxErrorSize-len("archera API returned HTTP 403: ")-len(k)/2)
	cases := map[string]string{
		"zero-width split":  "bad key " + split("\u200b"),
		"control split":     "bad key " + split("\u0001"),
		"bidi split":        "bad key " + split("\u202e"),
		"echoed twice":      k + " and " + split("\u200b"),
		"straddles the cap": pad + k,
		"bidi isolate+RLO":  "x\u202e\u2066y\u200b" + k,
	}
	for name, msg := range cases {
		t.Run("http "+name, func(t *testing.T) {
			setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(403)
				body, _ := json.Marshal(map[string]string{"message": msg})
				_, _ = w.Write(body)
			})
			out, errOut, err := runArchera(t, fullEnv())
			require.Error(t, err)
			assert.NotContains(t, out+errOut+err.Error(), k)
			assertArcheraNoControlChars(t, err.Error())
			assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
			assert.Contains(t, err.Error(), "retry after 30s")
			assert.NotContains(t, err.Error(), "\u202e")
		})
	}
	t.Run("key as enum value in a decode error", func(t *testing.T) {
		body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":"`+split("\u200b")+`"`, 1)
		setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		out, errOut, err := runArchera(t, fullEnv())
		require.Error(t, err)
		assert.NotContains(t, out+errOut+err.Error(), k)
		// pkg/insurance no longer echoes the rejected value at all.
		assert.Contains(t, err.Error(), "not shown")
	})
	t.Run("plain key as enum value", func(t *testing.T) {
		body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":"`+k+`"`, 1)
		setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		_, _, err := runArchera(t, fullEnv())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), k)
	})
}

// archeraKeyHidingSeparators split the key with characters that survive the
// strip, so an exact-match mask misses a key the terminal still displays.
var archeraKeyHidingSeparators = map[string]string{
	"variation selector 16":   "\ufe0f",
	"variation selector 17":   "\U000e0100",
	"combining grapheme join": "\u034f",
	"combining acute":         "\u0301",
	"enclosing circle":        "\u20dd",
	"hangul choseong filler":  "\u115f",
	"hangul jungseong filler": "\u1160",
	"hangul filler":           "\u3164",
	"halfwidth hangul filler": "\uffa0",
	"space":                   " ",
	"invalid utf-8":           "\xff",
}

func archeraSplitEverywhere(k, sep string) string {
	return strings.Join(strings.Split(k, ""), sep)
}

type archeraFailingTransport struct{ err error }

func (f archeraFailingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestArcheraErrorsWithholdKeyHiddenBehindSurvivingCharacters(t *testing.T) {
	k := archeraTestKey
	checkMsg := func(t *testing.T, got string) {
		t.Helper()
		assert.NotContains(t, got, k)
		assert.Contains(t, got, archeraWithheld)
		assertArcheraNoControlChars(t, got)
		assert.LessOrEqual(t, len(got), archeraMaxErrorSize)
	}
	for name, sep := range archeraKeyHidingSeparators {
		for shape, echo := range map[string]string{
			"split everywhere": archeraSplitEverywhere(k, sep),
			"split once":       k[:5] + sep + k[5:],
		} {
			msg := "bad key " + echo + " was rejected"
			t.Run("http "+name+" "+shape, func(t *testing.T) {
				setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(403)
					body, _ := json.Marshal(map[string]string{"message": msg})
					_, _ = w.Write(body)
				})
				out, errOut, err := runArchera(t, fullEnv())
				require.Error(t, err)
				assert.NotContains(t, out+errOut, k)
				checkMsg(t, err.Error())
				assert.Contains(t, err.Error(), "HTTP 403")
				assert.Contains(t, err.Error(), "retry after 30s")
			})
			t.Run("decode "+name+" "+shape, func(t *testing.T) {
				body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":`+mustJSON(t, echo), 1)
				setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
				out, errOut, err := runArchera(t, fullEnv())
				require.Error(t, err)
				assert.NotContains(t, out+errOut, k)
				// pkg/insurance no longer echoes the rejected value, so there is
				// nothing to withhold; the key must still not appear.
				assert.NotContains(t, err.Error(), k)
				assertArcheraNoControlChars(t, err.Error())
				assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
			})
			for kind, wrapped := range map[string]error{
				"transport": errors.New(msg),
				"deadline":  fmt.Errorf("%s: %w", msg, context.DeadlineExceeded),
				"canceled":  fmt.Errorf("%s: %w", msg, context.Canceled),
			} {
				t.Run(kind+" "+name+" "+shape, func(t *testing.T) {
					setupArchera(t, okHandler)
					archeraHTTPClient = &http.Client{Transport: archeraFailingTransport{err: wrapped}}
					out, errOut, err := runArchera(t, fullEnv())
					require.Error(t, err)
					assert.NotContains(t, out+errOut, k)
					checkMsg(t, err.Error())
				})
			}
		}
	}
}

func mustJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// The mask must run before the cap: a key cut by the cap boundary would
// otherwise survive as a readable prefix. Sweep the boundary and assert no
// 4+ byte prefix of the key remains, for a plain and a strip-split key.
func TestArcheraErrorsLeaveNoKeyPrefixAtCap(t *testing.T) {
	k := archeraTestKey
	for _, echo := range []string{k, k[:5] + "\u200b" + k[5:], archeraSplitEverywhere(k, "\u200b")} {
		for padLen := archeraMaxErrorSize - len(k) - 8; padLen <= archeraMaxErrorSize+2; padLen++ {
			err := archeraSafeError(errors.New(strings.Repeat("p", padLen)+echo), k)
			got := err.Error()
			for n := 4; n <= len(k); n++ {
				for start := 0; start+n <= len(k); start++ {
					require.NotContains(t, got, k[start:start+n], "pad %d, fragment %q", padLen, k[start:start+n])
				}
			}
		}
	}
}

// The withhold check must see the full text: a cut through a split key would
// hide it from a check run after the cap while leaving a readable prefix.
func TestArcheraErrorsWithholdSplitKeyAtAnyCapOffset(t *testing.T) {
	k := archeraTestKey
	for name, sep := range map[string]string{"variation selector": "\ufe0f", "invalid utf-8": "\xff", "combining acute": "\u0301"} {
		for padLen := archeraMaxErrorSize - 3*len(k); padLen <= archeraMaxErrorSize+2; padLen++ {
			got := archeraSafeError(errors.New(strings.Repeat("p", padLen)+archeraSplitEverywhere(k, sep)), k).Error()
			require.Equal(t, archeraWithheld, got, "%s pad %d", name, padLen)
		}
	}
}

func TestArcheraErrorsTrimKeyBeforeMasking(t *testing.T) {
	got := archeraSafeError(errors.New("bad key "+archeraTestKey[:5]+"\u200b"+archeraTestKey[5:]), " "+archeraTestKey+"\n").Error()
	assert.Equal(t, "bad key [redacted]", got)
}

// A log or slog write of the key would bypass os.Stderr captured per test, so
// route both default loggers into the capture for the whole run.
func TestArcheraRunDoesNotLogKey(t *testing.T) {
	var logs bytes.Buffer
	savedLog, savedFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	savedSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { log.SetOutput(savedLog); log.SetFlags(savedFlags); slog.SetDefault(savedSlog) })
	setupArchera(t, okHandler)
	_, _, err := runArchera(t, fullEnv())
	require.NoError(t, err)
	setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) })
	_, _, _ = runArchera(t, fullEnv())
	assert.NotContains(t, logs.String(), archeraTestKey)
}
