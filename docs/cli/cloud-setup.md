# Cloud Setup (Self-Hosted)

CUDly's SaaS offering provides a guided onboarding UI for connecting Azure and GCP accounts. If you are running a self-hosted deployment, two CLI subcommands perform the same bootstrap: `configure-azure` and `configure-gcp`. Both store credentials in AWS Secrets Manager, where the CUDly server reads them at runtime.

These are one-time setup operations, not part of the regular analysis/purchase workflow.

## Prerequisites

- An AWS profile with `secretsmanager:ListSecrets` and `secretsmanager:UpdateSecret` permissions on the secrets created by the CUDly CloudFormation stack.
- The target Secrets Manager secret must already exist (created by the CloudFormation stack). Both commands locate the secret by listing secrets with a name prefix (`<stack-name>-AzureCredentials` or `<stack-name>-GCPCredentials`) and updating the first match.

## configure-azure

Store Azure Service Principal credentials in Secrets Manager.

```
cudly configure-azure [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack-name` | `cudly` | CloudFormation stack name. Used to locate the Secrets Manager secret (`<stack-name>-AzureCredentials`). |
| `--profile` | (AWS default chain) | AWS profile to use when writing to Secrets Manager. |
| `--tenant-id` | | Azure AD Tenant ID (UUID format). |
| `--client-id` | | Azure Service Principal Client ID (UUID format). |
| `--client-secret` | | Azure Service Principal Client Secret. Read securely from stdin if omitted. |
| `--subscription-id` | | Azure Subscription ID (UUID format). |
| `--interactive` / `-i` | `false` | Prompt for all credential fields interactively, even if some are provided as flags. |
| `--skip-setup` | `false` | Skip the guided Azure CLI steps (az login, az account list, az ad sp create-for-rbac). Use when you already have a Service Principal and just want to store the credentials. |

### What it does

When `--skip-setup` is not set, the command runs an interactive guided flow:

1. **`az login`** - opens a browser window for Azure authentication (can be skipped at the prompt).
2. **`az account list --output table`** - lists subscriptions so you can identify the Subscription ID.
3. **`az ad sp create-for-rbac --name CUDly --role "Reservations Administrator" --scopes /subscriptions/<id>`** - creates a Service Principal with the correct role (can be skipped).

After the guided steps (or immediately with `--skip-setup`), the command prompts for or accepts any missing credential fields and writes them as JSON to the `<stack-name>-AzureCredentials` secret.

### Non-interactive usage

```bash
# Provide all credentials as flags (--client-secret is read from a variable to avoid shell history)
AZURE_SECRET="$(cat /run/secrets/azure-client-secret)"
cudly configure-azure \
  --stack-name prod-cudly \
  --profile cudly-admin \
  --tenant-id 12345678-1234-1234-1234-123456789012 \
  --client-id  87654321-4321-4321-4321-210987654321 \
  --client-secret "$AZURE_SECRET" \
  --subscription-id aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb \
  --skip-setup
```

### Interactive usage

```bash
# Let the command guide you through Azure CLI setup and credential collection
cudly configure-azure --stack-name prod-cudly --profile cudly-admin
```

### Required Azure permissions

The Service Principal must have the **Reservations Administrator** role at the subscription scope. This is the minimum role needed to create reservation purchases.

```bash
# Example: grant the role manually if the guided step was skipped
az role assignment create \
  --assignee "<client-id>" \
  --role "Reservations Administrator" \
  --scope "/subscriptions/<subscription-id>"
```

## configure-gcp

Store GCP Service Account credentials in Secrets Manager.

```
cudly configure-gcp [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--stack-name` | | `cudly` | CloudFormation stack name. Used to locate the secret (`<stack-name>-GCPCredentials`). |
| `--profile` | | (AWS default chain) | AWS profile to use when writing to Secrets Manager. |
| `--credentials-file` | `-f` | | Path to a GCP Service Account JSON key file. Supports `~` expansion. |
| `--project-id` | | | GCP Project ID. Overrides the value embedded in the credentials file if set. |
| `--interactive` / `-i` | | `false` | Prompt for the credentials file path interactively. |
| `--skip-setup` | | `false` | Skip the guided gcloud steps (gcloud auth login, create service account, grant roles, create key). Use when you already have a JSON key file. |

### What it does

When `--skip-setup` is not set, the command runs an interactive guided flow:

1. **`gcloud auth login`** - opens a browser window for GCP authentication.
2. **`gcloud projects list`** - lists projects so you can identify your Project ID.
3. **`gcloud config set project <id>`** - sets the active project.
4. **Creates a `cudly-service-account` Service Account** with the display name "CUDly Service Account".
5. **`gcloud projects add-iam-policy-binding`** - grants `roles/compute.admin` to the new Service Account.
6. **`gcloud iam service-accounts keys create ~/cudly-gcp-key.json`** - downloads a JSON key to your home directory.

After the guided steps (or with `--skip-setup --credentials-file <path>`), the command reads and validates the JSON file and writes it to the `<stack-name>-GCPCredentials` secret.

### Non-interactive usage

```bash
# Store an existing key file
cudly configure-gcp \
  --stack-name prod-cudly \
  --profile cudly-admin \
  --credentials-file ~/cudly-gcp-key.json \
  --skip-setup
```

### Interactive usage

```bash
# Let the command guide you through gcloud setup
cudly configure-gcp --stack-name prod-cudly --profile cudly-admin
```

### Required GCP permissions

The Service Account needs the following roles:

| Role | Purpose |
|------|---------|
| `roles/compute.admin` | Manage Compute Engine Committed Use Discounts |

If you manage Cloud SQL or Memorystore commitments, you may need additional roles. Check the GCP documentation for the minimum required permissions per commitment type.

### Credentials file format

The command expects a standard GCP Service Account JSON key file (type `service_account`) with at minimum:

```json
{
  "type": "service_account",
  "project_id": "your-project-id",
  "client_email": "cudly-service-account@your-project-id.iam.gserviceaccount.com",
  "private_key": "-----BEGIN RSA PRIVATE KEY-----\n..."
}
```

Any missing required field causes the command to exit with an error before writing to Secrets Manager.

## When to use vs. SaaS onboarding

| Scenario | Recommendation |
|----------|---------------|
| SaaS tenant | Use the Settings UI in the CUDly web app. It provides guided onboarding with validation and does not require local AWS credentials. |
| Self-hosted deployment | Use `configure-azure` / `configure-gcp` CLI commands. The CloudFormation stack must be deployed first; these commands only update existing secrets. |
| Rotating credentials | Both commands update (not create) the secret, so they can also be used to rotate credentials without redeploying the stack. |
