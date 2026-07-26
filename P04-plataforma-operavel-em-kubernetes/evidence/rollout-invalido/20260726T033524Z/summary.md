# rollout-invalido

Imagem que nunca fica pronta implantada; espera-se rollout travado, zero tráfego para a versão ruim, e rollback bem-sucedido.

| degrau | completadas | erros | p99 (ms) |
| --- | --- | --- | --- |
| antes1 | 450 | 0 | 0.78 |
| antes2 | 450 | 0 | 0.75 |
| ruim1 | 450 | 0 | 0.78 |
| ruim2 | 450 | 0 | 0.71 |
| ruim3 | 450 | 0 | 0.65 |
| rollback1 | 450 | 0 | 0.78 |
| rollback2 | 450 | 0 | 0.74 |
| rollback3 | 450 | 0 | 0.81 |

rollout status retornou código 1 (diferente de 0 = travou/expirou, como esperado).
pods com a imagem ruim nunca prontos no momento da checagem: 1.
IP de pod com a imagem ruim chegou a aparecer nos endpoints do Service: False (esperado False).
total de erros no cliente durante todo o experimento: 0 de 3600 requisições.