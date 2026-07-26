#!/usr/bin/env bash
# Experimento 2: pod encerrado. Um pod da origem é removido no meio de
# uma carga estável. O experimento mede erro, p99 e tempo de
# recuperação até um pod substituto ficar pronto.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
NAMESPACE="edge"
SCENARIO="pod-encerrado"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
# 6 degraus de 10s a uma taxa estável: dá granularidade temporal
# suficiente para isolar o degrau em que o pod foi removido.
STEPS="antes1:40:10s:16,antes2:40:10s:16,durante1:40:10s:16,durante2:40:10s:16,depois1:40:10s:16,depois2:40:10s:16"

ts_utc() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[pod-encerrado] $*" >&2; }

run_once() {
  local run_ts run_dir loadgen_out pod_events
  run_ts="$(date -u +%Y%m%dT%H%M%SZ)"
  run_dir="$EVIDENCE_DIR/$SCENARIO/$run_ts"
  mkdir -p "$run_dir"
  loadgen_out="$(mktemp)"
  pod_events="$(mktemp)"

  local pod_name="loadgen-pod-enc-$RANDOM"
  kubectl run -n "$NAMESPACE" "$pod_name" --rm -i --restart=Never \
    --image=p04-edge:local --image-pull-policy=Never \
    --overrides="{\"spec\":{\"serviceAccountName\":\"experiments\"},\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}" \
    --command -- /app/loadgen \
    -url="http://edge.${NAMESPACE}.svc.cluster.local:8080/work?n=20000" \
    -scenario="$SCENARIO" -steps="$STEPS" -evidence-dir=/tmp/ev -timeout=5s \
    >"$loadgen_out" 2>&1 &
  local loadgen_pid=$!

  # Espera o degrau "antes" terminar (20s) e cai no meio de "durante1".
  sleep 25

  local victim
  victim="$(kubectl -n "$NAMESPACE" get pods -l app=origin -o jsonpath='{.items[0].metadata.name}')"
  # Nomes de todos os pods de origin que já existiam antes da remoção,
  # para não confundir um pod antigo (que nunca deixou de existir, numa
  # réplica > 1) com o substituto de verdade criado pelo ReplicaSet.
  local pre_existing_names
  pre_existing_names="$(kubectl -n "$NAMESPACE" get pods -l app=origin -o jsonpath='{.items[*].metadata.name}')"

  local delete_ts
  delete_ts="$(ts_utc)"
  log "removendo pod $victim em $delete_ts (pods pré-existentes: $pre_existing_names)"

  (
    kubectl -n "$NAMESPACE" get pods -l app=origin -w -o jsonpath='{.metadata.name} {.status.phase} {.status.conditions[?(@.type=="Ready")].status}{"\n"}' 2>/dev/null
  ) >"$pod_events" &
  local watch_pid=$!

  kubectl -n "$NAMESPACE" delete pod "$victim" --wait=false

  # Espera um pod GENUINAMENTE NOVO (nome fora do conjunto pré-existente)
  # ficar Ready.
  local recovery_ts=""
  local deadline=$((SECONDS + 60))
  while [ "$SECONDS" -lt "$deadline" ]; do
    local new_ready
    new_ready="$(kubectl -n "$NAMESPACE" get pods -l app=origin --field-selector=status.phase=Running -o json \
      | python3 -c "
import json,sys
pre_existing = set('''$pre_existing_names'''.split())
data = json.load(sys.stdin)
for p in data['items']:
    name = p['metadata']['name']
    if name in pre_existing:
        continue
    conds = {c['type']: c['status'] for c in p['status'].get('conditions', [])}
    if conds.get('Ready') == 'True':
        print(name)
        break
")"
    if [ -n "$new_ready" ]; then
      recovery_ts="$(ts_utc)"
      log "pod substituto $new_ready pronto em $recovery_ts"
      break
    fi
    sleep 1
  done

  wait "$loadgen_pid" || true
  kill "$watch_pid" 2>/dev/null || true
  wait "$watch_pid" 2>/dev/null || true

  cat >"$run_dir/environment.md" <<EOF
# Ambiente

- cenário: $SCENARIO
- início (UTC): $run_ts
- commit: $(git -C "$PROJECT_ROOT" rev-parse HEAD 2>/dev/null || echo desconhecido)
- máquina: $(hostname)
- pod removido: $victim
- horário da remoção (UTC): $delete_ts
- horário em que o pod substituto ficou pronto (UTC): ${recovery_ts:-não observado em 60s}
EOF

  {
    echo "kubectl -n $NAMESPACE delete pod $victim --wait=false"
    echo "kubectl run -n $NAMESPACE $pod_name --rm -i --restart=Never --image=p04-edge:local --image-pull-policy=Never --command -- /app/loadgen -url=http://edge.$NAMESPACE.svc.cluster.local:8080/work?n=20000 -scenario=$SCENARIO -steps=$STEPS -evidence-dir=/tmp/ev -timeout=5s"
  } >"$run_dir/commands.txt"

  cp "$pod_events" "$run_dir/pod-events.txt"
  awk '/^\[/{flag=1} flag{print} /^\]/{flag=0}' "$loadgen_out" >"$run_dir/loadgen-result.json" || echo "[]" >"$run_dir/loadgen-result.json"

  python3 - "$run_dir" "$delete_ts" "$recovery_ts" <<'PYEOF'
import json, sys, pathlib
from datetime import datetime

run_dir = pathlib.Path(sys.argv[1])
delete_ts = sys.argv[2]
recovery_ts = sys.argv[3]

try:
    steps = json.loads((run_dir / "loadgen-result.json").read_text())
except Exception:
    steps = []

recovery_seconds = None
if recovery_ts:
    fmt = "%Y-%m-%dT%H:%M:%SZ"
    recovery_seconds = (datetime.strptime(recovery_ts, fmt) - datetime.strptime(delete_ts, fmt)).total_seconds()

metrics = {
    "scenario": "pod-encerrado",
    "steps": steps,
    "delete_ts": delete_ts,
    "recovery_ts": recovery_ts or None,
    "recovery_seconds": recovery_seconds,
}
(run_dir / "metrics.json").write_text(json.dumps(metrics, indent=2))

lines = ["# pod-encerrado", "", "Pod da origem removido durante carga estável.", ""]
lines.append("| degrau | completadas | erros | p50 (ms) | p99 (ms) |")
lines.append("| --- | --- | --- | --- | --- |")
for s in steps:
    lines.append(f"| {s['name']} | {s['completed']} | {s['errors']} | {s['p50_ms']:.2f} | {s['p99_ms']:.2f} |")
lines.append("")
if recovery_seconds is not None:
    lines.append(f"Tempo até um pod substituto ficar pronto: {recovery_seconds:.1f}s.")
else:
    lines.append("Pod substituto não observado pronto dentro da janela de 60s de observação.")
(run_dir / "summary.md").write_text("\n".join(lines))
PYEOF

  rm -f "$loadgen_out" "$pod_events"
  log "evidência gravada em $run_dir"
}

reps="${1:-3}"
for i in $(seq 1 "$reps"); do
  log "rodada $i de $reps"
  run_once
  sleep 15
done
