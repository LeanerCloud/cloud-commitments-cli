package main

import (
	"fmt"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/savingsplans"
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

// spGlobalLabel names the region in purchase IDs and audit records for an
// account-level Savings Plan that carries no region of its own.
const spGlobalLabel = "global"

// clientRegionFor returns the region the AWS client for service must be built
// with. The four Savings Plan services always use spAPIRegion and ignore the
// recommendation's region: the Savings Plans API has one endpoint, and the
// region a plan is scoped to is passed separately as an offering filter from
// Details.Region. Every other service needs the recommendation's own region,
// and an empty one is an error rather than a client that fails later with
// "Missing Region". The umbrella ServiceSavingsPlansAll has no plan type and
// no client, so it gets an error instead of a silently chosen region.
func clientRegionFor(service common.ServiceType, recRegion string) (string, error) {
	if common.IsSavingsPlan(service) {
		if _, ok := savingsplans.PlanTypeForServiceType(service); !ok {
			return "", fmt.Errorf("service %s is not a purchasable Savings Plan type", service)
		}
		return spAPIRegion, nil
	}
	if recRegion == "" {
		return "", fmt.Errorf("recommendation for %s has no region", service)
	}
	return recRegion, nil
}

// purchaseRegionLabel is the region shown in purchase IDs and audit records.
// For Savings Plans it is Details.Region (EC2Instance plans) or "global";
// for everything else it is the region the recommendation was grouped under.
func purchaseRegionLabel(rec common.Recommendation, groupRegion string) string {
	if !common.IsSavingsPlan(rec.Service) {
		return groupRegion
	}
	if d, ok := rec.Details.(*common.SavingsPlanDetails); ok && d != nil && d.Region != "" {
		return d.Region
	}
	return spGlobalLabel
}
