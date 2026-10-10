package main

import (
	"context"
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
		if r.Success {
			continue
		}
		AppLogger.Printf("   %s\n", unverifiedPurchaseLine(r))
	}
}

func unverifiedPurchaseLine(r common.PurchaseResult) string {
	return fmt.Sprintf("purchase of %s %s errored (%v): verify in AWS before re-running", r.Recommendation.Service, r.Recommendation.ResourceType, r.Error)
}
