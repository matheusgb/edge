resource "kubernetes_namespace" "observability" {
  metadata {
    name = var.observability_namespace
    labels = {
      "app.kubernetes.io/part-of" = "edge-p04"
    }
  }
}

# kube-prometheus-stack traz Prometheus, Grafana e os exporters
# necessários. kind não expõe os endpoints internos do control-plane
# (kube-controller-manager, kube-scheduler, kube-apiserver não escutam
# em endereços alcançáveis pelo ServiceMonitor), então esses monitors
# são desligados para não deixar o Prometheus com alvos sempre "down".
# Persistência fica desligada porque o cluster é descartável.
resource "helm_release" "kube_prometheus_stack" {
  name       = "kube-prometheus-stack"
  repository = "https://prometheus-community.github.io/helm-charts"
  chart      = "kube-prometheus-stack"
  version    = var.kube_prometheus_stack_version
  namespace  = kubernetes_namespace.observability.metadata[0].name

  timeout = var.helm_timeout_seconds
  wait    = true

  values = [yamlencode({
    kubeControllerManager = { enabled = false }
    kubeScheduler         = { enabled = false }
    kubeEtcd              = { enabled = false }
    kubeProxy             = { enabled = false }
    kubeApiServer         = { enabled = false }

    alertmanager = {
      alertmanagerSpec = {
        resources = {
          requests = { cpu = "25m", memory = "64Mi" }
        }
      }
    }

    prometheus = {
      prometheusSpec = {
        retention = "6h"
        resources = {
          requests = { cpu = "100m", memory = "256Mi" }
        }
        storageSpec = {}
      }
    }

    grafana = {
      persistence = { enabled = false }
      resources = {
        requests = { cpu = "50m", memory = "128Mi" }
      }
      sidecar = {
        dashboards = {
          enabled         = true
          label           = "edge_dashboard"
          searchNamespace = "ALL"
        }
      }
    }

    prometheusOperator = {
      resources = {
        requests = { cpu = "50m", memory = "128Mi" }
      }
    }
  })]
}

# ingress-nginx é instalado para satisfazer o requisito de plataforma
# (Nginx Ingress); a carga dos experimentos roda dentro do cluster
# direto contra o Service da borda, para não medir o overhead do
# port-forward/hostPort do kind junto com o comportamento do HPA.
resource "helm_release" "ingress_nginx" {
  name       = "ingress-nginx"
  repository = "https://kubernetes.github.io/ingress-nginx"
  chart      = "ingress-nginx"
  version    = var.ingress_nginx_version
  namespace  = kubernetes_namespace.observability.metadata[0].name

  timeout = var.helm_timeout_seconds
  wait    = true

  values = [yamlencode({
    controller = {
      service = { type = "NodePort" }
      resources = {
        requests = { cpu = "50m", memory = "90Mi" }
      }
      admissionWebhooks = { enabled = false }
    }
  })]
}

# kind não traz metrics-server; sem ele o HPA não tem de onde ler CPU e
# não reage.
resource "helm_release" "metrics_server" {
  name       = "metrics-server"
  repository = "https://kubernetes-sigs.github.io/metrics-server/"
  chart      = "metrics-server"
  version    = var.metrics_server_version
  namespace  = kubernetes_namespace.observability.metadata[0].name

  timeout = var.helm_timeout_seconds
  wait    = true

  values = [yamlencode({
    args = [
      "--kubelet-insecure-tls",
      "--kubelet-preferred-address-types=InternalIP",
    ]
    resources = {
      requests = { cpu = "25m", memory = "64Mi" }
    }
  })]
}
