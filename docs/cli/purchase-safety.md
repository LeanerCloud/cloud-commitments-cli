# Purchase Safety

CUDly is designed to be safe by default. Real purchases require multiple explicit opt-ins, and several mechanisms prevent duplicate or unintended buys.

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

```bash
# Unattended purchase (use with care):
cudly --services rds --purchase --yes
```

## Audit log: --audit-log

```text
--audit-log   string   default: ./cudly-audit.jsonl
```

Every recommendation - whether purchased or dry-run - is written as a JSON line to the audit log file before any purchase API call is made. The audit record includes:

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

This flag is accepted as a Go duration string (e.g. `24h`, `48h`, `1h30m`). The CLI does not validate the string at startup; it stores the raw value but does not yet subtract previously-purchased commitments from new recommendations based on this window. The deduction logic runs in the server-side scheduler path (where the duration IS parsed), not the CLI purchase loop. Passing this flag in CLI invocations has no effect on which recommendations are purchased.

The audit status value `skipped_covered` (idempotency hit) is defined in the audit record schema for use by the server path and is not emitted by the CLI.

```bash
# Accepted but currently has no effect on CLI recommendation deduction:
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
6. Note that `--idempotency-window` does not prevent double-buying in the CLI path; use a dry-run review (run without `--purchase`) and audit-log inspection to guard against retried runs.
