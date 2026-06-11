# Purchase Safety

CUDly is designed to be safe by default. Real purchases require multiple explicit opt-ins, and several mechanisms prevent duplicate or unintended buys.

## The purchase decision: --dry-run and --purchase

```text
--dry-run   bool   default: true
--purchase  bool   default: false
```

There are two code paths with different rules:

- **Cloud-fetch mode** (the default, recommendations fetched from the cloud APIs):

  ```text
  isDryRun = !ActualPurchase || DryRun
  ```

- **CSV input mode** (`--input-csv`): `--dry-run` is **ignored**:

  ```text
  isDryRun = !ActualPurchase
  ```

  In CSV mode, `--purchase` alone is enough to execute real purchases.

In plain terms, for cloud-fetch mode:

| `--purchase` | `--dry-run` | Result |
|---|---|---|
| (not set / false) | (not set / true) | Dry run - nothing purchased |
| `true` | (not set / true) | **Dry run** - `DryRun` default overrides `--purchase` |
| `true` | `false` | Real purchases |
| (not set / false) | `false` | Dry run - `ActualPurchase` is false, so `isDryRun` stays true |

The footgun: in cloud-fetch mode, passing `--purchase` alone is not sufficient - you must also pass `--dry-run=false`. This is intentional: two flags must be flipped to move money.

The reverse footgun: in `--input-csv` mode the two-flag protection does not apply. `--purchase` alone performs real purchases, and `--dry-run` has no effect. Treat any CSV-mode invocation that includes `--purchase` as a real purchase run.

```bash
# Correct way to execute real purchases (cloud-fetch mode):
cudly --services rds --purchase --dry-run=false

# Still a dry run (--dry-run defaults to true):
cudly --services rds --purchase

# CSV mode: this executes REAL purchases (--dry-run is ignored):
cudly --input-csv recs.csv --purchase
```

## Confirmation prompt: --yes

```text
--yes   bool   default: false
```

When running in purchase mode (`isDryRun=false`), cudly prints a summary of the total instance count and estimated savings and prompts for confirmation before executing any purchase. Pass `--yes` to skip this prompt in automation.

```bash
# Unattended purchase (use with care):
cudly --services rds --purchase --dry-run=false --yes
```

## Audit log: --audit-log

```text
--audit-log   string   default: ./cudly-audit.jsonl
```

Every recommendation - whether purchased or dry-run - is written as a JSON line to the audit log file before any purchase API call is made. The audit record includes:

- Run ID (UUID that groups all purchases in a single invocation)
- Recommendation details (service, region, instance type, count, term, payment)
- Purchase result (success/failure, commitment ID, error message)
- Audit status (`skipped` for dry-run, `skipped_covered` for idempotency hits where an equivalent commitment was already purchased within the window, `success` or `error` for real purchases)
- Whether the run was a dry run
- Purchase source (`cli`)

cudly verifies that the audit log path is writable before making any cloud API calls. If it is not writable (e.g. the directory does not exist), the command exits immediately with an error.

```bash
# Write audit records to a shared directory
cudly --services rds --purchase --dry-run=false \
  --audit-log /var/log/cudly/audit.jsonl
```

The default path (`./cudly-audit.jsonl`) writes to the current working directory. In production deployments, redirect this to a durable, monitored location.

## Duplicate purchase prevention: --idempotency-window

```text
--idempotency-window   string   default: 24h
```

Before purchasing a recommendation, cudly checks whether an equivalent commitment was purchased within the idempotency window. If a match is found, the recommendation count is reduced by the already-purchased count. This prevents double-buying when:

- The tool is run multiple times in quick succession
- A partial batch succeeds and the run is retried

The window is expressed as a Go duration string (e.g. `24h`, `48h`, `1h30m`).

```bash
# Extend the window for long-running multi-account deployments
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
6. Use `--idempotency-window` consistent with your run frequency to avoid double-buying on retries.
