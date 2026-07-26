# Falha controlada: round-robin contra a política adaptativa

Taxa oferecida 1200 req/s, janela de 8s por fase, gerador em modelo aberto.
O edge degradado é o edge-a; edge-b e edge-c ficam saudáveis o tempo todo.

## Fases do roteiro

- **sem-falha**: os três edges saudáveis, para ter a linha de base
- **latencia-50ms**: edge-a começa a ficar lento, ainda respondendo tudo
- **latencia-200ms**: edge-a piora, e o prazo por tentativa começa a apertar
- **latencia-800ms**: edge-a passa do prazo de uma tentativa, sem nunca ter caído
- **conexao-cortada**: além de lento, edge-a passa a cortar 30% das conexões
- **recuperacao**: edge-a volta ao normal, e a pergunta é quanto tempo até ser usado de novo

## O que o cliente observou

| Fase | Política | Concluída/s | Erro | p50 ms | p95 ms | p99 ms | max ms |
|---|---|---:|---:|---:|---:|---:|---:|
| sem-falha | round-robin | 1199 | 0.00% | 0.5 | 6.3 | 6.8 | 7.3 |
| sem-falha | adaptativa | 1199 | 0.00% | 0.5 | 6.4 | 6.9 | 7.6 |
| latencia-50ms | round-robin | 1191 | 0.00% | 0.6 | 59.9 | 65.1 | 151.7 |
| latencia-50ms | adaptativa | 1200 | 0.00% | 0.5 | 6.2 | 6.7 | 7.3 |
| latencia-200ms | round-robin | 1167 | 0.00% | 0.6 | 228.0 | 232.3 | 280.7 |
| latencia-200ms | adaptativa | 1200 | 0.00% | 0.6 | 6.4 | 6.8 | 7.8 |
| latencia-800ms | round-robin | 700 | 35.80% | 0.6 | 801.1 | 806.2 | 807.8 |
| latencia-800ms | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.7 | 7.5 |
| conexao-cortada | round-robin | 683 | 37.48% | 0.6 | 801.1 | 805.5 | 807.7 |
| conexao-cortada | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 7.4 |
| recuperacao | round-robin | 1199 | 0.00% | 0.6 | 6.5 | 6.9 | 8.5 |
| recuperacao | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.7 | 7.5 |

## Para onde o tráfego foi, e o que custou

| Fase | Política | Distribuição das tentativas | Falhas no doente | Retry concedido | Retry negado | Detecção |
|---|---|---|---:|---:|---:|---:|
| sem-falha | round-robin | edge-a 33.3% | edge-b 33.3% | edge-c 33.3% | 0 | 0 | 0 | não detectou |
| sem-falha | adaptativa | edge-a 33.9% | edge-b 32.2% | edge-c 33.9% | 0 | 0 | 0 | 2.5s |
| latencia-50ms | round-robin | edge-a 33.3% | edge-b 33.3% | edge-c 33.3% | 0 | 0 | 0 | não detectou |
| latencia-50ms | adaptativa | edge-b 49.6% | edge-c 50.4% | 0 | 0 | 0 | 0.3s |
| latencia-200ms | round-robin | edge-a 33.3% | edge-b 33.3% | edge-c 33.3% | 0 | 0 | 0 | 2.2s |
| latencia-200ms | adaptativa | edge-b 50.2% | edge-c 49.8% | 0 | 0 | 0 | 0.2s |
| latencia-800ms | round-robin | edge-a 30.2% | edge-b 35.3% | edge-c 34.4% | 1284 | 770 | 514 | 0.2s |
| latencia-800ms | adaptativa | edge-b 51.8% | edge-c 48.2% | 0 | 0 | 0 | 0.2s |
| conexao-cortada | round-robin | edge-a 30.5% | edge-b 35.3% | edge-c 34.1% | 1643 | 762 | 881 | 0.3s |
| conexao-cortada | adaptativa | edge-b 49.8% | edge-c 50.2% | 0 | 0 | 0 | 0.2s |
| recuperacao | round-robin | edge-a 33.3% | edge-b 33.3% | edge-c 33.3% | 0 | 0 | 0 | não detectou |
| recuperacao | adaptativa | edge-b 50.4% | edge-c 49.6% | 0 | 0 | 0 | 0.2s |

## O que aconteceu com quem estava saudável

A política adaptativa só é melhor se preservar o sistema inteiro. Deslocar
toda a carga e derrubar os edges restantes seria falha, não vitória.

| Fase | Política | HIT nos edges | MISS nos edges | Buscas na origem | Simultaneidade na origem | Goroutines no roteador |
|---|---|---:|---:|---:|---:|---:|
| sem-falha | round-robin | 8172 | 1428 | 1426 | 0 | 59 |
| sem-falha | adaptativa | 8202 | 1399 | 1394 | 0 | 1305 |
| latencia-50ms | round-robin | 8180 | 1419 | 1417 | 0 | 719 |
| latencia-50ms | adaptativa | 8234 | 1366 | 1364 | 0 | 1312 |
| latencia-200ms | round-robin | 8170 | 1429 | 1426 | 0 | 1019 |
| latencia-200ms | adaptativa | 8163 | 1437 | 1431 | 0 | 1142 |
| latencia-800ms | round-robin | 7181 | 1295 | 1294 | 0 | 1137 |
| latencia-800ms | adaptativa | 8198 | 1402 | 1398 | 0 | 1011 |
| conexao-cortada | round-robin | 7152 | 1233 | 1233 | 0 | 1483 |
| conexao-cortada | adaptativa | 8181 | 1419 | 1415 | 0 | 795 |
| recuperacao | round-robin | 8166 | 1434 | 1427 | 0 | 1476 |
| recuperacao | adaptativa | 8166 | 1434 | 1424 | 0 | 499 |
