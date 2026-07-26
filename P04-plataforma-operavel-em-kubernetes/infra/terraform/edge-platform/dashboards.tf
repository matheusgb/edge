# O sidecar do kube-prometheus-stack (bootstrap-observability) procura
# ConfigMaps com o rótulo edge_dashboard=1 em todos os namespaces
# (searchNamespace=ALL) e carrega o JSON automaticamente no Grafana.
resource "kubernetes_config_map" "dashboard" {
  metadata {
    name      = "edge-p04-dashboard"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels = {
      edge_dashboard = "1"
    }
  }

  data = {
    "edge-p04.json" = file("${path.module}/dashboards/edge-p04.json")
  }
}
