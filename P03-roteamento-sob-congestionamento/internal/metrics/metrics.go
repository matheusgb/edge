// Package metrics concentra os medidores Prometheus dos três serviços Go do P03.
//
// A regra dos projetos anteriores continua: medidor que não responde a uma
// pergunta do projeto não deveria existir, porque cada série custa memória no
// processo e no coletor.
//
// Aqui existe uma regra a mais, sobre cardinalidade. Nenhum rótulo carrega nome
// de objeto, identificador de correlação ou caminho de URL. Parecem rótulos úteis
// e são armadilhas: o catálogo tem centenas de nomes, o identificador é único por
// requisição, e cada valor distinto vira uma série nova que fica na memória do
// Prometheus para sempre. Quem precisa desse detalhe procura no log ou no trace,
// que são feitos para cardinalidade alta.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Router agrupa os medidores do roteador, que é onde as decisões acontecem.
type Router struct {
	Registry *prometheus.Registry

	// Requests e Duration são a visão do CLIENTE: o que ele pediu e quanto
	// esperou, incluindo retry, espera e recusa. É esta latência que entra no
	// SLO, e não a do destino, porque ninguém do lado de fora se importa com
	// quantas tentativas foram necessárias.
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec

	// Attempts e AttemptDuration são a visão do DESTINO, uma linha por tentativa.
	// Ter as duas visões separadas é o que permite dizer "o cliente viu 900ms
	// porque foram três tentativas de 300ms", em vez de escolher entre uma das
	// metades da frase.
	Attempts        *prometheus.CounterVec
	AttemptDuration *prometheus.HistogramVec

	// Retries conta o destino de cada pedido de nova tentativa. O rótulo
	// "negado-orcamento" é o mais importante do projeto: ele é a prova de que a
	// amplificação foi contida, e não que ela não chegou a acontecer.
	Retries *prometheus.CounterVec

	// Rejected conta quem foi recusado na porta, por motivo. Recusa é resultado
	// legítimo de um sistema saturado, então ela precisa ser visível e separada
	// de erro do destino.
	Rejected *prometheus.CounterVec

	// Bytes soma o corpo entregue ao cliente, por destino. É o throughput em
	// bytes, que não anda junto com o throughput em requisições quando os
	// objetos têm tamanhos diferentes.
	Bytes *prometheus.CounterVec

	// As gauges abaixo publicam o que o roteador ACHA de cada destino. Elas
	// existem para o dashboard poder mostrar a decisão ao lado do efeito dela:
	// sem isso, um gráfico de tráfego desbalanceado é um mistério.
	BackendInflight  *prometheus.GaugeVec
	BackendLatency   *prometheus.GaugeVec
	BackendErrorRate *prometheus.GaugeVec
	BackendCost      *prometheus.GaugeVec
	BackendOpen      *prometheus.GaugeVec

	BudgetTokens    prometheus.Gauge
	LimiterInflight prometheus.Gauge
	LimiterCapacity prometheus.Gauge
}

// NewRouter cria os medidores do roteador num registro próprio.
func NewRouter() *Router {
	m := &Router{Registry: prometheus.NewRegistry()}

	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "router_requests_total",
		Help: "Requisições concluídas pelo roteador, por código de status.",
	}, []string{"code"})

	m.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "router_request_duration_seconds",
		Help: "Duração vista pelo cliente, incluindo retry e espera.",
		// Buckets exponenciais de 1ms a ~16s. O topo alto é proposital: num
		// experimento de congestionamento, a cauda é o resultado, e um histograma
		// que satura no último bucket esconde justamente o que se quer medir.
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
	}, []string{"code"})

	m.Attempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "router_backend_attempts_total",
		Help: "Tentativas enviadas a um destino, por destino e desfecho.",
	}, []string{"backend", "outcome"})

	m.AttemptDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "router_backend_attempt_duration_seconds",
		Help:    "Duração de uma tentativa, por destino.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
	}, []string{"backend"})

	m.Retries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "router_retries_total",
		Help: "Pedidos de nova tentativa, por desfecho da decisão.",
	}, []string{"decision"})

	m.Rejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "router_rejected_total",
		Help: "Requisições recusadas pelo roteador antes de tentar um destino, por motivo.",
	}, []string{"reason"})

	m.Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "router_response_bytes_total",
		Help: "Bytes de corpo entregues ao cliente, por destino.",
	}, []string{"backend"})

	m.BackendInflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_backend_inflight",
		Help: "Requisições em andamento em cada destino, na contagem do roteador.",
	}, []string{"backend"})

	m.BackendLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_backend_latency_ewma_seconds",
		Help: "Latência média móvel observada por destino.",
	}, []string{"backend"})

	m.BackendErrorRate = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_backend_error_rate",
		Help: "Taxa de erro média móvel por destino, entre 0 e 1.",
	}, []string{"backend"})

	m.BackendCost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_backend_cost",
		Help: "Custo estimado da próxima requisição para o destino, na fórmula da política adaptativa.",
	}, []string{"backend"})

	m.BackendOpen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_backend_circuit_open",
		Help: "1 quando o disjuntor do destino está aberto.",
	}, []string{"backend"})

	m.BudgetTokens = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "router_retry_budget_tokens",
		Help: "Fichas restantes no orçamento de retry.",
	})

	m.LimiterInflight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "router_inflight",
		Help: "Requisições ocupando vaga no roteador agora.",
	})

	m.LimiterCapacity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "router_inflight_capacity",
		Help: "Teto de requisições simultâneas do roteador.",
	})

	m.Registry.MustRegister(
		m.Requests, m.Duration, m.Attempts, m.AttemptDuration, m.Retries,
		m.Rejected, m.Bytes, m.BackendInflight, m.BackendLatency,
		m.BackendErrorRate, m.BackendCost, m.BackendOpen,
		m.BudgetTokens, m.LimiterInflight, m.LimiterCapacity,
	)
	// O coletor de processo traz descritores de arquivo e memória residente; o de
	// Go traz goroutines e GC. Num experimento de saturação eles não são enfeite:
	// goroutine crescendo sem parar é o sintoma de acúmulo interno, e descritor
	// no teto é o primeiro recurso a limitar quem fala com a rede.
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return m
}

// Edge agrupa os medidores de um servidor de borda.
type Edge struct {
	Registry *prometheus.Registry

	// Requests separa HIT de MISS porque as duas respostas custam coisas
	// diferentes: uma é memória local, a outra é uma viagem até a origem.
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec

	// OriginRequests e OriginDuration medem o tráfego que ESCAPOU do cache. É o
	// que a origem sente, e o que o roteador não controla.
	OriginRequests *prometheus.CounterVec
	OriginDuration prometheus.Histogram

	// Coalesced conta as requisições que esperaram uma busca já em andamento em
	// vez de abrir a sua própria. Sem isso, um objeto popular ausente vira uma
	// rajada na origem, que é o cache stampede do P02 aparecendo de novo aqui.
	Coalesced prometheus.Counter

	Entries prometheus.Gauge
	Bytes   *prometheus.CounterVec
}

// NewEdge cria os medidores de um edge num registro próprio.
func NewEdge() *Edge {
	m := &Edge{Registry: prometheus.NewRegistry()}

	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "edge_requests_total",
		Help: "Requisições atendidas pelo edge, por status de cache e código.",
	}, []string{"cache", "code"})

	m.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "edge_request_duration_seconds",
		Help:    "Duração do handler do edge, por status de cache.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 15),
	}, []string{"cache"})

	m.OriginRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "edge_origin_requests_total",
		Help: "Buscas feitas à origem, por código de status.",
	}, []string{"code"})

	m.OriginDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "edge_origin_duration_seconds",
		Help:    "Duração das buscas à origem.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
	})

	m.Coalesced = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "edge_coalesced_total",
		Help: "Requisições que aproveitaram uma busca à origem já em andamento.",
	})

	m.Entries = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "edge_cache_entries",
		Help: "Objetos guardados no cache do edge.",
	})

	m.Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "edge_response_bytes_total",
		Help: "Bytes de corpo enviados pelo edge, por status de cache.",
	}, []string{"cache"})

	m.Registry.MustRegister(
		m.Requests, m.Duration, m.OriginRequests, m.OriginDuration,
		m.Coalesced, m.Entries, m.Bytes,
	)
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return m
}

// Origin agrupa os medidores da origem.
type Origin struct {
	Registry *prometheus.Registry

	Requests *prometheus.CounterVec
	Duration prometheus.Histogram
	Bytes    prometheus.Counter

	// Inflight mostra quantas buscas simultâneas os edges impuseram à origem.
	Inflight prometheus.Gauge

	// InflightPeak é a marca d'água da simultaneidade desde o último reset.
	//
	// Ela existe porque a gauge instantânea não responde à pergunta do cenário de
	// recuperação. Uma avalanche dura menos de um segundo, e uma leitura feita
	// depois dela devolve zero, o que faria o relatório afirmar que não houve
	// avalanche nenhuma justamente quando houve.
	InflightPeak prometheus.Gauge
}

// NewOrigin cria os medidores da origem num registro próprio.
func NewOrigin() *Origin {
	m := &Origin{Registry: prometheus.NewRegistry()}

	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_requests_total",
		Help: "Requisições que chegaram à origem, por código de status.",
	}, []string{"code"})

	m.Duration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "origin_request_duration_seconds",
		Help:    "Duração do handler da origem.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
	})

	m.Bytes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "origin_response_bytes_total",
		Help: "Bytes de corpo enviados pela origem.",
	})

	m.Inflight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "origin_inflight",
		Help: "Requisições em andamento na origem.",
	})

	m.InflightPeak = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "origin_inflight_peak",
		Help: "Maior número de requisições simultâneas visto pela origem desde o último reset.",
	})

	m.Registry.MustRegister(m.Requests, m.Duration, m.Bytes, m.Inflight, m.InflightPeak)
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return m
}
