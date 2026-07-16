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
--include-accounts  Only include recommendations for these account names (comma-separated)
--exclude-accounts  Exclude recommendations for these account names (comma-separated)
```

Account names are matched against the friendly alias resolved from AWS Organizations (`organizations:DescribeAccount`, called per account ID and cached). If the alias cannot be resolved, the account ID is used as a fallback.

```bash
# Only process two accounts
cudly --services rds --include-accounts prod-account,staging-account

# Skip the sandbox account
cudly --services rds --exclude-accounts sandbox
```

### Region filters

```text
--include-regions  Only include recommendations for these AWS regions (comma-separated)
--exclude-regions  Exclude recommendations for these AWS regions (comma-separated)
```

When `--regions` is omitted, cudly enumerates all opted-in AWS regions via EC2 `DescribeRegions` and queries each of them; discovering regions from Cost Explorer recommendations is only a fallback used when that region listing fails. (Savings Plans are account-level and are always queried once via `us-east-1` when `--regions` is empty.) `--include-regions` and `--exclude-regions` are applied afterwards as a filter on the fetched recommendations (matching each recommendation's region), so they reduce the results but do not change which regions are queried. If both `--regions` and `--include-regions` are set, the queried regions come from `--regions` and the results are then narrowed by the include/exclude lists.

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

Engine names are matched against the engine field returned by the Cost Explorer recommendation (e.g. `mysql`, `postgresql`, `redis`, `memcached`, `aurora-postgresql`). Comparisons are case-insensitive.

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
--min-count <int>   Minimum instance count (0 = no filter)
```

Drop recommendations where the adjusted instance count (after coverage scaling) is below this number. Useful for ignoring one-off or very small recommendations that are not worth the operational overhead.

```bash
# Only purchase recommendations for 3 or more instances
cudly --services rds --min-count 3
```

### --min-savings-pct

```text
--min-savings-pct <float>   Minimum savings percentage (0 = no filter)
```

Drop recommendations whose estimated savings percentage falls below this threshold.

**Important naming distinction:** `--min-savings-pct` is a **percentage** value (e.g. `10` means "at least 10% savings"). This is different from the GUI and API `min_savings` parameter, which filters by **dollar amount**. A value of `30` in `--min-savings-pct` means "30% savings", but `min_savings=30` in the API means "$30 in savings". Mixing up the two by copying a CLI value into the GUI filter (or vice versa) produces silent, incorrect filtering.

```bash
# Only recommendations with at least 20% projected savings
cudly --services rds,elasticache --min-savings-pct 20
```

### --max-break-even-months

```text
--max-break-even-months <int>   Maximum break-even period in months (0 = no filter)
```

Drop recommendations where the break-even period exceeds this many months. A break-even period is the number of months until the upfront cost of the RI is recovered by the recurring discount. Recommendations without a computable break-even period (e.g. no-upfront) pass through unfiltered.

```bash
# Only commitments that break even within 18 months
cudly --services ec2 --max-break-even-months 18
```

### --max-instances

```text
--max-instances <int>   Hard cap on total instances purchased (0 = no limit)
```

Cap the total number of instances purchased across all recommendations after coverage scaling. Applied as a final cut after all other filters. This is a safety net to prevent unexpectedly large batch purchases, not a primary sizing control.

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
