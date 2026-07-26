# Evidência

Cada execução de experimento grava uma pasta aqui, no formato
`evidence/<cenário>/<timestamp UTC>/`, com quatro arquivos:

| Arquivo | Para que serve |
| --- | --- |
| `environment.md` | máquina, kernel, CPUs, commit, limites do processo, containers e o estado da rede degradada |
| `commands.txt` | o comando exato que produziu aquele resultado |
| `summary.md` | as tabelas legíveis, com a conclusão e as ressalvas |
| `metrics.json` | os mesmos dados em JSON, incluindo a série temporal das decisões |

O `environment.md` deste projeto tem uma seção que os anteriores não têm: o
estado do Toxiproxy no momento da medição, lido do próprio Toxiproxy. Sem ela, um
relatório com p99 alto não diz se o número veio da política de roteamento ou de
uma toxina esquecida do cenário anterior.

## Os experimentos

| Cenário | O que ele responde |
| --- | --- |
| `matriz-de-carga` | como o sistema se comporta em nove condições diferentes, todas com a mesma política |
| `falha-controlada` | o que muda entre round-robin e a política adaptativa diante da mesma degradação |

## As execuções guardadas

| Execução | Por que está aqui |
| --- | --- |
| `matriz-de-carga/20260726T010254Z` | a matriz boa, e a que o README cita |
| `falha-controlada/20260726T010238Z` | a comparação entre as políticas, e a que o README cita |
| `matriz-de-carga/20260725T232715Z` | uma medição contaminada pelo gerador, mantida de propósito |
| `falha-controlada/20260725T235400Z` | a execução que mostrou o exílio permanente, mantida de propósito |

## Por que duas execuções ruins ficam no repositório

### A que mediu o gerador, não o serviço

Em `matriz-de-carga/20260725T232715Z`, quatro cenários aparecem com erro alto e
latência baixa ao mesmo tempo, o que é uma combinação estranha: um sistema
sobrecarregado costuma ficar lento antes de errar. O `metrics.json` explica, na
lista de mensagens de erro:

```text
dial tcp 0.0.0.0:0->127.0.0.1:9080: bind: address already in use
```

Não era o serviço recusando; era o GERADOR sem portas efêmeras. O pool de
conexões ociosas do Vegeta estava menor que o teto de conexões, então toda
conexão acima do pool era fechada em vez de reaproveitada, cada fechamento
deixava uma porta em `TIME_WAIT` por dezenas de segundos, e alguns milhares de
requisições por segundo esgotavam o intervalo de portas da máquina.

A correção está em `internal/loadtest/loadtest.go`, com o comentário explicando o
porquê. A execução fica guardada porque ela é o exemplo mais claro de uma
armadilha central deste projeto: **um experimento de carga mede o gerador com a
mesma facilidade com que mede o serviço**, e a única defesa é olhar as mensagens
de erro em vez de aceitar o resumo.

### A que mostrou o exílio permanente

Em `falha-controlada/20260725T235400Z`, a política adaptativa faz tudo certo até
a última fase: tira o edge doente de circulação em menos de um segundo, mantém o
p99 do cliente estável e preserva os edges saudáveis.

Na fase de recuperação, com o edge-a já perfeitamente saudável, a tabela mostra:

```text
recuperacao | adaptativa | edge-b 50.4%, edge-c 49.6%
```

O edge-a segue com zero. Ele foi exilado e não voltou mais.

A causa é um ponto fixo na própria fórmula de custo: um destino caro para de
receber tráfego, sem tráfego a média móvel dele nunca é atualizada, e sem
atualização ele continua caro para sempre. A correção, descrita no README, foi
envelhecer a informação e tratar medição velha como velha.

Guardar esta execução é mais honesto do que guardar só a versão corrigida. O
código passava em todos os testes que existiam na época; quem apontou o defeito
foi a fase do experimento que ninguém esperava que falhasse.

## O que não entra aqui

Objetos de carga. Eles são gerados a partir do nome, de forma determinística, e
regerar é mais barato que versionar.

Séries do Prometheus, do Loki e do Tempo. Elas vivem em volume efêmero e existem
para explicar um experimento de minutos. O que sustenta a conclusão é o resumo
agregado, que fica aqui.
