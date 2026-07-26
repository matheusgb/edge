#!/usr/bin/env bash
# Experimento 3: rollout inválido. Uma versão que nunca passa na
# readiness é implantada. O Deployment usa maxUnavailable=0, então os
# pods antigos continuam de pé e servindo enquanto o rollout trava; o
# Kubernetes nunca deveria enviar tráfego para a versão nova. Depois,
# um rollback restaura o estado anterior.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
NAMESPACE="edge"
SCENARIO="rollout-invalido"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
BAD_IMAGE="p04-edge:fail-ready"
GOOD_IMAGE="p04-edge:local"
STEPS="antes1:30:15s:16,antes2:30:15s:16,ruim1:30:15s:16,ruim2:30:15s:16,ruim3:30:15s:16,rollback1:30:15s:16,rollback2:30:15s:16,rollback3:30:15s:16"

ts_utc() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[rollout-invalido] $*" >&2; }

run_once() {
  local run_ts run_dir loadgen_out
  run_ts="$(date -u +%Y%m%dT%H%M%SZ)"
  run_dir="$EVIDENCE_DIR/$SCENARIO/$run_ts"
  mkdir -p "$run_dir"
  loadgen_out="$(mktemp)"

  local pod_name="loadgen-rollout-$RANDOM"
  kubectl run -n "$NAMESPACE" "$pod_name" --rm -i --restart=Never \
    --image=p04-edge:local --image-pull-policy=Never \
    --overrides="{\"spec\":{\"serviceAccountName\":\"experiments\"},\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}" \
    --command -- /app/loadgen \
    -url="http://edge.${NAMESPACE}.svc.cluster.local:8080/object/rollout-test.bin" \
    -scenario="$SCENARIO" -steps="$STEPS" -evidence-dir=/tmp/ev -timeout=5s \
    >"$loadgen_out" 2>&1 &
  local loadgen_pid=$!

  sleep 30 # cobre antes1+antes2

  log "aplicando a imagem inválida ($BAD_IMAGE)..."
  local endpoints_before
  endpoints_before="$(kubectl -n "$NAMESPACE" get endpoints origin -o jsonpath='{.subsets[*].addresses[*].ip}')"
  kubectl -n "$NAMESPACE" set image deployment/origin origin="$BAD_IMAGE"

  local rollout_status_output rollout_rc
  set +e
  rollout_status_output="$(kubectl -n "$NAMESPACE" rollout status deployment/origin --timeout=40s 2>&1)"
  rollout_rc=$?
  set -e
  log "rollout status (esperado: timeout/erro) rc=$rollout_rc"

  local endpoints_during
  endpoints_during="$(kubectl -n "$NAMESPACE" get endpoints origin -o jsonpath='{.subsets[*].addresses[*].ip}')"
  # Comparar o conjunto inteiro de endpoints "antes" vs "durante" é
  # ruidoso: um scale-down do HPA acontecendo na mesma janela também
  # muda o conjunto, sem relação com o rollout ruim. O sinal correto é
  # mais específico: nenhum IP de um pod com a imagem ruim pode
  # aparecer nos endpoints.
  local bad_pod_check
  bad_pod_check="$(kubectl -n "$NAMESPACE" get pods -l app=origin -o json | python3 -c "
import json,sys
data = json.load(sys.stdin)
bad_not_ready = 0
bad_ips = []
for p in data['items']:
    img = p['spec']['containers'][0]['image']
    conds = {c['type']: c['status'] for c in p['status'].get('conditions', [])}
    if img.endswith('fail-ready'):
        if conds.get('Ready') != 'True':
            bad_not_ready += 1
        ip = p['status'].get('podIP')
        if ip:
            bad_ips.append(ip)
print(bad_not_ready)
print(' '.join(bad_ips))
")"
  local bad_pods_not_ready bad_pod_ips
  bad_pods_not_ready="$(echo "$bad_pod_check" | sed -n '1p')"
  bad_pod_ips="$(echo "$bad_pod_check" | sed -n '2p')"
  local bad_ip_in_endpoints="nao"
  for ip in $bad_pod_ips; do
    if echo "$endpoints_during" | grep -qw "$ip"; then
      bad_ip_in_endpoints="sim"
    fi
  done

  log "revertendo para $GOOD_IMAGE..."
  kubectl -n "$NAMESPACE" rollout undo deployment/origin
  kubectl -n "$NAMESPACE" rollout status deployment/origin --timeout=60s || true

  wait "$loadgen_pid" || true

  local endpoints_after
  endpoints_after="$(kubectl -n "$NAMESPACE" get endpoints origin -o jsonpath='{.subsets[*].addresses[*].ip}')"

  cat >"$run_dir/environment.md" <<EOF
# Ambiente

- cenário: $SCENARIO
- início (UTC): $run_ts
- commit: $(git -C "$PROJECT_ROOT" rev-parse HEAD 2>/dev/null || echo desconhecido)
- máquina: $(hostname)
- imagem inválida aplicada: $BAD_IMAGE
- rc do 'kubectl rollout status' durante a imagem inválida (esperado != 0, ou seja, travou/expirou): $rollout_rc
- pods com a imagem inválida e nunca prontos, no momento da checagem: $bad_pods_not_ready
- IP de algum pod com a imagem inválida presente nos endpoints do Service (esperado "nao"): $bad_ip_in_endpoints
- endpoints do Service origin antes do rollout: $endpoints_before
- endpoints do Service origin durante a imagem inválida: $endpoints_during
- endpoints do Service origin depois do rollback: $endpoints_after
EOF

  {
    echo "kubectl -n $NAMESPACE set image deployment/origin origin=$BAD_IMAGE"
    echo "kubectl -n $NAMESPACE rollout status deployment/origin --timeout=40s"
    echo "kubectl -n $NAMESPACE get endpoints origin"
    echo "kubectl -n $NAMESPACE rollout undo deployment/origin"
    echo "kubectl -n $NAMESPACE rollout status deployment/origin --timeout=60s"
  } >"$run_dir/commands.txt"

  echo "$rollout_status_output" >"$run_dir/rollout-status-output.txt"
  awk '/^\[/{flag=1} flag{print} /^\]/{flag=0}' "$loadgen_out" >"$run_dir/loadgen-result.json" || echo "[]" >"$run_dir/loadgen-result.json"

  python3 - "$run_dir" "$rollout_rc" "$bad_pods_not_ready" "$bad_ip_in_endpoints" "$endpoints_before" "$endpoints_during" "$endpoints_after" <<'PYEOF'
import json, sys, pathlib

run_dir = pathlib.Path(sys.argv[1])
rollout_rc = int(sys.argv[2])
bad_pods_not_ready = int(sys.argv[3])
bad_ip_in_endpoints = sys.argv[4] == "sim"
endpoints_before, endpoints_during, endpoints_after = sys.argv[5], sys.argv[6], sys.argv[7]

try:
    steps = json.loads((run_dir / "loadgen-result.json").read_text())
except Exception:
    steps = []

total_errors = sum(s["errors"] for s in steps)
total_completed = sum(s["completed"] for s in steps)

metrics = {
    "scenario": "rollout-invalido",
    "steps": steps,
    "rollout_status_rc": rollout_rc,
    "bad_pods_never_ready": bad_pods_not_ready,
    "bad_pod_ip_reached_endpoints": bad_ip_in_endpoints,
    "endpoints_before": endpoints_before,
    "endpoints_during_bad_rollout": endpoints_during,
    "endpoints_after_rollback": endpoints_after,
    "total_errors": total_errors,
    "total_completed": total_completed,
}
(run_dir / "metrics.json").write_text(json.dumps(metrics, indent=2))

lines = ["# rollout-invalido", "", "Imagem que nunca fica pronta implantada; espera-se rollout travado, zero tráfego para a versão ruim, e rollback bem-sucedido.", ""]
lines.append("| degrau | completadas | erros | p99 (ms) |")
lines.append("| --- | --- | --- | --- |")
for s in steps:
    lines.append(f"| {s['name']} | {s['completed']} | {s['errors']} | {s['p99_ms']:.2f} |")
lines.append("")
lines.append(f"rollout status retornou código {rollout_rc} (diferente de 0 = travou/expirou, como esperado).")
lines.append(f"pods com a imagem ruim nunca prontos no momento da checagem: {bad_pods_not_ready}.")
lines.append(f"IP de pod com a imagem ruim chegou a aparecer nos endpoints do Service: {bad_ip_in_endpoints} (esperado False).")
lines.append(f"total de erros no cliente durante todo o experimento: {total_errors} de {total_completed} requisições.")
(run_dir / "summary.md").write_text("\n".join(lines))
PYEOF

  rm -f "$loadgen_out"
  log "evidência gravada em $run_dir"
}

reps="${1:-3}"
for i in $(seq 1 "$reps"); do
  log "rodada $i de $reps"
  run_once
  sleep 10
done
