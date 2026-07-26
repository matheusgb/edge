# Matriz de carga (política adaptativa)

Taxa base 1200 req/s. Gerador em modelo aberto: a taxa é disparada na hora
marcada, mesmo que a resposta anterior não tenha voltado.

## O que o cliente observou

| Cenário | Alvo req/s | Oferecida/s | Concluída/s | Erro | p50 ms | p95 ms | p99 ms | max ms |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| baseline | 300 | 300 | 300 | 0.00% | 0.6 | 6.2 | 6.5 | 8.0 |
| load | 1200 | 1200 | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 7.6 |
| spike | 7200 | 7073 | 4698 | 33.58% | 61.1 | 135.9 | 178.9 | 293.7 |
| stress | 12000 | 11788 | 2805 | 76.20% | 98.0 | 208.8 | 271.6 | 463.9 |
| bandwidth | 600 | 600 | 600 | 0.00% | 0.6 | 6.4 | 7.0 | 518.7 |
| latency | 1200 | 1200 | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 344.1 |
| conexao-cortada | 1200 | 1200 | 1200 | 0.00% | 0.5 | 6.4 | 6.9 | 26.7 |
| outage | 1200 | 1200 | 1072 | 10.67% | 0.6 | 6.8 | 7.7 | 72.8 |
| recovery | 1200 | 1200 | 1100 | 8.29% | 0.5 | 6.4 | 6.9 | 8.3 |

## O que o roteador relatou

| Cenário | Distribuição das tentativas | Retry concedido | Retry negado | Recusado | Goroutines | Descritores |
|---|---|---:|---:|---|---:|---:|
| baseline | edge-a 34.7%, edge-b 27.7%, edge-c 37.6% | 0 | 0 | nenhum | 35 | 26 |
| load | edge-a 31.6%, edge-b 33.5%, edge-c 34.9% | 0 | 0 | nenhum | 68 | 51 |
| spike | edge-a 33.8%, edge-b 32.5%, edge-c 33.7% | 0 | 0 | concorrencia 12090 | 2670 | 2261 |
| stress | edge-a 32.7%, edge-b 33.8%, edge-c 33.5% | 0 | 0 | concorrencia 91446 | 5934 | 5494 |
| bandwidth | edge-a 0.1%, edge-b 49.0%, edge-c 50.9% | 0 | 0 | nenhum | 5095 | 5080 |
| latency | edge-a 0.0%, edge-b 49.9%, edge-c 50.1% | 0 | 0 | nenhum | 5108 | 5089 |
| conexao-cortada | edge-a 29.4%, edge-b 36.1%, edge-c 34.5% | 0 | 0 | nenhum | 1877 | 1809 |
| outage | edge-a 7.0%, edge-b 43.3%, edge-c 49.6% | 1910 | 686 | nenhum | 333 | 268 |
| recovery | edge-a 47.7%, edge-b 21.0%, edge-c 31.3% | 200 | 1791 | nenhum | 254 | 230 |

## O que os edges e a origem relataram

| Cenário | HIT | MISS | Buscas na origem | Requisições na origem | Simultaneidade na origem no fim |
|---|---:|---:|---:|---:|---:|
| baseline | 5088 | 912 | 911 | 911 | 3 |
| load | 20565 | 3435 | 3431 | 3431 | 9 |
| spike | 20331 | 3579 | 3498 | 3498 | 31 |
| stress | 24447 | 4107 | 4047 | 4047 | 38 |
| bandwidth | 10211 | 1789 | 1788 | 1788 | 5 |
| latency | 20521 | 3478 | 3467 | 3467 | 9 |
| conexao-cortada | 20482 | 3519 | 3508 | 3508 | 10 |
| outage | 20581 | 3505 | 3481 | 3481 | 8 |
| recovery | 18757 | 3452 | 3435 | 3435 | 9 |

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
