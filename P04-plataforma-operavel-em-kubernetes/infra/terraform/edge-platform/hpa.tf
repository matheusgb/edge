# Comportamento de subida agressivo (reage rápido a um degrau de
# carga) e descida conservador (evita flapping quando a carga cai por
# pouco tempo), e o experimento de escala mede o atraso real de cada um.
resource "kubernetes_horizontal_pod_autoscaler_v2" "origin" {
  metadata {
    name      = "origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  spec {
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment_v1.origin.metadata[0].name
    }

    min_replicas = var.origin_replicas_min
    max_replicas = var.origin_replicas_max

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = var.hpa_target_cpu_utilization_percent
        }
      }
    }

    behavior {
      scale_up {
        stabilization_window_seconds = 0
        select_policy                = "Max"
        policy {
          type           = "Pods"
          value          = 2
          period_seconds = 30
        }
        policy {
          type           = "Percent"
          value          = 100
          period_seconds = 30
        }
      }
      scale_down {
        stabilization_window_seconds = 120
        select_policy                = "Min"
        policy {
          type           = "Pods"
          value          = 1
          period_seconds = 60
        }
      }
    }
  }
}

resource "kubernetes_horizontal_pod_autoscaler_v2" "edge" {
  metadata {
    name      = "edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  spec {
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment_v1.edge.metadata[0].name
    }

    min_replicas = var.edge_replicas_min
    max_replicas = var.edge_replicas_max

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = var.hpa_target_cpu_utilization_percent
        }
      }
    }

    behavior {
      scale_up {
        stabilization_window_seconds = 0
        select_policy                = "Max"
        policy {
          type           = "Pods"
          value          = 1
          period_seconds = 30
        }
      }
      scale_down {
        stabilization_window_seconds = 120
        select_policy                = "Min"
        policy {
          type           = "Pods"
          value          = 1
          period_seconds = 60
        }
      }
    }
  }
}
