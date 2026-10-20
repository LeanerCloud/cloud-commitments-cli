package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// executePurchasePipeline purchases each rec in the passed list (or dry-runs) and writes audit records.
// It stops at the first audit failure on a real run and returns errAuditTrailLost.
func executePurchasePipeline(ctx context.Context, awsCfg aws.Config, recs []common.Recommendation, isDryRun bool, runID string, cfg Config) ([]common.PurchaseResult, error) {
	results := make([]common.PurchaseResult, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		if stopRequested(ctx) {
			log.Printf("Shutdown requested — skipping %d remaining recommendations", len(recs)-i)
			break
		}
		if err := probeAuditLog(isDryRun, cfg.AuditLog); err != nil {
			return results, err
		}
		// The purchase runs on a context that survives Ctrl-C: canceling the
		// request mid-flight can leave AWS having created the commitment
		// while the CLI records a failure, and a re-run then buys it again.
		// The per-call SDK timeouts still bound it.
		result, status := purchaseSingleRec(context.WithoutCancel(ctx), awsCfg, rec, i+1, isDryRun, cfg)
		results = append(results, result)
		if err := writePurchaseAuditRecord(runID, rec, result, status, isDryRun, cfg.AuditLog); err != nil {
			return results, err
		}
		if !isDryRun && i < len(recs)-1 {
			waitBetweenPurchases(ctx)
		}
	}
	return results, nil
}

// writePurchaseAuditRecord writes a single purchase's audit record. Shared by
// both purchase entry points -- executePurchasePipeline (the main pipeline)
// and processPurchaseLoop (the --input-csv path) -- so every recommendation
// that reaches a purchase attempt, dry-run or real, is recorded to
// cfg.AuditLog regardless of which one produced it. Before #1609,
// processPurchaseLoop never wrote a record at all, so CSV-mode purchases left
// no audit trail: on a partial failure there was no durable, per-recommendation
// record of which rows succeeded, so an operator could only re-run the whole
// file, which is a double purchase for the rows that already succeeded.
//
// A failed write on a real run returns errAuditTrailLost (#2103): the caller
// must stop buying. The record may be missing or written but not durable
// (sync failed); both count as lost. A dry run only warns.
func writePurchaseAuditRecord(runID string, rec common.Recommendation, result common.PurchaseResult, status string, isDryRun bool, auditLogPath string) error {
	auditRec := common.NewAuditRecord(runID, rec, result, status, isDryRun, common.PurchaseSourceCLI)
	if err := writeAuditRecordFn(auditRec, auditLogPath); err != nil {
		if isDryRun {
			log.Printf("Warning: failed to write audit record: %v", err)
			return nil
		}
		return fmt.Errorf("%w: write: %w", errAuditTrailLost, err)
	}
	return nil
}

// probeAuditLog checks, before a real purchase, that the audit log can still
// be appended to, so a log that broke after the start-of-run check stops the
// run before money moves. Dry runs skip it.
func probeAuditLog(isDryRun bool, auditLogPath string) error {
	if isDryRun {
		return nil
	}
	if err := common.CheckAuditLogWritable(auditLogPath); err != nil {
		return fmt.Errorf("%w: probe before purchase: %w", errAuditTrailLost, err)
	}
	return nil
}

// newServiceClient is the service-client factory used by purchaseSingleRec.
// It is a seam so tests can drive the real purchase path with a fake provider.
var newServiceClient = createServiceClient

// purchaseSucceeded reports whether a purchase result is a success: the
// provider set Success AND reported no error. A provider may return
// Success=true with a non-nil Error; that is a failure (issue #1900).
func purchaseSucceeded(result common.PurchaseResult) bool {
	return result.Success && result.Error == nil
}

// purchaseAuditStatus derives the audit status for a completed (non-dry-run)
// purchase attempt. Callers handle the dry-run ("skipped") case separately,
// since that never reaches a PurchaseResult from an actual API call.
func purchaseAuditStatus(result common.PurchaseResult) string {
	if purchaseSucceeded(result) {
		return "success"
	}
	return "error"
}

// purchaseSingleRec executes or dry-runs a single purchase and returns the result + audit status.
func purchaseSingleRec(ctx context.Context, awsCfg aws.Config, rec common.Recommendation, index int, isDryRun bool, cfg Config) (purchaseResult common.PurchaseResult, auditStatus string) {
	label := purchaseRegionLabel(rec, rec.Region)
	AppLogger.Printf("  [%d] %s %s %s (count=%d)\n", index, rec.Service, label, rec.ResourceType, rec.Count)
	if err := validatePurchasePreconditions(rec, rec.Region, cfg.CallerAccountID); err != nil {
		AppLogger.Printf("    ❌ cannot purchase: %v\n", err)
		return preconditionFailure(rec, err, isDryRun), "error"
	}
	if isDryRun {
		result := createDryRunResult(rec, label, index, cfg)
		AppLogger.Printf("    [dry-run] %s\n", result.CommitmentID)
		return result, "skipped"
	}

	clientRegion, err := clientRegionFor(rec.Service, rec.Region)
	if err != nil {
		AppLogger.Printf("    ❌ %v\n", err)
		return common.PurchaseResult{Recommendation: rec, Success: false, Error: err, Timestamp: time.Now()}, "error"
	}
	regionalCfg := awsCfg.Copy()
	regionalCfg.Region = clientRegion
	serviceClient := newServiceClient(rec.Service, regionalCfg)
	if serviceClient == nil {
		AppLogger.Printf("    ⚠️  No service client for %s\n", rec.Service)
		return common.PurchaseResult{Success: false}, "error"
	}

	result := executePurchase(ctx, rec, label, index, serviceClient, cfg)
	status := purchaseAuditStatus(result)
	if purchaseSucceeded(result) {
		AppLogger.Printf("    ✅ %s\n", result.CommitmentID)
	} else {
		AppLogger.Printf("    ❌ %v\n", result.Error)
	}
	return result, status
}

// buildServiceStats computes per-service statistics from a purchase run.
// Results are assumed to be in the same order as recs (1:1 correspondence).
func buildServiceStats(recs []common.Recommendation, results []common.PurchaseResult) map[common.ServiceType]ServiceProcessingStats {
	byService := make(map[common.ServiceType][]common.Recommendation)
	resultsByService := make(map[common.ServiceType][]common.PurchaseResult)
	for i := range recs {
		rec := recs[i]
		byService[rec.Service] = append(byService[rec.Service], rec)
		if i < len(results) {
			resultsByService[rec.Service] = append(resultsByService[rec.Service], results[i])
		}
	}
	stats := make(map[common.ServiceType]ServiceProcessingStats)
	for service, serviceRecs := range byService {
		stats[service] = calculateServiceStats(service, serviceRecs, resultsByService[service])
	}
	return stats
}
