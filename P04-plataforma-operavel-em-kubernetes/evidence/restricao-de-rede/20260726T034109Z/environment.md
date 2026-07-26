# Ambiente

- cenário: restricao-de-rede
- início (UTC): 20260726T034109Z
- commit: d7f8355900e7af101d4d9e67a0d6a964ad1bec05
- máquina: windows-note
- CNI: Calico (kindnet padrão do kind não aplica NetworkPolicy, por isso foi substituído em infra/kind/bootstrap.sh)

## Casos

- A: pod com rótulo role=experiments -> edge:8080 (esperado: permitido)
- B: pod com rótulo role=experiments -> origin:8080 direto (esperado: bloqueado, só a borda tem permissão de falar com a origem)
- C: pod sem o rótulo -> edge:8080 (esperado: bloqueado)
- D: pod sem o rótulo -> origin:8080 direto (esperado: bloqueado)
- E: pod com rótulo role=experiments -> origin:8090/metrics, porta administrativa (esperado: bloqueado, só o Prometheus no namespace observability pode ler métricas)
