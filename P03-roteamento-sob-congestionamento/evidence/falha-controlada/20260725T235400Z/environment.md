# Ambiente

- Cenário: falha-controlada
- Início (UTC): 2026-07-25T23:54:00Z
- Commit: 430c404
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.25.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       9.1Gi       1.6Gi       6.4Mi       5.0Gi       6.3Gi
- Docker: 28.1.1

## Limites do processo gerador

```text
descritores de arquivo (ulimit -n): 1048576
processos e threads (ulimit -u):   (não suportado por este shell)
```

## Containers

```text
edge-a	p03-edge:local	Up 29 minutes
edge-b	p03-edge:local	Up 29 minutes
edge-c	p03-edge:local	Up 29 minutes
grafana	grafana/grafana:12.3.0	Up 29 minutes
loki	grafana/loki:3.5.7	Up 29 minutes
origin	p03-edge:local	Up 29 minutes
prometheus	prom/prometheus:v3.6.0	Up 29 minutes
promtail	grafana/promtail:3.5.7	Up 29 minutes
router	p03-edge:local	Up 2 minutes
tempo	grafana/tempo:2.9.1	Up 29 minutes
toxiproxy	ghcr.io/shopify/toxiproxy:2.12.0	Up 2 minutes
```

## Rede no momento da medição

```json
{
  "edge-a": {
    "enabled": true,
    "listen": "[::]:21001",
    "toxics": [
      {
        "attributes": {
          "jitter": 100,
          "latency": 800
        },
        "name": "latencia",
        "stream": "downstream",
        "toxicity": 0,
        "type": "latency"
      },
      {
        "attributes": {
          "timeout": 100
        },
        "name": "reset",
        "stream": "downstream",
        "toxicity": 0,
        "type": "reset_peer"
      }
    ],
    "upstream": "edge-a:8080"
  },
  "edge-b": {
    "enabled": true,
    "listen": "[::]:21002",
    "toxics": [],
    "upstream": "edge-b:8080"
  },
  "edge-c": {
    "enabled": true,
    "listen": "[::]:21003",
    "toxics": [],
    "upstream": "edge-c:8080"
  }
}
```

## Observações

As duas políticas rodam no mesmo processo e nos mesmos containers, com a mesma
semente de catálogo e a mesma sequência de degradação. O roteador é zerado entre
as políticas, mas não entre as fases: o que a política aprendeu na fase anterior
faz parte do comportamento que se quer observar.

O tempo de detecção é aproximado por amostragem a cada 250ms do estado do
roteador, e mede quando a fatia do edge doente caiu abaixo de 15% das tentativas.
Ele não é o instante exato da primeira decisão diferente.

O gerador divide a máquina com os containers medidos.
