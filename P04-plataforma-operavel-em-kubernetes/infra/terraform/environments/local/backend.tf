# Backend local, de propósito: este ambiente é um cluster kind
# descartável na máquina do laboratório, sem estado compartilhado com
# ninguém. Não há segredo real no state.
#
# Em produção, este bloco seria um backend remoto (ex.: S3 + DynamoDB
# para locking, ou Terraform Cloud), com uma identidade de workload
# federada em vez de credenciais estáticas, e um backend por ambiente
# (dev/staging/prod) para isolar o blast radius de um apply. Nada disso
# foi executado aqui: é uma explicação de como seria feito, não uma
# alegação de que foi feito.
terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}
