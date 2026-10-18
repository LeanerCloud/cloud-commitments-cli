package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArcheraComparisonSelectedAmountContract(t *testing.T) {
	for row, candidates := range []int{2, 1} {
		for candidate := -1; candidate < candidates; candidate++ {
			for _, variant := range []string{"missing", "null", "negative", "zero"} {
				t.Run(fmt.Sprintf("row%d/offer%d/%s", row, candidate, variant), func(t *testing.T) {
					var body map[string]any
					decoder := json.NewDecoder(strings.NewReader(archeraComparisonFixture()))
					decoder.UseNumber()
					require.NoError(t, decoder.Decode(&body))
					line := body["data"].([]any)[row].(map[string]any)
					offer := line["current"].(map[string]any)
					if candidate >= 0 {
						offer = line["candidates"].([]any)[candidate].(map[string]any)
					}
					switch variant {
					case "missing":
						delete(offer, "selected_amount")
					case "null":
						offer["selected_amount"] = nil
					case "negative":
						offer["selected_amount"] = json.Number("-1")
					case "zero":
						offer["selected_amount"] = json.Number("0")
					}
					wire, err := json.Marshal(body)
					require.NoError(t, err)
					rec := setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(wire) })
					out, stderr, err := runArchera(t, fullEnv(), "--format", "json")
					if variant == "zero" {
						require.NoError(t, err)
						assert.NotEmpty(t, out)
					} else {
						require.ErrorContains(t, err, "selected_amount")
						assert.Empty(t, out)
						assert.NotContains(t, stderr+err.Error(), archeraTestKey)
					}
					require.Len(t, rec.requests, 1)
				})
			}
		}
	}
}

func TestArcheraComparisonAcceptedWireDecimalPrecision(t *testing.T) {
	cases := []struct{ name, token, want string }{
		{"101 places", "0.1e-100", "0." + strings.Repeat("0", 100) + "1"},
		{"64 byte token", "0." + strings.Repeat("0", 56) + "1e-100", "0." + strings.Repeat("0", 156) + "1"},
		{"65 byte token rejected", "0." + strings.Repeat("0", 57) + "1e-100", ""},
		{"exponent rejected", "1e-101", ""},
	}
	require.Len(t, cases[1].token, 64)
	require.Len(t, cases[2].token, 65)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := strings.Replace(archeraComparisonFixture(), `"total":101`, `"total":`+tc.token, 1)
			require.NotEqual(t, archeraComparisonFixture(), wire)
			rec := setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(wire)) })
			client, err := insurance.NewClient(insurance.Config{APIKey: insurance.NewSecret(archeraTestKey), OrgID: archeraTestOrg}, archeraHTTPClient)
			require.NoError(t, err)
			comparison, err := client.Comparison(context.Background(), insurance.ComparisonRequest{PlanID: archeraTestPlan})
			if tc.want == "" {
				require.Error(t, err)
				assert.Nil(t, comparison)
			} else {
				require.NoError(t, err, "the real client must accept the wire token before formatting is tested")
				require.NotNil(t, comparison)
				got := archeraMoney(comparison.Current.Monthly.CommitmentCostTotal)
				require.NotNil(t, got)
				assert.Equal(t, tc.want, *got)
			}
			out, stderr, err := runArchera(t, fullEnv(), "--format", "json")
			if tc.want == "" {
				require.Error(t, err)
				assert.Empty(t, out)
				assert.NotContains(t, stderr+err.Error(), archeraTestKey)
			} else {
				require.NoError(t, err)
				var dto archeraComparisonDTO
				require.NoError(t, json.Unmarshal([]byte(out), &dto))
				require.NotNil(t, dto.Current.Monthly.CommitmentCostTotal)
				assert.Equal(t, tc.want, *dto.Current.Monthly.CommitmentCostTotal)
			}
			assert.Len(t, rec.requests, 2)
		})
	}
}
