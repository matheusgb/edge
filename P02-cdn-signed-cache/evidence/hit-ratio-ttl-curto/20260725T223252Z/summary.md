# Resumo: hit-ratio-ttl-curto

Carga a taxa constante de 500 req/s por 20s sobre 200 objetos, com 10 deles concentrando 80% do tráfego.
TTL anunciado pela origem: 3s.

| Medida | Valor |
|---|---:|
| Requisições oferecidas | 10000 |
| Requisições concluídas | 10000 |
| Taxa de sucesso | 100.00% |
| Vazão concluída | 500.0 req/s |
| p50 / p95 / p99 | 0.34 / 0.55 / 0.68 ms |
| Máxima | 2.31 ms |
| Bytes recebidos pelo cliente | 625.0 MiB |
| **Hit ratio na CDN** | **98.00%** |
| Requisições que chegaram à origem | 831 |
| Corpos servidos pela origem | 200 |
| Revalidações condicionais (304) | 631 |
| Bytes servidos pela origem | 12.5 MiB |
| **Alívio da origem** | **91.69%** |

## Status de cache no log da CDN

| Status | Respostas |
|---|---:|
| HIT | 9169 |
| MISS | 200 |
| STALE | 631 |

## Códigos HTTP vistos pelo cliente

| Código | Respostas |
|---|---:|
| 200 | 10000 |
