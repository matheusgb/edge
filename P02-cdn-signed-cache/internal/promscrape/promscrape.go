// Package promscrape lê um endpoint /metrics e devolve valores somados.
//
// Os experimentos precisam responder perguntas do tipo "quantas requisições
// chegaram à origem durante ESTA janela". Métrica de Prometheus é contador
// acumulado desde que o processo subiu, então a resposta é sempre uma diferença
// entre duas leituras, uma antes e uma depois.
//
// O parser é o expfmt, a mesma biblioteca que o Prometheus usa para ler o
// formato de exposição. Não há motivo para escrever um leitor de texto próprio:
// o formato tem detalhes (escape de rótulo, tipos, sufixos de histograma) que já
// estão resolvidos e testados ali.
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

// Fetch lê o endpoint e interpreta o corpo.
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
// Rótulo ausente em match significa "qualquer valor". Assim dá para perguntar
// "todas as respostas 200" ou "todas as respostas, seja qual for o código", sem
// precisar montar a lista de séries antes.
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

// Count devolve o número de amostras de um histograma que casam com os rótulos.
func (s Snapshot) Count(name string, match map[string]string) float64 {
	family, ok := s.families[name]
	if !ok {
		return 0
	}
	var total float64
	for _, metric := range family.GetMetric() {
		if matches(metric, match) && metric.GetHistogram() != nil {
			total += float64(metric.GetHistogram().GetSampleCount())
		}
	}
	return total
}

// LabelValues lista os valores observados de um rótulo, com a soma de cada um.
// Serve para montar a tabela de "quantos HIT, quantos MISS" sem saber de
// antemão quais status apareceram.
func (s Snapshot) LabelValues(name, label string) map[string]float64 {
	out := map[string]float64{}
	family, ok := s.families[name]
	if !ok {
		return out
	}
	for _, metric := range family.GetMetric() {
		for _, pair := range metric.GetLabel() {
			if pair.GetName() != label {
				continue
			}
			switch {
			case metric.GetCounter() != nil:
				out[pair.GetValue()] += metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				out[pair.GetValue()] += metric.GetGauge().GetValue()
			}
		}
	}
	return out
}

// Delta é a diferença entre duas leituras: o que aconteceu na janela.
func Delta(before, after Snapshot, name string, match map[string]string) float64 {
	return after.Sum(name, match) - before.Sum(name, match)
}

// DeltaByLabel devolve a diferença por valor de rótulo.
func DeltaByLabel(before, after Snapshot, name, label string) map[string]float64 {
	out := after.LabelValues(name, label)
	for value, was := range before.LabelValues(name, label) {
		out[value] -= was
		if out[value] == 0 {
			delete(out, value)
		}
	}
	return out
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
