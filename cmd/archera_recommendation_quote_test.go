package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

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
