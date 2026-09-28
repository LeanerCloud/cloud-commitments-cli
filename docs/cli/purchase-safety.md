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

cudly verifies that the audit log is readable and writable and that the immediate configured and resolved parent directories can be opened and synced before making any cloud API calls. Higher ancestors need search permission; the immediate parent directories also need read permission and a filesystem that supports directory `fsync`. If the parent does not exist or these durability checks fail, the command exits immediately with an error.

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

A duplicate check runs before every purchase, on both the `--services` and `--input-csv` paths: it fetches existing commitments and subtracts anything purchased within the window from each recommendation's count, so a retried run doesn't buy the same capacity twice.

The window is a Go duration string that must be a positive whole number of hours (e.g. `24h`, `48h`, `72h`). A value that doesn't parse, is zero or negative, or isn't whole hours (e.g. `90m`, `1h30m`) is rejected at startup, before any API call, rather than rounded or replaced with the default.

If the existing-commitments lookup itself fails (a transient API error), the two modes diverge: a dry run continues with a warning printed to the log (nothing is bought, so reporting fidelity wins), while a `--purchase` run refuses that (service, region) - it prints a "Refusing to purchase" line and buys nothing there, rather than falling back to the un-deduplicated counts ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)).

The audit status value `skipped_covered` (idempotency hit) is defined in the audit record schema for use by the server-side scheduler path and is not emitted by this CLI's dedup check.

```bash
# Subtract anything purchased in the last 72 hours:
cudly --services rds --idempotency-window 72h
```

## Pre-expiry rebuy: --rebuy-window-days

```text
--rebuy-window-days   int   default: 0 (disabled)
```

When using `--target-coverage`, cudly subtracts existing RI coverage from the sizing calculation so it only recommends incremental purchases. By default this subtraction treats all existing RIs as fully covering demand regardless of when they expire.

If the Cost Explorer coverage fetch fails, a dry run warns and sizes as if nothing is owned; a `--purchase` run aborts before sizing, since sizing against unknown coverage risks buying on top of what the account already owns ([#1942](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1942)).

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
2. Check that `--audit-log` points to a readable, writable, durable location.
3. If using `--target-coverage`, verify `--rebuy-window-days` is set appropriately for your RI renewal cadence.
4. Narrow the scope with `--include-regions`, `--include-accounts`, or `--min-savings-pct` before buying across all services.
5. Consider `--max-instances` as a final safety cap for a first run.
6. Set `--idempotency-window` to cover the time since the earlier run (e.g. `72h` for a re-run two days later), and note that a failed existing-commitments lookup makes a dry run proceed un-deduplicated with a warning, while a `--purchase` run refuses to purchase for that (service, region) instead ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)); watch the log for either signal and check the audit log afterward.
7. If an AI agent or other automation drives `cudly`, never pass `--yes` to it directly - have the agent hand off the dry-run recommendation to a human, who runs `--purchase` themselves. See [Automation and AI agents](#automation-and-ai-agents).
