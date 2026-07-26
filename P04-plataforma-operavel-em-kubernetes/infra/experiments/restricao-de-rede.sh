#!/usr/bin/env bash
# Experimento 4: restrição de rede. Confirma que a NetworkPolicy
# (aplicada pelo Calico, não pelo kindnet padrão) bloqueia de verdade o
# tráfego não autorizado, e não apenas existe como declaração. Cobre
# cinco casos: dois de tráfego permitido e três de tráfego que deve ser
# recusado.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
NAMESPACE="edge"
SCENARIO="restricao-de-rede"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"

ts_utc() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
log() { echo "[restricao-de-rede] $*" >&2; }

# Executa o loadgen num pod descartável e devolve só o bloco JSON de
# resultado. $1=nome $2=rotulado-como-experiments(true/false) $3=url
run_probe() {
  local name="$1" labeled="$2" url="$3"
  local overrides
  if [ "$labeled" = "true" ]; then
    overrides="{\"spec\":{\"serviceAccountName\":\"experiments\"},\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}"
  else
    overrides="{}"
  fi
  local out
  out="$(kubectl run -n "$NAMESPACE" "$name" --rm -i --restart=Never \
    --image=p04-edge-lab:local --image-pull-policy=Never \
    --overrides="$overrides" \
    --command -- /app/loadgen -url="$url" -scenario=probe -steps=x:2:3s:2 -evidence-dir=/tmp/ev -timeout=2s 2>&1 || true)"
  echo "$out" | awk '/^\[/{flag=1} flag{print} /^\]/{flag=0}'
}

run_once() {
  local run_ts run_dir
  run_ts="$(date -u +%Y%m%dT%H%M%SZ)"
  run_dir="$EVIDENCE_DIR/$SCENARIO/$run_ts"
  mkdir -p "$run_dir"

  log "caso A: autorizado -> edge (deveria funcionar)"
  local a_json b_json c_json d_json e_json
  a_json="$(run_probe "probe-a-$RANDOM" true "http://edge.${NAMESPACE}.svc.cluster.local:8080/object/rede-a.bin")"

  log "caso B: autorizado -> origin direto (deveria ser bloqueado, só edge tem permissão)"
  b_json="$(run_probe "probe-b-$RANDOM" true "http://origin.${NAMESPACE}.svc.cluster.local:8080/object/rede-b.bin")"

  log "caso C: não autorizado -> edge (deveria ser bloqueado)"
  c_json="$(run_probe "probe-c-$RANDOM" false "http://edge.${NAMESPACE}.svc.cluster.local:8080/object/rede-c.bin")"

  log "caso D: não autorizado -> origin direto (deveria ser bloqueado)"
  d_json="$(run_probe "probe-d-$RANDOM" false "http://origin.${NAMESPACE}.svc.cluster.local:8080/object/rede-d.bin")"

  log "caso E: autorizado -> porta admin da origin (deveria ser bloqueado, só observability pode ver /metrics)"
  e_json="$(run_probe "probe-e-$RANDOM" true "http://origin.${NAMESPACE}.svc.cluster.local:8090/metrics")"

  local netpol_describe
  netpol_describe="$(kubectl -n "$NAMESPACE" describe networkpolicy 2>&1)"
  echo "$netpol_describe" >"$run_dir/networkpolicy-describe.txt"

  cat >"$run_dir/environment.md" <<EOF
# Ambiente

- cenário: $SCENARIO
- início (UTC): $run_ts
- commit: $(git -C "$PROJECT_ROOT" rev-parse HEAD 2>/dev/null || echo desconhecido)
- máquina: $(hostname)
- CNI: Calico (kindnet padrão do kind não aplica NetworkPolicy, por isso foi substituído em infra/kind/bootstrap.sh)

## Casos

- A: pod com rótulo role=experiments -> edge:8080 (esperado: permitido)
- B: pod com rótulo role=experiments -> origin:8080 direto (esperado: bloqueado, só a borda tem permissão de falar com a origem)
- C: pod sem o rótulo -> edge:8080 (esperado: bloqueado)
- D: pod sem o rótulo -> origin:8080 direto (esperado: bloqueado)
- E: pod com rótulo role=experiments -> origin:8090/metrics, porta administrativa (esperado: bloqueado, só o Prometheus no namespace observability pode ler métricas)
EOF

  {
    echo "kubectl run -n $NAMESPACE probe-a --rm -i --restart=Never --image=p04-edge-lab:local --image-pull-policy=Never --overrides='{\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}' --command -- /app/loadgen -url=http://edge.$NAMESPACE.svc.cluster.local:8080/object/rede-a.bin ..."
    echo "kubectl run -n $NAMESPACE probe-b --rm -i --restart=Never ... --overrides='{\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}' --command -- /app/loadgen -url=http://origin.$NAMESPACE.svc.cluster.local:8080/object/rede-b.bin ..."
    echo "kubectl run -n $NAMESPACE probe-c --rm -i --restart=Never ... (sem rótulo) --command -- /app/loadgen -url=http://edge.$NAMESPACE.svc.cluster.local:8080/object/rede-c.bin ..."
    echo "kubectl run -n $NAMESPACE probe-d --rm -i --restart=Never ... (sem rótulo) --command -- /app/loadgen -url=http://origin.$NAMESPACE.svc.cluster.local:8080/object/rede-d.bin ..."
    echo "kubectl run -n $NAMESPACE probe-e --rm -i --restart=Never ... --overrides='{\"metadata\":{\"labels\":{\"role\":\"experiments\"}}}' --command -- /app/loadgen -url=http://origin.$NAMESPACE.svc.cluster.local:8090/metrics ..."
    echo "kubectl -n $NAMESPACE describe networkpolicy"
  } >"$run_dir/commands.txt"

  for c in a b c d e; do
    eval "echo \"\$${c}_json\"" >"$run_dir/caso-$c.json"
  done

  python3 - "$run_dir" <<'PYEOF'
import json, sys, pathlib

run_dir = pathlib.Path(sys.argv[1])

def load(name):
    try:
        return json.loads((run_dir / f"caso-{name}.json").read_text())
    except Exception:
        return []

cases = {
    "A_autorizado_para_edge": (load("a"), True),
    "B_autorizado_para_origin_direto": (load("b"), False),
    "C_nao_autorizado_para_edge": (load("c"), False),
    "D_nao_autorizado_para_origin_direto": (load("d"), False),
    "E_autorizado_para_porta_admin": (load("e"), False),
}

results = {}
all_ok = True
for name, (steps, expected_allowed) in cases.items():
    completed = sum(s.get("completed", 0) for s in steps)
    errors = sum(s.get("errors", 0) for s in steps)
    allowed = completed > 0 and errors == 0
    ok = allowed == expected_allowed
    all_ok = all_ok and ok
    results[name] = {
        "completed": completed,
        "errors": errors,
        "allowed_observado": allowed,
        "permitido_esperado": expected_allowed,
        "resultado_correto": ok,
    }

(run_dir / "metrics.json").write_text(json.dumps({"scenario": "restricao-de-rede", "cases": results, "all_as_expected": all_ok}, indent=2))

lines = ["# restricao-de-rede", "", "NetworkPolicy aplicada pelo Calico; cada caso confirma bloqueio ou liberação real de tráfego.", ""]
lines.append("| caso | permitido esperado | observado permitido | correto |")
lines.append("| --- | --- | --- | --- |")
for name, r in results.items():
    lines.append(f"| {name} | {r['permitido_esperado']} | {r['allowed_observado']} | {r['resultado_correto']} |")
lines.append("")
lines.append(f"Todos os casos se comportaram como esperado: {all_ok}.")
(run_dir / "summary.md").write_text("\n".join(lines))
PYEOF

  log "evidência gravada em $run_dir"
}

reps="${1:-3}"
for i in $(seq 1 "$reps"); do
  log "rodada $i de $reps"
  run_once
done
