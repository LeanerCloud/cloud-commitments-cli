# Purchase Safety

CUDly is designed to be safe by default. Real purchases require an explicit `--purchase` opt-in, and several mechanisms - coverage limits, a duplicate-purchase check, RDS extended-support filtering, and a full audit trail - guard against unintended or repeated buys. See [Duplicate purchase prevention](#duplicate-purchase-prevention---idempotency-window) below for what that check does and does not cover.

## Automation and AI agents

An AI agent (or any other non-interactive caller) can safely drive discovery, sizing, and filtering: that reads recommendations and existing commitments (for example, `--target-coverage`'s coverage lookup and the duplicate check that runs before every purchase), but never purchases anything on its own. Purchasing is different. `--purchase --yes` executes a real purchase from any invocation - a script, a CI job, or an agent running `cudly` as a subprocess included - because `--yes` skips the confirmation prompt before the interactive-terminal check ever runs. There is currently no automation boundary on the purchase path; [#1943](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1943) tracks closing that gap. Until it lands, treat `--purchase --yes` as unattended purchase automation, and keep it out of anything an agent can trigger on its own.

## The purchase decision: --purchase

```text
--purchase  bool   default: false
```

Whether a run executes real purchases is controlled by a single flag:

```text
isDryRun = !ActualPurchase
```

A bare invocation is always a dry run; passing `--purchase` is the one and only opt-in that moves money. This rule is identical in both cloud-fetch mode (the default) and CSV input mode (`--input-csv`).

| `--purchase` | Result |
|---|---|
| (not set / false) | Dry run - nothing purchased |
| `true` | Real purchases |

> **History:** earlier versions had a separate `--dry-run` flag. As a default-true flag it silently suppressed purchases even when `--purchase` was set (you had to pass `--purchase --dry-run=false` to actually buy - a footgun surfaced on #1364), and once its default was flipped to false it became a redundant "force dry-run even with `--purchase`" override that only muddied the contract. It has been removed in favour of the single `--purchase` control. Real purchases still require the `--yes` confirmation (or the interactive prompt) below, so moving money remains a deliberate act.

```bash
# Dry run (the default - nothing is purchased):
cudly --services rds

# Execute real purchases (prompts for confirmation unless --yes is given):
cudly --services rds --purchase

# CSV mode behaves identically:
cudly --input-csv recs.csv --purchase
```

## Confirmation prompt: --yes

```text
--yes   bool   default: false
```

When running in purchase mode (`isDryRun=false`), cudly prints a summary of the total instance count and estimated savings and prompts for confirmation before executing any purchase. Pass `--yes` to skip this prompt in automation.

`--yes` skips the prompt unconditionally - it is not gated on whether the process has a real, interactive terminal. A script, a CI job, or an agent driving `cudly` as a subprocess can pass `--yes` and execute a purchase exactly as a human at a terminal would. Treat `--purchase --yes` as fully unattended purchase automation, not as a convenience for a human who already confirmed elsewhere. See [#1943](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1943) for the tracked work to close this gap.

```bash
# Unattended purchase (use with care - see the note above):
cudly --services rds --purchase --yes
```

## Audit log: --audit-log

```text
--audit-log   string   default: ./cudly-audit.jsonl
```

Every recommendation - whether purchased or dry-run - gets a JSON line in the audit log file: the audit log *path* is checked for writability before any cloud API call is made (see below), but each record itself is written right after that recommendation's purchase call returns (immediately, for a dry run). The audit record includes:

- Run ID (UUID that groups all purchases in a single invocation)
- Recommendation details (service, region, instance type, count, term, payment)
- Purchase result (success/failure, commitment ID, error message)
- Audit status (`skipped` for dry-run, `success` or `error` for real purchases; `skipped_covered` is defined in the schema for server-side idempotency hits but is not emitted by the CLI path)
- Whether the run was a dry run
- Purchase source (`cli`)

cudly verifies that the audit log path is writable before making any cloud API calls. If it is not writable (e.g. the directory does not exist), the command exits immediately with an error.

```bash
# Write audit records to a shared directory
cudly --services rds --purchase \
  --audit-log /var/log/cudly/audit.jsonl
```

The default path (`./cudly-audit.jsonl`) writes to the current working directory. In production deployments, redirect this to a durable, monitored location.

## Duplicate purchase prevention: --idempotency-window

```text
--idempotency-window   string   default: 24h
```

A duplicate check runs before every purchase, on both the `--services` and `--input-csv` paths: it fetches existing commitments and subtracts anything purchased in the last 24 hours from each recommendation's count, so a retried run doesn't buy the same capacity twice.

That 24-hour lookback is fixed. This flag is accepted as a Go duration string (e.g. `24h`, `48h`, `1h30m`) and stored, but its value is never read by the check - passing `--idempotency-window 72h` (or any other value) has no effect on which recommendations are purchased ([#1262](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1262) tracks wiring it in).

If the existing-commitments lookup itself fails (a transient API error), the check is skipped for that batch and the run continues un-deduplicated, with a warning printed to the log rather than the run stopping ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)). Treat that warning as a signal to check the audit log for the run before trusting its purchase counts.

The audit status value `skipped_covered` (idempotency hit) is defined in the audit record schema for use by the server-side scheduler path and is not emitted by this CLI's dedup check.

```bash
# The dedup check always runs with a fixed 24h lookback; this flag's value is not applied:
cudly --services rds --idempotency-window 72h
```

## Pre-expiry rebuy: --rebuy-window-days

```text
--rebuy-window-days   int   default: 0 (disabled)
```

When using `--target-coverage`, cudly subtracts existing RI coverage from the sizing calculation so it only recommends incremental purchases. By default this subtraction treats all existing RIs as fully covering demand regardless of when they expire.

Setting `--rebuy-window-days` changes that behavior: any existing RI whose remaining term is at most this many days is treated as if it has already expired, so `--target-coverage` sizes a replacement recommendation before it actually lapses. This is useful to avoid the coverage gap that would otherwise appear between an RI expiring and a new one taking effect.

```bash
# Recommend replacements for RIs expiring within the next 60 days
cudly --services rds \
  --target-coverage 80 \
  --rebuy-window-days 60
```

`--rebuy-window-days` has no effect when `--target-coverage` is not set.

## Between-purchase delay

cudly inserts a short delay between consecutive purchases to respect AWS API rate limits. This delay is hardcoded (a few seconds) and cannot be configured via a user-facing flag.

### DISABLE_PURCHASE_DELAY (internal/test only)

```text
DISABLE_PURCHASE_DELAY=true  env var   default: unset
```

Setting this environment variable skips the between-purchase delay. This is an internal knob used in integration test environments where throughput matters and rate limiting is not a concern. It is not intended for production use. Do not set this in production deployments.

## Safety checklist for production runs

Before any real purchase run:

1. Run without `--purchase` first to review the dry-run CSV output.
2. Check that `--audit-log` points to a writable, durable location.
3. If using `--target-coverage`, verify `--rebuy-window-days` is set appropriately for your RI renewal cadence.
4. Narrow the scope with `--include-regions`, `--include-accounts`, or `--min-savings-pct` before buying across all services.
5. Consider `--max-instances` as a final safety cap for a first run.
6. Note that `--idempotency-window`'s value is not applied - dedup always uses a fixed 24h lookback ([#1262](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1262)) - and that a failed existing-commitments lookup lets the run proceed un-deduplicated with a warning ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)); watch the log for that warning and check the audit log afterward.
7. If an AI agent or other automation drives `cudly`, never pass `--yes` to it directly - have the agent hand off the dry-run recommendation to a human, who runs `--purchase` themselves. See [Automation and AI agents](#automation-and-ai-agents).
