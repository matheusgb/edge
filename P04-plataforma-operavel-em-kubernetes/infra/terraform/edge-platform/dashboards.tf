# O sidecar do kube-prometheus-stack (bootstrap-observability) procura
# ConfigMaps com o rótulo edge_lab_dashboard=1 em todos os namespaces
# (searchNamespace=ALL) e carrega o JSON automaticamente no Grafana.
resource "kubernetes_config_map" "dashboard" {
  metadata {
    name      = "edge-lab-p04-dashboard"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels = {
      edge_lab_dashboard = "1"
    }
  }

  data = {
    "edge-lab-p04.json" = file("${path.module}/dashboards/edge-lab-p04.json")
  }
}
