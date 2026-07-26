# Post-mortem: remoção de um pod da origem durante carga

**Status:** falha controlada, laboratório. **Severidade:** nenhuma percebida
pelo cliente, nas rodadas medidas. **Evidência:** `evidence/pod-encerrado/`.

## Resumo

Um pod da origem foi removido manualmente (`kubectl delete pod`) durante uma
carga estável de 40 requisições por segundo, para observar se o
PodDisruptionBudget, o Service e as probes conseguiam absorver a perda de uma
réplica sem impacto visível ao cliente. Em três rodadas medidas, o tempo até
um pod substituto ficar pronto foi de 5,0s, 5,0s e 5,0s (`evidence/pod-encerrado/*/metrics.json`),
e nenhuma delas registrou erro do cliente nos degraus que cobrem o momento da
remoção.

## Linha do tempo (rodada `20260726T032448Z`)

| horário (UTC) | evento |
| --- | --- |
| 03:24:53 (aprox.) | carga estável de 40 rps começa contra a borda |
| 03:25:03–03:25:13 | degrau "antes2": 1 erro em 400 requisições, antes da remoção (não relacionado à falha injetada) |
| 03:25:13 | `kubectl delete pod origin-7664f4ffcb-8pn7z` |
| 03:25:13–03:25:23 | degraus "durante1" e "durante2": 800 requisições, 0 erros |
| 03:25:18 | pod substituto `origin-7664f4ffcb-zjmf8` fica `Ready` (5,0s depois da remoção) |
| 03:25:23–03:25:43 | degraus "depois1" e "depois2": 800 requisições, 0 erros |

## Impacto

Nenhum erro do cliente correlacionado com a remoção do pod, nas três rodadas
medidas. O único erro observado (rodada `20260726T032448Z`, degrau "antes2",
1 de 400 requisições) ocorreu **antes** do `kubectl delete pod` e é mais
provavelmente ruído do início da carga do que efeito da falha injetada; as
outras duas rodadas não tiveram nenhum erro em nenhum degrau.

## Causa raiz (por que funcionou)

- `origin` rodava com mais de uma réplica no momento da remoção (o HPA já
  havia escalado por causa de experimentos anteriores na mesma sessão), então
  o Service `origin` continuou tendo pelo menos um endpoint saudável o tempo
  todo.
- O PodDisruptionBudget `origin` (`min_available = 1`,
  `infra/terraform/edge-platform/pdb.tf`) não impediu a remoção manual (PDB só
  protege contra *disrupções voluntárias* coordenadas pelo Kubernetes, como
  drain de nó; um `kubectl delete pod` direto não passa por ele), e o que
  efetivamente absorveu o impacto foi a borda já apontando para múltiplas
  réplicas da origem via Service/DNS, não o PDB.
- O `startupProbe` (`period_seconds=1`, `failure_threshold=30`) permitiu que o
  pod substituto entrasse em rotação assim que passou no `/readyz`, sem esperar
  o intervalo mais longo da `readinessProbe` regular (5s).

## O que funcionou bem

- Distribuição de carga entre réplicas pelo Service absorveu a perda de uma
  réplica sem fila visível no cliente.
- O tempo de recuperação (5s) ficou consistente nas três rodadas, sugerindo
  que o gargalo é o `warmup` configurado (`-warmup=3s` mais a primeira
  passagem do `startupProbe`), não uma variável instável do ambiente.

## O que não foi comprovado

- Este teste não teve **apenas** duas réplicas no momento da remoção (o
  esperado pelo `origin_replicas_min`); com exatamente o mínimo configurado, a
  margem para absorver a perda de uma réplica é menor, e o resultado poderia
  mostrar erro visível. Uma repetição com o HPA forçado de volta a 2 réplicas
  antes do teste ficaria mais fiel ao pior caso do "mínimo configurado".
- O laboratório não testou a remoção de réplicas via drenagem de nó (onde o
  PDB realmente entra em ação), só remoção direta de pod.

## Ações corretivas consideradas

1. Repetir o experimento fixando `origin_replicas_min` em 2 réplicas ativas no
   momento exato da remoção, para medir o pior caso real, não um caso já
   favorecido por escala prévia.
2. Adicionar um teste de drenagem de nó (`kubectl drain`) para exercitar o PDB
   de verdade, já que a remoção direta de pod não passa por ele.
