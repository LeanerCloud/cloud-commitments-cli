package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// purchaseDelay is the pause between consecutive real purchases.
var purchaseDelay = PurchaseDelaySeconds * time.Second

// waitBetweenPurchases pauses between real purchases to avoid rate limiting
// and returns at once when ctx is canceled, so Ctrl-C does not wait out the
// delay. It is a variable so tests can observe the moment the wait starts.
var waitBetweenPurchases = func(ctx context.Context) {
	if os.Getenv("DISABLE_PURCHASE_DELAY") == "true" {
		return
	}
	t := time.NewTimer(purchaseDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// printInterruptedSummary reports how far an interrupted purchase run got.
// Records of failed real purchases are called out because a failure reported
// while the run was being stopped may still have created the commitment at
// AWS; the audit status stays "error" and a re-run relies on the duplicate
// check, so the operator must look before re-running.
func printInterruptedSummary(attempted, total int, results []common.PurchaseResult, isDryRun bool) {
	AppLogger.Printf("\n⚠️  Run interrupted: %d of %d recommendation(s) attempted, the rest were not purchased.\n", attempted, total)
	if isDryRun {
		return
	}
	for i := range results {
		r := results[i]
		if purchaseSucceeded(r) {
			continue
		}
		AppLogger.Printf("   %s\n", unverifiedPurchaseLine(r))
	}
}

// errAuditTrailLost marks a real run whose audit record could not be written
// durably (or whose log failed the pre-purchase probe). The run stops and
// exits non-zero: continuing would buy with no reconcilable trail (#2103).
var errAuditTrailLost = errors.New("audit trail lost")

// writeAuditRecordFn is a seam so tests can fail a write after a purchase.
var writeAuditRecordFn = common.WriteAuditRecord

// auditStopMessage tells the operator what ran before the audit trail was
// lost. The last result may be missing from, or not durable in, the log.
func auditStopMessage(attempted, total int, results []common.PurchaseResult, cause error) string {
	last := "no purchase was attempted"
	if n := len(results); n > 0 {
		r := results[n-1]
		last = fmt.Sprintf("last attempted: %s %s", r.Recommendation.Service, r.Recommendation.ResourceType)
		if purchaseSucceeded(r) {
			last += " (commitment " + r.CommitmentID + ")"
		} else {
			last += " (errored: " + fmt.Sprint(r.Error) + ")"
		}
	}
	return fmt.Sprintf("\n❌ Audit record missing or not durable: stopping. %d of %d recommendation(s) attempted, the rest were not purchased; %s. Verify in AWS before re-running, a re-run may buy again. Cause: %v\n", attempted, total, last, cause)
}

func unverifiedPurchaseLine(r common.PurchaseResult) string {
	return fmt.Sprintf("purchase of %s %s errored (%v): verify in AWS before re-running", r.Recommendation.Service, r.Recommendation.ResourceType, r.Error)
}
