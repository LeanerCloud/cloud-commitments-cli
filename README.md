# AWS Reserved Instance Helper Tool

A comprehensive tool for analyzing AWS Cost Explorer Reserved Instance recommendations and optionally purchasing Reserved Instances across multiple AWS services.

## Supported Services

- **Amazon RDS** - Database Reserved Instances
- **Amazon ElastiCache** - Cache Node Reserved Instances
- **Amazon EC2** - Compute Reserved Instances
- **Amazon OpenSearch** - Search Domain Reserved Instances
- **Amazon Redshift** - Reserved Nodes
- **Amazon MemoryDB** - Reserved Nodes

## Features

- Multi-service Reserved Instance recommendations from AWS Cost Explorer
- **CSV input mode** - Purchase RIs from previously generated CSV recommendations
- Configurable payment options (all-upfront, partial-upfront, no-upfront)
- Flexible terms (1 year or 3 years)
- Coverage percentage control per service
- Multi-region support (processes all AWS regions by default)
- Filtering by region, instance type, and engine
- Instance purchase limits
- Dry-run mode for testing (default)
- CSV export of recommendations and purchase results
- Detailed cost estimates and savings calculations

## Installation

```bash
go install github.com/LeanerCloud/rds-ri-purchase-tool/cmd@latest
```

Or build from source:

```bash
git clone https://github.com/LeanerCloud/rds-ri-purchase-tool.git
cd rds-ri-purchase-tool
go build -o ri-helper cmd/*.go
```

## Usage

### Basic Usage (Dry Run)

```bash
# Get recommendations for all services with default settings (3-year, no-upfront)
./ri-helper --all-services

# Get recommendations for specific services
./ri-helper --services rds,elasticache,ec2

# RDS only with 50% coverage
./ri-helper --services rds --coverage 50
```

### Advanced Options

```bash
# Process specific services with different coverage percentages
./ri-helper \
  --services rds,elasticache \
  --rds-coverage 50 \
  --elasticache-coverage 80 \
  --payment partial-upfront \
  --term 1

# All services with 1-year all-upfront RIs
./ri-helper \
  --all-services \
  --payment all-upfront \
  --term 1 \
  --coverage 75
```

### CSV Input Mode

Apply purchases from a previously generated CSV file:

```bash
# Dry-run with CSV input
./ri-helper --input-csv rds-recommendations.csv

# Apply 50% coverage with regional filtering
./ri-helper \
  --input-csv rds-recommendations.csv \
  --coverage 50 \
  --include-regions us-east-1,us-west-2

# Filter by instance type and engine
./ri-helper \
  --input-csv rds-recommendations.csv \
  --include-instance-types db.t3.small,db.r5.large \
  --include-engines postgres,mysql \
  --coverage 75

# Actual purchase from CSV (BE CAREFUL!)
./ri-helper \
  --input-csv rds-recommendations.csv \
  --purchase \
  --max-instances 10
```

### Actual Purchase Mode

⚠️ **WARNING**: This will purchase actual Reserved Instances!

```bash
# Purchase RIs based on recommendations (BE CAREFUL!)
./ri-helper \
  --services rds \
  --purchase \
  --payment no-upfront \
  --term 3 \
  --coverage 50
```

## Command-Line Flags

### General Options
| Flag | Description | Default |
|------|-------------|---------|
| `-i, --input-csv` | Input CSV file with recommendations to purchase | - |
| `-o, --output` | Output CSV file path | auto-generated |
| `--purchase` | Enable actual RI purchases (default is dry-run) | false |
| `--yes` | Skip confirmation prompt for purchases | false |

### Service Selection (Cost Explorer Mode)
| Flag | Description | Default |
|------|-------------|---------|
| `-s, --services` | Comma-separated list of services | rds |
| `--all-services` | Process all supported services | false |
| `-r, --regions` | AWS regions to process | all regions |

### Purchase Options
| Flag | Description | Default |
|------|-------------|---------|
| `-p, --payment` | Payment option (all-upfront, partial-upfront, no-upfront) | no-upfront |
| `-t, --term` | Term in years (1 or 3) | 3 |
| `-c, --coverage` | Coverage percentage (0-100) | 80 |
| `--max-instances` | Maximum total instances to purchase (0 = no limit) | 0 |

### Filtering Options
| Flag | Description | Default |
|------|-------------|---------|
| `--include-regions` | Only include these regions (comma-separated) | - |
| `--exclude-regions` | Exclude these regions (comma-separated) | - |
| `--include-instance-types` | Only include these instance types (validated) | - |
| `--exclude-instance-types` | Exclude these instance types (validated) | - |
| `--include-engines` | Only include these engines (e.g., 'redis,mysql') | - |
| `--exclude-engines` | Exclude these engines | - |

**Instance Type Validation**: Instance types are validated at two levels:
1. **CLI Validation** - Fast static validation against 400+ known instance types when parsing flags
2. **Runtime Validation** - Dynamic fetching from AWS APIs for the most up-to-date list (cached for 24 hours)

Use full instance type names:
- **RDS**: `db.t3.small`, `db.r5.large`, `db.m5.xlarge`, `db.t4g.medium`
- **ElastiCache**: `cache.t3.small`, `cache.r5.large`, `cache.m5.xlarge`, `cache.r6g.large`
- **EC2**: `t3.small`, `r5.large`, `m5.xlarge`, `c5.xlarge`
- **OpenSearch**: `t3.small.search`, `r5.large.search`, `m5.xlarge.search`
- **Redshift**: `dc2.large`, `dc2.8xlarge`, `ra3.4xlarge`, `ra3.16xlarge`
- **MemoryDB**: `db.t4g.small`, `db.r6g.large`, `db.r7g.xlarge`

The tool queries AWS service APIs to fetch valid instance types:
- **RDS**: `DescribeReservedDBInstancesOfferings`
- **ElastiCache**: `DescribeReservedCacheNodesOfferings`
- **EC2**: `DescribeInstanceTypeOfferings`
- **OpenSearch, Redshift, MemoryDB**: Static lists (comprehensive)

## Coverage Percentage

The coverage percentage allows you to purchase only a portion of the recommended Reserved Instances:

- **100%** - Purchase all recommended RIs
- **75%** - Purchase 75% of recommended instance counts
- **50%** - Purchase half of recommended instance counts
- **0%** - Skip this service entirely

This is useful for:
- Gradual RI adoption
- Maintaining flexibility for scaling
- Risk management
- Budget constraints

## Output Files

The tool generates CSV files with detailed information:

- `{service}-{term}y-{payment}-dryrun-{timestamp}.csv` - Dry run results
- `{service}-{term}y-{payment}-purchase-{timestamp}.csv` - Actual purchase results

Each CSV includes:
- Timestamp
- Status (SUCCESS/FAILED)
- Region
- Instance/Node details
- Payment option and term
- Instance count
- Estimated costs and savings
- Purchase IDs (for actual purchases)

## AWS Credentials

The tool uses standard AWS SDK credential chain:

1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
2. Shared credentials file (`~/.aws/credentials`)
3. IAM instance role (when running on EC2)

### Required IAM Permissions

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ce:GetReservationPurchaseRecommendation",
        "ce:GetReservationUtilization",
        "ce:GetReservationCoverage"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "rds:DescribeReservedDBInstancesOfferings",
        "rds:PurchaseReservedDBInstancesOffering",
        "elasticache:DescribeReservedCacheNodesOfferings",
        "elasticache:PurchaseReservedCacheNodesOffering",
        "ec2:DescribeReservedInstancesOfferings",
        "ec2:PurchaseReservedInstancesOffering",
        "es:DescribeReservedInstanceOfferings",
        "es:PurchaseReservedInstanceOffering",
        "redshift:DescribeReservedNodeOfferings",
        "redshift:PurchaseReservedNodeOffering",
        "memorydb:DescribeReservedNodesOfferings",
        "memorydb:PurchaseReservedNodesOffering"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeRegions"
      ],
      "Resource": "*"
    }
  ]
}
```

## Examples

### Example 1: Analyze RDS Recommendations

```bash
./ri-helper --services rds --coverage 50 --payment partial-upfront
```

Output:
```
🔍 DRY RUN MODE - No actual purchases will be made
📊 Processing services: RDS
💳 Payment option: partial-upfront, Term: 3 year(s)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🎯 Processing Amazon RDS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

🌍 Region: us-east-1
Found 5 RDS recommendations
After applying 50.0% coverage: 3 recommendations selected
...
```

### Example 2: Multi-Service Processing

```bash
./ri-helper \
  --services rds,elasticache,ec2 \
  --rds-coverage 50 \
  --elasticache-coverage 80 \
  --ec2-coverage 100 \
  --payment no-upfront \
  --term 1
```

### Example 3: Process All Regions for EC2

```bash
./ri-helper \
  --services ec2 \
  --coverage 75 \
  --payment all-upfront \
  --term 3
```

### Example 4: Purchase from CSV with Filters

```bash
# Generate recommendations first
./ri-helper --services rds --payment partial-upfront --term 3

# Review the CSV file, then purchase selectively
./ri-helper \
  --input-csv ri-helper-dryrun-20251007-123456.csv \
  --coverage 50 \
  --include-regions us-east-1,eu-west-1 \
  --exclude-instance-types db.t2.micro \
  --purchase
```

### Example 5: Limit Total Purchases

```bash
# Purchase at most 20 instances from recommendations
./ri-helper \
  --input-csv rds-recommendations.csv \
  --max-instances 20 \
  --purchase
```

### Example 6: Filter Instance Types

```bash
# Exclude small instance types
./ri-helper \
  --services rds,elasticache \
  --exclude-instance-types db.t2.micro,db.t2.small,cache.t2.micro \
  --payment partial-upfront \
  --term 3

# Only include specific instance families
./ri-helper \
  --services rds \
  --include-instance-types db.r5.large,db.r5.xlarge,db.r5.2xlarge \
  --coverage 75
```

## Safety Features

1. **Dry-run by default** - No purchases without explicit `--purchase` flag
2. **Confirmation prompts** - Interactive confirmation before actual purchases
3. **CSV input mode** - Review recommendations before purchasing
4. **Coverage control** - Purchase only what you need
5. **Instance limits** - Cap the total number of instances purchased
6. **Filtering** - Precise control over regions, instance types, and engines
7. **Duplicate prevention** - Automatically checks for existing RIs
8. **Detailed logging** - Track all operations
9. **CSV exports** - Audit trail of all recommendations and purchases

## Typical Workflow

1. **Generate recommendations** - Run in dry-run mode to get CSV:
   ```bash
   ./ri-helper --services rds --payment partial-upfront --term 3 --coverage 50
   ```

2. **Review CSV** - Examine the generated CSV file to understand recommendations

3. **Refine with filters** - Test with filters in dry-run using CSV input:
   ```bash
   ./ri-helper --input-csv ri-helper-dryrun-*.csv --include-regions us-east-1 --coverage 75
   ```

4. **Purchase** - Execute purchases from CSV:
   ```bash
   ./ri-helper --input-csv ri-helper-dryrun-*.csv --purchase --yes
   ```

## Development

### Running Tests

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test ./internal/rds/...
```

### Project Structure

```
.
├── cmd/                    # CLI implementation
│   ├── main.go            # Entry point
│   └── multi_service.go   # Multi-service orchestration
├── internal/
│   ├── common/            # Shared types and interfaces
│   ├── rds/               # RDS-specific implementation
│   ├── elasticache/       # ElastiCache implementation
│   ├── ec2/               # EC2 implementation
│   ├── opensearch/        # OpenSearch implementation
│   ├── redshift/          # Redshift implementation
│   ├── memorydb/          # MemoryDB implementation
│   ├── recommendations/  # Cost Explorer client
│   ├── csv/               # CSV export utilities
│   └── config/            # Configuration management
└── README.md
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

For issues, questions, or contributions, please open an issue on GitHub.

## Disclaimer

This tool can make actual Reserved Instance purchases when used with the `--actual-purchase` flag. Always verify recommendations and test in dry-run mode first. The authors are not responsible for any unintended purchases or financial commitments.