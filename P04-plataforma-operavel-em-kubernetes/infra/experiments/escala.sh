#!/usr/bin/env bash
# Experimento 1: escala. Uma carga em degraus crescentes e depois
# decrescentes é aplicada contra a borda, de dentro do cluster (um pod
# rotulado role=experiments, o único autorizado pela NetworkPolicy a
# falar com a borda). O experimento acompanha saturação, criação de
# réplicas pelo HPA, tempo até capacidade útil e comportamento na
# redução de carga.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
NAMESPACE="edge"
SCENARIO="escala"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
STEPS="degrau1:20:20s:16,degrau2:60:20s:16,degrau3:120:20s:32,degrau4:220:20s:32,degrau5:350:20s:32,descida1:120:20s:32,descida2:60:20s:16,descida3:20:20s:16"

ts_utc() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }

log() { echo "[escala] $*" >&2; }

run_once() {
  local run_ts run_dir watch_log loadgen_out
  run_ts="$(date -u +%Y%m%dT%H%M%SZ)"
  run_dir="$EVIDENCE_DIR/$SCENARIO/$run_ts"
  mkdir -p "$run_dir"
  watch_log="$(mktemp)"
  loadgen_out="$(mktemp)"

  local initial_ready
  initial_ready="$(kubectl -n "$NAMESPACE" get deploy origin -o jsonpath='{.status.readyReplicas}')"
  log "réplicas no início: $initial_ready"

  # Acompanha, com timestamp, cada transição de réplicas do Deployment
  # origin, para medir o tempo até uma nova réplica ficar pronta.
  (
    while true; do
      kubectl -n "$NAMESPACE" get deploy origin -o jsonpath='{.status.replicas} {.status.readyReplicas}{"\n"}' 2>/dev/null
      sleep 2
    done
  ) | while read -r line; do echo "$(ts_utc) $line"; done >"$watch_log" &
  local watch_pid=$!

  local pod_name="loadgen-escala-$RANDOM"
  kubectl run -n "$NAMESPACE" "$pod_name" --rm -i --restart=Never \
    --image=p04-edge-lab:local --image-pull-policy=Never \
    --overrides="{\"spec\":{\"serviceAccountName\":\"experiments\"},\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}" \
    --command -- /app/loadgen \
    -url="http://edge.${NAMESPACE}.svc.cluster.local:8080/work?n=100000" \
    -scenario="$SCENARIO" -steps="$STEPS" -evidence-dir=/tmp/ev -timeout=5s \
    >"$loadgen_out" 2>&1 || true

  # Deixa o HPA terminar de reagir (janela de descida = 120s) antes de
  # parar de observar.
  sleep 130
  kill "$watch_pid" 2>/dev/null || true
  wait "$watch_pid" 2>/dev/null || true

  local final_ready
  final_ready="$(kubectl -n "$NAMESPACE" get deploy origin -o jsonpath='{.status.readyReplicas}')"

  cat >"$run_dir/environment.md" <<EOF
# Ambiente

- cenário: $SCENARIO
- início (UTC): $run_ts
- commit: $(git -C "$PROJECT_ROOT" rev-parse HEAD 2>/dev/null || echo desconhecido)
- máquina: $(hostname)
- sistema operacional: $(uname -s) $(uname -r)
- réplicas de origin no início: $initial_ready
- réplicas de origin ao final (após janela de descida de 120s): $final_ready

## Estado do cluster

\`\`\`
$(kubectl get nodes -o wide 2>&1)
\`\`\`
EOF

  {
    echo "kubectl run -n $NAMESPACE $pod_name --rm -i --restart=Never --image=p04-edge-lab:local --image-pull-policy=Never --command -- /app/loadgen -url=http://edge.$NAMESPACE.svc.cluster.local:8080/work?n=100000 -scenario=$SCENARIO -steps=$STEPS -evidence-dir=/tmp/ev -timeout=5s"
    echo "kubectl -n $NAMESPACE get deploy origin -o jsonpath='{.status.replicas} {.status.readyReplicas}' (observado a cada 2s durante a carga e 130s depois)"
  } >"$run_dir/commands.txt"

  cp "$watch_log" "$run_dir/replica-timeline.txt"

  # Extrai só o JSON de resultados (linha começando com '[') da saída do loadgen.
  awk '/^\[/{flag=1} flag{print} /^\]/{flag=0}' "$loadgen_out" >"$run_dir/loadgen-result.json" || echo "[]" >"$run_dir/loadgen-result.json"

  python3 - "$run_dir" "$final_ready" <<'PYEOF'
import json, sys, pathlib

run_dir = pathlib.Path(sys.argv[1])
final_ready = sys.argv[2]

try:
    steps = json.loads((run_dir / "loadgen-result.json").read_text())
except Exception:
    steps = []

timeline = (run_dir / "replica-timeline.txt").read_text().splitlines()

metrics = {
    "scenario": "escala",
    "steps": steps,
    "final_ready_replicas": final_ready,
    "replica_timeline_lines": len(timeline),
}
(run_dir / "metrics.json").write_text(json.dumps(metrics, indent=2))

lines = ["# escala", "", "Carga em degraus contra a borda, medindo reação do HPA da origem.", ""]
lines.append("| degrau | rps alvo | completadas | erros | throughput (rps) | p50 (ms) | p99 (ms) |")
lines.append("| --- | --- | --- | --- | --- | --- | --- |")
for s in steps:
    lines.append(f"| {s['name']} | {s['target_rps']} | {s['completed']} | {s['errors']} | {s['throughput_rps']:.1f} | {s['p50_ms']:.2f} | {s['p99_ms']:.2f} |")
lines.append("")
lines.append(f"Réplicas de origin ao final da janela de descida (120s): {final_ready}.")
(run_dir / "summary.md").write_text("\n".join(lines))
PYEOF

  rm -f "$watch_log" "$loadgen_out"
  log "evidência gravada em $run_dir"
}

wait_for_baseline() {
  local deadline=$((SECONDS + 180))
  while [ "$SECONDS" -lt "$deadline" ]; do
    local ready
    ready="$(kubectl -n "$NAMESPACE" get deploy origin -o jsonpath='{.status.readyReplicas}')"
    if [ "$ready" = "2" ]; then
      return
    fi
    sleep 5
  done
  log "aviso: origin não voltou a 2 réplicas em 180s, seguindo com $(kubectl -n "$NAMESPACE" get deploy origin -o jsonpath='{.status.readyReplicas}') réplicas"
}

reps="${1:-3}"
for i in $(seq 1 "$reps"); do
  log "rodada $i de $reps"
  wait_for_baseline
  run_once
done
