# Azure AKS Compute Module
# Azure Kubernetes Service with managed node pools

locals {
  name_prefix = "${var.project_name}-${var.environment}-aks"

  common_tags = merge(
    var.tags,
    {
      Module      = "compute/azure/aks"
      Environment = var.environment
    }
  )
}

# ==============================================
# Log Analytics Workspace (for Container Insights)
# ==============================================

resource "azurerm_log_analytics_workspace" "aks" {
  count = var.enable_log_analytics ? 1 : 0

  name                = "${local.name_prefix}-logs"
  location            = var.location
  resource_group_name = var.resource_group_name
  sku                 = "PerGB2018"
  retention_in_days   = 30

  tags = local.common_tags
}

# ==============================================
# AKS Cluster
# ==============================================

resource "azurerm_kubernetes_cluster" "main" {
  name                = local.name_prefix
  location            = var.location
  resource_group_name = var.resource_group_name
  dns_prefix          = local.name_prefix
  kubernetes_version  = var.kubernetes_version

  # Default node pool
  default_node_pool {
    name                = "default"
    node_count          = var.node_count
    vm_size             = var.node_vm_size
    vnet_subnet_id      = var.vnet_subnet_id
    enable_auto_scaling = var.enable_auto_scaling
    min_count           = var.enable_auto_scaling ? var.min_node_count : null
    max_count           = var.enable_auto_scaling ? var.max_node_count : null
    os_disk_size_gb     = 30
    type                = "VirtualMachineScaleSets"

    upgrade_settings {
      max_surge = "10%"
    }

    tags = local.common_tags
  }

  # Managed identity
  identity {
    type = "SystemAssigned"
  }

  # Network profile
  network_profile {
    network_plugin    = "azure"
    network_policy    = "azure"
    load_balancer_sku = "standard"
    service_cidr      = var.service_cidr
    dns_service_ip    = var.dns_service_ip
  }

  # Add-ons
  dynamic "oms_agent" {
    for_each = var.enable_log_analytics ? [1] : []
    content {
      log_analytics_workspace_id = azurerm_log_analytics_workspace.aks[0].id
    }
  }

  azure_policy_enabled = var.enable_azure_policy

  # RBAC and Azure AD integration
  role_based_access_control_enabled = true

  tags = local.common_tags
}

# ==============================================
# User Assigned Identity for Workload
# ==============================================

resource "azurerm_user_assigned_identity" "workload" {
  name                = "${local.name_prefix}-workload-identity"
  location            = var.location
  resource_group_name = var.resource_group_name

  tags = local.common_tags
}

# Grant the workload identity access to Key Vault secrets.
#
# This MUST be an RBAC role assignment, not an azurerm_key_vault_access_policy.
# The vault this module is pointed at (terraform/modules/secrets/azure/main.tf)
# sets enable_rbac_authorization = true, and an RBAC-enabled vault never
# consults its accessPolicies array. Terraform applies an access policy against
# such a vault successfully, so the mistake is invisible at plan and apply time
# and only surfaces as a runtime 403 when a pod fetches a secret (#1621).
#
# "Key Vault Secrets User" is the exact RBAC equivalent of the access policy this
# replaced: its dataActions are getSecret + readMetadata, which is precisely
# secret_permissions = ["Get", "List"], and its actions list is empty so it
# confers no management-plane rights. Deliberately not "Key Vault Secrets
# Officer" (dataActions secrets/*), which would grant write and delete that the
# access policy never did.
resource "azurerm_role_assignment" "workload_kv_secrets_user" {
  scope                = var.key_vault_id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = azurerm_user_assigned_identity.workload.principal_id
}

# Grant AKS cluster identity access to pull images from ACR
resource "azurerm_role_assignment" "aks_acr" {
  principal_id                     = azurerm_kubernetes_cluster.main.kubelet_identity[0].object_id
  role_definition_name             = "AcrPull"
  scope                            = "/subscriptions/${data.azurerm_client_config.current.subscription_id}/resourceGroups/${var.resource_group_name}"
  skip_service_principal_aad_check = true
}

data "azurerm_client_config" "current" {}

# ==============================================
# Kubernetes Resources (CONDITIONAL)
# ==============================================
#
# NOTE: These resources are conditionally created based on var.deploy_kubernetes_resources
# To use these resources:
# 1. Set deploy_kubernetes_resources = true
# 2. Configure kubernetes/helm providers at the root level (see providers.tf)
# 3. The providers will use the cluster's kube_config output
#
# Default: false (only creates the AKS cluster infrastructure)
# ==============================================

# ==============================================
# Kubernetes Namespace
# ==============================================

resource "kubernetes_namespace" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name = var.environment

    labels = {
      environment = var.environment
      project     = var.project_name
    }
  }

  depends_on = [azurerm_kubernetes_cluster.main]
}

# ==============================================
# Kubernetes Secret for Database
# ==============================================

# Note: In production, use Azure Key Vault CSI driver instead
# This is a simplified approach for getting started

resource "kubernetes_secret" "database" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "database-credentials"
    namespace = kubernetes_namespace.app[0].metadata[0].name
  }

  # NOTE: `password_secret_name` carries the *Key Vault secret name*, not
  # the cleartext password. Workloads must resolve it via the Key Vault API
  # (or the Azure Key Vault CSI driver) before connecting. The earlier key
  # name `password` was misleading because it implied the cleartext value
  # lived inline.
  data = {
    host                 = var.database_host
    name                 = var.database_name
    username             = var.database_username
    password_secret_name = var.database_password_secret_name
  }

  type = "Opaque"

  depends_on = [kubernetes_namespace.app]
}

# ==============================================
# Kubernetes Service Account
# ==============================================

resource "kubernetes_service_account" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-api"
    namespace = kubernetes_namespace.app[0].metadata[0].name

    annotations = {
      "azure.workload.identity/client-id" = azurerm_user_assigned_identity.workload.client_id
    }
  }

  depends_on = [kubernetes_namespace.app]
}

# ==============================================
# Kubernetes Deployment
# ==============================================

resource "kubernetes_deployment" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-api"
    namespace = kubernetes_namespace.app[0].metadata[0].name

    labels = {
      app         = "${var.project_name}-api"
      environment = var.environment
    }
  }

  spec {
    replicas = 2

    selector {
      match_labels = {
        app = "${var.project_name}-api"
      }
    }

    template {
      metadata {
        labels = {
          app         = "${var.project_name}-api"
          environment = var.environment
        }
      }

      spec {
        container {
          name  = "api"
          image = "${var.image_name}:${var.image_tag}"

          port {
            container_port = 8080
            protocol       = "TCP"
          }

          env {
            name  = "PORT"
            value = "8080"
          }

          env {
            name  = "ENVIRONMENT"
            value = var.environment
          }

          env {
            name  = "RUNTIME_MODE"
            value = "http"
          }

          env {
            name = "DB_HOST"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.database[0].metadata[0].name
                key  = "host"
              }
            }
          }

          env {
            name  = "DB_PORT"
            value = "5432"
          }

          env {
            name = "DB_NAME"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.database[0].metadata[0].name
                key  = "name"
              }
            }
          }

          env {
            name = "DB_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.database[0].metadata[0].name
                key  = "username"
              }
            }
          }

          env {
            name = "DB_PASSWORD_SECRET"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.database[0].metadata[0].name
                key  = "password"
              }
            }
          }

          env {
            name  = "DB_SSL_MODE"
            value = "require"
          }

          env {
            name  = "DB_CONNECT_TIMEOUT"
            value = "8s"
          }

          env {
            name  = "DB_AUTO_MIGRATE"
            value = tostring(var.auto_migrate)
          }

          env {
            name  = "DB_MIGRATIONS_PATH"
            value = "/app/migrations"
          }

          env {
            name  = "ADMIN_EMAIL"
            value = var.admin_email
          }

          env {
            name  = "ADMIN_PASSWORD_SECRET"
            value = var.admin_password_secret_name
          }

          env {
            name  = "SECRET_PROVIDER"
            value = "azure"
          }

          env {
            name  = "AZURE_KEY_VAULT_URL"
            value = var.key_vault_uri
          }

          env {
            name  = "AZURE_REGION"
            value = var.location
          }

          env {
            name  = "ALLOWED_ORIGINS"
            value = join(",", var.allowed_origins)
          }

          # Scheduled-task auth — Azure stays on bearer mode (Logic Apps
          # fetch the shared secret from Key Vault at invocation time;
          # Logic Apps' HTTP Connector does not emit Entra OIDC tokens).
          # Validated app-side via internal/server/scheduledauth.
          env {
            name  = "SCHEDULED_TASK_AUTH_MODE"
            value = "bearer"
          }

          dynamic "env" {
            for_each = var.additional_env_vars
            content {
              name  = env.key
              value = env.value
            }
          }

          resources {
            requests = {
              cpu    = "250m"
              memory = "512Mi"
            }
            limits = {
              cpu    = "1000m"
              memory = "1Gi"
            }
          }

          liveness_probe {
            http_get {
              path = "/health"
              port = 8080
            }
            initial_delay_seconds = 30
            period_seconds        = 10
            timeout_seconds       = 5
            failure_threshold     = 3
          }

          readiness_probe {
            http_get {
              path = "/health"
              port = 8080
            }
            initial_delay_seconds = 10
            period_seconds        = 5
            timeout_seconds       = 3
            failure_threshold     = 3
          }
        }

        service_account_name = kubernetes_service_account.app[0].metadata[0].name
      }
    }

    strategy {
      type = "RollingUpdate"
      rolling_update {
        max_surge       = "25%"
        max_unavailable = "25%"
      }
    }
  }

  # The Key Vault grant is listed here because these pods resolve
  # ADMIN_PASSWORD_SECRET, CREDENTIAL_ENCRYPTION_KEY_SECRET_NAME and the
  # AZURE_SMTP_* secrets from the vault at startup through the workload
  # identity. Azure RBAC propagation is asynchronous and takes up to 10
  # minutes, and Terraform's implicit graph does not capture that edge.
  depends_on = [
    kubernetes_namespace.app,
    kubernetes_secret.database,
    azurerm_role_assignment.workload_kv_secrets_user
  ]
}

# ==============================================
# Kubernetes Service (ClusterIP)
# ==============================================

resource "kubernetes_service" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-api"
    namespace = kubernetes_namespace.app[0].metadata[0].name

    labels = {
      app = "${var.project_name}-api"
    }
  }

  spec {
    selector = {
      app = "${var.project_name}-api"
    }

    port {
      name        = "http"
      port        = 80
      target_port = 8080
      protocol    = "TCP"
    }

    type = "ClusterIP"
  }

  depends_on = [kubernetes_namespace.app]
}

# ==============================================
# Kubernetes Ingress (NGINX)
# ==============================================

resource "kubernetes_ingress_v1" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-api"
    namespace = kubernetes_namespace.app[0].metadata[0].name

    annotations = {
      "kubernetes.io/ingress.class"                = "nginx"
      "nginx.ingress.kubernetes.io/rewrite-target" = "/"
      "nginx.ingress.kubernetes.io/ssl-redirect"   = "false"
    }
  }

  spec {
    rule {
      http {
        path {
          path      = "/"
          path_type = "Prefix"

          backend {
            service {
              name = kubernetes_service.app[0].metadata[0].name
              port {
                number = 80
              }
            }
          }
        }
      }
    }
  }

  depends_on = [
    kubernetes_service.app,
    helm_release.nginx_ingress
  ]
}

# ==============================================
# Network Policies
# ==============================================

# Default deny all ingress - requires explicit allow rules
resource "kubernetes_network_policy" "default_deny_ingress" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "default-deny-ingress"
    namespace = kubernetes_namespace.app[0].metadata[0].name
  }

  spec {
    pod_selector {}

    policy_types = ["Ingress"]
  }

  depends_on = [kubernetes_namespace.app]
}

# Allow ingress from NGINX ingress controller to app pods
resource "kubernetes_network_policy" "allow_ingress_from_nginx" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "allow-ingress-from-nginx"
    namespace = kubernetes_namespace.app[0].metadata[0].name
  }

  spec {
    pod_selector {
      match_labels = {
        app = "${var.project_name}-api"
      }
    }

    ingress {
      from {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = "ingress-nginx"
          }
        }
      }

      ports {
        port     = 8080
        protocol = "TCP"
      }
    }

    policy_types = ["Ingress"]
  }

  depends_on = [kubernetes_namespace.app]
}

# ==============================================
# Resource Quota
# ==============================================

resource "kubernetes_resource_quota" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-quota"
    namespace = kubernetes_namespace.app[0].metadata[0].name
  }

  spec {
    hard = {
      "requests.cpu"    = "4"
      "requests.memory" = "8Gi"
      "limits.cpu"      = "8"
      "limits.memory"   = "16Gi"
      "pods"            = "20"
    }
  }

  depends_on = [kubernetes_namespace.app]
}

# ==============================================
# Horizontal Pod Autoscaler
# ==============================================

resource "kubernetes_horizontal_pod_autoscaler_v2" "app" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "${var.project_name}-api"
    namespace = kubernetes_namespace.app[0].metadata[0].name
  }

  spec {
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment.app[0].metadata[0].name
    }

    min_replicas = 2
    max_replicas = 10

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = 70
        }
      }
    }

    metric {
      type = "Resource"
      resource {
        name = "memory"
        target {
          type                = "Utilization"
          average_utilization = 80
        }
      }
    }
  }

  depends_on = [kubernetes_deployment.app]
}

# ==============================================
# Helm Release: NGINX Ingress Controller
# ==============================================

resource "helm_release" "nginx_ingress" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  name       = "nginx-ingress"
  repository = "https://kubernetes.github.io/ingress-nginx"
  chart      = "ingress-nginx"
  namespace  = "ingress-nginx"
  version    = var.nginx_ingress_version

  create_namespace = true

  set {
    name  = "controller.service.type"
    value = "LoadBalancer"
  }

  set {
    name  = "controller.service.annotations.service\\.beta\\.kubernetes\\.io/azure-load-balancer-health-probe-request-path"
    value = "/healthz"
  }

  depends_on = [azurerm_kubernetes_cluster.main]
}

# ==============================================
# Get Ingress Load Balancer IP
# ==============================================
# Azure provisions the external LoadBalancer IP asynchronously (2–5 min)
# after Helm installs the NGINX ingress. Historically the kubernetes_service
# data source would run before the IP was ready, so outputs emitted ""
# and a second `terraform apply` was required to pick up the real IP.
#
# time_sleep below waits 5 minutes after the Helm release — covering
# Azure's typical provisioning window — before the data source reads the
# service status. This lets a single `terraform apply` produce a populated
# load_balancer_ip output in the common case. The try() fallback on the
# output is retained as a safety net for the rare tenant where
# provisioning takes longer than the budget.
#
# Trade-off: happy-path apply is ~5 minutes slower than an event-driven
# wait would be, but total AKS apply time is dominated by cluster +
# Helm install (~10–15 min) so the overhead is proportionally small.
# We don't poll via kubectl because this module doesn't bootstrap a
# local kubeconfig (the k8s provider uses exec-auth directly, which
# doesn't help a local-exec subprocess).

resource "time_sleep" "wait_for_lb_ip" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  depends_on = [helm_release.nginx_ingress]

  create_duration = "5m"

  # Re-wait when the cluster or helm release changes, so a redeploy
  # doesn't read a stale IP from a previous LB provisioning cycle.
  triggers = {
    cluster_id   = azurerm_kubernetes_cluster.main.id
    release_name = helm_release.nginx_ingress[0].name
  }
}

data "kubernetes_service" "nginx_ingress" {
  count = var.deploy_kubernetes_resources ? 1 : 0

  metadata {
    name      = "nginx-ingress-ingress-nginx-controller"
    namespace = "ingress-nginx"
  }

  depends_on = [
    helm_release.nginx_ingress,
    time_sleep.wait_for_lb_ip,
  ]
}
