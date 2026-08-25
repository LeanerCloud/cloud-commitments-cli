package tools

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// EnvAuditLog names the operator-controlled path for the MCP server's
// purchase audit log (one JSON line per purchase attempt, including
// previews). Auditing is ON BY DEFAULT -- unlike EnvEnableRealPurchases,
// which fails closed, this fails open toward keeping a record: an unset
// variable resolves to a default path under the user's XDG state directory
// (see AuditLogPath), not to "no logging". Setting this variable to the
// empty string is the explicit operator opt-out that disables the log
// entirely; setting it to a non-empty value overrides the default path.
// This mirrors logPurchaseAttempt/logPurchaseOutcome's stderr trail, which
// records the same events for a human watching the process, but the JSONL
// file is the durable, machine-readable counterpart: without it, an MCP
// purchase left no record anywhere once the process exited, unlike the CLI
// (cmd/multi_service.go) and web (purchase_executions) paths.
const EnvAuditLog = "CUDLY_MCP_AUDIT_LOG"

// auditRunID identifies every purchase made by this server process in one
// audit trail. Initialized once at process startup (not per purchase) so an
// operator reading the log can correlate every purchase in one server
// lifetime, matching how a single CLI invocation shares one run.
var auditRunID = uuid.NewString()

// auditStatusSuccess, auditStatusError, and auditStatusSkipped are the three
// statuses this server ever writes. They mirror cmd/multi_service.go's
// purchaseSingleRec (dry run -> skipped, provider success -> success,
// anything else -> error). The fourth status common.NewAuditRecord documents,
// "skipped_covered", belongs to the CLI's recent-duplicate guard, which this
// server does not run yet.
const (
	auditStatusSuccess = "success"
	auditStatusError   = "error"
	auditStatusSkipped = "skipped"
)

// auditStatusFor maps a provider PurchaseResult to the audit status it
// represents, mirroring cmd/multi_service.go's purchaseSingleRec: a
// provider call that returns without a Go error but reports Success=false
// is still an error, never a success.
func auditStatusFor(result common.PurchaseResult) string {
	if result.Success {
		return auditStatusSuccess
	}
	return auditStatusError
}

// AuditLogPath resolves where the MCP purchase audit log is written.
// enabled is false only when EnvAuditLog is set to the empty string -- the
// explicit operator opt-out. An unset variable selects the default path,
// $XDG_STATE_HOME/cudly/mcp-audit.jsonl (falling back to
// ~/.local/state/cudly/mcp-audit.jsonl when XDG_STATE_HOME is unset or
// empty). This is the single resolver: the planned reader tools
// (cudly_audit_summary, cudly_server_info) call it too, so writer and
// readers can never disagree about which file they mean.
//
// os.LookupEnv, not os.Getenv, is required here: it is the only way to
// distinguish "the operator set this to empty on purpose" from "the
// operator never set this at all", and those two cases must resolve to
// opposite outcomes (disabled vs. the default path).
func AuditLogPath() (path string, enabled bool, err error) {
	if v, isSet := os.LookupEnv(EnvAuditLog); isSet {
		// Trimmed, matching how every other operator env var in this package
		// is read: a value of " " is a typo, not a request to create a file
		// whose name is a space.
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed, true, nil
		}
		return "", false, nil
	}

	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", false, fmt.Errorf("resolve default audit log path: %w", homeErr)
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "cudly", "mcp-audit.jsonl"), true, nil
}

// EnsureAuditLogWritable resolves the audit path, creates its parent
// directory, and probes it. Returns nil when auditing is disabled. Called
// once from mcp.NewServer so a misconfigured path fails server construction
// loudly, rather than silently dropping every audit record for the life of
// the process.
func EnsureAuditLogWritable() error {
	path, enabled, err := AuditLogPath()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	// 0700: the default path lives under the user's own state directory, and
	// this is a per-user record of money-spending decisions.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create audit log directory for %s: %w", path, err)
	}
	if err := common.CheckAuditLogWritable(path); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}

// recordPurchaseAudit appends one JSONL record for a purchase attempt
// (preview or real). Resolves the path per call, rather than once at
// startup, so a test or operator override of EnvAuditLog after process
// start still takes effect.
//
// A write failure warns on stderr and returns: the caller's purchase result
// is authoritative and must never change because the audit trail hiccuped
// -- losing one line is a mundane operational problem, silently turning a
// completed purchase into a reported failure would not be.
func recordPurchaseAudit(rec common.Recommendation, result common.PurchaseResult, status string, dryRun bool) {
	path, enabled, err := AuditLogPath()
	if err != nil {
		log.Printf("mcp audit log: %v", err)
		return
	}
	if !enabled {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("mcp audit log: create directory for %s: %v", path, err)
		return
	}
	record := common.NewAuditRecord(auditRunID, rec, result, status, dryRun, common.PurchaseSourceMCP)
	if err := common.WriteAuditRecord(record, path); err != nil {
		log.Printf("mcp audit log: %v", err)
	}
}
