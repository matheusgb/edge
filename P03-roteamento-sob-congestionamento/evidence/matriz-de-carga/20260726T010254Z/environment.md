# Ambiente

- Cenário: matriz-de-carga
- Início (UTC): 2026-07-26T01:02:54Z
- Commit: 430c404
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.26.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       9.4Gi       1.7Gi       5.7Mi       4.7Gi       6.0Gi
- Docker: 28.1.1

## Limites do processo gerador

```text
descritores de arquivo (ulimit -n): 1048576
processos e threads (ulimit -u):   (não suportado por este shell)
```

## Containers

```text
edge-a	p03-edge-lab:local	Up 10 minutes
edge-b	p03-edge-lab:local	Up 10 minutes
edge-c	p03-edge-lab:local	Up 10 minutes
grafana	grafana/grafana:12.3.0	Up 2 hours
loki	grafana/loki:3.5.7	Up 2 hours
origin	p03-edge-lab:local	Up 10 minutes
prometheus	prom/prometheus:v3.6.0	Up 2 hours
promtail	grafana/promtail:3.5.7	Up 2 hours
router	p03-edge-lab:local	Up 10 minutes
tempo	grafana/tempo:2.9.1	Up 2 hours
toxiproxy	ghcr.io/shopify/toxiproxy:2.12.0	Up 4 minutes
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
          "rate": 512
        },
        "name": "banda",
        "stream": "downstream",
        "toxicity": 0,
        "type": "bandwidth"
      },
      {
        "attributes": {
          "jitter": 50,
          "latency": 300
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

O gerador de carga divide a máquina com os containers medidos. Parte da
latência da cauda é disputa por CPU entre quem mede e quem é medido, e não
capacidade do serviço.

O cenário chamado "conexao-cortada" corta conexões TCP com probabilidade; ele
não descarta pacotes IP. O Toxiproxy trabalha acima da camada de transporte, e
usar o nome "perda de pacotes" aqui seria impreciso.
