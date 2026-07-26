# Default-deny: nenhum pod do namespace edge fala com nada, em nenhuma
# direção, a menos que uma policy abaixo libere explicitamente. Sem
# isso, "tráfego mínimo necessário" seria só um rótulo, não uma
# garantia.
resource "kubernetes_network_policy_v1" "default_deny" {
  metadata {
    name      = "default-deny-all"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {}
    policy_types = ["Ingress", "Egress"]
  }
}

# DNS é a exceção universal: sem egress para o kube-dns, nenhum pod
# resolve nomes, incluindo o nome da própria origem.
resource "kubernetes_network_policy_v1" "allow_dns_egress" {
  metadata {
    name      = "allow-dns-egress"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {}
    policy_types = ["Egress"]
    egress {
      to {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = "kube-system"
          }
        }
      }
      ports {
        port     = "53"
        protocol = "UDP"
      }
      ports {
        port     = "53"
        protocol = "TCP"
      }
    }
  }
}

# edge -> origin, só na porta pública, só o que tem o rótulo app=edge.
resource "kubernetes_network_policy_v1" "allow_edge_to_origin" {
  metadata {
    name      = "allow-edge-to-origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {
      match_labels = { app = "origin" }
    }
    policy_types = ["Ingress"]
    ingress {
      from {
        pod_selector {
          match_labels = { app = "edge" }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
  }
}

resource "kubernetes_network_policy_v1" "allow_edge_egress_to_origin" {
  metadata {
    name      = "allow-edge-egress-to-origin"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {
      match_labels = { app = "edge" }
    }
    policy_types = ["Egress"]
    egress {
      to {
        pod_selector {
          match_labels = { app = "origin" }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
  }
}

# ingress-nginx (no namespace observability) -> edge, porta pública.
resource "kubernetes_network_policy_v1" "allow_ingress_nginx_to_edge" {
  metadata {
    name      = "allow-ingress-nginx-to-edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {
      match_labels = { app = "edge" }
    }
    policy_types = ["Ingress"]
    ingress {
      from {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = var.observability_namespace
          }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
  }
}

# Prometheus (namespace observability) -> origin e edge, só na porta
# administrativa (/metrics). O Prometheus nunca deve alcançar a porta
# pública dos serviços.
resource "kubernetes_network_policy_v1" "allow_prometheus_scrape" {
  metadata {
    name      = "allow-prometheus-scrape"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {}
    policy_types = ["Ingress"]
    ingress {
      from {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = var.observability_namespace
          }
        }
      }
      ports {
        port     = "8090"
        protocol = "TCP"
      }
    }
  }
}

# Pods de experimento (loadgen, testes negativos) rodam com o rótulo
# role=experiments e são o único ponto autorizado a gerar carga contra
# a borda de dentro do cluster.
resource "kubernetes_network_policy_v1" "allow_experiments_to_edge" {
  metadata {
    name      = "allow-experiments-to-edge"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {
      match_labels = { app = "edge" }
    }
    policy_types = ["Ingress"]
    ingress {
      from {
        pod_selector {
          match_labels = { role = "experiments" }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
  }
}

resource "kubernetes_network_policy_v1" "allow_experiments_egress" {
  metadata {
    name      = "allow-experiments-egress"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
  spec {
    pod_selector {
      match_labels = { role = "experiments" }
    }
    policy_types = ["Egress"]
    egress {
      to {
        pod_selector {
          match_labels = { app = "edge" }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
  }
}
