# Resumo: hit-ratio-ttl-longo

Carga a taxa constante de 500 req/s por 20s sobre 200 objetos, com 10 deles concentrando 80% do tráfego.
TTL anunciado pela origem: 1m0s.

| Medida | Valor |
|---|---:|
| Requisições oferecidas | 10000 |
| Requisições concluídas | 10000 |
| Taxa de sucesso | 100.00% |
| Vazão concluída | 500.0 req/s |
| p50 / p95 / p99 | 0.34 / 0.54 / 0.65 ms |
| Máxima | 1.08 ms |
| Bytes recebidos pelo cliente | 625.0 MiB |
| **Hit ratio na borda** | **98.00%** |
| Requisições que chegaram à origem | 200 |
| Corpos servidos pela origem | 200 |
| Revalidações condicionais (304) | 0 |
| Bytes servidos pela origem | 12.5 MiB |
| **Alívio da origem** | **98.00%** |

## Status de cache no log da borda

| Status | Respostas |
|---|---:|
| HIT | 9800 |
| MISS | 200 |

## Códigos HTTP vistos pelo cliente

| Código | Respostas |
|---|---:|
| 200 | 10000 |
