#!/usr/bin/env bash
# Experimento 5: destruição. Depois de "terraform destroy", confirma
# que os recursos que o Terraform gerenciava desapareceram de verdade,
# e que o cluster kind em si continua de pé (destruir é escopo do
# Terraform, não do kind: infra/kind/bootstrap.sh down é quem derruba o
# cluster inteiro). Cada rodada destrói e reaplica, para deixar o
# cluster utilizável para o próximo experimento.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
TF_DIR="$PROJECT_ROOT/infra/terraform/environments/local"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
SCENARIO="destruicao"

ts_utc() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[destruicao] $*" >&2; }

run_once() {
  local run_ts run_dir
  run_ts="$(date -u +%Y%m%dT%H%M%SZ)"
  run_dir="$EVIDENCE_DIR/$SCENARIO/$run_ts"
  mkdir -p "$run_dir"

  log "kind ainda de pé antes do destroy? $(kubectl get nodes --no-headers 2>&1 | wc -l) nós"

  local destroy_output destroy_rc
  set +e
  destroy_output="$(cd "$TF_DIR" && terraform destroy -auto-approve 2>&1)"
  destroy_rc=$?
  set -e
  log "terraform destroy rc=$destroy_rc"
  echo "$destroy_output" >"$run_dir/terraform-destroy-output.txt"

  local ns_edge ns_obs nodes_after
  ns_edge="$(kubectl get ns edge -o name 2>&1 || true)"
  ns_obs="$(kubectl get ns observability -o name 2>&1 || true)"
  nodes_after="$(kubectl get nodes --no-headers 2>&1 | wc -l)"

  local edge_gone="nao"
  local obs_gone="nao"
  echo "$ns_edge" | grep -qi "not found" && edge_gone="sim"
  echo "$ns_obs" | grep -qi "not found" && obs_gone="sim"

  cat >"$run_dir/environment.md" <<EOF
# Ambiente

- cenário: $SCENARIO
- início (UTC): $run_ts
- commit: $(git -C "$PROJECT_ROOT" rev-parse HEAD 2>/dev/null || echo desconhecido)
- máquina: $(hostname)
- rc do terraform destroy: $destroy_rc
- namespace edge removido: $edge_gone
- namespace observability removido: $obs_gone
- nós do cluster kind ainda de pé depois do destroy: $nodes_after
EOF

  {
    echo "terraform -chdir=infra/terraform/environments/local destroy -auto-approve"
    echo "kubectl get ns edge"
    echo "kubectl get ns observability"
    echo "kubectl get nodes"
  } >"$run_dir/commands.txt"

  python3 - "$run_dir" "$destroy_rc" "$edge_gone" "$obs_gone" "$nodes_after" <<'PYEOF'
import json, sys, pathlib

run_dir = pathlib.Path(sys.argv[1])
destroy_rc = int(sys.argv[2])
edge_gone = sys.argv[3] == "sim"
obs_gone = sys.argv[4] == "sim"
nodes_after = int(sys.argv[5])

ok = destroy_rc == 0 and edge_gone and obs_gone and nodes_after == 3

metrics = {
    "scenario": "destruicao",
    "destroy_rc": destroy_rc,
    "edge_namespace_gone": edge_gone,
    "observability_namespace_gone": obs_gone,
    "kind_nodes_still_up": nodes_after,
    "destruction_confirmed_and_cluster_survives": ok,
}
(run_dir / "metrics.json").write_text(json.dumps(metrics, indent=2))

lines = [
    "# destruicao", "",
    f"terraform destroy retornou código {destroy_rc}.",
    f"namespace edge removido: {edge_gone}.",
    f"namespace observability removido: {obs_gone}.",
    f"nós do cluster kind ainda de pé depois do destroy: {nodes_after} (esperado 3, já que destroy é escopo do Terraform, não do kind).",
    "",
    f"Resultado geral esperado (destruição confirmada e cluster kind sobrevive): {ok}.",
]
(run_dir / "summary.md").write_text("\n".join(lines))
PYEOF

  log "evidência gravada em $run_dir"

  log "reaplicando para deixar o cluster utilizável para o próximo experimento..."
  (cd "$TF_DIR" && terraform apply -auto-approve >/tmp/destruicao-reapply.log 2>&1)
  log "reapply concluído"
}

reps="${1:-3}"
for i in $(seq 1 "$reps"); do
  log "rodada $i de $reps"
  run_once
done
