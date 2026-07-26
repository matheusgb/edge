resource "kubernetes_namespace" "edge" {
  metadata {
    name = var.edge_namespace
    labels = {
      "app.kubernetes.io/part-of" = "edge-p04"
    }
  }
}

resource "kubernetes_service_account" "origin" {
  metadata {
    name      = "origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  automount_service_account_token = false
}

resource "kubernetes_service_account" "edge" {
  metadata {
    name      = "edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  automount_service_account_token = false
}

# Conta usada só para o loadgen dos experimentos (Job) e para o pod de
# teste negativo do experimento de restrição de rede.
resource "kubernetes_service_account" "experiments" {
  metadata {
    name      = "experiments"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  automount_service_account_token = false
}
