# GitHub Actions Workflows

This directory contains CI/CD workflows for the CUDly project, providing automated testing, deployment, and operations across AWS, GCP, and Azure.

## 📋 Workflows Overview

| Workflow | Purpose | Trigger | Duration |
|----------|---------|---------|----------|
| [ci.yml](#ci-workflow) | Continuous Integration | PR, Push to main | ~10 min |
| [deploy-aws-lambda.yml](#aws-lambda-deployment) | Deploy to AWS Lambda | Push to main, Manual | ~8 min |
| [deploy-aws-fargate.yml](#aws-fargate-deployment) | Deploy to AWS Fargate | Manual | ~10 min |
| [deploy-gcp.yml](#gcp-deployment) | Deploy to GCP Cloud Run | Manual | ~8 min |
| [deploy-azure.yml](#azure-deployment) | Deploy to Azure Container Apps | Manual | ~10 min |
| [deploy-all.yml](#multi-cloud-deployment) | Deploy to all clouds | Manual, Release | ~15 min |
| [database-migration.yml](#database-migrations) | Run DB migrations | Manual | ~5 min |
| [rollback.yml](#rollback) | Rollback deployment | Manual | ~5 min |

> **Note:** Frontend deployment is handled automatically via Terraform as part of the backend deployment workflows.

---

## CI Workflow

**File:** `ci.yml`

### Purpose

Runs comprehensive quality checks on every pull request and push to main branch.

### Jobs

1. **Lint** - golangci-lint, go vet
2. **Unit Tests** - Go tests with race detection, coverage reporting
3. **Integration Tests** - Tests with real PostgreSQL
4. **Docker Build** - Build and test Docker image
5. **Terraform Validate** - Validate all Terraform configs (AWS, GCP, Azure)
6. **Security Scan** - gosec, trivy, tfsec
7. **Snyk Scan** - Dependency vulnerability scanning
8. **E2E Tests** - Docker Compose end-to-end tests
9. **Cost Estimate** - Infracost cost estimation (PR only)

### Triggers

- Pull requests to `main` or `develop`
- Pushes to `main` or `develop`
- Manual dispatch

### Required Secrets

- `SNYK_TOKEN` (optional - for Snyk scanning)
- `INFRACOST_API_KEY` (optional - for cost estimation)

### Required Variables

- `GO_VERSION` (default: 1.25)

### Example

```bash
# Automatically runs on PR
git push origin feature-branch

# Or trigger manually
gh workflow run ci.yml
```

---

## AWS Lambda Deployment

**File:** `deploy-aws-lambda.yml`

### Purpose

Deploy CUDly to AWS Lambda with Function URL. Serverless, event-driven platform.

### Jobs

1. **Prepare** - Determine environment and image tag
2. **Build & Push** - Build Docker image, push to ECR
3. **Deploy** - Deploy with Terraform
4. **Test** - Health check and smoke tests

### Triggers

- Push to `main` (deploys to staging)
- Release creation (deploys to prod)
- Manual dispatch with environment selection

### Required Secrets

- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`

### Required Variables

- `AWS_REGION` (default: us-east-1)
- `AWS_ACCOUNT_ID`
- `ECR_REPOSITORY` (default: cudly)

### Example

```bash
# Deploy to dev
gh workflow run deploy-aws-lambda.yml -f environment=dev

# Deploy to staging
git push origin main

# Deploy to prod
gh release create v1.0.0
```

### Output

- Function URL: `https://<id>.lambda-url.us-east-1.on.aws`
- Deployment info artifact

---

## AWS Fargate Deployment

**File:** `deploy-aws-fargate.yml`

### Purpose

Deploy CUDly to AWS ECS Fargate with ALB. Always-on containerized platform.

### Jobs

1. **Build & Push** - Build Docker image, push to ECR
2. **Deploy** - Deploy with Terraform (Fargate mode)
3. **Test** - Health check verification

### Triggers

- Manual dispatch only

### Required Secrets

- Same as AWS Lambda

### Example

```bash
# Deploy to staging with Fargate
gh workflow run deploy-aws-fargate.yml -f environment=staging
```

---

## GCP Deployment

**File:** `deploy-gcp.yml`

### Purpose

Deploy CUDly to GCP Cloud Run. Serverless container platform.

### Jobs

1. **Build & Deploy** - Build, push to Artifact Registry, deploy with Terraform
2. **Test** - Health check and smoke tests

### Triggers

- Manual dispatch
- Called by deploy-all.yml

### Required Secrets

- `GCP_SA_KEY` (Service Account JSON with permissions)
- `GCP_PROJECT_ID`

### Required Variables

- `GCP_REGION` (default: us-central1)
- `ARTIFACT_REGISTRY_REPO` (default: cudly)

### Example

```bash
# Deploy to GCP dev
gh workflow run deploy-gcp.yml -f environment=dev
```

### Output

- Service URL: `https://cudly-<hash>-uc.a.run.app`

---

## Azure Deployment

**File:** `deploy-azure.yml`

### Purpose

Deploy CUDly to Azure Container Apps. Serverless container platform with built-in HTTPS.

### Jobs

1. **Build & Deploy** - Build, push to ACR, deploy with Terraform
2. **Test** - Health check and smoke tests

### Triggers

- Manual dispatch
- Called by deploy-all.yml

### Required Secrets

- `AZURE_CREDENTIALS` (Service Principal JSON)
- `AZURE_SUBSCRIPTION_ID`

### Required Variables

- `AZURE_LOCATION` (default: eastus)
- `ACR_NAME` (default: cudlyacr)
- `RESOURCE_GROUP` (default: cudly-rg)

### Example

```bash
# Deploy to Azure staging
gh workflow run deploy-azure.yml -f environment=staging
```

### Output

- App URL: `https://<app-name>.<region>.azurecontainerapps.io`

---

## Multi-Cloud Deployment

**File:** `deploy-all.yml`

### Purpose

Orchestrate deployment to multiple cloud providers in parallel.

### Jobs

1. **Determine Strategy** - Choose which clouds to deploy to
2. **Deploy AWS Lambda** - Parallel deployment
3. **Deploy AWS Fargate** - Parallel deployment (optional)
4. **Deploy GCP** - Parallel deployment
5. **Deploy Azure** - Parallel deployment
6. **Notify** - Aggregate results

### Triggers

- Manual dispatch with provider selection
- Release creation (deploys to all clouds in prod)

### Required Secrets

- All secrets from individual deployment workflows

### Deployment Options

- `all` - Deploy to AWS, GCP, and Azure
- `aws-only` - AWS Lambda only
- `gcp-only` - GCP Cloud Run only
- `azure-only` - Azure Container Apps only
- `aws-gcp` - AWS and GCP
- `aws-azure` - AWS and Azure
- `gcp-azure` - GCP and Azure

### Example

```bash
# Deploy to all clouds (staging)
gh workflow run deploy-all.yml -f environment=staging -f deploy_to=all

# Deploy to AWS and GCP (prod)
gh workflow run deploy-all.yml -f environment=prod -f deploy_to=aws-gcp

# Automatic on release
gh release create v1.0.0
```

### Benefits

- **Disaster Recovery** - Multi-cloud redundancy
- **Cost Optimization** - Compare costs across providers
- **Testing** - Validate across all platforms
- **Global Reach** - Deploy to optimal regions per cloud

---

## Database Migrations

**File:** `database-migration.yml`

### Purpose

Apply or rollback database schema migrations across cloud providers.

### Jobs

1. **Validate** - Safety checks
2. **Migrate AWS** - Run golang-migrate on Aurora
3. **Migrate GCP** - Run golang-migrate on Cloud SQL
4. **Migrate Azure** - Run golang-migrate on Flexible Server

### Triggers

- Manual dispatch only (safety measure)
- Can be called by deployment workflows

### Required Secrets

- `DB_PASSWORD_AWS`
- `DB_PASSWORD_GCP`
- `DB_PASSWORD_AZURE`
- Cloud credentials (same as deployment workflows)

### Required Variables

- Database endpoints per environment

### Migration Directions

- `up` - Apply migrations (default)
- `down` - Rollback migrations (DANGEROUS)

### Example

```bash
# Apply all migrations to AWS dev
gh workflow run database-migration.yml \
  -f cloud=aws \
  -f environment=dev \
  -f direction=up

# Rollback last 2 migrations on GCP staging
gh workflow run database-migration.yml \
  -f cloud=gcp \
  -f environment=staging \
  -f direction=down \
  -f steps=2

# Apply to all clouds
gh workflow run database-migration.yml \
  -f cloud=all \
  -f environment=prod \
  -f direction=up
```

### Safety Features

- **Validation** - Checks migration files exist
- **Production Warnings** - Warns on prod down migrations
- **Audit Trail** - Records all migrations
- **Step Control** - Apply specific number of migrations

---

## Rollback

**File:** `rollback.yml`

### Purpose

Quickly rollback to a previous deployment version by redeploying a known-good Docker image.

### Jobs

1. **Validate** - Validate image tag and construct image URI
2. **Verify Image** - Confirm image exists in registry
3. **Rollback** - Deploy previous image with Terraform
4. **Summary** - Create audit record

### Triggers

- Manual dispatch only (safety measure)

### Required Secrets

- Cloud credentials (same as deployment workflows)

### Example

```bash
# Rollback AWS Lambda production to previous version
gh workflow run rollback.yml \
  -f cloud=aws-lambda \
  -f environment=prod \
  -f image_tag=sha-abc123 \
  -f reason="Critical bug in v1.2.3"

# Rollback GCP staging
gh workflow run rollback.yml \
  -f cloud=gcp \
  -f environment=staging \
  -f image_tag=v1.2.2
```

### Safety Features

- **Image Verification** - Confirms image exists before deploying
- **Audit Trail** - Records all rollbacks (365 day retention)
- **Reason Tracking** - Requires reason for accountability
- **Manual Only** - Cannot be triggered automatically

### Finding Image Tags

```bash
# AWS ECR
aws ecr list-images --repository-name cudly

# GCP Artifact Registry
gcloud artifacts docker images list <region>-docker.pkg.dev/<project>/<repo>/cudly

# Azure ACR
az acr repository show-tags --name cudlyacr --repository cudly
```

---

## Setup Guide

### 1. Configure GitHub Secrets

**AWS:**

```bash
# Create secrets
gh secret set AWS_ACCESS_KEY_ID
gh secret set AWS_SECRET_ACCESS_KEY
gh secret set DB_PASSWORD_AWS
```

**GCP:**

```bash
# Create service account and download JSON
gcloud iam service-accounts create cudly-cicd --project=<project-id>

# Grant permissions
gcloud projects add-iam-policy-binding <project-id> \
  --member="serviceAccount:cudly-cicd@<project-id>.iam.gserviceaccount.com" \
  --role="roles/run.admin"

# Create and download key
gcloud iam service-accounts keys create key.json \
  --iam-account=cudly-cicd@<project-id>.iam.gserviceaccount.com

# Set secrets
gh secret set GCP_SA_KEY < key.json
gh secret set GCP_PROJECT_ID -b"<project-id>"
gh secret set DB_PASSWORD_GCP
```

**Azure:**

```bash
# Create service principal
az ad sp create-for-rbac --name cudly-cicd --sdk-auth > azure-credentials.json

# Set secrets
gh secret set AZURE_CREDENTIALS < azure-credentials.json
gh secret set AZURE_SUBSCRIPTION_ID -b"<subscription-id>"
gh secret set DB_PASSWORD_AZURE
```

**Optional:**

```bash
gh secret set SNYK_TOKEN
gh secret set INFRACOST_API_KEY
```

### 2. Configure GitHub Variables

```bash
# AWS
gh variable set AWS_REGION -b"us-east-1"
gh variable set AWS_ACCOUNT_ID -b"123456789012"
gh variable set ECR_REPOSITORY -b"cudly"

# GCP
gh variable set GCP_REGION -b"us-central1"
gh variable set ARTIFACT_REGISTRY_REPO -b"cudly"

# Azure
gh variable set AZURE_LOCATION -b"eastus"
gh variable set ACR_NAME -b"cudlyacr"
gh variable set RESOURCE_GROUP -b"cudly-rg"

# Frontend
gh variable set CLOUD_PROVIDER -b"aws"
gh variable set FRONTEND_BUCKET -b"cudly-frontend-prod"
gh variable set CLOUDFRONT_DISTRIBUTION_ID -b"E1234567890"
gh variable set API_URL -b"https://api.cudly.example.com"
```

### 3. Set Up Environments

GitHub Environments provide deployment protection and environment-specific secrets:

1. Go to **Settings** → **Environments**
2. Create environments:
   - `aws-lambda-dev`, `aws-lambda-staging`, `aws-lambda-prod`
   - `aws-fargate-dev`, `aws-fargate-staging`, `aws-fargate-prod`
   - `gcp-dev`, `gcp-staging`, `gcp-prod`
   - `azure-dev`, `azure-staging`, `azure-prod`
   - `frontend-aws-dev`, etc.

3. Configure protection rules:
   - **Production**: Require approvals, restrict to main branch
   - **Staging**: Optional approvals
   - **Dev**: No restrictions

---

## Troubleshooting

### CI Workflow Fails

**Unit tests fail:**

```bash
# Run locally
make test-unit
```

**Integration tests fail:**

```bash
# Run with testcontainers
make test-integration
```

**Security scan fails:**

```bash
# Run locally
make security-scan-all
```

### Deployment Fails

**AWS - Image not found:**

```bash
# Check ECR
aws ecr describe-images --repository-name cudly --region us-east-1

# Re-push image
docker push <account>.dkr.ecr.us-east-1.amazonaws.com/cudly:latest
```

**GCP - Permission denied:**

```bash
# Check service account permissions
gcloud projects get-iam-policy <project-id>

# Grant missing roles
gcloud projects add-iam-policy-binding <project-id> \
  --member="serviceAccount:<sa>@<project>.iam.gserviceaccount.com" \
  --role="roles/run.admin"
```

**Azure - Resource not found:**

```bash
# Verify resource group exists
az group show --name cudly-rg

# Create if missing
az group create --name cudly-rg --location eastus
```

### Database Migration Fails

**Connection timeout:**

- Check database security groups/firewall rules
- Verify VPN/bastion access if required
- Check database is running

**Migration already applied:**

```bash
# Check current version
migrate -path migrations -database <url> version

# Force version (use with caution)
migrate -path migrations -database <url> force <version>
```

---

## Best Practices

### 1. Branch Protection

- Require CI to pass before merging
- Require code reviews
- Restrict direct pushes to main

### 2. Environment Strategy

- **Dev**: Auto-deploy on push to develop branch
- **Staging**: Auto-deploy on push to main
- **Prod**: Manual approval required, deploy on release

### 3. Rollback Strategy

- Keep last 10 images in each registry
- Document rollback procedures
- Test rollback in staging first

### 4. Monitoring

- Set up CloudWatch/Cloud Logging alerts
- Monitor deployment success rates
- Track deployment frequency

### 5. Security

- Rotate secrets regularly
- Use environment protection rules
- Enable secret scanning
- Review security scan results

---

## Metrics & Monitoring

### Workflow Success Rate

```bash
# View recent workflow runs
gh run list --limit 50

# View specific workflow
gh run list --workflow=ci.yml --limit 20
```

### Deployment Frequency

- Target: Multiple deployments per day
- Track via GitHub Actions insights

### Mean Time to Recovery (MTTR)

- Use rollback workflow for quick recovery
- Target: < 15 minutes

### CI Duration

- Unit tests: ~5 min
- Integration tests: ~3 min
- Security scans: ~2 min
- Total: ~10 min target

---

## Additional Resources

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [AWS ECR Documentation](https://docs.aws.amazon.com/ecr/)
- [GCP Artifact Registry](https://cloud.google.com/artifact-registry/docs)
- [Azure Container Registry](https://docs.microsoft.com/en-us/azure/container-registry/)
- [golang-migrate](https://github.com/golang-migrate/migrate)
- [Terraform Cloud](https://www.terraform.io/cloud)
