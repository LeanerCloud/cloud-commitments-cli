# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added

- `archera-comparison` command: a read-only, default-off comparison of an
  Archera commitment plan (table or JSON). See `docs/cli/archera-comparison.md`
  (#2156).

### Fixed

- Savings Plans purchases now send the hourly commitment truncated to 5 decimals
  (at least 2 shown) instead of rounded to cents, and accept amounts from 0.001
  to 1,000,000, so a 0.001 to 0.004 commitment is no longer rejected (library
  bump, #2235).
- `--target-coverage` with `--rebuy-window-days` no longer buys replacement EC2 and
  other non-RDS RIs again when they were already bought inside the coverage
  lookback: the expiring RIs are netted against those replacements (#2235).
- Recommendations with no account ID or name are now reported in the drop summary
  when an account filter (`--include-accounts` or `--exclude-accounts`) removes
  them (#1613).
- A dry run now refuses an EC2 Instance Savings Plan that has no instance family
  and no offering ID, the same way the real run does, instead of reporting
  success for a purchase the offering lookup would reject (#2164).
- A purchase result with `Success=true` and a non-nil `Error` is now treated as
  a failure in the Ctrl-C summary, the audit-stop message, the per-service
  success/failed counts and the non-zero exit. The CSV `Success` column keeps the
  raw provider flag; read the `Error` column alongside it (#2205).
- `configure gcp`: pressing Enter at a `[r]un, [S]kip (default)?` step now skips
  instead of creating the service account, granting roles, minting a key or
  running `gcloud auth login`; type `r` to run a step (#1613).
- `configure gcp` key cleanup: the minted key is now deleted when the upload
  panics, a second Ctrl-C aborts the cleanup, the local key file is removed
  first, and a possibly orphaned key is reported when `CreateKey` fails
  ambiguously (cancellation, timeout, 5xx or network error), with the `gcloud`
  commands to list and delete it (#2117).
- `--min-savings-pct` now rejects values below 0 or above 100 at validation.
  Previously a negative value was accepted as no filter and a value above 100
  silently dropped every recommendation (#2161).
- Replaying or retrying an `--input-csv` purchase no longer re-buys an
  ElastiCache or MemoryDB reservation that the first run already bought: CSV
  rows now carry the AWS provider, and MemoryDB rows use the engine AWS reports
  for existing nodes whatever the CSV says, so the duplicate check matches
  them. CSV Savings Plan rows also now match `--include-regions` and
  `--exclude-regions` on their plan region (#2228).
- `configure-gcp`: skipping (or giving an unknown answer at) the "Create Service
  Account" step no longer continues with the default
  `cudly-service-account@<project>` email that was never created. The wizard now
  asks for the existing service account email, validates it as a
  `*.gserviceaccount.com` address, and uses it for the role grant and key steps
  (#2211).
- When `--max-instances`, `--count-override` or the duplicate-RI check lowers a
  recommendation's count, `ProjectedCoverage` and `ProjectedUtilization` are
  re-derived for the new count instead of reporting the figures sized for the
  original count. This applies to RI rows with a usage signal;
  `ProjectedCoverage` is only re-derived when existing coverage is known.
  Savings Plans and projections that were never computed are left as they are.
  The Redshift `NumberOfNodes` now follows the count, including after
  `--coverage` and `--target-coverage` sizing. RDS `ProjectedCoverage` is a
  family-wide figure and is not re-derived yet; see issue #2214 (#2206).
- An explicit `--output` path that already exists (file, directory or symlink)
  is now refused during validation instead of being silently truncated. Choose a
  new path or remove the old file; scripts that reuse one path must do the same.
  Auto-generated filenames are unaffected (#2050).
- `--regions` now also narrows EC2 Instance Savings Plan recommendations to the listed
  regions by the plan's own region (previously only `--include-regions` did); a plan
  with no known region is dropped. Compute, SageMaker and Database plans stay
  unfiltered. In `--input-csv` mode `--regions` still does not narrow Savings
  Plans (rows are used as supplied).
- A `--purchase` run now stops, and exits 1, when an audit record cannot be
  written durably (disk full, log removed or made unwritable mid-run) instead
  of warning and buying more with no audit trail. The audit log is also probed
  before each real purchase, so a log that broke after the start-of-run check
  stops the run before buying. The report and summary for what already ran are
  still written, and the message says how many recommendations were attempted
  and to verify in AWS before re-running. Applies to the main pipeline and
  `--input-csv`; dry runs still only warn (#2103).

- Stop silently disabling the extended-support exclusion when AWS queries
  fail. A failed engine lifecycle query or region listing now aborts the run
  before any purchase; an RDS recommendation in a region whose instance
  inventory could not be read aborts it too, while other regions proceed with
  a warning. The queries are skipped entirely under `--include-extended-support`
  or when no RDS recommendations are in scope (#2147).
- Ctrl-C now cancels the whole invocation instead of only setting a flag that
  was polled between purchases. The engine-version lifecycle queries, region
  discovery, recommendation fetch, coverage fetch and duplicate checks treat a
  canceled or expired context as terminal (the run stops and buys nothing)
  instead of continuing with empty lifecycle data, "no recommendations" or
  un-deduplicated counts. A purchase already in flight is not aborted: it
  finishes, is written to the audit log (before the delay between purchases,
  which the `--input-csv` path used to run first), and the report and an
  "interrupted: N of M attempted" summary are still produced. Errored
  purchases in an interrupted run are flagged "verify in AWS before
  re-running". Further Ctrl-Cs are absorbed; Ctrl-\ force-quits and may lose
  the in-flight audit record (#2151).
- Reject NaN and infinite values for `--coverage`, `--target-coverage`,
  `--min-pool-size` and `--min-savings-pct` before any API call. NaN used to
  pass validation and silently switch `--target-coverage` runs to
  `--coverage` sizing (#2136).
- `--include-accounts` and `--exclude-accounts` are both exact now: they match
  the account name or ID exactly (case-insensitive, surrounding whitespace
  ignored) instead of by substring. Previously `--include-accounts dev` also selected `devops-prod`,
  so a purchase run could buy in accounts that were not named. Behavior change:
  anyone relying on substring matching must now list the full account names or
  IDs. An `--exclude-accounts` value that matches no account exactly but is
  part of account names now stops the run, naming those accounts, instead of
  being applied as a substring (#2237).

### Changed

- BREAKING: `--input-csv` now requires `Term` and `PaymentOption` columns in
  addition to `Service`, `Region`, `ResourceType` and `Count`. `Term` must be
  `1yr` or `3yr` and `PaymentOption` must be `all-upfront`, `partial-upfront`
  or `no-upfront` on every row. CSVs with only the older four columns, or with
  a blank or unknown value, now fail to load with an error naming the missing
  column or the row.
- Split the CLI out of the CUDly monorepo into its own module,
  `github.com/LeanerCloud/cloud-commitments-cli`. The shared client and
  provider code it depends on now lives in
  `github.com/LeanerCloud/cloud-commitments-go`, pinned in `go.mod`.
  History predating the split lives in the monorepo's changelog.
