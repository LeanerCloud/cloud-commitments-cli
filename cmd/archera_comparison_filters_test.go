package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const archeraTestLine = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

func TestArcheraComparisonFiltersQuery(t *testing.T) {
	terms := insurance.ContractTerms()
	cases := []struct {
		name string
		args []string
		want url.Values
	}{
		{"omitted", nil, url.Values{}},
		{"line items", []string{"--line-item-ids", archeraTestLine}, url.Values{"line_item_ids": {archeraTestLine}}},
		{"terms", []string{"--contract-terms", "one_year_gris,three_year"}, url.Values{"contract_terms": {"one_year_gris", "three_year"}}},
		{"payments", []string{"--payment-options", "no_upfront", "--payment-options", "all_upfront"}, url.Values{"payment_options": {"no_upfront", "all_upfront"}}},
		{"combined duplicates", []string{"--line-item-ids", archeraTestLine + "," + archeraTestOrg, "--line-item-ids", archeraTestLine, "--contract-terms", "one_year", "--payment-options", "partial_upfront"}, url.Values{"line_item_ids": {archeraTestLine, archeraTestOrg, archeraTestLine}, "contract_terms": {"one_year"}, "payment_options": {"partial_upfront"}}},
		{"200 IDs", []string{"--line-item-ids", strings.TrimSuffix(strings.Repeat(archeraTestLine+",", 200), ",")}, url.Values{"line_item_ids": strings.Split(strings.TrimSuffix(strings.Repeat(archeraTestLine+",", 200), ","), ",")}},
		{"all terms", []string{"--contract-terms", strings.Join(terms, ",")}, url.Values{"contract_terms": terms}},
		{"three payments", []string{"--payment-options", "no_upfront,partial_upfront,all_upfront"}, url.Values{"payment_options": {"no_upfront", "partial_upfront", "all_upfront"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := setupArchera(t, okHandler)
			out, stderr, err := runArchera(t, fullEnv(), tc.args...)
			require.NoError(t, err)
			require.Len(t, rec.requests, 1)
			req := rec.requests[0]
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/comparison", req.URL.Path)
			assert.Equal(t, archeraTestKey, req.Header.Get("x-api-key"))
			got := req.URL.Query()
			require.Len(t, got, len(tc.want))
			for key, values := range tc.want {
				assert.ElementsMatch(t, values, got[key])
			}
			assert.NotContains(t, out+stderr, archeraTestKey)
		})
	}
}

func TestArcheraComparisonFiltersRejectBeforeHTTP(t *testing.T) {
	for _, flag := range []string{"line-item-ids", "contract-terms", "payment-options"} {
		for _, value := range []string{"", " ", ",", "one_year,", ",one_year", "one_year,,three_year", archeraTestKey, "ONE_YEAR"} {
			t.Run(flag+"/"+value, func(t *testing.T) {
				rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP request") })
				out, stderr, err := runArchera(t, fullEnv(), "--"+flag+"="+value)
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "unknown flag")
				assert.Contains(t, err.Error(), "--"+flag)
				assert.NotContains(t, out+stderr+err.Error(), archeraTestKey)
				assert.Empty(t, rec.requests)
			})
		}
	}
	cases := []struct{ flag, value string }{
		{"line-item-ids", strings.TrimSuffix(strings.Repeat(archeraTestLine+",", 201), ",")},
		{"contract-terms", strings.Join(append(insurance.ContractTerms(), "one_year"), ",")},
		{"payment-options", "no_upfront,no_upfront,no_upfront,no_upfront"},
		{"line-item-ids", "cccccccccccc4ccc8ccccccccccccccc"},
		{"line-item-ids", "{" + archeraTestLine + "}"},
		{"line-item-ids", "urn:uuid:" + archeraTestLine},
		{"contract-terms", " one_year"},
		{"payment-options", "No_Upfront"},
	}
	for _, tc := range cases {
		t.Run(tc.flag+"/"+tc.value[:min(12, len(tc.value))], func(t *testing.T) {
			rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP request") })
			_, _, err := runArchera(t, fullEnv(), "--"+tc.flag, tc.value)
			require.ErrorContains(t, err, "--"+tc.flag)
			assert.Empty(t, rec.requests)
		})
	}
	for _, tc := range []struct{ flag, valid string }{{"line-item-ids", archeraTestLine}, {"contract-terms", "one_year"}, {"payment-options", "no_upfront"}} {
		t.Run("repeated empty "+tc.flag, func(t *testing.T) {
			rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP request") })
			_, _, err := runArchera(t, fullEnv(), "--"+tc.flag, tc.valid, "--"+tc.flag+"=")
			require.ErrorContains(t, err, "--"+tc.flag)
			assert.Empty(t, rec.requests)
		})
	}
}

func TestArcheraComparisonFiltersHelpAndReset(t *testing.T) {
	rec := setupArchera(t, okHandler)
	out, _, err := runArchera(t, fullEnv(), "--help")
	require.NoError(t, err)
	for _, flag := range []string{"line-item-ids", "contract-terms", "payment-options"} {
		assert.Contains(t, out, "--"+flag)
	}
	assert.Empty(t, rec.requests)
	_, _, err = runArchera(t, fullEnv(), "--line-item-ids", archeraTestLine)
	require.NoError(t, err)
	_, _, err = runArchera(t, fullEnv())
	require.NoError(t, err)
	require.Len(t, rec.requests, 2)
	assert.Empty(t, rec.requests[1].URL.RawQuery)
	for _, flag := range []string{"line-item-ids", "contract-terms", "payment-options"} {
		assert.False(t, archeraComparisonCmd.Flags().Changed(flag))
	}
	rootCmd.SetArgs([]string{"configure-gcp", "--help"})
	require.NoError(t, rootCmd.Execute())
	resetHelpFlags()
	assert.Len(t, rec.requests, 2)
}
