package main

import (
	"fmt"
	"strconv"
	"time"

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

// validatePurchasePreconditions reports why rec cannot be purchased, using the
// same requirements the purchase path enforces, so a dry run predicts what a
// real run will do instead of reporting every row as a success. It checks only
// what the library would reject for a missing or malformed field; it does not
// call any API. region is the one the client will be built from: the
// recommendation's own on the main path, the CSV group's on the CSV path.
func validatePurchasePreconditions(rec common.Recommendation, region string) error {
	if _, err := clientRegionFor(rec.Service, region); err != nil {
		return err
	}
	switch {
	case common.IsSavingsPlan(rec.Service):
		return validateSavingsPlanPreconditions(rec)
	case rec.Service == common.ServiceEC2:
		d, ok := rec.Details.(*common.ComputeDetails)
		if !ok || d == nil {
			return fmt.Errorf("EC2 recommendation for %s has no compute details (platform, tenancy, scope)", rec.ResourceType)
		}
		if d.Platform == "" || d.Tenancy == "" || d.Scope == "" {
			return fmt.Errorf("EC2 recommendation for %s is missing platform, tenancy or scope (got %q, %q, %q)", rec.ResourceType, d.Platform, d.Tenancy, d.Scope)
		}
	}
	return nil
}

func validateSavingsPlanPreconditions(rec common.Recommendation) error {
	d, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok || d == nil {
		return fmt.Errorf("%s recommendation has no Savings Plan details (plan type, hourly commitment)", rec.Service)
	}
	if d.PlanType == "" {
		return fmt.Errorf("%s recommendation is missing the plan type", rec.Service)
	}
	if d.HourlyCommitment <= 0 {
		return fmt.Errorf("%s recommendation has no positive hourly commitment (got %v)", rec.Service, d.HourlyCommitment)
	}
	if rec.Service == common.ServiceSavingsPlansEC2Instance && d.Region == "" {
		return fmt.Errorf("EC2 Instance Savings Plan needs a region to pick the offering")
	}
	return nil
}

// preconditionFailure builds the failed result recorded for a row that cannot
// be purchased.
func preconditionFailure(rec common.Recommendation, err error, isDryRun bool) common.PurchaseResult {
	return common.PurchaseResult{Recommendation: rec, Success: false, Error: err, DryRun: isDryRun, Timestamp: time.Now()}
}
