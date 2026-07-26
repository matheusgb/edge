// Package router implementa o roteador do P03: escolha de destino, orçamento de
// retry, limite de concorrência e propagação de prazo.
//
// A pergunta do projeto é o que fazer quando um destino continua saudável no
// protocolo, respondendo 200 e aceitando conexão, mas ficou lento ou saturado.
// Health check por "está de pé?" não enxerga esse estado: o edge doente responde
// que está vivo, e continua recebendo um terço da carga.
//
// Por isso o estado de saúde aqui é PASSIVO. Ninguém pergunta nada ao edge; o
// roteador aprende com as respostas do tráfego real que já está passando. A
// limitação disso está registrada no README e vale repetir: o estado é local ao
// processo. Com várias réplicas de roteador, cada uma aprende sozinha, e um edge
// doente é descoberto N vezes em vez de uma.
package router

import (
	"math"
	"sync/atomic"
	"time"
)

// Backend é um destino do roteador, com o que se sabe sobre ele agora.
//
// Todos os campos de estado são atômicos porque o hot path lê e escreve nos dois
// sentidos em muitas goroutines ao mesmo tempo: cada requisição incrementa
// inflight antes de sair e atualiza latência e erro quando volta.
type Backend struct {
	Name string
	URL  string

	inflight atomic.Int64

	// ewmaLatencyNanos e ewmaErrPPM guardam médias móveis exponenciais.
	//
	// Elas são inteiros, e não float64, porque o pacote sync/atomic não tem
	// operação atômica para float. Latência cabe em nanossegundos; taxa de erro
	// vira partes por milhão (1_000_000 = 100% de erro). A alternativa seria um
	// mutex ou math.Float64bits com CompareAndSwap, e as duas custam mais
	// atenção de quem lê do que a conversão para inteiro.
	ewmaLatencyNanos atomic.Int64
	ewmaErrPPM       atomic.Int64

	// consecutiveFails e openUntil formam o disjuntor. Ele é deliberadamente
	// simples: N falhas seguidas abrem por uma janela, e a primeira tentativa
	// depois da janela é a sondagem que decide se fecha de novo.
	consecutiveFails atomic.Int64
	openUntil        atomic.Int64 // unix nano; 0 = fechado

	// ultimaTentativa é quando este destino recebeu tráfego pela última vez.
	// Sem ele, um destino evitado fica evitado para sempre: veja Cost.
	ultimaTentativa atomic.Int64 // unix nano

	// ultimaMedicao é quando a média móvel foi atualizada pela última vez. É o
	// que permite saber se o que se sabe sobre o destino ainda vale: veja Finish.
	ultimaMedicao atomic.Int64 // unix nano

	// Contadores acumulados, para a evidência do experimento.
	total    atomic.Int64
	failures atomic.Int64
	opens    atomic.Int64
}

// Config dos parâmetros de aprendizado e do disjuntor.
type Config struct {
	// EWMAAlpha é o peso da amostra nova. 0.2 significa que uma medição sozinha
	// move a média em 20%: rápido o bastante para perceber um edge degradando em
	// poucos segundos, lento o bastante para não reagir a um único outlier.
	EWMAAlpha float64

	// FailuresToOpen é quantas falhas seguidas abrem o disjuntor.
	FailuresToOpen int64

	// OpenFor é quanto tempo o destino fica de fora antes da sondagem.
	OpenFor time.Duration

	// ErrorPenalty multiplica o peso da taxa de erro no custo. Com 10, um destino
	// errando 10% das vezes é tratado como se estivesse duas vezes mais caro que
	// um destino com a mesma latência e sem erro.
	ErrorPenalty float64

	// AgingWindow é a janela de envelhecimento da informação. Veja Cost.
	AgingWindow time.Duration
}

// DefaultConfig são os valores usados quando nada é dito.
func DefaultConfig() Config {
	return Config{
		EWMAAlpha:      0.2,
		FailuresToOpen: 5,
		OpenFor:        2 * time.Second,
		ErrorPenalty:   10,
		AgingWindow:    1 * time.Second,
	}
}

// NewBackend cria um destino já com uma latência inicial plausível.
//
// Começar em zero seria pior do que parece: o primeiro destino com custo zero
// atrairia toda a carga inicial, e o roteador aprenderia sobre um edge só. Um
// palpite igual para todos deixa a primeira rodada distribuída, e o tráfego real
// corrige o palpite em seguida.
func NewBackend(name, url string, palpiteInicial time.Duration) *Backend {
	b := &Backend{Name: name, URL: url}
	b.ewmaLatencyNanos.Store(int64(palpiteInicial))
	return b
}

// Begin registra o início de uma tentativa e devolve a função que a encerra.
func (b *Backend) Begin() int64 {
	b.ultimaTentativa.Store(time.Now().UnixNano())
	return b.inflight.Add(1)
}

// Finish atualiza o que se sabe sobre o destino depois de uma tentativa.
//
// "falhou" aqui significa falha de transporte, timeout ou 5xx: coisas que o
// destino causou. Um 404 não conta, porque o objeto não existir não diz nada
// sobre a saúde de quem respondeu, e penalizar o edge por isso mandaria a carga
// para longe do destino certo pelo motivo errado.
func (b *Backend) Finish(cfg Config, latencia time.Duration, falhou bool) {
	b.inflight.Add(-1)
	b.total.Add(1)

	// Misturar a amostra nova com a média antiga só faz sentido se a média antiga
	// ainda descrever a realidade. Depois de um silêncio longo ela não descreve:
	// é a lembrança de um destino que pode ter sido reiniciado, corrigido ou
	// trocado desde então. Nesse caso a sondagem que acabou de voltar SUBSTITUI o
	// que se sabia, em vez de ser diluída em vinte por cento dele.
	//
	// Sem isto, o destino recuperado precisaria de dezenas de sondagens espaçadas
	// para a média cair, e como cada sondagem só acontece depois de outro
	// silêncio longo, a volta levaria minutos.
	alpha := cfg.EWMAAlpha
	if b.informacaoVelha(cfg) {
		alpha = 1
	}

	anterior := b.ewmaLatencyNanos.Load()
	b.ewmaLatencyNanos.Store(int64(alpha*float64(latencia) + (1-alpha)*float64(anterior)))

	amostra := int64(0)
	if falhou {
		amostra = 1_000_000
	}
	errAnterior := b.ewmaErrPPM.Load()
	b.ewmaErrPPM.Store(int64(alpha*float64(amostra) + (1-alpha)*float64(errAnterior)))
	b.ultimaMedicao.Store(time.Now().UnixNano())

	if !falhou {
		b.consecutiveFails.Store(0)
		// Sucesso fecha o disjuntor imediatamente. É o outro lado da sondagem:
		// se a resposta boa não fechasse, a janela aberta seria eterna.
		b.openUntil.Store(0)
		return
	}

	b.failures.Add(1)
	if b.consecutiveFails.Add(1) >= cfg.FailuresToOpen {
		if b.openUntil.Swap(time.Now().Add(cfg.OpenFor).UnixNano()) == 0 {
			b.opens.Add(1)
		}
		b.consecutiveFails.Store(0)
	}
}

// Reset apaga o que o destino aprendeu e volta ao palpite inicial.
//
// Existe por causa da comparação entre políticas: rodar a segunda metade do
// experimento com a memória da primeira daria vantagem ou desvantagem gratuita a
// ela. Zerar aqui é mais barato e mais limpo que reiniciar o container, que
// mudaria também o pool de conexões e o aquecimento do processo.
func (b *Backend) Reset(palpiteInicial time.Duration) {
	b.ewmaLatencyNanos.Store(int64(palpiteInicial))
	b.ewmaErrPPM.Store(0)
	b.ultimaTentativa.Store(0)
	b.ultimaMedicao.Store(0)
	b.consecutiveFails.Store(0)
	b.openUntil.Store(0)
	b.total.Store(0)
	b.failures.Store(0)
	b.opens.Store(0)
}

// informacaoVelha diz se a última medição é antiga demais para ser misturada com
// uma nova.
//
// O limiar é duas janelas de envelhecimento, e o número não é arbitrário. Um
// destino em rotação normal recebe respostas o tempo todo, com intervalos de
// milissegundos, então para ele isto nunca é verdade. Já um destino que voltou a
// ser sondado depois do exílio passou, por construção, pelo menos log2 da
// diferença de custo em janelas de silêncio, que é sempre mais de duas. Ou seja:
// o limiar separa exatamente os dois casos que precisam ser separados.
//
// A primeira versão usava oito janelas, e ficou apertada demais. A sondagem de
// um destino exilado acontecia a cada seis ou sete segundos, abaixo do limiar,
// então cada resposta boa era diluída a vinte por cento numa média de 800ms. Na
// prática o destino recuperado precisaria de mais de um minuto para voltar, e a
// evidência da fase de recuperação saía com ele em 0% do tráfego.
func (b *Backend) informacaoVelha(cfg Config) bool {
	ultima := b.ultimaMedicao.Load()
	if ultima == 0 || cfg.AgingWindow <= 0 {
		return false
	}
	return time.Since(time.Unix(0, ultima)) > 2*cfg.AgingWindow
}

// Open diz se o disjuntor está aberto agora.
func (b *Backend) Open(agora time.Time) bool {
	until := b.openUntil.Load()
	return until != 0 && agora.UnixNano() < until
}

// Inflight é o número de requisições em andamento neste destino.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// LatencyEWMA é a latência média móvel observada.
func (b *Backend) LatencyEWMA() time.Duration { return time.Duration(b.ewmaLatencyNanos.Load()) }

// ErrorRate é a taxa de erro média móvel, entre 0 e 1.
func (b *Backend) ErrorRate() float64 { return float64(b.ewmaErrPPM.Load()) / 1_000_000 }

// Cost é o custo estimado de mandar a PRÓXIMA requisição para este destino.
//
// A fórmula é (fila + 1) x latência x (1 + erro x penalidade).
//
// Os três fatores existem por motivos diferentes e nenhum deles resolve sozinho:
//
//   - latência sozinha reage tarde, porque a média só sobe depois que várias
//     respostas lentas voltaram;
//   - fila sozinha não distingue um destino com dez requisições rápidas de um
//     com dez requisições travadas;
//   - erro sozinho ignora o destino que responde certo, só que devagar, que é
//     justamente o caso que este projeto investiga.
//
// Multiplicar em vez de somar evita ter que escolher unidade comum entre "três
// requisições na fila" e "quarenta milissegundos".
//
// Sobre o quarto fator, o envelhecimento: o custo é DIVIDIDO pelo tempo que o
// destino passou sem receber nada. Ele não estava na primeira versão, e a
// evidência mostrou por que precisa estar.
//
// Sem envelhecer, a conta tem um ponto fixo desagradável. Um destino caro para
// de receber tráfego; sem tráfego, a média móvel dele não é atualizada; sem
// atualização, ele continua caro para sempre. Na execução guardada em
// evidence/falha-controlada/20260725T235204Z, o edge-a voltou a ficar saudável
// na fase de recuperação e continuou com 0% do tráfego até o fim: exílio
// permanente por falta de notícias novas.
//
// Envelhecer resolve isso dizendo a verdade sobre a informação: uma medição de
// cinco segundos atrás vale menos que uma de agora. Depois de uma janela sem
// tráfego, o custo cai o suficiente para o destino ser sorteado de novo, e a
// primeira resposta dele conta a história atualizada, para melhor ou para pior.
func (b *Backend) Cost(cfg Config) float64 {
	latencia := float64(b.ewmaLatencyNanos.Load())
	if latencia <= 0 {
		latencia = float64(time.Millisecond)
	}
	// A fila tem piso 1. No caminho quente ela nunca fica negativa, porque todo
	// Finish tem um Begin antes, mas custo negativo seria um destino
	// irresistível: qualquer erro de contagem viraria "mande tudo para cá".
	fila := float64(b.inflight.Load() + 1)
	if fila < 1 {
		fila = 1
	}
	custo := fila * latencia * (1 + b.ErrorRate()*cfg.ErrorPenalty)
	custo /= envelhecimento(b.ultimaTentativa.Load(), cfg.AgingWindow)
	if math.IsNaN(custo) || math.IsInf(custo, 0) {
		return math.MaxFloat64
	}
	return custo
}

// envelhecimento é o divisor de confiança: 1 logo depois de uma tentativa, e
// DOBRANDO a cada janela de silêncio.
//
// A primeira versão crescia devagar, de forma linear, e não dava conta. Um edge
// com 800ms de média móvel ao lado de dois com 1ms é oitocentas vezes mais caro,
// e um desconto que soma 1 por janela levaria mais de vinte minutos para
// alcançar essa diferença. Dobrando, a conta vira logarítmica: cada dobra de
// silêncio corta o custo pela metade, e uma diferença de oitocentas vezes é
// vencida em cerca de dez janelas. O tempo até um destino evitado ser sondado de
// novo cresce com o LOGARITMO de quão ruim ele parecia, que é o comportamento
// desejado: quanto pior a última notícia, mais tempo até insistir.
//
// O expoente tem teto porque 2^1000 é infinito em float64, e custo zero faria o
// destino ser escolhido sempre, sem exceção.
func envelhecimento(ultimaNano int64, janela time.Duration) float64 {
	if ultimaNano == 0 || janela <= 0 {
		// Destino que nunca recebeu nada não ganha desconto: o palpite inicial
		// já o deixa em pé de igualdade com os outros.
		return 1
	}
	parado := time.Since(time.Unix(0, ultimaNano))
	if parado <= 0 {
		return 1
	}
	dobras := float64(parado) / float64(janela)
	if dobras > 20 {
		dobras = 20
	}
	return math.Exp2(dobras)
}

// Stats é a foto do destino, para métrica e evidência.
type Stats struct {
	Name      string  `json:"name"`
	Inflight  int64   `json:"inflight"`
	LatencyMS float64 `json:"latency_ewma_ms"`
	ErrorRate float64 `json:"error_rate"`
	Cost      float64 `json:"cost"`
	Open      bool    `json:"circuit_open"`
	Total     int64   `json:"attempts_total"`
	Failures  int64   `json:"failures_total"`
	Opens     int64   `json:"circuit_opens_total"`
}

// Stats devolve a foto atual.
func (b *Backend) Stats(cfg Config, agora time.Time) Stats {
	return Stats{
		Name:      b.Name,
		Inflight:  b.inflight.Load(),
		LatencyMS: float64(b.LatencyEWMA()) / float64(time.Millisecond),
		ErrorRate: b.ErrorRate(),
		Cost:      b.Cost(cfg),
		Open:      b.Open(agora),
		Total:     b.total.Load(),
		Failures:  b.failures.Load(),
		Opens:     b.opens.Load(),
	}
}
