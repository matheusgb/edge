// Package promscrape lê o /metrics do servidor medido e devolve valores somados.
//
// Ele existe por um motivo de método: quem mede não deveria ser quem é medido.
// O gerador de carga sabe quantas requisições terminaram e quanto tempo cada uma
// levou, mas não sabe quanta memória o servidor alocou para responder, quantas
// coletas de lixo aconteceram nem quantos bytes saíram de fato pelo socket.
//
// Essas grandezas o próprio servidor já publica. Duas leituras, uma antes e uma
// depois da janela, transformam contadores acumulados na diferença que interessa.
//
// O parser é o expfmt, a mesma biblioteca que o Prometheus usa. O formato de
// exposição tem detalhes de escape, tipo e sufixo de histograma que já estão
// resolvidos e testados ali.
package promscrape

import (
	"context"
	"fmt"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Snapshot é uma leitura de /metrics em um instante.
type Snapshot struct {
	families map[string]*dto.MetricFamily
	At       time.Time
}

// Fetch lê o endpoint administrativo e interpreta o corpo.
func Fetch(ctx context.Context, url string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("lendo %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("%s devolveu %s", url, resp.Status)
	}

	// O parser precisa receber o esquema de validação de nomes explicitamente.
	// O zero value de TextParser deixa o esquema "unset", e a biblioteca entra
	// em pânico na primeira métrica lida. UTF8Validation é o padrão moderno do
	// Prometheus, que aceita nomes fora do conjunto legado.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return Snapshot{}, fmt.Errorf("interpretando %s: %w", url, err)
	}
	return Snapshot{families: families, At: time.Now()}, nil
}

// Sum soma as séries de uma métrica que casam com os rótulos pedidos.
//
// Rótulo ausente em match significa "qualquer valor", então dá para perguntar
// "os bytes do modo buffered" ou "os bytes de todos os modos" sem montar a lista
// de séries antes.
func (s Snapshot) Sum(name string, match map[string]string) float64 {
	family, ok := s.families[name]
	if !ok {
		return 0
	}
	var total float64
	for _, metric := range family.GetMetric() {
		if !matches(metric, match) {
			continue
		}
		switch {
		case metric.GetCounter() != nil:
			total += metric.GetCounter().GetValue()
		case metric.GetGauge() != nil:
			total += metric.GetGauge().GetValue()
		case metric.GetHistogram() != nil:
			total += metric.GetHistogram().GetSampleSum()
		}
	}
	return total
}

// Delta é a diferença entre duas leituras: o que aconteceu durante a janela.
//
// Só faz sentido para contador, que é monotônico. Para gauge, como o heap em uso,
// o que interessa é o valor no fim, e aí basta Sum sobre a leitura final.
func Delta(before, after Snapshot, name string, match map[string]string) float64 {
	return after.Sum(name, match) - before.Sum(name, match)
}

func matches(metric *dto.Metric, match map[string]string) bool {
	for name, want := range match {
		found := false
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == name {
				if pair.GetValue() != want {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
