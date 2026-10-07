# Cloud Setup (Self-Hosted)

CUDly is a self-hosted tool. Two CLI subcommands handle the one-time credential bootstrap for Azure and GCP: `configure-azure` and `configure-gcp`. Both write credentials to AWS Secrets Manager, where the CUDly server reads them at runtime.

These are one-time setup operations, not part of the regular analysis/purchase workflow.

## Deployment overview

For the full Terraform deployment guide (supported runtimes, quick-deploy script, manual steps, `tfvars` reference, accessing the dashboard after deploy), see the [Deployment (self-hosted via Terraform)](../../README.md#deployment-self-hosted-via-terraform) section in the README.

The `ci-cd-permissions/` module within each environment provisions the CI/CD deploy identity and is applied once, manually, by a privileged operator. The main deploy workflow (`.github/workflows/deploy-*.yml`) then runs `terraform init / plan / apply` against the environment directory using OIDC keyless authentication.

## Prerequisites

- An AWS profile with `secretsmanager:ListSecrets` and `secretsmanager:UpdateSecret` permissions on the secrets created by the Terraform deployment.
- The target Secrets Manager secret must already exist (created by the Terraform deployment). Both commands locate the secret by listing secrets with a name prefix (`<stack-name>-AzureCredentials` or `<stack-name>-GCPCredentials`) and updating the first match.

## configure-azure

Store Azure Service Principal credentials in Secrets Manager.

```text
cudly configure-azure [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack-name` | `cudly` | Deployment stack name prefix. Used to locate the Secrets Manager secret (`<stack-name>-AzureCredentials`). Must match the name used when the Terraform deployment created the secret. |
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

```text
cudly configure-gcp [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--stack-name` | | `cudly` | Deployment stack name prefix. Used to locate the secret (`<stack-name>-GCPCredentials`). Must match the name used when the Terraform deployment created the secret. |
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
5. **Grant IAM roles via SDK** - creates or validates the project custom role `cudlyCommitmentPurchaser`, then grants it and `roles/compute.viewer` to the Service Account.
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
| `roles/compute.viewer` | Read Compute Engine resources and commitment operations |
| `projects/PROJECT_ID/roles/cudlyCommitmentPurchaser` | Purchase commitments with only `compute.commitments.create` |

The setup operator needs `iam.roles.get`, `iam.roles.create`,
`resourcemanager.projects.getIamPolicy`, and `resourcemanager.projects.setIamPolicy`
for this step. These setup permissions are not granted to the Service Account.
An existing custom role must have exactly the purchase permission and be enabled;
the wizard refuses incompatible roles rather than changing them. It also refuses
to widen an existing conditional-only grant for this Service Account.

Rerunning the wizard does not remove broad grants from older installations.
Recommendation access requires additional Recommender permissions; neither
role above provides them.

### Migrating legacy Compute Admin grants

Earlier setup wizard versions granted
`roles/compute.admin` to the `cudly-service-account` Service Account before
minting its key. Rerunning the current wizard adds the narrow roles but
preserves existing bindings, so every older installation needs a one-time
operator review. Work through this checklist with the project owner's
authorization:

1. **Inventory broad grants.** List the project IAM policy and identify CUDly
   identities that still hold Compute Admin:

   ```bash
   gcloud projects get-iam-policy PROJECT_ID \
     --flatten=bindings[].members \
     --filter=bindings.role:roles/compute.admin \
     --format='table(bindings.role, bindings.members, bindings.condition)'
   ```

   The project policy does not include inherited bindings, so check folder and
   organization policies too:

   ```bash
   gcloud projects get-ancestors-iam-policy PROJECT_ID \
     --flatten=policy.bindings[].members \
     --filter=policy.bindings.role:roles/compute.admin \
     --format='table(type, id, policy.bindings.role, policy.bindings.members, policy.bindings.condition)'
   ```

   Also save the full JSON output of both commands with `--format=json` and
   without `--flatten` or `--filter`, so all bindings and conditions remain
   available for review. Record each exact Service Account email, resource
   type and ID, role, and condition. Do not change live IAM during inventory.

2. **Establish the narrow permissions.** Rerun `cudly configure-gcp` (or grant
   manually) so the Service Account holds `roles/compute.viewer` and
   `projects/PROJECT_ID/roles/cudlyCommitmentPurchaser` as described above. An
   existing custom role must contain exactly `compute.commitments.create` and
   be enabled; the wizard refuses incompatible roles rather than changing them.

   Before removing anything, inspect the full project policy for both exact
   role names and the exact Service Account member, including conditions:

   ```bash
   gcloud projects get-iam-policy PROJECT_ID --format=json
   gcloud iam roles describe cudlyCommitmentPurchaser \
     --project=PROJECT_ID --format=json
   ```

   Confirm the custom role is not deleted, its stage is not `DISABLED`, and
   `includedPermissions` contains only `compute.commitments.create`. Confirm
   the replacement bindings' conditions permit the intended workflow. A
   successful permission check while Compute Admin remains granted cannot
   prove the narrow roles are sufficient: the broad grant masks missing access.

3. **Revoke each approved binding at its recorded scope.** Obtain owner
   authorization for each exact resource, member, role, and condition. Record
   the approved rollback (restoring that exact binding and condition) before
   removal. As the operator, use the command matching the resource that owns
   the binding. These examples remove only unconditional grants:

   ```bash
   gcloud projects remove-iam-policy-binding PROJECT_ID \
     --member=serviceAccount:cudly-service-account@PROJECT_ID.iam.gserviceaccount.com \
     --role=roles/compute.admin --condition=None
   gcloud resource-manager folders remove-iam-policy-binding FOLDER_ID \
     --member=serviceAccount:cudly-service-account@PROJECT_ID.iam.gserviceaccount.com \
     --role=roles/compute.admin --condition=None
   gcloud organizations remove-iam-policy-binding ORGANIZATION_ID \
     --member=serviceAccount:cudly-service-account@PROJECT_ID.iam.gserviceaccount.com \
     --role=roles/compute.admin --condition=None
   ```

   Substitute the inventoried Service Account email, which may belong to a
   different project. For a conditional binding, replace `--condition=None`
   with the exact owner-approved condition using `--condition` or
   `--condition-from-file`; preserve its expression, title, and description.
   Do not use `--all` or remove other bindings. Recheck the owning policy after
   each removal and confirm only the authorized binding changed.

4. **Verify after removal without purchasing.** Authenticate with the
   installation's existing Service Account key. A missing key requires separate
   authorization for credential recovery, not automatic key creation. Obtain
   a token as that Service Account and call Cloud Resource Manager's
   `projects.testIamPermissions` REST endpoint:

   Before authenticating, ensure the `auth/impersonate_service_account` gcloud
   configuration property and `CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT`
   environment variable are unset. If either is set, stop and select an
   owner-approved configuration and environment without impersonation; do not
   automatically change existing settings. Substitute the inventoried Service
   Account email in the token command below.

   ```bash
   gcloud auth activate-service-account --key-file=~/cudly-gcp-key.json
   CUDLY_GCP_TOKEN="$(gcloud auth print-access-token --account=cudly-service-account@PROJECT_ID.iam.gserviceaccount.com)"
   curl --fail-with-body --request POST \
     "https://cloudresourcemanager.googleapis.com/v1/projects/PROJECT_ID:testIamPermissions" \
     --header "Authorization: Bearer ${CUDLY_GCP_TOKEN}" \
     --header 'Content-Type: application/json' \
     --data '{"permissions":["compute.commitments.create","compute.commitments.list"]}'
   unset CUDLY_GCP_TOKEN
   ```

   The response omits permissions the caller lacks. Confirm both requested
   permissions appear, then run the installation's read-only CUDly analysis
   workflow with the same credentials and project and confirm it completes
   without permission errors. Do not purchase a commitment as a verification
   step. If validation fails, stop, switch back to the authorized operator
   identity with `gcloud auth login`, then follow the authorized rollback; do
   not broaden roles without owner approval. Also switch back to your operator
   identity before any further operator actions after successful validation.

   Record the inventory result and any deployment-specific follow-up. Never
   delete bindings or rotate keys without per-resource authorization.

   This documentation change was verified with offline fixtures and a local
   HTTP endpoint, not a live cloud account. Those checks cover command data
   shape and request construction; they do not prove deployment-specific IAM
   propagation or the live read-only workflow. Record those coverage gaps
   honestly when reviewing or applying the migration.

If you manage Cloud SQL or Memorystore commitments, you may need additional roles. Check the GCP documentation for the minimum required permissions per commitment type.

### Credentials file format

The command expects a standard GCP Service Account JSON key file (type `service_account`) with at minimum:

```json
{
  "type": "service_account",
  "project_id": "your-project-id",
  "client_email": "cudly-service-account@your-project-id.iam.gserviceaccount.com",
  "private_key": "<YOUR_SERVICE_ACCOUNT_PRIVATE_KEY>"
}
```

Any missing required field causes the command to exit with an error before writing to Secrets Manager.

## Usage notes

| Scenario | Notes |
|----------|-------|
| First-time setup | Deploy with Terraform first (`terraform/environments/<cloud>/`). The secrets are created as part of that deployment; these commands only update them. |
| Rotating credentials | Both commands update (not create) the secret, so they can be run again to rotate credentials without redeploying. |
