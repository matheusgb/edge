# Evidência

Cada subpasta é um cenário: `escala`, `pod-encerrado`, `rollout-invalido`,
`restricao-de-rede`, `destruicao`. Dentro de cada cenário há uma pasta por
rodada, nomeada pelo timestamp UTC de início
(`evidence/<cenário>/<timestamp>/`).

Cada rodada contém:

- `environment.md`: commit, máquina, sistema operacional e o estado relevante
  do cluster/rede no início da rodada;
- `commands.txt`: os comandos originais executados, sem paráfrase;
- `summary.md`: o resultado em prosa e tabela, pronto para citar num README;
- `metrics.json`: os dados agregados, em formato reaproveitável.

Alguns cenários também gravam arquivos extras específicos: por exemplo,
`replica-timeline.txt` em `escala/`, `pod-events.txt` em `pod-encerrado/`,
`networkpolicy-describe.txt` em `restricao-de-rede/`.

Cada cenário rodou pelo menos três vezes, conforme o protocolo de medição do
repositório (`edge.md`). O arquivo `modelo-de-capacidade.md`, nesta mesma
pasta, documenta a estimativa de réplicas derivada da premissa de 50 mil rps,
separando o que foi medido do que foi projetado.

## O que não está aqui

- As séries brutas do Prometheus e os dashboards do Grafana. Eles vivem no
  cluster kind, que é descartável. O `metrics.json` de cada rodada é o resumo
  agregado que sustenta as conclusões.
- Objetos sintéticos de carga. `internal/originsrv` gera esses objetos de
  forma determinística a partir do nome pedido, então não há nada para
  guardar.
- Rodadas com bug de instrumentação. Por exemplo, uma primeira tentativa do
  experimento de pod encerrado confundiu um pod já existente com o substituto
  de verdade. Essas rodadas foram descartadas antes de virar evidência, e não
  publicadas como rodada "ruim": o bug estava no script de medição, não no
  comportamento do cluster, então mantê-las não agregaria nada ao relatório.
