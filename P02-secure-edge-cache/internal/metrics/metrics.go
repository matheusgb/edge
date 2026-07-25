// Package metrics concentra os medidores Prometheus dos serviços Go do P02.
//
// A regra é a mesma do P01: medidor que não responde a uma pergunta do projeto
// não deveria existir, porque cada série custa memória no processo e no coletor.
//
// Aqui existe uma regra a mais, que é de segurança: NENHUM rótulo carrega token,
// assinatura, query string ou nome de usuário. Rótulo de métrica é gravado no
// Prometheus, aparece em dashboard e vai para captura de tela. Um segredo que
// entra numa métrica vaza para muito mais lugares do que um segredo em log.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Origin agrupa os medidores do servidor de origem.
type Origin struct {
	Registry *prometheus.Registry

	// Requests conta requisições que CHEGARAM à origem, por rota e status.
	// Responde à pergunta central do projeto: quanto tráfego a borda absorveu?
	// Tudo que o cache serviu não aparece aqui, e é justamente essa ausência que
	// mede o alívio da origem.
	Requests *prometheus.CounterVec

	// Bodies conta as respostas em que a origem realmente enviou o objeto.
	// Separada de Requests porque uma revalidação condicional devolve 304 sem
	// corpo: é uma chamada barata, e somá-la às caras esconderia o alívio real.
	Bodies *prometheus.CounterVec

	// Duration mede o tempo do handler, o "tempo na origem" visto de dentro.
	Duration *prometheus.HistogramVec

	// AuthFailures conta requisições que chegaram sem o segredo compartilhado.
	// Se isso subir, ou a borda está mal configurada, ou alguém achou a origem.
	AuthFailures *prometheus.CounterVec

	// Bytes soma o corpo entregue.
	Bytes *prometheus.CounterVec
}

// NewOrigin cria os medidores da origem num registro próprio.
func NewOrigin() *Origin {
	m := &Origin{Registry: prometheus.NewRegistry()}

	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_requests_total",
		Help: "Requisições que chegaram à origem, por rota e código de status.",
	}, []string{"route", "code"})

	m.Bodies = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_object_bodies_total",
		Help: "Respostas em que a origem enviou o objeto inteiro, por rota.",
	}, []string{"route"})

	m.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "origin_request_duration_seconds",
		Help:    "Duração do handler da origem, por rota.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
	}, []string{"route"})

	m.AuthFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_edge_auth_failures_total",
		Help: "Requisições sem o segredo compartilhado da borda, por rota.",
	}, []string{"route"})

	m.Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_response_bytes_total",
		Help: "Bytes de corpo enviados pela origem, por rota.",
	}, []string{"route"})

	m.Registry.MustRegister(m.Requests, m.Bodies, m.Duration, m.AuthFailures, m.Bytes)
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return m
}

// Token agrupa os medidores do serviço de assinatura e validação.
type Token struct {
	Registry *prometheus.Registry

	// Auth conta cada decisão de autorização, por resultado e motivo.
	// Responde: quanta tentativa inválida chega, e de que tipo. "expirado" é
	// rotina; "assinatura inválida" crescendo é sinal de alguém testando.
	Auth *prometheus.CounterVec

	// AuthDuration mede o custo da decisão. A borda faz uma subrequisição por
	// requisição do cliente, inclusive quando o objeto vem do cache, então este
	// tempo entra no caminho quente de TODAS as respostas.
	AuthDuration prometheus.Histogram

	// Signed conta emissões de token, por chave usada. Durante uma rotação, é
	// esta série que mostra o tráfego migrando da chave antiga para a nova.
	Signed *prometheus.CounterVec
}

// NewToken cria os medidores do serviço de token num registro próprio.
func NewToken() *Token {
	m := &Token{Registry: prometheus.NewRegistry()}

	m.Auth = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "token_auth_total",
		Help: "Decisões de autorização, por resultado e motivo.",
	}, []string{"result", "reason"})

	m.AuthDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "token_auth_duration_seconds",
		Help:    "Duração da decisão de autorização.",
		Buckets: prometheus.ExponentialBuckets(0.00005, 2, 14),
	})

	m.Signed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "token_signed_total",
		Help: "Tokens emitidos, por identificador de chave.",
	}, []string{"kid"})

	m.Registry.MustRegister(m.Auth, m.AuthDuration, m.Signed)
	m.Registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return m
}
