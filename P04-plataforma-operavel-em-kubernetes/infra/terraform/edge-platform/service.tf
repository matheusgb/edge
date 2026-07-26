resource "kubernetes_service_v1" "origin" {
  metadata {
    name      = "origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels    = { app = "origin" }
  }

  spec {
    selector = { app = "origin" }

    port {
      name        = "public"
      port        = 8080
      target_port = "public"
    }
    port {
      name        = "admin"
      port        = 8090
      target_port = "admin"
    }
  }
}

resource "kubernetes_service_v1" "edge" {
  metadata {
    name      = "edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
    labels    = { app = "edge" }
  }

  spec {
    selector = { app = "edge" }

    port {
      name        = "public"
      port        = 8080
      target_port = "public"
    }
    port {
      name        = "admin"
      port        = 8090
      target_port = "admin"
    }
  }
}
