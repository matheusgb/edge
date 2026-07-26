# ConfigMap com os parâmetros operacionais que fazem sentido mudar sem
# rebuild de imagem. EDGE_ORIGIN_URL usa o DNS interno do cluster
# (Service origin no namespace edge).
resource "kubernetes_config_map" "platform" {
  metadata {
    name      = "platform-config"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  data = {
    EDGE_ORIGIN_URL = "http://origin.${kubernetes_namespace.edge.metadata[0].name}.svc.cluster.local:8080"
  }
}
