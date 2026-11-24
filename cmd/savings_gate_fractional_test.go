package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ceFractionalDetail is one EC2 RI row whose Cost Explorer quantity is
// fractional: 40% savings, 125 on-demand and 50 savings for the whole quantity.
func ceFractionalDetail(qty string) string {
	return fmt.Sprintf(`{"Recommendations":[{"RecommendationDetails":[{"RecommendedNumberOfInstancesToPurchase":%q,`+
		`"AccountId":"Source-account-a","CurrencyCode":"USD","UpfrontCost":"2500","RecurringStandardMonthlyCost":"100",`+
		`"EstimatedMonthlySavingsAmount":"50","EstimatedMonthlySavingsPercentage":"40","EstimatedMonthlyOnDemandCost":"125",`+
		`"EstimatedBreakEvenInMonths":"7","AverageNumberOfInstancesUsedPerHour":"2.7",`+
		`"InstanceDetails":{"EC2InstanceDetails":{"InstanceType":"m5.large","Platform":"Linux/UNIX","Region":"us-east-1","Tenancy":"shared"}}}]}]}`, qty)
}

// Pins what the CLI sees for a fractional Cost Explorer quantity after the
// cloud-commitments-go bump (go#635 option C, go#687). Probe on the old pin
// dcb5ba0b4061 vs the new pin e0d8a7e, CE row of 40% savings, 125 on-demand,
// 50 savings and 2500 upfront:
//
//	qty 2.5: old count 3, upfront 2500, savings 50, on-demand 125, 40%
//	         new count 2, upfront 2000, savings 40, on-demand 100, 40%
//	qty 2.4: old count 2 with the unscaled 2500/50/125; new count 2, 2083/41.7/104.2
//	qty 0.4: old count 0 with 40% and 2500 upfront, which passed --min-savings-pct;
//	         new count 0, every figure and the percentage 0, which the gate drops
//
// The row now rounds down instead of half up and its money scales with the
// bought count; the percentage stays the ratio CE reported.
func TestMinSavingsGateOnFractionalRoundedDownQuantity(t *testing.T) {
	for _, tc := range []struct {
		qty            string
		count          int
		wantPct        float64
		wantOnDemand   float64
		wantPassesGate bool
	}{
		{"2.5", 2, 40, 100, true},
		{"2.4", 2, 40, 125 * 2 / 2.4, true},
		{"3", 3, 40, 125, true},
		{"0.4", 0, 0, 0, false},
	} {
		t.Run(tc.qty, func(t *testing.T) {
			h := &hookTransport{handle: func(*http.Request) (int, string) { return http.StatusOK, ceFractionalDetail(tc.qty) }}
			cfg := hookedAWSConfig(h)
			cfg.Region = "us-east-1"
			client := recommendations.NewClient(&cfg)
			recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{
				Service: common.ServiceEC2, PaymentOption: "partial-upfront", Term: "1yr", LookbackPeriod: "7d",
			})
			require.NoError(t, err)
			require.Len(t, recs, 1)
			t.Logf("qty %s: count %d savings%% %v on-demand %v upfront %v savings %v", tc.qty, recs[0].Count, recs[0].SavingsPercentage, recs[0].OnDemandCost, recs[0].CommitmentCost, recs[0].EstimatedSavings)
			assert.Equal(t, tc.count, recs[0].Count)
			assert.InDelta(t, tc.wantPct, recs[0].SavingsPercentage, 1e-9)
			assert.InDelta(t, tc.wantOnDemand, recs[0].OnDemandCost, 1e-9)
			passed := len(scorer.Score(recs, scorer.Config{MinSavingsPct: 35}).Passed) == 1
			assert.Equal(t, tc.wantPassesGate, passed)
		})
	}
}
