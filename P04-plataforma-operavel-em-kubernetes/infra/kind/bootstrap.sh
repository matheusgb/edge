#!/usr/bin/env bash
# Bootstrap do cluster kind do P04. Deliberadamente separado do
# Terraform: o cluster local é uma ferramenta de teste, não
# infraestrutura de cloud, e o Terraform só deve assumir que um cluster
# Kubernetes com contexto kind-edge-lab já existe.
#
# Uso:
#   infra/kind/bootstrap.sh up       cria o cluster, instala Calico, carrega as imagens
#   infra/kind/bootstrap.sh load     builda e carrega as imagens de novo (sem recriar o cluster)
#   infra/kind/bootstrap.sh down     destrói o cluster inteiro
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
CLUSTER_NAME="edge-lab"
CALICO_VERSION="v3.28.2"
CALICO_MANIFEST="https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
IMAGE="p04-edge-lab:local"

log() { echo "[bootstrap] $*" >&2; }

wait_for_calico() {
  log "esperando os pods do Calico ficarem prontos..."
  kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=calico-node --timeout=300s
  kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=calico-kube-controllers --timeout=300s
}

build_and_load() {
  log "buildando a imagem $IMAGE..."
  docker build -t "$IMAGE" "$PROJECT_ROOT"

  log "buildando a imagem de rollout inválido (fail-ready)..."
  cat >/tmp/p04-fail-ready.Dockerfile <<EOF
FROM $IMAGE
ENV ORIGIN_FAIL_READY=true
ENV EDGE_FAIL_READY=true
EOF
  docker build -t p04-edge-lab:fail-ready -f /tmp/p04-fail-ready.Dockerfile "$PROJECT_ROOT"

  log "carregando as imagens no cluster kind..."
  kind load docker-image "$IMAGE" --name "$CLUSTER_NAME"
  kind load docker-image p04-edge-lab:fail-ready --name "$CLUSTER_NAME"

  log "digest carregado (origin):"
  docker inspect --format='{{index .RepoDigests 0}}' "$IMAGE" 2>/dev/null || docker inspect --format='{{.Id}}' "$IMAGE"
}

case "${1:-}" in
  up)
    log "criando o cluster kind ($CLUSTER_NAME)..."
    kind create cluster --name "$CLUSTER_NAME" --config "$HERE/cluster.yaml"

    log "aplicando o Calico $CALICO_VERSION (kindnet não aplica NetworkPolicy)..."
    kubectl apply -f "$CALICO_MANIFEST"
    wait_for_calico

    build_and_load

    log "cluster pronto. Contexto: kind-$CLUSTER_NAME"
    kubectl get nodes -o wide
    ;;
  load)
    build_and_load
    ;;
  down)
    log "destruindo o cluster kind ($CLUSTER_NAME)..."
    kind delete cluster --name "$CLUSTER_NAME"
    ;;
  *)
    echo "uso: $0 {up|load|down}" >&2
    exit 2
    ;;
esac
