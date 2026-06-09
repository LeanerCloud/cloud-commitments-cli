# CUDly - Multi-Cloud Commitment & Usage Discount Manager

[![License: OSL-3.0](https://img.shields.io/badge/License-OSL--3.0-blue.svg)](https://opensource.org/licenses/OSL-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev/)

CUDly is a comprehensive CLI tool for managing cloud cost commitments across AWS, Azure, and GCP. It helps organizations optimize cloud spending by automating the discovery, analysis, and purchase of multiple Reserved Instances, Savings Plans, and Committed Use Discounts by running a single command.

## Key Features

- **Multi-Cloud Support** - Unified interface for AWS (production), Azure (experimental), and GCP (experimental)
- **Intelligent Recommendations** - Fetches and analyzes commitment recommendations from cloud provider APIs
- **Safe Purchase Automation** - Execute purchases with built-in safety controls (dry-run by default)
- **Flexible Coverage Control** - Purchase only a percentage of recommendations for gradual adoption
- **CSV Workflow** - Generate recommendations, review offline, then execute purchases
- **Advanced Filtering** - Filter by region, instance type, engine, and account
- **Comprehensive Reporting** - Detailed cost estimates, savings calculations, and audit trails

## Supported Cloud Providers & Services

### AWS Services

| Service | Commitment Type | Description |
|---------|----------------|-------------|
| Amazon RDS | Reserved Instances | MySQL, PostgreSQL, MariaDB, Oracle, SQL Server, Aurora |
| Amazon ElastiCache | Reserved Nodes | Redis, Memcached |
| Amazon EC2 | Reserved Instances | All instance families |
| Amazon OpenSearch | Reserved Instances | Search domain instances |
| Amazon Redshift | Reserved Nodes | DC2 and RA3 node types |
| Amazon MemoryDB | Reserved Nodes | Memory-optimized nodes |
| Savings Plans | Hourly Commitments | Compute, EC2 Instance, SageMaker, Database |

### Azure Services (Experimental)

| Service | Commitment Type |
|---------|----------------|
| Azure SQL Database | Reserved Capacity |
| Azure Virtual Machines | Reserved Instances |
| Azure Cache for Redis | Reserved Capacity |
| Azure Cosmos DB | Reserved Capacity |
| Azure Cognitive Search | Reserved Capacity |

### GCP Services (Experimental)

| Service | Commitment Type |
|---------|----------------|
| Compute Engine | Committed Use Discounts |
| Cloud SQL | Committed Use Discounts |
| Memorystore | Committed Use Discounts |
| Cloud Storage | Committed Use Discounts |

### AWS CLI Support Matrix

**Tested** means the service has been exercised end-to-end with real AWS
accounts and validated in production workloads. **Experimental** means the
implementation exists and is functional, but needs real-world validation --
contributions and testers are very welcome.

| AWS Service | CLI Flag | Status |
| ----------- | -------- | ------ |
| Amazon RDS | `rds` | **Tested** |
| Amazon ElastiCache | `elasticache` | **Tested** |
| Amazon EC2 (Reserved Instances) | `ec2` | Experimental (seeking testers) |
| Amazon OpenSearch | `opensearch` | Experimental (seeking testers) |
| Amazon Redshift | `redshift` | Experimental (seeking testers) |
| Amazon MemoryDB | `memorydb` | Experimental (seeking testers) |
| Savings Plans (Compute, EC2 Instance, SageMaker, Database) | `savingsplans` | Experimental (seeking testers) |

## Installation

### From Source

```bash
git clone https://github.com/LeanerCloud/CUDly.git
cd CUDly
go build -o cudly cmd/*.go
```

### Using Go Install

```bash
go install github.com/LeanerCloud/CUDly/cmd@latest
```

## Quick Start

### 1. Get Recommendations (Dry Run)

```bash
# Get RDS recommendations with default settings (3-year, no-upfront, 80% coverage)
./cudly --services rds

# Get recommendations for multiple services
./cudly --services rds,elasticache,ec2

# Get recommendations for all supported services
./cudly --all-services
```

### 2. Review and Refine

```bash
# Apply filters to narrow down recommendations
./cudly --services rds \
  --include-regions us-east-1,eu-west-1 \
  --exclude-instance-types db.t2.micro \
  --coverage 50
```

### 3. Execute Purchases

```bash
# Purchase from generated CSV (requires explicit --purchase flag)
./cudly --input-csv cudly-dryrun-*.csv --purchase

# Skip confirmation prompt
./cudly --input-csv cudly-dryrun-*.csv --purchase --yes
```

## Command Reference

### Service Selection

| Flag | Description | Default |
|------|-------------|---------|
| `-s, --services` | Comma-separated service list (rds,elasticache,ec2,opensearch,redshift,memorydb,savingsplans) | rds |
| `--all-services` | Process all supported services | false |

### Purchase Configuration

| Flag | Description | Default |
|------|-------------|---------|
| `-p, --payment` | Payment option: `all-upfront`, `partial-upfront`, `no-upfront` | no-upfront |
| `-t, --term` | Term in years: `1` or `3` | 3 |
| `-c, --coverage` | Coverage percentage (0-100) — % of each recommendation's instance count to purchase | 80 |
| `-u, --target-coverage` | Target % (0-100) of historical demand to cover with commitments; the rest spills to on-demand. Sizes counts so projected coverage approximates target, projected utilization stays near 100%. Overrides `--coverage`. | 0 (disabled) |
| `--max-instances` | Maximum instances to purchase (0 = unlimited) | 0 |
| `--override-count` | Override recommended count with specific value | 0 |

> **`--coverage` vs `--target-coverage`**: two related but distinct
> sizing levers. `--coverage` scales each AWS recommendation's instance
> count by a fixed fraction (`rec.Count * coverage/100`).
> `--target-coverage` sizes against historical average hourly usage
> instead (`floor(avg * target/100)`), so the resulting count reflects
> real demand rather than AWS's recommended count. Both lean the same
> direction (higher value = more RIs, lower value = fewer), but
> `--target-coverage` is the right lever when the historical-usage
> signal is what you want to size by and you're explicitly leaving
> on-demand headroom for growth or bursts.

### Execution Control

| Flag | Description | Default |
|------|-------------|---------|
| `--purchase` | Execute actual purchases (dry-run by default) | false |
| `--yes` | Skip confirmation prompts | false |
| `-i, --input-csv` | Input CSV file with recommendations | - |
| `-o, --output` | Output CSV file path | auto-generated |

### Filtering

| Flag | Description |
|------|-------------|
| `--include-regions` | Only include these regions |
| `--exclude-regions` | Exclude these regions |
| `--include-instance-types` | Only include these instance types |
| `--exclude-instance-types` | Exclude these instance types |
| `--include-engines` | Only include these database engines |
| `--exclude-engines` | Exclude these database engines |
| `--include-accounts` | Only include these account names |
| `--exclude-accounts` | Exclude these account names |
| `--include-extended-support` | Include instances on extended support engine versions (see below) |
| `--include-sp-types` | Only include these Savings Plan types (Compute, EC2Instance, SageMaker, Database) |
| `--exclude-sp-types` | Exclude these Savings Plan types |

### Extended Support Filtering

By default, CUDly excludes instances running on database engine versions that are in AWS Extended Support. This is because Extended Support incurs additional per-vCPU-hour charges that may offset RI savings.

For example, MySQL 5.7 and PostgreSQL 11 are in Extended Support. Instances running these versions are automatically excluded from RI recommendations.

**Note:** This feature requires the `--validation-profile` flag to specify an AWS profile with permissions to describe RDS instances across all member accounts in your organization.

```bash
# Extended support filtering with validation profile
./cudly --services rds --validation-profile my-org-reader-profile

# Include extended support instances (skip filtering)
./cudly --services rds --include-extended-support
```

This is useful if you plan to upgrade the database version before the RI term ends, or if the Extended Support charges are acceptable for your use case.

### Duplicate Purchase Prevention

CUDly automatically checks for Reserved Instances purchased within the last 24 hours and adjusts recommendations to avoid duplicate purchases. This is useful when running the tool multiple times in quick succession or when recovering from partial purchase failures.

For example, if you purchase 5 db.r6g.large RIs and run CUDly again within 24 hours, those 5 instances will be subtracted from the recommendation count to prevent double-purchasing.

### Authentication

| Flag | Description |
|------|-------------|
| `--profile` | AWS profile to use |
| `--validation-profile` | AWS profile for instance type validation |

## Usage Examples

### Example 1: Conservative RDS Adoption

Purchase 50% of 1-year partial-upfront RDS recommendations:

```bash
./cudly --services rds \
  --payment partial-upfront \
  --term 1 \
  --coverage 50
```

### Example 2: Multi-Service with Different Coverage

Apply different coverage percentages per service:

```bash
./cudly \
  --services rds,elasticache,ec2 \
  --rds-coverage 50 \
  --elasticache-coverage 80 \
  --ec2-coverage 100 \
  --payment no-upfront \
  --term 3
```

### Example 3: Regional Focus

Only process specific regions with instance limits:

```bash
./cudly --services ec2 \
  --include-regions us-east-1,us-west-2 \
  --max-instances 50 \
  --payment all-upfront \
  --term 3
```

### Example 4: CSV-Based Workflow

```bash
# Step 1: Generate recommendations
./cudly --all-services --output recommendations.csv

# Step 2: Review CSV file externally

# Step 3: Purchase with filters
./cudly \
  --input-csv recommendations.csv \
  --include-regions us-east-1 \
  --exclude-instance-types db.t2.micro,cache.t2.micro \
  --coverage 75 \
  --purchase
```

### Example 5: Exclude Small Instances

```bash
./cudly --services rds,elasticache \
  --exclude-instance-types db.t2.micro,db.t2.small,db.t3.micro,cache.t2.micro \
  --payment partial-upfront \
  --term 3
```

### Example 6: Database Savings Plans Only

```bash
# Get only Database Savings Plans recommendations
./cudly --services savingsplans \
  --include-sp-types Database \
  --term 1 \
  --coverage 80
```

### Example 7: Exclude SageMaker Savings Plans

```bash
# Get all Savings Plans except SageMaker
./cudly --services savingsplans \
  --exclude-sp-types SageMaker \
  --term 3
```

## Coverage Percentage

The coverage percentage controls what portion of recommendations to act on:

| Coverage | Description | Use Case |
|----------|-------------|----------|
| 100% | All recommended instances | Maximum savings, stable workloads |
| 75% | Three-quarters of recommendations | Balanced approach |
| 50% | Half of recommendations | Conservative adoption |
| 25% | Quarter of recommendations | Testing/validation |
| 0% | Skip service entirely | Exclude from processing |

## Safety Features

CUDly includes multiple safety mechanisms to prevent unintended purchases:

1. **Dry-run by default** - No purchases without explicit `--purchase` flag
2. **Interactive confirmation** - Prompts before actual purchases (unless `--yes`)
3. **CSV workflow** - Review recommendations before purchasing
4. **Coverage control** - Purchase only what you need
5. **Instance limits** - Cap total purchases with `--max-instances`
6. **Duplicate prevention** - Checks for existing commitments
7. **Instance type validation** - Validates against known types
8. **Detailed logging** - Full audit trail of operations
9. **CSV exports** - Permanent record of all recommendations and purchases

## Cloud Provider Authentication

### AWS

CUDly uses the standard AWS SDK credential chain:

1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`)
2. Shared credentials file (`~/.aws/credentials`)
3. AWS config file (`~/.aws/config`)
4. IAM instance role (EC2/ECS)

#### Required IAM Permissions

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "CostExplorer",
      "Effect": "Allow",
      "Action": [
        "ce:GetReservationPurchaseRecommendation",
        "ce:GetReservationUtilization",
        "ce:GetReservationCoverage",
        "ce:GetSavingsPlansPurchaseRecommendation"
      ],
      "Resource": "*"
    },
    {
      "Sid": "ReservedInstanceOperations",
      "Effect": "Allow",
      "Action": [
        "rds:DescribeReservedDBInstancesOfferings",
        "rds:DescribeReservedDBInstances",
        "rds:PurchaseReservedDBInstancesOffering",
        "elasticache:DescribeReservedCacheNodesOfferings",
        "elasticache:DescribeReservedCacheNodes",
        "elasticache:PurchaseReservedCacheNodesOffering",
        "ec2:DescribeReservedInstancesOfferings",
        "ec2:DescribeReservedInstances",
        "ec2:PurchaseReservedInstancesOffering",
        "es:DescribeReservedInstanceOfferings",
        "es:DescribeReservedInstances",
        "es:PurchaseReservedInstanceOffering",
        "redshift:DescribeReservedNodeOfferings",
        "redshift:DescribeReservedNodes",
        "redshift:PurchaseReservedNodeOffering",
        "memorydb:DescribeReservedNodesOfferings",
        "memorydb:DescribeReservedNodes",
        "memorydb:PurchaseReservedNodesOffering",
        "savingsplans:DescribeSavingsPlans",
        "savingsplans:CreateSavingsPlan"
      ],
      "Resource": "*"
    },
    {
      "Sid": "RegionDiscovery",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeRegions",
        "ec2:DescribeInstanceTypeOfferings"
      ],
      "Resource": "*"
    },
    {
      "Sid": "AccountDiscovery",
      "Effect": "Allow",
      "Action": [
        "sts:GetCallerIdentity",
        "organizations:ListAccounts"
      ],
      "Resource": "*"
    }
  ]
}
```

### Azure (Experimental)

Uses Azure SDK DefaultAzureCredential:

1. Azure CLI (`az login`)
2. Environment variables (`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`)
3. Managed Identity (Azure VM)

### GCP (Experimental)

Uses Google Cloud SDK credential chain:

1. Service account JSON (`GOOGLE_APPLICATION_CREDENTIALS`)
2. Application Default Credentials
3. gcloud CLI authentication

## Output Format

CUDly generates CSV files with comprehensive details:

```csv
Timestamp,Status,Service,Provider,Account,Region,ResourceType,Count,Term,PaymentOption,UpfrontCost,RecurringCost,TotalCost,EstimatedSavings,PurchaseID
```

### File Naming Convention

- Dry run: `cudly-dryrun-YYYYMMDD-HHMMSS.csv`
- Purchase: `cudly-purchase-YYYYMMDD-HHMMSS.csv`

## Architecture

```text
CUDly/
├── cmd/                      # CLI entry point and orchestration
├── pkg/                      # Shared multi-cloud packages
│   ├── common/              # Cloud-agnostic types and interfaces
│   └── provider/            # Provider abstraction layer
└── providers/               # Cloud-specific implementations
    ├── aws/                 # AWS provider (production)
    │   ├── services/        # Service clients (RDS, EC2, etc.)
    │   └── recommendations/ # Cost Explorer integration
    ├── azure/               # Azure provider (experimental)
    │   └── services/        # Azure service clients
    └── gcp/                 # GCP provider (experimental)
        └── services/        # GCP service clients
```

### Design Principles

- **Interface-driven** - All implementations follow defined interfaces for testability
- **Multi-cloud abstraction** - Unified types and behaviors across providers
- **Plugin architecture** - Services registered and discovered at runtime
- **Safety-first** - Multiple layers of protection against unintended purchases

## Web Interface (Experimental)

> **Note: The web GUI is experimental.** It is under active development and
> has not been validated at scale. Use the CLI for production workloads.

In addition to the CLI, this branch ships a browser-based dashboard. The same
Go binary that runs the CLI also acts as the application server: it serves the
pre-built TypeScript/Webpack frontend as static files (controlled by the
`STATIC_DIR` environment variable) and exposes a REST API at `/api/`. There is
no separate web server process.

### What the dashboard provides

| Area | What you can do |
| ---- | --------------- |
| **Dashboard** | Summary of active commitments, upcoming expirations, and savings trends |
| **Recommendations** | Browse and refresh commitment recommendations; trigger purchases from the UI |
| **Purchase plans** | Create, approve, pause, resume, and delete planned-purchase workflows; view execution history |
| **History** | Full purchase history with analytics and cost-breakdown views |
| **Inventory & Coverage** | List active commitments across accounts; view per-provider, per-service coverage breakdown |
| **RI Exchange** | AWS Convertible RI exchange: reshape recommendations, quote, and execute exchanges |
| **Settings** | Application configuration, cloud account credentials, user/group management, API keys |

### Capabilities and limitations

The web interface is part of the `feat/multicloud-web-frontend` branch and is
not yet merged to `main`. The dashboard is operational for AWS workloads; Azure
and GCP support in the web UI follows the same maturity as the CLI providers
(both are experimental). Specifically:

- **AWS**: recommendations, purchases, RI exchange, inventory, and coverage
  views are all wired and backed by real AWS APIs (Cost Explorer, EC2, RDS,
  etc.). This is the primary tested path.
- **Azure**: reservation recommendations and purchases are implemented in the
  API handlers (see `internal/api/handler_recommendations.go`,
  `providers/azure/`), but Azure support is experimental. The RI Exchange
  feature covers Azure Convertible RIs as a distinct code path.
- **GCP**: GCP commitment recommendations and purchases are experimental. The
  handler routing exists, but end-to-end coverage is limited compared to AWS.
- The RI Exchange feature currently targets AWS Convertible EC2 Reserved
  Instances only.
- Multi-account support (AWS Organizations) is implemented; Azure/GCP
  multi-account federation is in progress.

### Deployment (self-hosted via Terraform)

CUDly is **self-hosted only**. You deploy it into your own cloud account using
the Terraform configurations under `terraform/environments/`. The Terraform
modules build and push a Docker container image, provision the database,
secrets, and networking, and deploy the application to one of the supported
runtimes.

| Cloud | Runtime | Terraform environment |
| ----- | ------- | --------------------- |
| AWS | Lambda (default) or Fargate (ECS) | `terraform/environments/aws/` |
| GCP | Cloud Run | `terraform/environments/gcp/` |
| Azure | Container Apps | `terraform/environments/azure/` |

#### Prerequisites

- Terraform >= 1.6.0
- Docker with buildx
- Go 1.25+
- Cloud CLI authenticated: `aws`, `gcloud`, or `az`

#### Quick deploy (using the helper script)

```bash
# AWS dev
./scripts/tf-deploy.sh aws dev

# GCP dev
./scripts/tf-deploy.sh gcp dev

# Azure dev
./scripts/tf-deploy.sh azure dev
```

#### Manual Terraform (AWS example)

```bash
cd terraform/environments/aws
cp dev.tfvars.example dev.tfvars   # edit with your values
terraform init -backend-config=backends/dev.tfbackend
terraform plan -var-file=dev.tfvars
terraform apply -var-file=dev.tfvars
```

See [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for the full deployment guide,
including Azure and GCP details, CDN/CloudFront configuration, remote state
backends, and CI/CD integration.

**Key `tfvars` fields**

| Variable | Purpose |
| -------- | ------- |
| `admin_email` | Email address for the initial administrator account |
| `admin_password` | Initial admin password (leave unset to auto-generate and store in Secrets Manager) |
| `compute_platform` | AWS only: `"lambda"` (default, scale-to-zero) or `"fargate"` (always-warm ECS) |

The Terraform apply also handles Docker image build/push and database
migrations automatically on each apply.

### Accessing the dashboard

After `terraform apply` completes, retrieve the application URL from the
Terraform outputs:

```bash
# AWS Lambda
terraform -chdir=terraform/environments/aws output lambda_function_url

# AWS Fargate (ALB)
terraform -chdir=terraform/environments/aws output fargate_api_url

# GCP Cloud Run
terraform -chdir=terraform/environments/gcp output cloud_run_service_url

# Azure Container Apps
terraform -chdir=terraform/environments/azure output container_app_url
```

Open that URL in your browser. On a fresh deployment the login page includes a
one-time **"Set up admin"** step. Provide the `admin_email` you configured in
`tfvars` and either the password you set or the one retrieved from Secrets
Manager:

```bash
# Retrieve the auto-generated admin password (AWS)
aws secretsmanager get-secret-value \
  --secret-id "$(terraform -chdir=terraform/environments/aws output -raw admin_password_secret_name)" \
  --query SecretString --output text
```

After the admin account is created, log in with that email and password. You
can then add more users, configure cloud account credentials, and begin using
the dashboard.

## Development

### Prerequisites

- Go 1.23 or later
- AWS/Azure/GCP credentials for integration testing

### Building

```bash
# Build binary
go build -o cudly cmd/*.go

# Run tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run specific package tests
go test ./providers/aws/...
```

### Project Structure

| Directory | Purpose |
|-----------|---------|
| `cmd/` | CLI implementation, flag parsing, orchestration |
| `pkg/common/` | Cloud-agnostic types (Provider, Service, Commitment) |
| `pkg/provider/` | Provider interface, registry, factory |
| `providers/aws/` | AWS implementation with 8 service clients |
| `providers/azure/` | Azure implementation (experimental) |
| `providers/gcp/` | GCP implementation (experimental) |

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Areas for Contribution

- Additional AWS services (Lambda, DynamoDB, etc.)
- Azure and GCP service implementations
- Enhanced reporting and analytics
- Web UI dashboard
- Terraform/CloudFormation integration

## License

This project is licensed under the Open Software License 3.0 (OSL-3.0). See the [LICENSE](LICENSE) file for details.

The OSL-3.0 is an OSI-approved open source license that:

- Allows commercial use, modification, and distribution
- Requires attribution and license preservation
- Includes a patent grant
- Requires derivative works to be licensed under OSL-3.0

## Disclaimer

**This tool can make actual cloud commitment purchases when used with the `--purchase` flag.**

- Always verify recommendations before purchasing
- Test thoroughly in dry-run mode first
- Start with low coverage percentages
- Use instance limits for safety
- The authors are not responsible for unintended purchases or financial commitments

## Support

- **Issues**: [GitHub Issues](https://github.com/LeanerCloud/CUDly/issues)
- **Discussions**: [GitHub Discussions](https://github.com/LeanerCloud/CUDly/discussions)

## Shameless Plug

This tool is brought to you by [LeanerCloud](https://github.com/LeanerCloud). We help companies reduce their cloud costs using a mix of services and tools such as [AutoSpotting](https://github.com/LeanerCloud/AutoSpotting).

Running at significant scale on AWS and looking for cost optimization help? We can help you avoid committing to suboptimal resources by rightsizing and other optimizations before purchasing commitments. [Contact us](https://leanercloud.com).
