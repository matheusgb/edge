# Testes de plano, não de apply: verificam contagem e atributos dos
# recursos que o Terraform pretende criar, sem tocar no cluster. A
# correção contra o cluster real (Prometheus/Grafana/ingress-nginx de
# pé, scraping funcionando) é responsabilidade dos scripts de
# experimento em infra/experiments/, não deste arquivo.
#
# Os providers precisam de um contexto kubeconfig válido para inicializar
# o client mesmo em modo plan; aqui apontam para o cluster kind local já
# criado por infra/kind/bootstrap.sh.
provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "kind-edge-lab"
}

provider "helm" {
  kubernetes {
    config_path    = "~/.kube/config"
    config_context = "kind-edge-lab"
  }
}

run "plano_cria_namespace_e_tres_helm_releases" {
  command = plan

  assert {
    condition     = kubernetes_namespace.observability.metadata[0].name == "observability"
    error_message = "namespace de observabilidade deveria se chamar 'observability' por padrão"
  }

  assert {
    condition     = helm_release.kube_prometheus_stack.chart == "kube-prometheus-stack"
    error_message = "chart do kube-prometheus-stack incorreto"
  }

  assert {
    condition     = helm_release.kube_prometheus_stack.repository == "https://prometheus-community.github.io/helm-charts"
    error_message = "repositório do kube-prometheus-stack incorreto"
  }

  assert {
    condition     = helm_release.ingress_nginx.namespace == kubernetes_namespace.observability.metadata[0].name
    error_message = "ingress-nginx deveria ser instalado no namespace de observabilidade"
  }

  assert {
    condition     = helm_release.metrics_server.chart == "metrics-server"
    error_message = "chart do metrics-server incorreto"
  }

  assert {
    condition     = helm_release.kube_prometheus_stack.wait == true
    error_message = "kube-prometheus-stack deveria esperar (wait=true) para o apply só terminar com os pods de pé"
  }
}

run "variavel_de_namespace_customizado_e_respeitada" {
  command = plan

  variables {
    observability_namespace = "obs-custom"
  }

  assert {
    condition     = kubernetes_namespace.observability.metadata[0].name == "obs-custom"
    error_message = "a variável observability_namespace deveria mudar o nome do namespace"
  }

  assert {
    condition     = helm_release.ingress_nginx.namespace == "obs-custom"
    error_message = "ingress-nginx deveria seguir o namespace customizado"
  }
}
