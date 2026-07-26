# escala

Carga em degraus contra a borda, medindo reação do HPA da origem.

| degrau | rps alvo | completadas | erros | throughput (rps) | p50 (ms) | p99 (ms) |
| --- | --- | --- | --- | --- | --- | --- |
| degrau1 | 20 | 400 | 0 | 20.0 | 1.55 | 1.91 |
| degrau2 | 60 | 1200 | 0 | 60.0 | 1.44 | 1.79 |
| degrau3 | 120 | 2400 | 0 | 120.0 | 1.37 | 1.82 |
| degrau4 | 220 | 4400 | 0 | 220.0 | 1.30 | 1.62 |
| degrau5 | 350 | 7000 | 0 | 350.0 | 1.30 | 1.55 |
| descida1 | 120 | 2400 | 0 | 120.0 | 1.34 | 1.74 |
| descida2 | 60 | 1200 | 0 | 60.0 | 1.42 | 1.79 |
| descida3 | 20 | 400 | 0 | 20.0 | 1.59 | 1.90 |

Réplicas de origin ao final da janela de descida (120s): 5.