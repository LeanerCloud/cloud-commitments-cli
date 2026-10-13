package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWritePurchaseAuditRecord_SavingsPlanCarriesRecurringMonthlyCost(t *testing.T) {
	recurring := 1968.81
	rec := common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceSavingsPlansEC2Instance,
		CommitmentType: common.CommitmentSavingsPlan, Term: "1yr", PaymentOption: "no-upfront",
		Count: 1, RecurringMonthlyCost: &recurring,
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	require.NoError(t, writePurchaseAuditRecord("run-1", rec, common.PurchaseResult{Recommendation: rec}, "skipped", true, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var line map[string]any
	require.NoError(t, json.Unmarshal(data, &line))
	assert.Equal(t, 0.0, line["estimated_cost"], "upfront stays where it was")
	assert.Equal(t, 1968.81, line["recurring_monthly_cost"])
}
