# Provider kubernetes/helm são configurados por quem chama este módulo
# (infra/terraform/environments/local), não aqui: um módulo chamado que
# define seu próprio bloco provider não pode ser usado com depends_on.
variable "observability_namespace" {
  description = "Namespace onde Prometheus, Grafana e o Ingress Controller são instalados."
  type        = string
  default     = "observability"
}

variable "kube_prometheus_stack_version" {
  description = "Versão do chart prometheus-community/kube-prometheus-stack."
  type        = string
  default     = "65.5.1"
}

variable "ingress_nginx_version" {
  description = "Versão do chart ingress-nginx/ingress-nginx."
  type        = string
  default     = "4.11.3"
}

variable "metrics_server_version" {
  description = "Versão do chart metrics-server/metrics-server."
  type        = string
  default     = "3.12.2"
}

variable "helm_timeout_seconds" {
  description = "Timeout de instalação para cada helm_release, em segundos."
  type        = number
  default     = 600
}
