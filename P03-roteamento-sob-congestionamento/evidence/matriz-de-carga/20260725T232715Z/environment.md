# Ambiente

- Cenário: matriz-de-carga
- Início (UTC): 2026-07-25T23:27:15Z
- Commit: 430c404
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.25.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       9.3Gi       1.3Gi       6.4Mi       5.2Gi       6.2Gi
- Docker: 28.1.1

## Limites do processo gerador

```text
descritores de arquivo (ulimit -n): 1048576
processos e threads (ulimit -u):   (não suportado por este shell)
```

## Containers

```text
edge-a	p03-edge:local	Up 5 minutes
edge-b	p03-edge:local	Up 5 minutes
edge-c	p03-edge:local	Up 5 minutes
grafana	grafana/grafana:12.3.0	Up 5 minutes
loki	grafana/loki:3.5.7	Up 5 minutes
origin	p03-edge:local	Up 5 minutes
prometheus	prom/prometheus:v3.6.0	Up 5 minutes
promtail	grafana/promtail:3.5.7	Up 5 minutes
router	p03-edge:local	Up 5 minutes
tempo	grafana/tempo:2.9.1	Up 5 minutes
toxiproxy	ghcr.io/shopify/toxiproxy:2.12.0	Up 5 minutes
```

## Rede no momento da medição

```json
{
  "edge-a": {
    "enabled": true,
    "listen": "[::]:21001",
    "toxics": [],
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
