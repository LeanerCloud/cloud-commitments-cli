# Filtering

CUDly supports two categories of filters on the root command:

1. **Scoping filters** - include/exclude subsets of recommendations by account, region, engine, instance type, or Savings Plan type.
2. **Threshold filters** - drop recommendations that do not meet minimum quality or size bars.

All filters are evaluated before any purchase is attempted. Filtered-out recommendations are listed in the terminal output under an "Excluded" section, so you can verify that the right rows were dropped.

## Scoping filters

Scoping filters operate on include/exclude list pairs. For region, instance-type, and engine pairs, an item that appears in both lists is rejected at startup with an error:

> `region 'us-east-1' cannot be both included and excluded`

The conflict check is NOT applied to `--include-accounts`/`--exclude-accounts` or `--include-sp-types`/`--exclude-sp-types`; for those, a name listed in both lists silently behaves as excluded (exclude wins). Avoid listing the same value in both lists.

### Account filters

```text
--include-accounts  Only include recommendations for these accounts, matched exactly (case-insensitive) on account name or ID (comma-separated)
--exclude-accounts  Exclude recommendations for these accounts, matched exactly (case-insensitive) on account name or ID; a value that only partly matches account names stops the run (comma-separated)
```

Each filter value is matched against the account ID and against the account name. The match is exact and case-insensitive, after trimming surrounding whitespace; it is never a substring match, so `dev` does not select `devops-prod`. The name is the friendly name from AWS Organizations (`organizations:DescribeAccount`, called once per account ID per run). Member accounts usually cannot call it, so filter by account ID there.

An `--exclude-accounts` value that matches no account exactly but is part of one or more account names (for example `prod` with accounts `prod-eu` and `prod-us`) stops the run with an error naming those accounts: `--exclude-accounts "prod" matches no account exactly but is part of "prod-eu", "prod-us"; list the full account names to exclude`. A value that matches nothing at all is ignored.

When a filter value is a name (anything other than a 12-digit account ID) and an account's name cannot be resolved, cudly stops with an error instead of guessing: carrying on would treat an account you excluded as "not excluded". ID-only filters never need the name. The same rule applies to `--input-csv` replay, where the name comes from the CSV's `AccountName` column; a row whose name column holds the account ID counts as unresolved.

A recommendation with neither an account ID nor an account name (for example a CSV row with empty `Account` and `AccountName`) cannot be placed by an account filter, so it is dropped under either `--include-accounts` or `--exclude-accounts`. On live runs the end-of-run drop summary itemises it as `unidentified-account (--include-accounts)` or `unidentified-account (--exclude-accounts)` (the include label when both filters are set), provided it passed every other filter. On `--input-csv` runs there is no drop summary, so such rows are counted only in the "filtered out N" line.

```bash
# Only process two accounts
cudly --services rds --include-accounts prod-account,staging-account

# Skip the account named sandbox-account
cudly --services rds --exclude-accounts sandbox-account
```

### Region filters

```text
--include-regions  Only include recommendations for these AWS regions (comma-separated)
--exclude-regions  Exclude recommendations for these AWS regions (comma-separated)
```

When `--regions` is omitted, cudly takes the regions from the Cost Explorer recommendations themselves (not from EC2 `DescribeRegions`), so recommendations in regions that only a linked account has opted into are included. Savings Plans are account-level and are always queried once via `us-east-1`; `--regions`, `--include-regions` and `--exclude-regions` narrow only EC2 Instance Savings Plans, which are tied to a region (matched on the plan's own region; one with no known region is dropped when `--regions` is set); Compute, SageMaker and Database plans are region-agnostic and skip the region filters. In `--input-csv` mode `--regions` still does not narrow Savings Plans (rows are used as supplied). `--include-regions` and `--exclude-regions` are applied afterwards as a filter on the fetched recommendations (matching each recommendation's region), so they reduce the results but do not change which regions are queried. If both `--regions` and `--include-regions` are set, the queried regions come from `--regions` and the results are then narrowed by the include/exclude lists.

```bash
# Only US and EU recommendations
cudly --services ec2 --include-regions us-east-1,us-west-2,eu-west-1

# Exclude a region you're migrating away from
cudly --services rds --exclude-regions ap-southeast-1
```

### Engine filters

```text
--include-engines  Only include these database engines (comma-separated)
--exclude-engines  Exclude these database engines (comma-separated)
```

Engine names are matched against the engine field returned by the Cost Explorer recommendation (e.g. `mysql`, `postgresql`, `redis`, `memcached`, `aurora-postgresql`). Comparisons are case-insensitive. EC2 recommendations match on the platform string, so `--exclude-engines windows` excludes `Windows` but does not match `Windows with SQL Server`; list each platform you want to exclude.

```bash
# Only Redis ElastiCache RIs
cudly --services elasticache --include-engines redis

# Skip MySQL (planning to migrate)
cudly --services rds --exclude-engines mysql
```

### Instance-type filters

```text
--include-instance-types  Only include these instance types (comma-separated)
--exclude-instance-types  Exclude these instance types (comma-separated)
```

Instance type strings must contain a `.` separator (e.g. `db.t3.micro`). cudly validates the format at startup. Matching is by exact string equality - prefix or family matching is not supported, so every instance type you want included or excluded must be listed in full (e.g. `db.t2.micro,db.t2.small`, not `db.t2`).

```bash
# Only modern graviton RDS instances
cudly --services rds --include-instance-types db.r8g.large,db.r8g.xlarge

# Skip deprecated t2 instances
cudly --services rds,elasticache --exclude-instance-types db.t2.micro,db.t2.small,cache.t2.micro
```

### Savings Plan type filters

```text
--include-sp-types  Only include these Savings Plan types (comma-separated)
--exclude-sp-types  Exclude these Savings Plan types (comma-separated)
```

Valid type values: `Compute`, `EC2Instance`, `SageMaker`, `Database`.

These filters apply only when `--services` includes `savingsplans` (or a specific SP slug). They have no effect on RI-based services.

```bash
# Compute and EC2Instance SPs only
cudly --services savingsplans --include-sp-types Compute,EC2Instance

# All SPs except SageMaker
cudly --services savingsplans --exclude-sp-types SageMaker
```

### Extended support filter

```text
--include-extended-support  Include instances on Extended Support engine versions (default: excluded)
```

AWS Extended Support applies additional per-vCPU-hour charges to database engine versions past their standard end-of-life date (e.g. MySQL 5.7, PostgreSQL 11). By default cudly excludes recommendations for instances running these versions because the surcharges can eliminate or reverse the RI savings.

When `--include-extended-support` is set, this exclusion is skipped and all instances are considered regardless of their engine version status.

Extended-support detection requires querying running instances across your accounts. Use `--validation-profile` to supply an AWS profile with cross-account `rds:DescribeDBInstances` permissions if the main profile does not have them.

```bash
# Include extended-support instances (you plan to upgrade before the RI term ends)
cudly --services rds --include-extended-support

# Use a separate read-only profile for cross-account validation
cudly --services rds --validation-profile org-reader
```

## Threshold filters

Threshold filters drop individual recommendations that fall below a quality or size bar. They are applied after scoping filters and before purchase.

### --min-count

```text
--min-count <int>   Minimum instance count (0 = no filter; does not apply to Savings Plans)
```

Drop recommendations where the adjusted instance count (after coverage scaling) is below this number. Useful for ignoring one-off or very small recommendations that are not worth the operational overhead. Savings Plans are priced in dollars per hour, not instances, so the floor does not apply to them.

The floor applies on both paths, including `--input-csv`: a row already below it is dropped, and a row `--max-instances` would truncate below it is dropped rather than purchased short. On `--input-csv` runs it is re-checked after existing matching commitments are deducted, so a row whose remaining count falls under the floor is dropped rather than purchased at the smaller size.

```bash
# Only purchase recommendations for 3 or more instances
cudly --services rds --min-count 3
```

### --min-savings-pct

```text
--min-savings-pct <float>   Minimum savings percentage (0 = no filter)
```

Drop recommendations whose estimated savings percentage falls below this threshold. The value must be between 0 and 100.

**Important naming distinction:** `--min-savings-pct` is a **percentage** value (e.g. `10` means "at least 10% savings"). This is different from the GUI and API `min_savings` parameter, which filters by **dollar amount**. A value of `30` in `--min-savings-pct` means "30% savings", but `min_savings=30` in the API means "$30 in savings". Mixing up the two by copying a CLI value into the GUI filter (or vice versa) produces silent, incorrect filtering.

**On `--input-csv` runs** the filter reads the `SavingsPercentage` column of the report (a percent, so `25.5` is 25.5%), which the CSV report writer emits. A CSV without that column (a report written by an older version), or a row with that cell blank, fails the run with an error naming the flag instead of treating the missing figure as 0%. Add the column or drop the flag. Dropped rows are named on stdout.

```bash
# Only recommendations with at least 20% projected savings
cudly --services rds,elasticache --min-savings-pct 20
```

### --max-break-even-months

```text
--max-break-even-months <int>   Maximum break-even period in months (0 = no filter)
```

Drop recommendations where the break-even period exceeds this many months. A break-even period is the number of months until the upfront payment is recovered by the recurring discount:

```text
gross monthly savings = EstimatedSavings + upfront / term months
break-even months     = upfront / gross monthly savings
```

`EstimatedSavings` is already net of the amortized upfront, so the term amortization is added back before dividing. A provider-supplied break-even wins over the formula. No-upfront recommendations (upfront 0) have a break-even of 0 and always pass. A recommendation with an upfront payment whose break-even cannot be computed (negative or non-finite money, or a term other than 1 or 3 years) is dropped, with the reason in the drop summary, because the limit cannot be enforced on it. If gross monthly savings are not positive (net `EstimatedSavings` at or below `-upfront / term months`) the break-even is infinite and the recommendation is dropped. Net savings of 0 give a break-even equal to the term.

**On `--input-csv` runs** the filter reads the `BreakEvenMonths` column of the report (months; `0` for a no-upfront row), which the CSV report writer emits. A blank cell means the writer could not state a finite break-even. A CSV without the column (a report written by an older version), or a row with that cell blank, fails the run with an error naming the flag instead of treating the missing figure as passing. Add the column or drop the flag. Dropped rows are named on stdout.

```bash
# Only commitments that break even within 18 months
cudly --services ec2 --max-break-even-months 18
```

### --max-instances

```text
--max-instances <int>   Hard cap on total instances purchased (0 = no limit; does not apply to Savings Plans)
```

Cap the total number of instances purchased across all recommendations after coverage scaling. Applied as a final cut after all other filters. This is a safety net to prevent unexpectedly large batch purchases, not a primary sizing control. It does not apply to Savings Plans: they are priced in dollars per hour, so they neither count against the cap nor get reduced or dropped by it.

The cap covers the whole run on both code paths: it is applied once to the combined set of recommendations, never once per service or per region.

Which recommendations survive depends on the path:

- **Default (recommendation-driven) runs** apply the cap after scoring, so the surviving instances are the highest-savings-percentage ones across every service and region. Recommendations the cap reduces or drops are listed by name before the confirmation prompt and counted in the end-of-run drop summary, so a capped run never shrinks silently.
- **`--input-csv` runs** apply the same cap but rank on a different key, because the cap is a budget in instances and older CSVs carry no savings percentage. Rows are ordered by **savings per instance** (`EstimatedSavings / Count`), whatever `SavingsPercentage` holds, so the budget buys the rows returning the most per instance rather than the rows whose dollar total happens to be largest, and file position is irrelevant. Reduced and dropped rows are named on stdout, alongside the ranking rule that selected them.

  Because that ordering is the only thing deciding what gets bought, a run whose cap actually binds is **refused** when any surviving row has no usable `EstimatedSavings` value. A blank cell, or a file written without the column at all, loads as `0` and is indistinguishable from a row genuinely worth $0: capping on it would silently select by instance-type name. Populate `EstimatedSavings` on every row, or drop `--max-instances` and cap the file itself. A cap that does not bind chooses nothing and is never refused.

If the cap would truncate a recommendation below `--min-count`, that recommendation is dropped rather than purchased at the smaller size, because `--min-count` is a floor rather than a preference. The freed budget is not reallocated to the next recommendation. This holds on both paths.

```bash
# Never purchase more than 100 instances in a single run
cudly --services rds --max-instances 100
```

### --min-pool-size

```text
--min-pool-size <float>   Minimum pool AverageInstancesUsedPerHour (0 = no filter)
```

When set, drop RI recommendations for pools whose `AverageInstancesUsedPerHour` (as reported by Cost Explorer) is below this threshold. This filter is specifically useful when using `--target-coverage` on small pools.

**Why this matters with `--target-coverage`:** integer arithmetic forces over-coverage on tiny pools. For example, a pool with an average of 1 instance cannot approximate 80% target coverage - the only valid recommendation counts are 0 (0% coverage) or 1 (100% coverage). Skipping these pools avoids systematically over-buying on pools where the target cannot be approximated.

SPs and any recommendations that do not carry a per-hour usage signal pass through this filter unaffected.

```bash
# Skip pools smaller than 5 average instances/hour when using target-coverage
cudly --services rds \
  --target-coverage 80 \
  --min-pool-size 5
```

## Coverage sizing

`--coverage <percent>` scales each recommendation's count by the percentage and rounds the result **down**. A row whose count rounds to 0 is dropped and counted in the drop summary as `--coverage-sized-to-zero`. For example, `--coverage 50` turns a count of 1 into 0 and drops it, and a count of 3 into 1. Money fields are scaled by the same ratio as the count. `--target-coverage` sizing reports its own `target-sized-to-zero` reason.

## Combining filters

All filters compose with AND semantics: a recommendation must pass every active filter to be included.

```bash
# Conservative multi-filter example:
# - Only US regions
# - Only modern instance families
# - Minimum 5-instance pools
# - At least 15% savings
# - Break-even within 24 months
# - Cap at 200 total instances
cudly --services rds \
  --include-regions us-east-1,us-west-2 \
  --exclude-instance-types db.t2.micro,db.t2.small \
  --min-pool-size 5 \
  --min-savings-pct 15 \
  --max-break-even-months 24 \
  --max-instances 200 \
  --target-coverage 80
```
