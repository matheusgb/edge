package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewRegistraTodosOsMedidores(t *testing.T) {
	t.Parallel()

	m := New()
	// Tocar em cada vetor cria a série com aquele conjunto de rótulos. Sem isso,
	// um CounterVec sem uso não aparece na coleta.
	m.Requests.WithLabelValues("streamed", "GET", "200").Inc()
	m.Duration.WithLabelValues("streamed").Observe(0.01)
	m.ResponseBytes.WithLabelValues("streamed").Add(1024)
	m.InFlight.WithLabelValues("streamed").Set(0)
	m.Cancellations.WithLabelValues("streamed").Inc()
	m.Errors.WithLabelValues("streamed", "not_found").Inc()

	esperados := []string{
		"origin_http_requests_total",
		"origin_http_request_duration_seconds",
		"origin_http_response_bytes_total",
		"origin_http_requests_in_flight",
		"origin_http_client_cancellations_total",
		"origin_http_errors_total",
	}
	for _, nome := range esperados {
		if got := testutil.CollectAndCount(m.Registry, nome); got == 0 {
			t.Errorf("métrica %q não foi registrada", nome)
		}
	}
}

func TestRegistrosSaoIsoladosEntreInstancias(t *testing.T) {
	t.Parallel()

	// Dois registros independentes é o que permite testes em paralelo sem que um
	// contamine a contagem do outro. Se usássemos o registro global, isto entraria
	// em pânico por dupla-registro.
	a, b := New(), New()

	a.Requests.WithLabelValues("buffered", "GET", "200").Add(5)
	b.Requests.WithLabelValues("buffered", "GET", "200").Add(1)

	if got := testutil.ToFloat64(a.Requests.WithLabelValues("buffered", "GET", "200")); got != 5 {
		t.Errorf("registro A = %v, esperava 5", got)
	}
	if got := testutil.ToFloat64(b.Requests.WithLabelValues("buffered", "GET", "200")); got != 1 {
		t.Errorf("registro B = %v, esperava 1", got)
	}
}

func TestColetoresDeProcessoERuntimeEstaoPresentes(t *testing.T) {
	t.Parallel()

	m := New()
	familias, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("coletando: %v", err)
	}

	var temGo, temProcesso bool
	for _, f := range familias {
		nome := f.GetName()
		// As métricas de coleta de lixo e de heap são o que liga a pergunta do lab
		// à causa; sem elas, o buffered vs streamed vira só um número de latência.
		if strings.HasPrefix(nome, "go_") {
			temGo = true
		}
		if strings.HasPrefix(nome, "process_") {
			temProcesso = true
		}
	}
	if !temGo {
		t.Error("faltam métricas do runtime Go (go_*)")
	}
	if !temProcesso {
		t.Error("faltam métricas de processo (process_*), incluindo descritores de arquivo")
	}
}
