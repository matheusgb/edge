resource "kubernetes_deployment_v1" "origin" {
  metadata {
    name      = "origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels    = { app = "origin" }
  }

  spec {
    replicas = var.origin_replicas_min

    selector {
      match_labels = { app = "origin" }
    }

    strategy {
      type = "RollingUpdate"
      rolling_update {
        max_surge       = "1"
        max_unavailable = "0"
      }
    }

    template {
      metadata {
        labels = { app = "origin" }
      }

      spec {
        service_account_name             = kubernetes_service_account.origin.metadata[0].name
        automount_service_account_token  = false
        termination_grace_period_seconds = var.termination_grace_period_seconds

        topology_spread_constraint {
          max_skew           = 1
          topology_key       = "kubernetes.io/hostname"
          when_unsatisfiable = "ScheduleAnyway"
          label_selector {
            match_labels = { app = "origin" }
          }
        }

        security_context {
          run_as_non_root = true
          run_as_user     = 65532
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        container {
          name              = "origin"
          image             = var.origin_image
          image_pull_policy = "Never" # imagem chega via kind load docker-image, não via pull de registry

          command = ["/app/origin"]
          args = [
            "-addr=:8080",
            "-admin-addr=:8090",
            "-shutdown-timeout=${var.shutdown_timeout}",
            "-warmup=${var.warmup}",
          ]

          port {
            name           = "public"
            container_port = 8080
          }
          port {
            name           = "admin"
            container_port = 8090
          }

          resources {
            requests = {
              cpu    = var.origin_resources.requests_cpu
              memory = var.origin_resources.requests_memory
            }
            limits = {
              cpu    = var.origin_resources.limits_cpu
              memory = var.origin_resources.limits_memory
            }
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities {
              drop = ["ALL"]
            }
          }

          # startupProbe dá tempo para o warmup configurado antes de a
          # readinessProbe/livenessProbe entrarem a valer: sem ele, uma
          # liveness agressiva poderia matar o pod antes do warmup
          # terminar.
          startup_probe {
            http_get {
              path = "/readyz"
              port = "admin"
            }
            period_seconds    = 1
            failure_threshold = 30
          }

          readiness_probe {
            http_get {
              path = "/readyz"
              port = "admin"
            }
            period_seconds    = 5
            failure_threshold = 3
          }

          liveness_probe {
            http_get {
              path = "/healthz"
              port = "admin"
            }
            period_seconds    = 10
            failure_threshold = 3
          }
        }
      }
    }
  }
}

resource "kubernetes_deployment_v1" "edge" {
  metadata {
    name      = "edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels    = { app = "edge" }
  }

  spec {
    replicas = var.edge_replicas_min

    selector {
      match_labels = { app = "edge" }
    }

    strategy {
      type = "RollingUpdate"
      rolling_update {
        max_surge       = "1"
        max_unavailable = "0"
      }
    }

    template {
      metadata {
        labels = { app = "edge" }
      }

      spec {
        service_account_name             = kubernetes_service_account.edge.metadata[0].name
        automount_service_account_token  = false
        termination_grace_period_seconds = var.termination_grace_period_seconds

        topology_spread_constraint {
          max_skew           = 1
          topology_key       = "kubernetes.io/hostname"
          when_unsatisfiable = "ScheduleAnyway"
          label_selector {
            match_labels = { app = "edge" }
          }
        }

        security_context {
          run_as_non_root = true
          run_as_user     = 65532
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        container {
          name              = "edge"
          image             = var.edge_image
          image_pull_policy = "Never"

          command = ["/app/edge"]
          args = [
            "-addr=:8080",
            "-admin-addr=:8090",
            "-shutdown-timeout=${var.shutdown_timeout}",
            "-warmup=${var.warmup}",
            "-cache-ttl=${var.cache_ttl}",
          ]

          env_from {
            config_map_ref {
              name = kubernetes_config_map.platform.metadata[0].name
            }
          }

          port {
            name           = "public"
            container_port = 8080
          }
          port {
            name           = "admin"
            container_port = 8090
          }

          resources {
            requests = {
              cpu    = var.edge_resources.requests_cpu
              memory = var.edge_resources.requests_memory
            }
            limits = {
              cpu    = var.edge_resources.limits_cpu
              memory = var.edge_resources.limits_memory
            }
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities {
              drop = ["ALL"]
            }
          }

          startup_probe {
            http_get {
              path = "/readyz"
              port = "admin"
            }
            period_seconds    = 1
            failure_threshold = 30
          }

          readiness_probe {
            http_get {
              path = "/readyz"
              port = "admin"
            }
            period_seconds    = 5
            failure_threshold = 3
          }

          liveness_probe {
            http_get {
              path = "/healthz"
              port = "admin"
            }
            period_seconds    = 10
            failure_threshold = 3
          }
        }
      }
    }
  }
}
