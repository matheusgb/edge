# Falha controlada: round-robin contra a política adaptativa

Taxa oferecida 1200 req/s, janela de 20s por fase, gerador em modelo aberto.
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
| sem-falha | round-robin | 1200 | 0.00% | 0.6 | 6.4 | 6.9 | 7.8 |
| sem-falha | adaptativa | 1200 | 0.00% | 0.5 | 6.2 | 6.7 | 8.7 |
| latencia-50ms | round-robin | 1196 | 0.00% | 0.6 | 59.9 | 65.1 | 139.1 |
| latencia-50ms | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.7 | 65.6 |
| latencia-200ms | round-robin | 1187 | 0.00% | 0.6 | 228.0 | 231.4 | 260.7 |
| latencia-200ms | adaptativa | 1187 | 0.00% | 0.5 | 6.3 | 6.8 | 236.5 |
| latencia-800ms | round-robin | 731 | 36.62% | 0.6 | 801.1 | 805.9 | 809.0 |
| latencia-800ms | adaptativa | 1200 | 0.01% | 0.5 | 6.4 | 6.8 | 801.0 |
| conexao-cortada | round-robin | 724 | 37.28% | 0.6 | 801.1 | 805.3 | 808.0 |
| conexao-cortada | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 800.9 |
| recuperacao | round-robin | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 9.5 |
| recuperacao | adaptativa | 1200 | 0.00% | 0.5 | 6.3 | 6.8 | 9.1 |

## Para onde o tráfego foi, e o que custou

| Fase | Política | Distribuição das tentativas | Falhas no doente | Retry concedido | Retry negado | Detecção |
|---|---|---|---:|---:|---:|---:|
| sem-falha | round-robin | edge-a 33.3%, edge-b 33.3%, edge-c 33.3% | 0 | 0 | 0 | não detectou |
| sem-falha | adaptativa | edge-a 32.6%, edge-b 33.0%, edge-c 34.4% | 0 | 0 | 0 | não detectou |
| latencia-50ms | round-robin | edge-a 33.3%, edge-b 33.3%, edge-c 33.3% | 0 | 0 | 0 | não detectou |
| latencia-50ms | adaptativa | edge-a 0.0%, edge-b 49.9%, edge-c 50.1% | 0 | 0 | 0 | 0.7s |
| latencia-200ms | round-robin | edge-a 33.3%, edge-b 33.3%, edge-c 33.3% | 0 | 0 | 0 | não detectou |
| latencia-200ms | adaptativa | edge-a 0.0%, edge-b 48.9%, edge-c 51.1% | 0 | 0 | 0 | 0.8s |
| latencia-800ms | round-robin | edge-a 30.5%, edge-b 34.7%, edge-c 34.8% | 3253 | 1902 | 1351 | 4.2s |
| latencia-800ms | adaptativa | edge-a 0.0%, edge-b 50.3%, edge-c 49.6% | 0 | 0 | 0 | 0.7s |
| conexao-cortada | round-robin | edge-a 30.5%, edge-b 35.0%, edge-c 34.6% | 4077 | 1901 | 2176 | 4.2s |
| conexao-cortada | adaptativa | edge-a 0.0%, edge-b 50.4%, edge-c 49.6% | 1 | 1 | 0 | 0.8s |
| recuperacao | round-robin | edge-a 33.3%, edge-b 33.3%, edge-c 33.3% | 0 | 0 | 0 | não detectou |
| recuperacao | adaptativa | edge-a 34.1%, edge-b 34.0%, edge-c 31.9% | 0 | 0 | 0 | 0.7s |

## O que aconteceu com quem estava saudável

A política adaptativa só é melhor se preservar o sistema inteiro. Deslocar
toda a carga e derrubar os edges restantes seria falha, não vitória.

| Fase | Política | HIT nos edges | MISS nos edges | Buscas na origem | Simultaneidade na origem | Goroutines no roteador |
|---|---|---:|---:|---:|---:|---:|
| sem-falha | round-robin | 20516 | 3484 | 3479 | 9 | 64 |
| sem-falha | adaptativa | 20521 | 3478 | 3472 | 9 | 586 |
| latencia-50ms | round-robin | 20383 | 3617 | 3607 | 17 | 787 |
| latencia-50ms | adaptativa | 20461 | 3539 | 3528 | 9 | 276 |
| latencia-200ms | round-robin | 20454 | 3546 | 3538 | 17 | 986 |
| latencia-200ms | adaptativa | 20448 | 3552 | 3534 | 9 | 107 |
| latencia-800ms | round-robin | 17840 | 3081 | 3073 | 17 | 1130 |
| latencia-800ms | adaptativa | 20467 | 3532 | 3513 | 9 | 103 |
| conexao-cortada | round-robin | 17818 | 3093 | 3087 | 17 | 1023 |
| conexao-cortada | adaptativa | 20521 | 3480 | 3470 | 9 | 98 |
| recuperacao | round-robin | 20539 | 3461 | 3458 | 17 | 874 |
| recuperacao | adaptativa | 20514 | 3486 | 3483 | 9 | 100 |
