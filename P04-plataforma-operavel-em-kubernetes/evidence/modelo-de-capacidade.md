# Modelo de capacidade

Este documento aplica `cmd/capacity` à premissa de 3 milhões de requisições por
minuto (50 mil rps) definida em `edge.md`. Ele separa o que foi **medido**
neste laboratório do que é **projetado** a partir dessa premissa.

## Entrada medida

O experimento de escala (`evidence/escala/`) nunca chegou a saturar uma única
réplica da origem. O HPA ([Horizontal Pod Autoscaler](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/), o mecanismo do Kubernetes que ajusta o número de réplicas automaticamente) reagiu de forma proativa antes que qualquer réplica individual ficasse
sobrecarregada. Por isso não existe uma medição direta de "quantas requisições
por segundo uma única réplica aguenta até degradar".

A capacidade por réplica usada aqui vem, em vez disso, do `BenchmarkWork`
(`internal/originsrv/originsrv_bench_test.go`), que mediu o custo de CPU do
handler `/work` neste hardware:

```
BenchmarkWork/n=100000-32    200    789919 ns/op
```

Ou seja, 100.000 iterações de hash custam ~0,79 ms de CPU. O request de CPU da
origem é `100m` (`infra/terraform/edge-platform/variables.tf`). A 100% de uso
desse request, uma réplica sustentaria:

```
0,1 core / 0,00078992 s por requisição ≈ 126,6 requisições/s
```

O HPA da origem tem como alvo 50% de utilização do request
(`hpa_target_cpu_utilization_percent = 50`). Esse é o mesmo ponto em que ele
efetivamente decide escalar. Por isso `capacity-per-replica-rps=126.6` e
`target-utilization=0.5` entram juntos no modelo: a combinação reproduz o
comportamento real do HPA configurado, não um número arbitrário.

## Comando executado

```
go run ./cmd/capacity \
  -capacity-per-replica-rps=126.6 \
  -target-utilization=0.5 \
  -headroom=1.2 \
  -cache-hit-rate=0 \
  -avg-response-bytes=65536 \
  -zones=3 \
  -zone-failure-tolerant=true
```

## Resultado

| Rótulo | Valor |
| --- | --- |
| Medido | capacidade por réplica ≈ 127 rps (via benchmark de CPU, não via saturação end-to-end) |
| Premissa | 50.000 rps |
| Projetado | rps efetivo após cache hit de 0% = 50.000 rps |
| Projetado | réplicas base (utilização alvo 50%) = 790 |
| Projetado | réplicas com headroom (1,2x) = 948 |
| Projetado | réplicas finais tolerantes à perda de 1 de 3 zonas = 1.422 |

Banda necessária estimada: ~3,28 GB/s no total, ~2,3 MB/s por réplica com
1.422 réplicas. Isso fica dentro de qualquer limite razoável por réplica, então
banda não é o fator limitante deste cenário.

## Limites explícitos

- **0% de cache hit é a suposição mais conservadora.** Na prática, o cache da
  borda (TTL de 5s) reduziria a carga na origem, na proporção em que o acesso
  se concentrasse em poucos objetos populares. O `edge.md` descreve esse
  padrão de tráfego desigual como premissa do laboratório, mas ele não foi
  medido aqui isoladamente.
- **"3 zonas" é uma analogia com os 3 nós do cluster kind local** (1
  control-plane + 2 workers), não zonas de disponibilidade reais de um
  provedor de nuvem. O `topology_spread_constraint` usado em
  `infra/terraform/edge-platform/deployment.tf` distribui pods entre esses
  nós. Ainda assim, um único host físico continua sendo um ponto único de
  falha real.
- **1.422 réplicas não foram criadas nem testadas.** O laboratório não prova
  que este laptop, este cluster kind ou qualquer configuração local sustenta
  50 mil requisições por segundo. Ele projeta quantas réplicas seriam
  necessárias **se** a capacidade medida por réplica se mantivesse linear
  nessa escala. Essa é uma suposição forte, que uma medição real de produção
  poderia contradizer (contenção de rede, limites do nó, cache real, etc.).
