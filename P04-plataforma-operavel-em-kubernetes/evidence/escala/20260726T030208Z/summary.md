# escala

Carga em degraus contra a borda, medindo reação do HPA da origem.

| degrau | rps alvo | completadas | erros | throughput (rps) | p50 (ms) | p99 (ms) |
| --- | --- | --- | --- | --- | --- | --- |
| degrau1 | 20 | 400 | 0 | 20.0 | 1.56 | 2.31 |
| degrau2 | 60 | 1200 | 0 | 60.0 | 1.39 | 1.73 |
| degrau3 | 120 | 2400 | 0 | 120.0 | 1.39 | 1.83 |
| degrau4 | 220 | 4400 | 0 | 220.0 | 1.30 | 1.64 |
| degrau5 | 350 | 7000 | 0 | 350.0 | 1.30 | 1.60 |
| descida1 | 120 | 2400 | 0 | 120.0 | 1.39 | 1.95 |
| descida2 | 60 | 1200 | 0 | 60.0 | 1.46 | 1.85 |
| descida3 | 20 | 400 | 0 | 20.0 | 1.66 | 2.37 |

Réplicas de origin ao final da janela de descida (120s): 7.