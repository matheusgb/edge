# Pré-condição: o cluster kind (criado por infra/kind/bootstrap.sh, de
# propósito fora do Terraform) precisa existir e o contexto precisar
# bater com o esperado, antes de qualquer módulo rodar. Sem isso, um
# "terraform apply" com o kubeconfig apontando para o contexto errado
# aplicaria contra o cluster errado silenciosamente.
resource "null_resource" "preflight_kind_context" {
  triggers = {
    always_run = timestamp()
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      current="$(kubectl config current-context)"
      if [ "$current" != "${var.kube_context}" ]; then
        echo "contexto atual é '$current', esperado '${var.kube_context}'. Rode infra/kind/bootstrap.sh up primeiro." >&2
        exit 1
      fi
    EOT
  }
}

module "bootstrap_observability" {
  source = "../../bootstrap-observability"

  providers = {
    kubernetes = kubernetes
    helm       = helm
  }

  depends_on = [null_resource.preflight_kind_context]
}

module "edge_platform" {
  source = "../../edge-platform"

  providers = {
    kubernetes = kubernetes
  }

  kube_context            = var.kube_context
  observability_namespace = module.bootstrap_observability.observability_namespace
  origin_image            = var.origin_image
  edge_image              = var.edge_image

  depends_on = [module.bootstrap_observability]
}
