# Matriz de carga (política adaptativa)

Taxa base 1200 req/s. Gerador em modelo aberto: a taxa é disparada na hora
marcada, mesmo que a resposta anterior não tenha voltado.

## O que o cliente observou

| Cenário | Alvo req/s | Oferecida/s | Concluída/s | Erro | p50 ms | p95 ms | p99 ms | max ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| baseline | 300 | 300 | 300 | 0.00% | 0.6 | 6.2 | 6.3 | 8.0 |
| load | 1200 | 1200 | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 8.0 |
| spike | 7200 | 7175 | 4432 | 38.23% | 53.4 | 171.0 | 235.1 | 349.2 |
| stress | 12000 | 11999 | 0 | 100.00% | 1.1 | 1.7 | 1.9 | 6.3 |
| bandwidth | 600 | 600 | 205 | 65.83% | 0.8 | 5.1 | 6.9 | 520.3 |
| latency | 1200 | 1200 | 675 | 43.75% | 0.8 | 6.0 | 6.7 | 302.6 |
| conexao-cortada | 1200 | 1200 | 1200 | 0.00% | 0.5 | 6.3 | 6.7 | 8.1 |
| outage | 1200 | 1200 | 1078 | 10.14% | 0.6 | 6.9 | 12.6 | 43.3 |
| recovery | 1200 | 1200 | 1101 | 8.26% | 0.6 | 6.4 | 6.9 | 34.0 |

## O que o roteador relatou

| Cenário | Distribuição das tentativas | Retry concedido | Retry negado | Recusado | Goroutines | Descritores |
|---|---|---:|---:|---|---:|---:|
| baseline | edge-a 42.1% | edge-b 25.8% | edge-c 32.1% | 0 | 0 | nenhum | 817 | 417 |
| load | edge-a 33.2% | edge-b 32.9% | edge-c 33.9% | 0 | 0 | nenhum | 833 | 433 |
| spike | edge-a 34.5% | edge-b 32.7% | edge-c 32.7% | 0 | 0 | concorrencia 6996 | 1188 | 810 |
| stress |  | 0 | 0 | nenhum | 1188 | 810 |
| bandwidth | edge-a 0.0% | edge-b 48.5% | edge-c 51.5% | 0 | 0 | nenhum | 1180 | 806 |
| latency | edge-a 0.0% | edge-b 49.4% | edge-c 50.6% | 0 | 0 | nenhum | 1176 | 802 |
| conexao-cortada | edge-b 49.5% | edge-c 50.5% | 0 | 0 | nenhum | 72 | 56 |
| outage | edge-a 5.5% | edge-b 17.9% | edge-c 76.6% | 1900 | 673 | nenhum | 87 | 72 |
| recovery | edge-a 57.8% | edge-b 0.4% | edge-c 41.8% | 198 | 1785 | nenhum | 131 | 106 |

## O que os edges e a origem relataram

| Cenário | HIT | MISS | Buscas na origem | Requisições na origem | Simultaneidade na origem no fim |
|---|---:|---:|---:|---:|---:|
| baseline | 5101 | 899 | 898 | 898 | 0 |
| load | 20519 | 3480 | 3474 | 3474 | 0 |
| spike | 19057 | 3181 | 3116 | 3116 | 0 |
| stress | 0 | 0 | 0 | 0 | 0 |
| bandwidth | 3482 | 619 | 619 | 619 | 0 |
| latency | 11493 | 2007 | 2001 | 2001 | 0 |
| conexao-cortada | 20450 | 3550 | 3542 | 3542 | 0 |
| outage | 20722 | 3757 | 3733 | 3733 | 0 |
| recovery | 18769 | 3503 | 3482 | 3482 | 0 |

## A pergunta de cada cenário

- **baseline**: qual a latência e o throughput sem nenhuma falha
- **load**: o sistema sustenta a taxa alvo abaixo da saturação
- **spike**: o que a rajada provoca, e o backpressure aparece
- **stress**: qual recurso limita primeiro quando a taxa passa do teto
- **bandwidth**: o que acontece quando o link de um edge fica estreito
- **latency**: um edge saudável e lento continua recebendo a mesma carga
- **conexao-cortada**: timeout, retry e recuperação quando a conexão cai no meio
- **outage**: um edge fora do ar, e depois a origem também
- **recovery**: a volta causa nova avalanche na origem
