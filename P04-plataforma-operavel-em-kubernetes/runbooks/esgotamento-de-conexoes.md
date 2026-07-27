# Runbook: esgotamento de conexões

## Sintoma

Requisições começam a falhar com timeout ou conexão recusada, mesmo com CPU e
memória dos pods aparentemente normais. O alerta `DeploymentComPodsIndisponiveis`
pode disparar junto, se o sintoma também derrubar a readiness.

Este runbook é procedimental. O laboratório não rodou um experimento dedicado
de esgotamento de conexões (os cinco experimentos obrigatórios estão em
`evidence/`). Por isso os passos abaixo descrevem como investigar a partir do
que a plataforma realmente expõe, e não a partir de um número medido de
"quantas conexões até esgotar".

## Diagnóstico

1. Confirmar que não é o cenário do runbook de latência alta (CPU saturada).
   Ver `runbooks/latencia-alta.md`.
2. Checar conexões em andamento por pod, via a porta administrativa (nunca
   exposta fora do cluster):
   ```bash
   kubectl -n edge port-forward deploy/origin 8090:8090
   curl -s localhost:8090/metrics | grep -E "origin_requests_in_flight|edge_requests_in_flight"
   ```
   Um valor de `_in_flight` crescendo sem parar, sem corresponder a mais
   respostas concluídas em `origin_requests_total`/`edge_requests_total`,
   indica requisições penduradas.
3. Os `http.Server` de origin e edge (`internal/httpx/httpx.go`) têm
   `ReadTimeout=30s`, `WriteTimeout=60s` e `IdleTimeout=90s`. Se as conexões
   penduradas excederem esses valores e ainda assim continuarem contadas como
   em andamento, o processo pode estar bloqueado em código, não em rede. Nesse
   caso, coletar um perfil de goroutines (`pprof` na porta administrativa)
   antes de reiniciar qualquer pod, para não perder o diagnóstico.
4. Checar se a borda está de fato reaproveitando conexões com a origem
   (`http.Transport.MaxIdleConnsPerHost`, configurado em
   `internal/loadtest/loadtest.go` para o gerador de carga). O cliente HTTP da
   borda em `internal/edgesrv/edgesrv.go` usa o transport padrão da
   biblioteca, sem um pool dedicado. Ajustar isso é a correção mais provável
   se o sintoma for causado por reabertura excessiva de conexões da borda para
   a origem.

## Ação humana

1. Se o `_in_flight` estiver alto mas as respostas nunca chegam a `5xx` nem a
   completar, coletar o perfil de goroutines antes de agir.
2. Se o diagnóstico apontar para excesso de conexões novas da borda para a
   origem, considerar configurar `MaxIdleConnsPerHost` no cliente HTTP de
   `internal/edgesrv/edgesrv.go` e reconstruir a imagem.
3. Se for apenas volume genuíno de tráfego, seguir o runbook de latência alta.
   O sintoma de conexões é, com frequência, o mesmo problema de capacidade
   visto de outro ângulo.
4. Em último caso, um `kubectl -n edge rollout restart deployment/origin` (ou
   `edge`) libera conexões penduradas, mas descarta o diagnóstico. Só usar
   depois de já ter coletado o perfil do passo 1.

## Confirmação da recuperação

```bash
kubectl -n edge port-forward deploy/origin 8090:8090
curl -s localhost:8090/metrics | grep origin_requests_in_flight
```

O valor deve voltar a oscilar perto de zero entre picos de carga, e
`kubectl -n edge get pods` deve mostrar todos os pods `Ready`, sem
`RESTARTS` crescendo.
