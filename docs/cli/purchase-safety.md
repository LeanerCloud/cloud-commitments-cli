# Purchase Safety

CUDly is designed to be safe by default. Real purchases require an explicit `--purchase` opt-in, and several mechanisms - coverage limits, a duplicate-purchase check, RDS extended-support filtering, and a full audit trail - guard against unintended or repeated buys. See [Duplicate purchase prevention](#duplicate-purchase-prevention---idempotency-window) below for what that check does and does not cover.

## Automation and AI agents

Scripts and AI agents can run discovery, sizing, filtering and dry-run reports without confirmation. Real purchases require `--purchase` and an affirmative response at an interactive terminal, once for the whole run. Nonterminal stdin is refused, even if it contains `yes`.

The `--yes` bypass has been removed ([#1943](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1943)). Existing commands that pass it are rejected during command parsing. Hand dry-run recommendations to a human, who reviews the totals and runs `--purchase` at a terminal. This prompt is an operator safeguard, not a security boundary against software that can control a terminal.

## The purchase decision: --purchase

```text
--purchase  bool   default: false
```

Whether a run executes real purchases is controlled by a single flag:

```text
isDryRun = !ActualPurchase
```

A bare invocation is always a dry run; passing `--purchase` opts into real purchases, subject to interactive confirmation. This rule is identical in both cloud-fetch mode (the default) and CSV input mode (`--input-csv`).

| `--purchase` | Result |
|---|---|
| (not set / false) | Dry run - nothing purchased |
| `true` | Real purchases after interactive confirmation |

> **History:** earlier versions had a separate `--dry-run` flag. As a default-true flag it silently suppressed purchases even when `--purchase` was set (you had to pass `--purchase --dry-run=false` to actually buy - a footgun surfaced on #1364), and once its default was flipped to false it became a redundant "force dry-run even with `--purchase`" override that only muddied the contract. It has been removed in favour of the single `--purchase` control. Real purchases still require the interactive confirmation below.

```bash
# Dry run (the default - nothing is purchased):
cudly --services rds

# Execute real purchases (requires interactive confirmation):
cudly --services rds --purchase

# CSV mode behaves identically:
cudly --input-csv recs.csv --purchase
```

## Confirmation prompt

When running in purchase mode (`isDryRun=false`) at a terminal, cudly prints the total instance count and estimated monthly savings and prompts once before executing purchases. Enter `yes` or `y` to proceed; any other answer or an input error cancels the entire run. Input is case-insensitive. There is no flag to skip confirmation.

Dry runs never prompt. A purchase run with nonterminal stdin is canceled, so piping `yes` into the command does not authorize a purchase.

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

If the existing-commitments lookup itself fails (a transient API error), the two modes diverge: a dry run continues with a warning printed to the log (nothing is bought, so reporting fidelity wins), while a `--purchase` run refuses that (service, region) - it prints a "Refusing to purchase" line and buys nothing there, rather than falling back to the un-deduplicated counts ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)). The exception is `--rebuy-window-days` with `--target-coverage` on an RI service: there a failed existing-RI inventory read exits 1 first, on dry runs too (see [Scope](#scope)).

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

### Scope

Rebuy reads the existing-RI inventory of the credentials' own account only. The `--target-coverage` demand it sizes against can be broader: a payer or management account sees the usage of its member accounts. Netting one account's expiring RIs against another account's demand would be wrong, so a run with `--rebuy-window-days` and `--target-coverage` on an RI service refuses when it cannot show that the credentials' account owns all the in-scope usage. Savings Plans are never expiry-adjusted and are not checked.

The run exits 1 and writes no CSV, on dry runs and `--purchase` runs alike, in these cases:

- the credentials belong to a member account of an AWS Organization. Replacements bought centrally are not visible to a member account, so its expiry netting could buy them again. Gated rebuy runs from a standalone account, or from a management account whose member accounts have no in-scope usage;
- a recommendation row belongs to another account;
- a Cost Explorer coverage query (grouped by linked account, over the longer of `--coverage-lookback-days` and `--rec-lookback-period`, with the same service and, when `--regions` is set, region filters as sizing) shows usage in another account, so recommendations hidden behind your own account's rows are caught too;
- the check itself could not run: an unrecognized `--rec-lookback-period`, the STS identity lookup, the `organizations:DescribeOrganization` role check, the Cost Explorer scope query or the RI inventory read failed. Before this check, a failed RI inventory read on a dry run only warned and sized without the expiry netting; now it exits 1.

A scope refusal names the other account and the service. To recover, run from a standalone account or from a management account whose member accounts have no in-scope usage, or omit `--rebuy-window-days`. A check failure says "the scope check could not run" and the cause: fix access or retry, or omit `--rebuy-window-days`. A failed role check also asks you to grant `organizations:DescribeOrganization` (read-only); see [cloud-setup.md](cloud-setup.md#aws-permissions-for---rebuy-window-days). A Cost Explorer AccessDenied also asks you to grant `ce:GetReservationCoverage` or enable Cost Explorer access for the account. A Cost Explorer failure while building the coverage map on a dry run still only warns, as described above. `--input-csv` runs never apply rebuy.

Limits of the check, in both directions, for a management account that passes it:

- expiring RIs owned by member accounts are missing from the inventory, so the run can under-replace;
- a replacement bought by a member account and shared through RI sharing to the management account's usage is not credited against the management account's own expiring RIs, so the run can buy it again: an over-purchase. This is rarer than the member case the check refuses, and it cannot be detected from the management account.

Purchases for other accounts are still refused regardless.

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
3. If using `--target-coverage`, verify `--rebuy-window-days` is set appropriately for your RI renewal cadence, and that the credentials' account owns all in-scope usage (see [Scope](#scope)).
4. Narrow the scope with `--include-regions`, `--include-accounts`, or `--min-savings-pct` before buying across all services.
5. Consider `--max-instances` as a final safety cap for a first run. It limits instance-based reservations only; Savings Plans are exempt, so it does not cap their hourly commitment spend.
6. Set `--idempotency-window` to cover the time since the earlier run (e.g. `72h` for a re-run two days later), and note that a failed existing-commitments lookup makes a dry run proceed un-deduplicated with a warning, while a `--purchase` run refuses to purchase for that (service, region) instead ([#1941](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1941)); watch the log for either signal and check the audit log afterward. With `--rebuy-window-days` and `--target-coverage`, a failed RI inventory read exits 1 instead.
7. If an AI agent or other automation drives `cudly`, have it hand off the dry-run recommendation to a human, who reviews the totals and confirms `--purchase` at a terminal. See [Automation and AI agents](#automation-and-ai-agents).
