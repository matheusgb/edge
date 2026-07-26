# Ambiente

- Cenário: falha-controlada
- Início (UTC): 2026-07-26T01:02:38Z
- Commit: 430c404
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.26.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       9.7Gi       1.3Gi       5.7Mi       4.8Gi       5.7Gi
- Docker: 28.1.1

## Limites do processo gerador

```text
descritores de arquivo (ulimit -n): 1048576
processos e threads (ulimit -u):   (não suportado por este shell)
```

## Containers

```text
edge-a	p03-edge-lab:local	Up 5 minutes
edge-b	p03-edge-lab:local	Up 5 minutes
edge-c	p03-edge-lab:local	Up 5 minutes
grafana	grafana/grafana:12.3.0	Up 2 hours
loki	grafana/loki:3.5.7	Up 2 hours
origin	p03-edge-lab:local	Up 5 minutes
prometheus	prom/prometheus:v3.6.0	Up 2 hours
promtail	grafana/promtail:3.5.7	Up 2 hours
router	p03-edge-lab:local	Up 5 minutes
tempo	grafana/tempo:2.9.1	Up 2 hours
toxiproxy	ghcr.io/shopify/toxiproxy:2.12.0	Up 5 minutes
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
