# O recurso kubernetes_manifest do provider hashicorp/kubernetes
# precisa conhecer o schema da CRD já no "terraform plan", então falha
# quando a CRD (PrometheusRule, instalada pelo Helm de
# bootstrap-observability) ainda não existe no cluster no momento do
# plan, mesmo com depends_on correto: é uma limitação conhecida do
# provider para CRDs criadas fora do próprio plan. kubectl via
# local-exec evita esse problema; o preço é sair da malha puramente
# declarativa só para este recurso, o que fica documentado aqui e no
# README.
resource "null_resource" "prometheus_rules" {
  triggers = {
    rules_sha256 = filesha256("${path.module}/alerts/edge-p04-rules.yaml")
    kube_context = var.kube_context
  }

  provisioner "local-exec" {
    command = "kubectl --context=${var.kube_context} apply -f ${path.module}/alerts/edge-p04-rules.yaml"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "kubectl --context=${self.triggers.kube_context} delete -f ${path.module}/alerts/edge-p04-rules.yaml --ignore-not-found=true"
  }

  depends_on = [kubernetes_namespace.edge]
}
