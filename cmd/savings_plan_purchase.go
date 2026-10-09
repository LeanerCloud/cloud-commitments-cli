package main

import (
	"fmt"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// spAPIRegion is the region the account-level Savings Plans API is called in.
// Savings Plans has a single endpoint, so every Savings Plan call (collection
// and purchase) uses this region regardless of the recommendation's own
// region. It is the commercial-partition value; GovCloud and China use other
// regions and are not supported for Savings Plan purchases.
const spAPIRegion = "us-east-1"

// savingsPlanRowKey identifies one Savings Plan purchase. Two rows with the
// same key buy the same plan twice.
func savingsPlanRowKey(rec common.Recommendation) string {
	key := fmt.Sprintf("%s|%s|%s|%s", rec.Service, rec.Account, rec.Term, rec.PaymentOption)
	if d, ok := rec.Details.(*common.SavingsPlanDetails); ok && d != nil {
		key += fmt.Sprintf("|%s|%s|%s|%s|%s", d.PlanType, d.InstanceFamily, d.Region,
			strconv.FormatFloat(d.HourlyCommitment, 'f', -1, 64), d.OfferingID)
	}
	return key
}

// rejectDuplicateSavingsPlanRows fails the run when --input-csv lists the same
// Savings Plan more than once, for example a CSV written by an older
// multi-region run that repeated the account-level plans per region. It runs
// before any confirmation or purchase, dry run included, so a duplicated file
// can never buy a plan twice.
func rejectDuplicateSavingsPlanRows(recs []common.Recommendation) error {
	seen := make(map[string]int)
	for i := range recs {
		if !common.IsSavingsPlan(recs[i].Service) {
			continue
		}
		key := savingsPlanRowKey(recs[i])
		if first, dup := seen[key]; dup {
			return fmt.Errorf("duplicate Savings Plan rows %d and %d in CSV (service %s, account %q, term %q, payment %q): remove the repeat, it would be purchased twice",
				first+1, i+1, recs[i].Service, recs[i].Account, recs[i].Term, recs[i].PaymentOption)
		}
		seen[key] = i
	}
	return nil
}
