resource "kubernetes_pod_disruption_budget_v1" "origin" {
  metadata {
    name      = "origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  spec {
    min_available = 1
    selector {
      match_labels = { app = "origin" }
    }
  }
}

resource "kubernetes_pod_disruption_budget_v1" "edge" {
  metadata {
    name      = "edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  spec {
    min_available = 1
    selector {
      match_labels = { app = "edge" }
    }
  }
}
