# Ambiente

- cenário: rollout-invalido
- início (UTC): 20260726T033312Z
- commit: d7f8355900e7af101d4d9e67a0d6a964ad1bec05
- máquina: windows-note
- imagem inválida aplicada: p04-edge:fail-ready
- rc do 'kubectl rollout status' durante a imagem inválida (esperado != 0, ou seja, travou/expirou): 1
- pods com a imagem inválida e nunca prontos, no momento da checagem: 1
- IP de algum pod com a imagem inválida presente nos endpoints do Service (esperado "nao"): nao
- endpoints do Service origin antes do rollout: 10.244.140.148 10.244.140.153
- endpoints do Service origin durante a imagem inválida: 10.244.140.148 10.244.140.153
- endpoints do Service origin depois do rollback: 10.244.140.148 10.244.140.153
