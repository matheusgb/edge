// Package metrics centraliza os contadores Prometheus expostos pelos
// serviços origin e edge na porta administrativa.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Origin agrupa as métricas do serviço de origem.
type Origin struct {
	Registry        *prometheus.Registry
	Requests        *prometheus.CounterVec
	Duration        *prometheus.HistogramVec
	InFlight        prometheus.Gauge
	WorkIterations  prometheus.Counter
	ShutdownSeconds prometheus.Gauge
}

// NewOrigin cria e registra as métricas do serviço de origem.
func NewOrigin() *Origin {
	reg := prometheus.NewRegistry()
	m := &Origin{
		Registry: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "origin_requests_total",
			Help: "Total de requisições recebidas pela origem, por rota e status.",
		}, []string{"route", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "origin_request_duration_seconds",
			Help:    "Duração das requisições da origem, por rota.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		InFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "origin_requests_in_flight",
			Help: "Requisições em andamento na origem.",
		}),
		WorkIterations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "origin_work_iterations_total",
			Help: "Total de iterações de trabalho de CPU executadas.",
		}),
		ShutdownSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "origin_shutdown_in_progress",
			Help: "1 quando o shutdown gracioso começou, 0 caso contrário.",
		}),
	}
	reg.MustRegister(m.Requests, m.Duration, m.InFlight, m.WorkIterations, m.ShutdownSeconds)
	return m
}

// Edge agrupa as métricas do serviço de borda (CDN mínima).
type Edge struct {
	Registry        *prometheus.Registry
	Requests        *prometheus.CounterVec
	Duration        *prometheus.HistogramVec
	InFlight        prometheus.Gauge
	CacheHits       prometheus.Counter
	CacheMisses     prometheus.Counter
	CacheBypass     prometheus.Counter
	OriginErrors    prometheus.Counter
	ShutdownSeconds prometheus.Gauge
}

// NewEdge cria e registra as métricas do serviço de borda.
func NewEdge() *Edge {
	reg := prometheus.NewRegistry()
	m := &Edge{
		Registry: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edge_requests_total",
			Help: "Total de requisições recebidas pela borda, por rota e status.",
		}, []string{"route", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "edge_request_duration_seconds",
			Help:    "Duração das requisições da borda, por rota.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		InFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "edge_requests_in_flight",
			Help: "Requisições em andamento na borda.",
		}),
		CacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "edge_cache_hits_total",
			Help: "Total de acertos de cache na borda.",
		}),
		CacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "edge_cache_misses_total",
			Help: "Total de faltas de cache na borda.",
		}),
		CacheBypass: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "edge_cache_bypass_total",
			Help: "Total de requisições que não passam pelo cache (ex: /work).",
		}),
		OriginErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "edge_origin_errors_total",
			Help: "Total de falhas ao encaminhar requisições para a origem.",
		}),
		ShutdownSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "edge_shutdown_in_progress",
			Help: "1 quando o shutdown gracioso começou, 0 caso contrário.",
		}),
	}
	reg.MustRegister(m.Requests, m.Duration, m.InFlight, m.CacheHits, m.CacheMisses,
		m.CacheBypass, m.OriginErrors, m.ShutdownSeconds)
	return m
}
