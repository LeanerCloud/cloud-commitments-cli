# Storage account for Function App
resource "azurerm_storage_account" "cleanup" {
  name                     = var.storage_account_name
  resource_group_name      = var.resource_group_name
  location                 = var.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
  tags                     = var.tags
}

# App Service Plan for Linux containers
resource "azurerm_service_plan" "cleanup" {
  name                = "${var.function_app_name}-plan"
  resource_group_name = var.resource_group_name
  location            = var.location
  os_type             = "Linux"
  sku_name            = "B1" # Basic tier for scheduled functions

  tags = var.tags
}

# Managed identity for Function App
resource "azurerm_user_assigned_identity" "cleanup" {
  name                = "${var.function_app_name}-identity"
  resource_group_name = var.resource_group_name
  location            = var.location
  tags                = var.tags
}

# Grant Key Vault access to the managed identity.
#
# This MUST be an RBAC role assignment, not an azurerm_key_vault_access_policy.
# The vault this module is pointed at (terraform/modules/secrets/azure/main.tf)
# sets enable_rbac_authorization = true, and an RBAC-enabled vault never
# consults its accessPolicies array. Terraform applies an access policy against
# such a vault successfully, so the mistake is invisible at plan and apply time
# and only surfaces as a runtime 403 when the function reads db-password (#1621).
#
# "Key Vault Secrets User" is the exact RBAC equivalent of the access policy this
# replaced: its dataActions are getSecret + readMetadata, which is precisely
# secret_permissions = ["Get", "List"], and its actions list is empty so it
# confers no management-plane rights.
resource "azurerm_role_assignment" "cleanup_kv_secrets_user" {
  scope                = var.key_vault_id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = azurerm_user_assigned_identity.cleanup.principal_id
}

# Linux Function App with container
resource "azurerm_linux_function_app" "cleanup" {
  name                = var.function_app_name
  resource_group_name = var.resource_group_name
  location            = var.location
  service_plan_id     = azurerm_service_plan.cleanup.id

  storage_account_name       = azurerm_storage_account.cleanup.name
  storage_account_access_key = azurerm_storage_account.cleanup.primary_access_key

  # Use container image
  site_config {
    always_on = true

    application_stack {
      docker {
        registry_url = split("/", var.image_uri)[0]
        image_name   = join("/", slice(split("/", var.image_uri), 1, length(split("/", var.image_uri)) - 1))
        image_tag    = split(":", var.image_uri)[1]
      }
    }

    # VNet integration: vnet_route_all_enabled is a scalar bool on
    # site_config, so set it conditionally rather than wrapping in a
    # dynamic block (which expects nested-block content).
    vnet_route_all_enabled = var.subnet_id != ""
  }

  # App settings (environment variables)
  app_settings = {
    DB_HOST             = var.db_host
    DB_PORT             = "5432"
    DB_NAME             = "cudly"
    DB_USER             = "cudly"
    DB_PASSWORD_SECRET  = var.db_password_secret_uri
    DB_SSL_MODE         = "require"
    SECRET_PROVIDER     = "azure"
    AZURE_KEY_VAULT_URL = replace(var.db_password_secret_uri, "/secrets/.*", "")

    # Function runtime settings
    FUNCTIONS_WORKER_RUNTIME            = "custom"
    WEBSITES_ENABLE_APP_SERVICE_STORAGE = "false"
  }

  # Managed identity
  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.cleanup.id]
  }

  # VNet integration: virtual_network_subnet_id is a scalar string
  # attribute on the function-app resource — null when no VNet is
  # supplied, otherwise the configured subnet ID. The previous
  # dynamic-around-scalar wrapper was rejected by Terraform.
  virtual_network_subnet_id = var.subnet_id != "" ? var.subnet_id : null

  tags = var.tags

  # Azure RBAC propagation is asynchronous and takes up to 10 minutes. Without
  # this edge Terraform may create the function app in parallel with its Key
  # Vault grant, and the first invocation 403s on db-password.
  depends_on = [azurerm_role_assignment.cleanup_kv_secrets_user]
}

# Timer trigger function (defined in host.json and function.json)
# Note: Azure Functions require function.json in the container image
# The container should have a structure like:
#   /home/site/wwwroot/cleanup/function.json
#   /home/site/wwwroot/host.json

# Logic App workflow to trigger the function (alternative to timer trigger)
resource "azurerm_logic_app_workflow" "cleanup_trigger" {
  name                = "${var.function_app_name}-trigger"
  location            = var.location
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

# Recurrence trigger
resource "azurerm_logic_app_trigger_recurrence" "cleanup" {
  name         = "cleanup-schedule"
  logic_app_id = azurerm_logic_app_workflow.cleanup_trigger.id
  frequency    = "Day"
  interval     = 1
  start_time   = formatdate("YYYY-MM-DD'T'02:00:00'Z'", timestamp())
  time_zone    = "UTC"
}

# HTTP action to call Function App
resource "azurerm_logic_app_action_http" "cleanup" {
  name         = "call-cleanup-function"
  logic_app_id = azurerm_logic_app_workflow.cleanup_trigger.id
  method       = "POST"
  uri          = "https://${azurerm_linux_function_app.cleanup.default_hostname}/api/cleanup"

  headers = {
    "Content-Type"    = "application/json"
    "x-functions-key" = "@listKeys('${azurerm_linux_function_app.cleanup.id}/host/default', '2022-03-01').functionKeys.default"
  }

  body = jsonencode({
    dryRun = false
  })
}
