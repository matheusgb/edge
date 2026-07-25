# Resumo: hit-ratio-ttl-curto

Carga a taxa constante de 500 req/s por 20s sobre 200 objetos, com 10 deles concentrando 80% do tráfego.
TTL anunciado pela origem: 3s.

| Medida | Valor |
|---|---:|
| Requisições oferecidas | 10000 |
| Requisições concluídas | 10000 |
| Taxa de sucesso | 100.00% |
| Vazão concluída | 500.0 req/s |
| p50 / p95 / p99 | 0.35 / 0.57 / 0.69 ms |
| Máxima | 2.50 ms |
| Bytes recebidos pelo cliente | 625.0 MiB |
| **Hit ratio na borda** | **98.00%** |
| Requisições que chegaram à origem | 816 |
| Corpos servidos pela origem | 200 |
| Revalidações condicionais (304) | 616 |
| Bytes servidos pela origem | 12.5 MiB |
| **Alívio da origem** | **91.84%** |

## Status de cache no log da borda

| Status | Respostas |
|---|---:|
| HIT | 9184 |
| MISS | 200 |
| STALE | 616 |

## Códigos HTTP vistos pelo cliente

| Código | Respostas |
|---|---:|
| 200 | 10000 |
