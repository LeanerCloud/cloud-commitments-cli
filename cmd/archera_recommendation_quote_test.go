package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	quoteTestRec  = "9aadf00d-ffff-4aba-8ada-abcdefabcdef"
	quoteTestItem = "caadf00d-ffff-4aba-8ada-abcdefabcdef"
	quoteVendor   = "VENDOR-INTERNAL-DETAIL"
)

func quoteFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return string(b)
}

type quoteVendorOpts struct {
	items, compare string
	failPath       string
	failStatus     int
}

func quoteHandler(t *testing.T, o quoteVendorOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o.failPath != "" && strings.HasSuffix(r.URL.Path, o.failPath) {
			if o.failStatus == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			http.Error(w, quoteVendor+" "+archeraTestKey, o.failStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/line-items"):
			w.Header().Set("X-Pagination", fmt.Sprintf(`{"total":%d,"total_pages":1,"page":1,"first_page":1,"last_page":1}`, strings.Count(o.items, `"is_selected"`)))
			_, _ = w.Write([]byte(o.items))
		case strings.HasSuffix(r.URL.Path, "/comparison"):
			_, _ = w.Write([]byte(o.compare))
		default:
			t.Errorf("unexpected Archera path %s", r.URL.Path)
		}
	}
}

func quoteDefaults(t *testing.T) quoteVendorOpts {
	return quoteVendorOpts{items: quoteFixture(t, "archera_quote_line_items.json"), compare: quoteFixture(t, "archera_quote_comparison.json")}
}

// quoteArgs is the recommendation that matches the fixture line item exactly.
func quoteArgs(overrides ...string) []string {
	args := map[string]string{
		"recommendation-id": quoteTestRec, "provider": "aws", "service": "savings-plans-ec2instance",
		"plan-type": "EC2Instance", "source-account": "Source-account-a", "offering-id": "offering-a",
		"region": "us-east-1", "instance-family": "m5", "term": "1yr", "payment": "no_upfront",
		"hourly-commitment": "1.25",
	}
	for i := 0; i+1 < len(overrides); i += 2 {
		if overrides[i+1] == "\x00" {
			delete(args, overrides[i])
		} else {
			args[overrides[i]] = overrides[i+1]
		}
	}
	out := make([]string, 0, 2*len(args))
	for k, v := range args {
		out = append(out, "--"+k, v)
	}
	return out
}

func runQuote(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runArcheraCommand(t, "archera-recommendation-quote", fullEnv(), args...)
}

func TestArcheraQuoteGoldenJSONAndTable(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		t.Run(format, func(t *testing.T) {
			rec := setupArchera(t, quoteHandler(t, quoteDefaults(t)))
			out, errOut, err := runQuote(t, append(quoteArgs(), "--format", format)...)
			require.NoError(t, err)
			assert.Empty(t, errOut)
			require.Len(t, rec.requests, 2)
			assert.Equal(t, "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/line-items", rec.requests[0].URL.Path)
			assert.Equal(t, "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/comparison", rec.requests[1].URL.Path)
			assert.Equal(t, []string{quoteTestItem}, rec.requests[1].URL.Query()["line_item_ids"])
			assert.NotContains(t, out, archeraTestKey)
			checkArcheraGolden(t, "archera_quote_"+format+".golden", out)
		})
	}
}

func TestArcheraQuoteFiguresAreVerbatimDecimalStrings(t *testing.T) {
	setupArchera(t, quoteHandler(t, quoteDefaults(t)))
	out, _, err := runQuote(t, append(quoteArgs(), "--format", "json")...)
	require.NoError(t, err)
	for _, want := range []string{
		`"monthly_730h_commitment_cost_total": "301"`, `"monthly_730h_cloud_provider_cost": "302"`,
		`"monthly_730h_archera_premium": "303"`, `"monthly_730h_gross_savings": "304"`,
		`"monthly_730h_net_savings": "305"`, `"monthly_730h_covered_on_demand_cost": "306"`,
		`"upfront_one_time_cost": "307"`, `"hourly_selected_commitment": "1.25"`,
		`"monthly_730h_commitment_cost_total": "401"`,
	} {
		assert.Contains(t, out, want)
	}
}

func TestArcheraQuoteNoQuoteStatuses(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		items      func(string) string
		compare    func(string) string
		wantStatus string
		wantReason string
		wantText   string
		wantCalls  int
	}{
		{name: "region mismatch is unavailable", args: quoteArgs("region", "eu-west-1"), wantStatus: "unavailable", wantReason: "identity_unproven", wantText: "No exact Archera line item", wantCalls: 1},
		{name: "source account mismatch", args: quoteArgs("source-account", "Other-account-b"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "offering mismatch", args: quoteArgs("offering-id", "offering-b"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "family mismatch", args: quoteArgs("instance-family", "c5"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "payment mismatch", args: quoteArgs("payment", "all_upfront"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "term mismatch", args: quoteArgs("term", "3yr"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "inexact commitment never prints a figure", args: quoteArgs("hourly-commitment", "1.26"), wantStatus: "unavailable", wantReason: "identity_unproven", wantCalls: 1},
		{name: "comparison amount differs", args: quoteArgs(), compare: func(s string) string {
			return strings.ReplaceAll(s, `"selected_amount":1.25`, `"selected_amount":1.3`)
		}, wantStatus: "unavailable", wantReason: "comparison_basis_unproven", wantCalls: 2},
		{name: "two matching line items are ambiguous", args: quoteArgs(), items: func(s string) string {
			second := strings.Replace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]"), quoteTestItem, "eaadf00d-ffff-4aba-8ada-abcdefabcdef", 1)
			return strings.TrimSuffix(strings.TrimSpace(s), "]") + "," + second + "]"
		}, wantStatus: "ambiguous", wantReason: "ambiguous_line_items", wantText: "More than one Archera line item", wantCalls: 1},
		{name: "other provider is ineligible without a vendor call", args: quoteArgs("provider", "azure"), wantStatus: "ineligible", wantReason: "identity_unproven", wantText: "Archera was not called", wantCalls: 0},
		{name: "other plan type is ineligible", args: quoteArgs("plan-type", "Compute"), wantStatus: "ineligible", wantReason: "identity_unproven", wantCalls: 0},
		{name: "other service is ineligible", args: quoteArgs("service", "savings-plans-compute"), wantStatus: "ineligible", wantReason: "identity_unproven", wantCalls: 0},
	}
	for _, tc := range cases {
		for _, format := range []string{"json", "table"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				o := quoteDefaults(t)
				if tc.items != nil {
					o.items = tc.items(o.items)
				}
				if tc.compare != nil {
					o.compare = tc.compare(o.compare)
				}
				rec := setupArchera(t, quoteHandler(t, o))
				out, _, err := runQuote(t, append(tc.args, "--format", format)...)
				require.NoError(t, err)
				assert.Len(t, rec.requests, tc.wantCalls)
				assert.Contains(t, out, "No Archera quote")
				assert.Contains(t, out, tc.wantStatus)
				assert.Contains(t, out, tc.wantReason)
				assert.Contains(t, out, tc.wantText)
				for _, figure := range []string{"301", "305", "307", "401", "1.25"} {
					assert.NotContains(t, out, figure, "an unproven quote must show no figure")
				}
				if format == "json" {
					assert.Contains(t, out, `"quote": null`)
				}
			})
		}
	}
}

func TestArcheraQuoteVendorErrorsPrintFixedTextOnly(t *testing.T) {
	cases := []struct {
		path   string
		status int
		want   string
	}{
		{"/line-items", 500, "archera recommendation quote failed: vendor_unavailable at line_items (HTTP 500)"},
		{"/comparison", 429, "archera recommendation quote failed: vendor_rejected at comparison (HTTP 429) (retry after 7s; not retried automatically)"},
		{"/line-items", 401, "archera recommendation quote failed: vendor_rejected at line_items (HTTP 401)"},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.want[:0]+http.StatusText(tc.status), func(t *testing.T) {
			o := quoteDefaults(t)
			o.failPath, o.failStatus = tc.path, tc.status
			setupArchera(t, quoteHandler(t, o))
			out, errOut, err := runQuote(t, quoteArgs()...)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			all := out + errOut + err.Error()
			assert.NotContains(t, all, quoteVendor)
			assert.NotContains(t, all, archeraTestKey)
			assert.Empty(t, out)
		})
	}
}

func TestArcheraQuoteInvalidInputMakesNoRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"non-uuid recommendation", quoteArgs("recommendation-id", "not-a-uuid"), "--recommendation-id must be a canonical UUID"},
		{"braced uuid", quoteArgs("recommendation-id", "{"+quoteTestRec+"}"), "--recommendation-id must be a canonical UUID"},
		{"urn uuid", quoteArgs("recommendation-id", "urn:uuid:"+quoteTestRec), "--recommendation-id must be a canonical UUID"},
		{"missing recommendation", quoteArgs("recommendation-id", "\x00"), "--recommendation-id must be a canonical UUID"},
		{"quoted number", quoteArgs("hourly-commitment", `"1.25"`), "--hourly-commitment must be a plain positive decimal string"},
		{"exponent", quoteArgs("hourly-commitment", "1e3"), "--hourly-commitment must be a plain positive decimal string"},
		{"negative", quoteArgs("hourly-commitment", "-1"), "--hourly-commitment must be a plain positive decimal string"},
		{"zero", quoteArgs("hourly-commitment", "0.000"), "--hourly-commitment must be a plain positive decimal string"},
		{"trailing dot", quoteArgs("hourly-commitment", "1."), "--hourly-commitment must be a plain positive decimal string"},
		{"leading dot", quoteArgs("hourly-commitment", ".5"), "--hourly-commitment must be a plain positive decimal string"},
		{"leading zero", quoteArgs("hourly-commitment", "01.5"), "--hourly-commitment must be a plain positive decimal string"},
		{"spaced", quoteArgs("hourly-commitment", " 1.25"), "--hourly-commitment must be a plain positive decimal string"},
		{"too long", quoteArgs("hourly-commitment", "1."+strings.Repeat("1", 60)), "--hourly-commitment must be a plain positive decimal string"},
		{"missing commitment", quoteArgs("hourly-commitment", "\x00"), "--hourly-commitment must be a plain positive decimal string"},
		{"bad term", quoteArgs("term", "2yr"), "--term must be 1yr or 3yr"},
		{"bad payment", quoteArgs("payment", "No Upfront"), "--payment must be no_upfront"},
		{"control char region", quoteArgs("region", "us-east-1\x1b[31m"), "--region is required and must be printable text"},
		{"blank account", quoteArgs("source-account", ""), "--source-account is required"},
		{"missing offering", quoteArgs("offering-id", "\x00"), "--offering-id is required"},
		{"missing family", quoteArgs("instance-family", "\x00"), "--instance-family is required"},
		{"missing provider", quoteArgs("provider", "\x00"), "--provider is required"},
		{"missing service", quoteArgs("service", "\x00"), "--service is required"},
		{"missing plan type", quoteArgs("plan-type", "\x00"), "--plan-type is required"},
		{"bad format", append(quoteArgs(), "--format", "xml"), "--format must be table or json"},
		{"bad plan id", append(quoteArgs(), "--plan-id", "plan"), "--plan-id must be a canonical UUID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected Archera request") })
			out, errOut, err := runQuote(t, tc.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, rec.requests)
			assert.Empty(t, out)
			assert.NotContains(t, out+errOut+err.Error(), archeraTestKey)
			assert.NotContains(t, err.Error(), "\x1b")
		})
	}
}

func TestArcheraQuoteIncompleteConfigMakesNoRequest(t *testing.T) {
	rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	_, _, err := runArcheraCommand(t, "archera-recommendation-quote", map[string]string{archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}, quoteArgs()...)
	require.Error(t, err)
	assert.Equal(t, archeraEnvAPIKey+" is not set (read from the environment only)", err.Error())
	assert.Empty(t, rec.requests)
}

func TestArcheraQuoteRenderRefusesOutputThatShowsTheKey(t *testing.T) {
	// The key sits past byte 1056 and, in the second case, hides behind the < > &
	// that JSON escapes; both shown in the table and the decoded JSON strings.
	for _, key := range []string{archeraTestKey, "abcd<efgh", "Ab&Cd>Ef12"} {
		padded := strings.Repeat("z", 1200) + key
		for name, out := range map[string]archeraQuoteOutput{
			"message": {Status: "unavailable", Message: "vendor said " + key},
			"late":    {Status: "unavailable", Message: padded},
			"candidate": {Status: "quote", Quote: &insurance.RecommendationComparisonDTO{
				Candidates: []insurance.RecommendationAlternativeDTO{{OfferID: padded}},
			}},
		} {
			for _, format := range []string{"json", "table"} {
				t.Run(name+"/"+format+"/"+key, func(t *testing.T) {
					body, err := archeraQuoteRender(&out, format, key)
					require.Error(t, err)
					assert.Nil(t, body)
					assert.NotContains(t, err.Error(), key)
				})
			}
		}
	}
	body, err := archeraQuoteRender(&archeraQuoteOutput{Status: "unavailable"}, "json", archeraTestKey)
	require.NoError(t, err)
	assert.NotEmpty(t, body)
}

// The key can be readable only across the encoded JSON punctuation, across
// separators the encoder keeps or across spacing variants, so the guard checks
// the encoded bytes and the skeleton of each text, not just the decoded strings.
func TestArcheraQuoteRenderGuardSeesAcrossFieldsAndSpacing(t *testing.T) {
	cases := []struct {
		name, key, message, format string
	}{
		{"encoded json punctuation", `"status": "unavailable"`, "", "json"},
		{"no-break spaces", "ab cd ef gh", "ab\u00a0cd\u00a0ef\u00a0gh", "json"},
		{"no-break spaces table", "ab cd ef gh", "ab\u00a0cd\u00a0ef\u00a0gh", "table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := archeraQuoteRender(&archeraQuoteOutput{Status: "unavailable", Message: tc.message}, tc.format, tc.key)
			require.Error(t, err)
		})
	}
	// A key that reads only across two table fields is refused in the table and
	// harmless in JSON, where the fields are separate strings.
	out := archeraQuoteOutput{Status: "quote", Quote: &insurance.RecommendationComparisonDTO{
		Current: &insurance.RecommendationOfferDTO{Provider: "aws", CommitmentType: "savings_plan"},
	}}
	_, err := archeraQuoteRender(&out, "table", "aws savings_plan")
	require.Error(t, err)
	_, err = archeraQuoteRender(&out, "json", "aws savings_plan")
	require.NoError(t, err)
}

// A key echoed by the vendor in a candidate offer id is masked in every vendor
// string before rendering, so the command still succeeds and prints [redacted].
func TestArcheraQuoteCandidateOfferIDNeverCarriesTheKey(t *testing.T) {
	for _, key := range []string{archeraTestKey, "abcd<efgh", "Ab&Cd>Ef12"} {
		for name, offerID := range map[string]string{
			"plain": "x" + key + "y",
			"late":  strings.Repeat("z", 1200) + key,
			"early": key,
		} {
			for _, format := range []string{"json", "table"} {
				t.Run(name+"/"+format+"/"+key, func(t *testing.T) {
					o := quoteDefaults(t)
					o.compare = strings.ReplaceAll(o.compare, "faadf00d-ffff-4aba-8ada-abcdefabcdef", offerID)
					setupArchera(t, quoteHandler(t, o))
					env := fullEnv()
					env[archeraEnvAPIKey] = key
					out, errOut, err := runArcheraCommand(t, "archera-recommendation-quote", env, append(quoteArgs(), "--format", format)...)
					require.NoError(t, err)
					if name != "late" { // the 256-byte field cap cuts the mask off the long id
						assert.Contains(t, out, "[redacted]")
					}
					assertQuoteHasNoKey(t, out+errOut, key)
				})
			}
		}
	}
}

// The offers hang off pointers and the contract term is a *string: both are
// walked, so a key equal to the vendor's term is masked and the command succeeds.
func TestArcheraQuoteKeyInPointerHeldStringsIsMasked(t *testing.T) {
	const key = "one_year_gris"
	for _, format := range []string{"json", "table"} {
		t.Run(format, func(t *testing.T) {
			setupArchera(t, quoteHandler(t, quoteDefaults(t)))
			env := fullEnv()
			env[archeraEnvAPIKey] = key
			out, errOut, err := runArcheraCommand(t, "archera-recommendation-quote", env, append(quoteArgs(), "--format", format)...)
			require.NoError(t, err)
			assert.Contains(t, out, "[redacted]")
			assertQuoteHasNoKey(t, out+errOut, key)
		})
	}
}

// assertQuoteHasNoKey fails when the key is readable in text or, for JSON, in
// any decoded string or object key.
func assertQuoteHasNoKey(t *testing.T, text, key string) {
	t.Helper()
	assert.NotContains(t, text, key)
	assert.False(t, strings.Contains(archeraSkeleton(text), archeraSkeleton(key)))
	var decoded any
	if json.Unmarshal([]byte(text), &decoded) == nil {
		assert.False(t, archeraJSONHasKey([]byte(text), key))
	}
}

func TestArcheraQuoteTextFlagCapIs128Bytes(t *testing.T) {
	cases := map[string]bool{strings.Repeat("a", 128): true, strings.Repeat("a", 129): false}
	for region, ok := range cases {
		rec := setupArchera(t, quoteHandler(t, quoteDefaults(t)))
		_, _, err := runQuote(t, quoteArgs("region", region)...)
		if ok {
			require.NoError(t, err)
			assert.NotEmpty(t, rec.requests)
			continue
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--region is required and must be printable text of at most 128 bytes")
		assert.Empty(t, rec.requests)
	}
}

func TestArcheraQuoteMissingCurrentIsUnknownNeverZero(t *testing.T) {
	table := string(archeraQuoteTable(&archeraQuoteOutput{Status: "quote", Quote: &insurance.RecommendationComparisonDTO{}}))
	assert.Contains(t, table, "Current: unknown")
	assert.NotContains(t, table, "Current: 0")
}

func TestArcheraQuoteUnknownFigureIsUnknownNeverZero(t *testing.T) {
	o := quoteDefaults(t)
	o.compare = strings.Replace(o.compare, `"archera_premium":303`, `"archera_premium":null`, 1)
	for format, want := range map[string]string{"json": `"monthly_730h_archera_premium": null`, "table": "premium unknown, gross savings 304"} {
		setupArchera(t, quoteHandler(t, o))
		out, _, err := runQuote(t, append(quoteArgs(), "--format", format)...)
		require.NoError(t, err)
		assert.Contains(t, out, want)
		assert.NotContains(t, out, `"monthly_730h_archera_premium": "0`)
	}
}
