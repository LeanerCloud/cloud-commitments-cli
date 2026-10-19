# CUDly CLI Reference

This section documents the full CLI surface of the `cudly` binary. The Makefile builds it as `cudly`, while the cobra root command is named `ri-helper` (a legacy name that still appears in `--help` output and in auto-generated CSV filenames). The following cobra commands are available:

- **root** (`ri-helper`, invoked via the `cudly` binary) - the main RI/SP analysis and purchase command
- **configure-azure** - bootstrap Azure Service Principal credentials
- **configure-gcp** - bootstrap GCP Service Account credentials
- **archera-comparison** - read-only vendor comparison with optional filters
- **archera-line-items** - [discover vendor line-item UUIDs](archera-line-items.md)

## Topic pages

| Page | Covers |
|------|--------|
| [filtering.md](filtering.md) | Account, region, engine, instance-type, and Savings Plan type include/exclude filters; numeric threshold filters (min-count, min-savings-pct, max-break-even-months, min-pool-size, max-instances) |
| [purchase-safety.md](purchase-safety.md) | Purchase pipeline guardrails: dry-run, --purchase, interactive confirmation, --audit-log, --idempotency-window, --rebuy-window-days, DISABLE_PURCHASE_DELAY env |
| [cloud-setup.md](cloud-setup.md) | `configure-azure` and `configure-gcp` subcommands to bootstrap credentials for the [self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform) |

## Complete flag reference

All flags belong to the root command unless noted otherwise.

### Service selection

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--services` | `-s` | `rds` | Comma-separated list of services to process. Valid values: `rds`, `elasticache`, `ec2`, `opensearch`, `redshift`, `memorydb`, `savingsplans` (fans out to all four SP types), `savingsplans-compute`, `savingsplans-ec2instance`, `savingsplans-sagemaker`, `savingsplans-database`. The legacy alias `elasticsearch` maps to `opensearch`. |
| `--all-services` | | `false` | Process all supported services; equivalent to listing every service in `--services`. |
| `--regions` | `-r` | (all opted-in regions) | AWS regions to process (comma-separated or repeated). When empty, cudly enumerates all opted-in AWS regions via EC2 `DescribeRegions`; only if that listing fails does it fall back to discovering regions from Cost Explorer recommendations. Savings Plans are account-level, so they are always queried once via `us-east-1`; `--regions` then narrows EC2 Instance Savings Plans by their own region, while Compute, SageMaker and Database plans are unaffected. In `--input-csv` mode `--regions` does not narrow Savings Plans (rows are used as supplied). The `--include-regions` / `--exclude-regions` scoping filters are applied to the fetched recommendations afterwards (see [filtering.md](filtering.md)). |

### Coverage and sizing

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--coverage` | `-c` | `80` | Percentage (0-100) of each recommendation's instance count to purchase (`rec.Count * coverage/100`). Ignored when `--target-coverage` is also set. With `--input-csv`, omitting the flag buys each row's full `Count` (100%); any explicitly passed value, including `80`, is applied. |
| `--target-coverage` | `-u` | `0` (disabled) | Target percentage (0-100) of historical average hourly usage to cover with commitments. Sizes each recommendation to `floor(avg * target/100)`, leaving the remainder on-demand. Overrides `--coverage` when non-zero. Pairs with `--rebuy-window-days` and `--min-pool-size` (see [filtering.md](filtering.md)). |
| `--coverage-lookback-days` | | `30` | Calendar days of historical demand fed to `GetReservationCoverage` when computing the existing-RI coverage map for `--target-coverage` sizing. Match this to your AWS console coverage report window to reconcile cudly's `ExistingCoverage` column against the console export. Only affects `--target-coverage`. |
| `--override-count` | | `0` (disabled) | Replace every recommendation's count with this fixed number, rescaling that row's costs and savings by the same ratio so they describe the quantity that will actually be bought. Savings Plans are exempt and pass through unchanged: an SP commitment is priced in dollars per hour, not in instances. Rows with a non-positive count are exempt too, having no quantity to rescale from. Overriding above the quantity the provider's figures cover keeps the costs accurate but extrapolates the savings past the observed demand; all three cases are named on stdout. Applied before `--max-instances`. Useful when testing a specific purchase size. |
| `--max-instances` | | `0` (no limit) | Hard cap on the total number of instances purchased across all recommendations. Applied after coverage scaling, once per run across every service and region (not per service or per region). The cap is run-wide on both paths, but the two rank on different keys: recommendation-driven runs keep the highest savings *percentage*, while `--input-csv` rows are ranked on savings *per instance* (`EstimatedSavings / Count`), never on file order. A binding cap is refused if any row lacks an `EstimatedSavings` value to rank on. A recommendation the cap would truncate below `--min-count` is dropped instead. Does not apply to Savings Plans. See [filtering.md](filtering.md). |

### Purchase terms

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--payment` | `-p` | `no-upfront` | Payment option: `all-upfront`, `partial-upfront`, or `no-upfront`. Note: AWS does not offer 3-year no-upfront RDS RIs; cudly warns and skips RDS if that combination is selected. |
| `--term` | `-t` | `3` | Commitment term in years. Must be `1` or `3`. |

### Purchase execution and safety

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--purchase` | | `false` | Opt into real purchases, requiring confirmation at an interactive terminal. A bare run is always a dry run. Applies to both cloud-fetch and `--input-csv` modes. See [purchase-safety.md](purchase-safety.md). |
| `--audit-log` | | `./cudly-audit.jsonl` | Path to the JSONL audit log file. Written for every recommendation (dry-run and real). See [purchase-safety.md](purchase-safety.md). |
| `--idempotency-window` | | `24h` | Lookback window for duplicate purchase detection. A Go duration string that must be a positive whole number of hours (e.g. `24h`, `72h`); anything else is rejected at startup. See [purchase-safety.md](purchase-safety.md). |

### Recommendation quality filters

| Flag | Default | Description |
|------|---------|-------------|
| `--min-savings-pct` | `0` (no filter) | Drop recommendations whose estimated savings percentage is below this threshold. This is a **percentage** (e.g. `10` = 10%), not a dollar amount. See [filtering.md](filtering.md) for the naming distinction from the GUI `min_savings` dollar filter. |
| `--max-break-even-months` | `0` (no filter) | Drop recommendations whose break-even period exceeds this many months. `0` disables the filter. See [filtering.md](filtering.md). |
| `--min-count` | `0` (no filter) | Drop recommendations for fewer than this many instances. `0` disables the filter. Does not apply to Savings Plans. See [filtering.md](filtering.md). |

### Scoping filters

| Flag | Default | Description |
|------|---------|-------------|
| `--include-regions` | (all) | Only include recommendations for these AWS regions (comma-separated). Conflicts with any region also listed in `--exclude-regions`. |
| `--exclude-regions` | (none) | Exclude recommendations for these AWS regions (comma-separated). |
| `--include-instance-types` | (all) | Only include these instance types (e.g. `db.t3.micro,cache.t3.small`). Must contain a `.` separator; validated at startup. |
| `--exclude-instance-types` | (none) | Exclude these instance types. Same format as `--include-instance-types`. |
| `--include-engines` | (all) | Only include recommendations for these database engines (e.g. `redis,mysql,postgresql`). |
| `--exclude-engines` | (none) | Exclude recommendations for these database engines. |
| `--include-accounts` | (all) | Only include recommendations for these accounts, matched exactly (case-insensitive) on account name or ID (comma-separated). |
| `--exclude-accounts` | (none) | Exclude recommendations for these accounts, matched exactly (case-insensitive) on account name or ID. A value that only partly matches account names stops the run. |
| `--include-sp-types` | (all) | Only include these Savings Plan types: `Compute`, `EC2Instance`, `SageMaker`, `Database`. |
| `--exclude-sp-types` | (none) | Exclude these Savings Plan types. |
| `--sp-account-scope` | `linked` | Cost Explorer account scope for Savings Plan recommendations: `linked` or `payer`. A management account may only get recommendations at `payer` scope. |
| `--include-extended-support` | `false` | By default, instances running engine versions in AWS Extended Support are excluded because Extended Support surcharges can erase RI savings. Pass this flag to include them. |

### Target-coverage helpers

| Flag | Default | Description |
|------|---------|-------------|
| `--rebuy-window-days` | `0` (disabled) | When set, treat existing RIs whose remaining term is at most this many days as already uncovered, so `--target-coverage` sizes replacements before they expire. `0` fully trusts existing coverage. See [purchase-safety.md](purchase-safety.md). |
| `--min-pool-size` | `0` (no filter) | When set, drop RI recommendations for pools whose `AverageInstancesUsedPerHour` is below this threshold. Prevents integer-arithmetic over-coverage on tiny pools (e.g. average=1 cannot approximate 80% coverage without effectively buying 100%). SPs and recommendations without a per-hour signal pass through unfiltered. See [filtering.md](filtering.md). |

### Input / output

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | `-o` | auto-generated | Output CSV file path. Auto-name format: `ri-helper-dryrun-YYYYMMDD-HHMMSS.csv` or `ri-helper-purchase-YYYYMMDD-HHMMSS.csv`. An explicit path that already exists (file, directory or symlink) is refused instead of overwritten; choose a new path or remove the old file. The `Success` column is the raw flag the provider returned; a provider can report `true` together with an error, so read the `Error` column too (a row with an error counts as failed in the run summary). |
| `--input-csv` | `-i` | (none) | Read recommendations from an existing CSV instead of fetching from cloud APIs. The file must have a `.csv` extension and exist at the path given. The header must contain `Service`, `Region`, `ResourceType`, `Count`, `Term` and `PaymentOption`; a missing column is a load error that names it. `Term` must be `1yr` or `3yr` and `PaymentOption` must be `all-upfront`, `partial-upfront` or `no-upfront` on every row; a blank or unknown value fails the load with the row number. CSVs with only the older four columns are rejected. |

### Authentication

| Flag | Default | Description |
|------|---------|-------------|
| `--profile` | (AWS default chain) | AWS profile to use for all API calls. Falls back to the standard SDK chain if not set. |
| `--validation-profile` | (same as `--profile`) | AWS profile to use specifically for querying running instances to validate engine versions. Required for extended-support filtering in multi-account orgs where the main profile may not have cross-account describe permissions. |

### Subcommand flags

See [cloud-setup.md](cloud-setup.md) for `configure-azure` and `configure-gcp` flags.
