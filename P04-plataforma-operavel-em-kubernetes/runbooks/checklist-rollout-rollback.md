# Checklist de rollout e rollback

Este checklist é baseado no experimento `rollout-invalido`
(`evidence/rollout-invalido/`). Ele testou o seguinte cenário: uma imagem que
nunca passa na [checagem de prontidão](https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/)
(readiness probe), aplicada a um Deployment com `maxUnavailable=0`. Nesse
caso, os pods antigos não devem perder tráfego em nenhum momento.

## Antes do rollout

- [ ] `go test -race ./...` e `go vet ./...` passando.
- [ ] Imagem construída e carregada no cluster (`infra/kind/bootstrap.sh load`
      num cluster já de pé), com a tag/digest que vai para
      `origin_image`/`edge_image` em `infra/terraform/environments/local`.
- [ ] `terraform plan -out=edge.tfplan` revisado: confirmar que só a imagem (e
      o que depende dela) está mudando, não recursos inesperados.
- [ ] Confirmar `min_available` do PDB e `origin_replicas_min`/`edge_replicas_min`
      atuais, para saber quantas réplicas saudáveis existem antes de começar.

## Durante o rollout

- [ ] `terraform apply edge.tfplan`.
- [ ] Acompanhar `kubectl -n edge rollout status deployment/<origin|edge>`. Se
      ele não terminar dentro de alguns minutos, é sinal de readiness falhando
      na versão nova. **Não forçar** `kubectl rollout resume` nem aumentar
      `maxUnavailable` só para destravar; ir direto para "Critério de
      rollback" abaixo.
- [ ] Confirmar que os pods antigos continuam `Ready` e recebendo tráfego:
      ```bash
      kubectl -n edge get endpoints origin
      kubectl -n edge get pods -l app=origin
      ```
- [ ] Observar `origin_requests_total{status=~"5.."}` no Prometheus/Grafana
      durante toda a janela: não deveria subir.

## Critério de rollback

Reverter imediatamente se qualquer um for verdadeiro:

- `kubectl rollout status` não termina em até 2 minutos (o teste mediu rc
  diferente de 0 já aos 40s, ver `evidence/rollout-invalido/*/metrics.json`).
- Qualquer pod da versão nova nunca fica `Ready` depois de
  `startup_probe.failure_threshold * period_seconds` segundos (30s neste
  projeto).
- A taxa de erro do cliente sobe, mesmo que os pods antigos ainda estejam de
  pé (sinal de que algo além da readiness também quebrou).

## Rollback

```bash
kubectl -n edge rollout undo deployment/origin
kubectl -n edge rollout status deployment/origin --timeout=60s
```

Ou, para manter tudo declarado em Terraform: reverter `origin_image`/`edge_image`
em `infra/terraform/environments/local/terraform.tfvars` para a tag/digest
anterior e rodar `terraform apply` de novo.

## Depois do rollback

- [ ] Confirmar `kubectl -n edge get endpoints origin` (ou `edge`) só lista IPs
      de pods com a imagem antiga.
- [ ] Confirmar que a taxa de erro do cliente voltou à taxa anterior ao
      rollout (nas rodadas medidas, 0 erros do início ao fim, ver
      `evidence/rollout-invalido/*/summary.md`).
- [ ] Abrir o post-mortem se o rollback teve qualquer impacto visível ao
      cliente (nas rodadas medidas aqui, não teve).
