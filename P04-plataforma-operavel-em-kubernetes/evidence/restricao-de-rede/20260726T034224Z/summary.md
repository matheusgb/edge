# restricao-de-rede

NetworkPolicy aplicada pelo Calico; cada caso confirma bloqueio ou liberação real de tráfego.

| caso | permitido esperado | observado permitido | correto |
| --- | --- | --- | --- |
| A_autorizado_para_edge | True | True | True |
| B_autorizado_para_origin_direto | False | False | True |
| C_nao_autorizado_para_edge | False | False | True |
| D_nao_autorizado_para_origin_direto | False | False | True |
| E_autorizado_para_porta_admin | False | False | True |

Todos os casos se comportaram como esperado: True.