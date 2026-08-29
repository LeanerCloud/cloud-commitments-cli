package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"time"
)

// WriteAuditRecord marshals record to a single JSON line and appends it to path.
// Returns an error if RunID is empty or if any I/O step fails.
func WriteAuditRecord(record AuditRecord, path string) error {
	if record.RunID == "" {
		return fmt.Errorf("audit record RunID must not be empty")
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}

	// 0644 (world-readable) is intentional: the audit log is consumed by
	// ops tooling and reconciled against purchase_history; restricting to
	// 0600 would break that workflow without adding meaningful protection
	// since the file lives under the run-owned working dir.
	return appendJSONL(path, data, 0644)
}

// CheckAuditLogWritable opens the audit log file in append mode to verify it is writable.
// Returns an error if the path cannot be opened for writing.
func CheckAuditLogWritable(path string) error {
	f, err := openAuditLogForAppend(path, 0644)
	if err != nil {
		return fmt.Errorf("audit log %q not writable: %w", path, err)
	}
	if err := probeAuditLogWritable(path, f); err != nil {
		return fmt.Errorf("audit log %q not writable: %w", path, err)
	}
	return nil
}

func openAuditLogForAppend(path string, perm fs.FileMode) (auditLogFile, error) {
	if err := validateAuditLogTarget(path); err != nil {
		return nil, err
	}

	// #nosec G302,G304 -- audit log path is operator-configured; WriteAuditRecord intentionally uses 0644 for downstream readers.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		closeErr := f.Close()
		return nil, errors.Join(fmt.Errorf("stat opened audit log %s: %w", path, err), closeErr)
	}
	if !info.Mode().IsRegular() {
		closeErr := f.Close()
		return nil, errors.Join(nonRegularAuditLogTargetError(path, info.Mode()), closeErr)
	}
	return &auditOSFile{File: f}, nil
}

type auditLogFile interface {
	Lock() error
	Write([]byte) (int, error)
	Sync() error
	Unlock() error
	Close() error
}

type auditOSFile struct {
	*os.File
}

func appendJSONL(path string, payload []byte, perm fs.FileMode) error {
	f, err := openAuditLogForAppend(path, perm)
	if err != nil {
		return err
	}
	return appendJSONLFile(path, f, payload)
}

func appendJSONLFile(path string, f auditLogFile, payload []byte) error {
	return withAuditLogTransaction(path, f, func() error {
		return appendJSONLLocked(path, f, payload)
	})
}

func probeAuditLogWritable(path string, f auditLogFile) error {
	return withAuditLogTransaction(path, f, nil)
}

func appendJSONLLocked(path string, f auditLogFile, payload []byte) error {
	line := make([]byte, 0, len(payload)+1)
	line = append(line, payload...)
	line = append(line, '\n')

	n, err := f.Write(line)
	var opErr error
	switch {
	case err != nil:
		opErr = fmt.Errorf("write audit record to %s: %w", path, err)
	case n != len(line):
		opErr = fmt.Errorf("write audit record to %s: %w", path, io.ErrShortWrite)
	default:
		if err := f.Sync(); err != nil {
			opErr = fmt.Errorf("sync audit record to %s: %w", path, err)
		}
	}

	if opErr != nil && n > 0 && n < len(line) {
		if err := terminatePartialAuditRecord(path, f); err != nil {
			opErr = errors.Join(opErr, err)
		}
	}

	return opErr
}

func withAuditLogTransaction(path string, f auditLogFile, work func() error) error {
	if err := f.Lock(); err != nil {
		return closeAuditLog(path, f, fmt.Errorf("lock audit log %s: %w", path, err))
	}

	var opErr error
	if work != nil {
		opErr = work()
	}
	if err := f.Unlock(); err != nil {
		opErr = errors.Join(opErr, fmt.Errorf("unlock audit log %s: %w", path, err))
	}
	return closeAuditLog(path, f, opErr)
}

func closeAuditLog(path string, f auditLogFile, opErr error) error {
	if err := f.Close(); err != nil {
		return errors.Join(opErr, fmt.Errorf("close audit log %s: %w", path, err))
	}
	return opErr
}

func terminatePartialAuditRecord(path string, f auditLogFile) error {
	n, err := f.Write([]byte{'\n'})
	var opErr error
	switch {
	case err != nil:
		opErr = fmt.Errorf("write audit record separator to %s: %w", path, err)
	case n != 1:
		opErr = fmt.Errorf("write audit record separator to %s: %w", path, io.ErrShortWrite)
	}
	if n == 1 {
		if err := f.Sync(); err != nil {
			opErr = errors.Join(opErr, fmt.Errorf("sync audit record separator to %s: %w", path, err))
		}
	}
	return opErr
}

func validateAuditLogTarget(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat audit log target %q: %w", path, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		targetInfo, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("resolve audit log symlink %q: %w", path, err)
		}
		if !targetInfo.Mode().IsRegular() {
			return nonRegularAuditLogTargetError(path, targetInfo.Mode())
		}
		return nil
	}

	if !info.Mode().IsRegular() {
		return nonRegularAuditLogTargetError(path, info.Mode())
	}
	return nil
}

func nonRegularAuditLogTargetError(path string, mode fs.FileMode) error {
	return fmt.Errorf("non-regular audit log target %q has mode %s", path, mode.Type())
}

// NewAuditRecord constructs an AuditRecord from a Recommendation and a PurchaseResult.
// status must be one of: "success", "error", "skipped" (dry-run), "skipped_covered" (idempotency).
// source is the CUDly surface that triggered the run — copied into the JSONL so CLI
// audit logs can be reconciled against the DB's purchase_history.source column.
func NewAuditRecord(runID string, rec Recommendation, result PurchaseResult, status string, dryRun bool, source string) AuditRecord {
	errMsg := ""
	if result.Error != nil {
		errMsg = result.Error.Error()
	}
	return AuditRecord{
		RunID:             runID,
		Provider:          rec.Provider,
		AccountID:         rec.Account,
		AccountName:       rec.AccountName,
		Region:            rec.Region,
		Service:           string(rec.Service),
		ResourceType:      rec.ResourceType,
		CommitmentType:    rec.CommitmentType,
		Term:              termMonths(rec.Term),
		Count:             rec.Count,
		EstimatedCost:     rec.CommitmentCost,
		EstimatedSavings:  rec.EstimatedSavings,
		CommitmentID:      result.CommitmentID,
		Status:            status,
		ErrorMessage:      errMsg,
		Timestamp:         time.Now().UTC(),
		DryRun:            dryRun,
		RawRecommendation: rec.RawRecommendation,
		Source:            source,
	}
}

// termMonths converts a term string ("1yr", "3yr") to months.
// Returns 0 for unrecognized strings.
func termMonths(t string) int {
	switch t {
	case "1yr":
		return 12
	case "3yr":
		return 36
	default:
		if t != "" {
			log.Printf("warn: unrecognized term string %q, using 0 months", t)
		}
		return 0
	}
}
