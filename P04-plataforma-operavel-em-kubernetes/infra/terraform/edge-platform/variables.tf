# Provider kubernetes é configurado por quem chama este módulo
# (infra/terraform/environments/local), não aqui: um módulo chamado que
# define seu próprio bloco provider não pode ser usado com depends_on.
#
# kube_context ainda é usado (não pelo provider, mas pelo local-exec do
# kubectl em alerts.tf, que roda fora do grafo de providers do
# Terraform).
variable "kube_context" {
  type    = string
  default = "kind-edge"
}

variable "edge_namespace" {
  description = "Namespace dos workloads da plataforma (origin, edge)."
  type        = string
  default     = "edge"
}

variable "observability_namespace" {
  description = "Namespace do Prometheus, usado para liberar o scrape via NetworkPolicy."
  type        = string
  default     = "observability"
}

variable "origin_image" {
  description = <<-EOT
    Referência da imagem da origem, carregada no kind por tag (kind load
    docker-image), não por digest. Em produção, com um registry real,
    esta variável apontaria para nome@sha256:<digest>; um cluster kind
    local não tem registry, então o Laboratório usa uma tag
    determinística e documenta a diferença no README.
  EOT
  type        = string
  default     = "p04-edge:local"
}

variable "edge_image" {
  description = "Referência da imagem da borda. Mesma observação de origin_image."
  type        = string
  default     = "p04-edge:local"
}

variable "origin_replicas_min" {
  type    = number
  default = 2
}

variable "origin_replicas_max" {
  type    = number
  default = 8
}

variable "edge_replicas_min" {
  type    = number
  default = 2
}

variable "edge_replicas_max" {
  type    = number
  default = 4
}

variable "hpa_target_cpu_utilization_percent" {
  description = "Utilização de CPU alvo do HPA, em porcentagem do request."
  type        = number
  default     = 50
}

variable "origin_resources" {
  description = <<-EOT
    Requests/limits da origem. Medido a partir do benchmark local de
    /work (BenchmarkWork, ~7,9ns por iteração de hash neste hardware) e
    de observação de RSS do processo em repouso (~poucos MiB, binário
    estático sem framework). Ajustado com folga para o experimento de
    escala não ser limitado por memória antes de ser limitado por CPU.
  EOT
  type = object({
    requests_cpu    = string
    requests_memory = string
    limits_cpu      = string
    limits_memory   = string
  })
  default = {
    requests_cpu    = "100m"
    requests_memory = "32Mi"
    limits_cpu      = "500m"
    limits_memory   = "128Mi"
  }
}

variable "edge_resources" {
  type = object({
    requests_cpu    = string
    requests_memory = string
    limits_cpu      = string
    limits_memory   = string
  })
  default = {
    requests_cpu    = "50m"
    requests_memory = "32Mi"
    limits_cpu      = "250m"
    limits_memory   = "64Mi"
  }
}

variable "shutdown_timeout" {
  description = "Valor passado para -shutdown-timeout nos binários; terminationGracePeriodSeconds deve exceder este valor com margem."
  type        = string
  default     = "15s"
}

variable "termination_grace_period_seconds" {
  type    = number
  default = 30
}

variable "warmup" {
  description = "Valor passado para -warmup; usado também para calibrar o startupProbe."
  type        = string
  default     = "3s"
}

variable "cache_ttl" {
  type    = string
  default = "5s"
}
