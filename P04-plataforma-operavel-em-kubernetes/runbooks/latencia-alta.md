# Runbook: latência alta

## Sintoma

O alerta `OrigemP99Alto` dispara (p99 da origem acima de 500ms por 5 minutos
seguidos, ver `infra/terraform/edge-platform/alerts/edge-lab-p04-rules.yaml`)
ou o dashboard `edge-lab P04 - plataforma` no Grafana mostra o painel "p99 de
latência (origin)" subindo de forma sustentada.

## Diagnóstico

1. Confirmar o sintoma no Grafana (Prometheus namespace `observability`,
   dashboard carregado via ConfigMap rotulado `edge_lab_dashboard=1`) ou
   diretamente:
   ```bash
   kubectl -n observability port-forward svc/kube-prometheus-stack-prometheus 9090:9090
   # depois, no navegador: histogram_quantile(0.99, sum(rate(origin_request_duration_seconds_bucket[5m])) by (le))
   ```
2. Checar se o HPA já reagiu:
   ```bash
   kubectl -n edge get hpa origin
   ```
   Se `TARGETS` estiver perto ou acima do alvo (50%) e `REPLICAS` já estiver em
   `origin_replicas_max`, a origem está no teto de escala configurado, ver
   "Ação" abaixo, item 2.
3. Checar saturação de CPU por pod:
   ```bash
   kubectl -n edge top pods -l app=origin
   ```
4. Checar se existe um rollout em andamento que possa estar sem capacidade
   suficiente ainda de pé:
   ```bash
   kubectl -n edge rollout status deployment/origin
   ```

## Ação humana

1. **HPA ainda tem margem** (`REPLICAS` < `origin_replicas_max`): aguardar a
   janela de reação do HPA. O experimento de escala
   (`evidence/escala/`) mediu, neste laboratório, réplicas subindo de 2 para
   8 em cerca de 2 minutos sob carga crescente; esse é o retardo de referência,
   não uma garantia de produção.
2. **HPA no teto** (`REPLICAS` == `origin_replicas_max`): a origem está
   subdimensionada para a carga atual. Rodar o modelo de capacidade
   (`go run ./cmd/capacity -capacity-per-replica-rps=<medido> ...`, ver
   `evidence/modelo-de-capacidade.md`) com a capacidade por réplica atualizada
   e, se justificado, aumentar `origin_replicas_max` em
   `infra/terraform/edge-platform/variables.tf` e reaplicar.
3. **CPU não está saturada mas a latência subiu mesmo assim**: suspeitar da
   origem real dos milissegundos (rede entre nós, GC, contenção no host) e
   coletar um perfil (`pprof` na porta administrativa `:8090`, nunca exposta
   fora do cluster) antes de mudar limites às cegas.

## Confirmação da recuperação

```bash
kubectl -n edge get hpa origin
# TARGETS deve voltar a ficar abaixo do alvo de forma sustentada
```

E, no Prometheus/Grafana, `OrigemP99Alto` deve voltar ao estado `inactive` e o
painel de p99 deve estabilizar abaixo de 500ms por pelo menos os mesmos 5
minutos usados como janela do alerta.
