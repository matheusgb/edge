// Package metrics concentra os medidores Prometheus do servidor de origem.
//
// A ideia central: métrica não é enfeite. Cada medidor aqui existe para responder
// uma pergunta específica do lab. Se um medidor não responde nenhuma pergunta, ele
// não deveria existir, porque cada série custa memória no processo e no coletor.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics agrupa os medidores e o registro onde eles vivem.
//
// Usamos um registro próprio em vez do global (prometheus.DefaultRegisterer)
// porque assim cada teste cria o seu, sem estado compartilhado entre testes, o
// que quebraria a execução em paralelo.
type Metrics struct {
	Registry *prometheus.Registry

	// Requests conta requisições concluídas, separadas por modo, método e status.
	// Responde: quantas deram certo, quantas deram erro, em cada modo.
	Requests *prometheus.CounterVec

	// Duration mede o tempo do handler, do primeiro byte lido até o último escrito.
	// Responde: p50, p95 e p99, o coração da comparação do lab.
	Duration *prometheus.HistogramVec

	// ResponseBytes soma bytes de corpo efetivamente escritos no socket.
	// Responde: throughput real, e quanto de um objeto foi entregue antes de um
	// cancelamento.
	ResponseBytes *prometheus.CounterVec

	// InFlight conta requisições em andamento agora.
	// Responde: saturação. Se sobe sem parar, a origem não vaza, ela enfileira.
	InFlight *prometheus.GaugeVec

	// Cancellations conta requisições em que o CLIENTE desistiu no meio.
	// Responde: qual fatia da "carga oferecida" nunca virou "carga concluída".
	Cancellations *prometheus.CounterVec

	// Errors conta falhas por motivo (objeto ausente, erro de leitura, etc).
	Errors *prometheus.CounterVec
}

// New cria os medidores e os registra num registro novo.
func New() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}

	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_http_requests_total",
		Help: "Requisições HTTP concluídas, por modo, método e código de status.",
	}, []string{"mode", "method", "code"})

	m.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "origin_http_request_duration_seconds",
		Help: "Duração do handler em segundos, por modo.",
		// Os buckets vão de 1 ms a ~16 s. Buckets são escolha de projeto: o
		// histograma só enxerga o que os buckets delimitam, e p99 calculado fora
		// da faixa é interpolação, não medida.
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
	}, []string{"mode"})

	m.ResponseBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_http_response_bytes_total",
		Help: "Bytes de corpo escritos na resposta, por modo.",
	}, []string{"mode"})

	m.InFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "origin_http_requests_in_flight",
		Help: "Requisições em andamento no momento, por modo.",
	}, []string{"mode"})

	m.Cancellations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_http_client_cancellations_total",
		Help: "Requisições abortadas pelo cliente antes do fim, por modo.",
	}, []string{"mode"})

	m.Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_http_errors_total",
		Help: "Erros no atendimento, por modo e motivo.",
	}, []string{"mode", "reason"})

	m.Registry.MustRegister(
		m.Requests, m.Duration, m.ResponseBytes,
		m.InFlight, m.Cancellations, m.Errors,
	)

	// Os coletores de processo e de runtime são o que conecta a pergunta do lab às
	// causas: alocação, coleta de lixo, goroutines e descritores de arquivo.
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(
				collectors.MetricsGC,
				collectors.MetricsMemory,
				collectors.MetricsScheduler,
			),
		),
	)

	return m
}
