# origin e edge não falam com a API do Kubernetes (automount do token
# desligado em main.tf), então não recebem nenhuma Role: o RBAC mais
# restrito possível é não ter permissão nenhuma.
#
# A conta "experiments" recebe leitura restrita a pods/deployments no
# próprio namespace, usada por uma futura automação de experimento que
# rode dentro do cluster (hoje os scripts em infra/experiments/ rodam
# via kubectl a partir do host, com o kubeconfig de administrador do
# kind, não com esta conta).
resource "kubernetes_role" "experiments_read_only" {
  metadata {
    name      = "experiments-read-only"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  rule {
    api_groups = [""]
    resources  = ["pods", "pods/log", "services", "endpoints"]
    verbs      = ["get", "list", "watch"]
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "replicasets"]
    verbs      = ["get", "list", "watch"]
  }
}

resource "kubernetes_role_binding" "experiments_read_only" {
  metadata {
    name      = "experiments-read-only"
    namespace = kubernetes_namespace.edge.metadata[0].name
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.experiments_read_only.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.experiments.metadata[0].name
    namespace = kubernetes_namespace.edge.metadata[0].name
  }
}
