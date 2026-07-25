# Resumo: cache stampede

100 clientes pedem o MESMO objeto ausente no mesmo instante, com a origem a 300ms de latência.

| Variante | Lock | Chamadas na origem | Corpos servidos | MiB da origem | p50 ms | p99 ms |
|---|---|---:|---:|---:|---:|---:|
| com-lock | true | 1 | 1 | 1.0 | 621.7 | 694.0 |
| sem-lock | false | 100 | 100 | 100.0 | 319.9 | 375.9 |

Sem lock a origem recebeu **100.0 vezes** mais chamadas para entregar o mesmo objeto.

## com-lock (/objects/hot-1MiB-com-lock-1785014088.bin)

| Status de cache | Respostas |
|---|---:|
| HIT | 99 |
| MISS | 1 |

| Código HTTP | Respostas |
|---|---:|
| 200 | 100 |

## sem-lock (/nolock/hot-1MiB-sem-lock-1785014088.bin)

| Status de cache | Respostas |
|---|---:|
| MISS | 100 |

| Código HTTP | Respostas |
|---|---:|
| 200 | 100 |
