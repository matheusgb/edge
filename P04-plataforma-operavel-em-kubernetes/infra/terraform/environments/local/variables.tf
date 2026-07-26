variable "kubeconfig_path" {
  type    = string
  default = "~/.kube/config"
}

variable "kube_context" {
  description = "Contexto esperado do kubeconfig. O bootstrap do kind (infra/kind/bootstrap.sh) precisa ter rodado antes, criando este contexto; o Terraform não cria o cluster."
  type        = string
  default     = "kind-edge-lab"
}

variable "origin_image" {
  type    = string
  default = "p04-edge-lab:local"
}

variable "edge_image" {
  type    = string
  default = "p04-edge-lab:local"
}
