// Package logexport transforma o log de acesso do Nginx em métricas Prometheus.
//
// Por que isso existe: o Nginx de código aberto não expõe contadores de cache.
// Ele sabe se cada resposta foi HIT, MISS, EXPIRED, STALE ou BYPASS, e escreve
// isso no log de acesso, mas não há endpoint para perguntar "qual foi o hit ratio
// da última hora". Sem número, não há evidência, e o projeto inteiro depende
// dessa medida.
//
// Este pacote faz só a parte que é do domínio do projeto: transformar uma linha
// de log em métrica com os rótulos certos. Seguir o arquivo, tratar rotação e
// truncamento é problema resolvido, e fica com a biblioteca nxadm/tail, usada no
// comando logexporter.
//
// O formato do log é decisão de segurança. O token viaja na query string, então o
// log grava $uri (caminho normalizado, sem query) e nunca $request_uri. Este
// pacote ainda vigia o que lê: se uma linha trouxer marca de token, ele conta
// isso numa métrica própria, porque uma configuração alterada por engano não pode
// vazar credencial em silêncio.
package logexport

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/signer"
	"github.com/prometheus/client_golang/prometheus"
)

// Line é uma linha do log de acesso em JSON.
type Line struct {
	Time         string  `json:"time"`
	Host         string  `json:"host"`
	Method       string  `json:"method"`
	URI          string  `json:"uri"`
	Status       int     `json:"status"`
	Cache        string  `json:"cache"`
	UpstreamTime string  `json:"upstream_time"`
	RequestTime  float64 `json:"request_time"`
	Bytes        int64   `json:"bytes"`
}

// Parse lê uma linha do log.
func Parse(raw []byte) (Line, error) {
	var line Line
	if err := json.Unmarshal(raw, &line); err != nil {
		return Line{}, fmt.Errorf("linha não é JSON: %w", err)
	}
	return line, nil
}

// CacheStatus normaliza o valor de $upstream_cache_status.
//
// O Nginx escreve "-" quando a requisição nem chegou a consultar o cache, o que
// acontece com uma resposta que ele mesmo gerou: um 403 do auth_request, um 429
// do rate limit, um 405 de método recusado. Chamar isso de "none" separa
// "não passou pelo cache" de "passou e não achou", que são coisas diferentes.
func (l Line) CacheStatus() string {
	status := strings.ToUpper(strings.TrimSpace(l.Cache))
	if status == "" || status == "-" {
		return "none"
	}
	return status
}

// UpstreamSeconds devolve o tempo gasto na origem.
//
// O Nginx pode escrever vários valores separados por vírgula quando há mais de
// uma tentativa de upstream. Somamos todos: o que interessa é o tempo total que
// a origem custou àquela requisição, não o de uma tentativa isolada.
func (l Line) UpstreamSeconds() (float64, bool) {
	raw := strings.TrimSpace(l.UpstreamTime)
	if raw == "" || raw == "-" {
		return 0, false
	}
	var total float64
	var found bool
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			continue
		}
		total += value
		found = true
	}
	return total, found
}

// Collector acumula as métricas derivadas do log.
type Collector struct {
	Registry *prometheus.Registry

	// Responses conta cada resposta da CDN por status de cache e código HTTP.
	// É daqui que sai o hit ratio, a prova principal do projeto.
	Responses *prometheus.CounterVec

	// Bytes soma o que a CDN entregou, separado por status de cache: mostra
	// quanto tráfego o cache poupou da origem em volume, não só em contagem.
	Bytes *prometheus.CounterVec

	// UpstreamSeconds mede o tempo na origem visto pela CDN. Comparado com o
	// tempo medido dentro da própria origem, mostra o custo da rede entre as duas.
	UpstreamSeconds prometheus.Histogram

	// RequestSeconds mede o tempo total visto pelo cliente, por status de cache.
	RequestSeconds *prometheus.HistogramVec

	// Malformed conta linhas que não deram para ler. Se subir, o formato do log
	// e este leitor saíram de sincronia, e todas as outras séries ficam suspeitas.
	Malformed prometheus.Counter

	// TokenLeaks conta linhas de log com marca de token. O valor esperado é zero
	// para sempre; qualquer coisa acima disso é incidente, não estatística.
	TokenLeaks prometheus.Counter
}

// NewCollector cria o coletor com registro próprio.
func NewCollector() *Collector {
	c := &Collector{Registry: prometheus.NewRegistry()}

	c.Responses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cdn_responses_total",
		Help: "Respostas da CDN, por status de cache e código HTTP.",
	}, []string{"cache", "code"})

	c.Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cdn_response_bytes_total",
		Help: "Bytes entregues pela CDN, por status de cache.",
	}, []string{"cache"})

	c.UpstreamSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cdn_upstream_duration_seconds",
		Help:    "Tempo gasto na origem, quando a CDN precisou consultá-la.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
	})

	c.RequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "cdn_request_duration_seconds",
		Help:    "Tempo total da requisição visto pela CDN, por status de cache.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
	}, []string{"cache"})

	c.Malformed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cdn_log_malformed_lines_total",
		Help: "Linhas do log de acesso que este leitor não conseguiu interpretar.",
	})

	c.TokenLeaks = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cdn_log_token_leaks_total",
		Help: "Linhas do log de acesso contendo marca de token. O esperado é zero.",
	})

	c.Registry.MustRegister(c.Responses, c.Bytes, c.UpstreamSeconds, c.RequestSeconds, c.Malformed, c.TokenLeaks)
	return c
}

// Feed processa uma linha crua do log.
func (c *Collector) Feed(raw []byte) {
	// A vigilância acontece ANTES do parse: uma linha que nem é JSON válido pode
	// muito bem estar carregando um token, e ignorá-la esconderia o vazamento.
	if bytesContainsToken(raw) {
		c.TokenLeaks.Inc()
	}
	line, err := Parse(raw)
	if err != nil {
		c.Malformed.Inc()
		return
	}
	c.Observe(line)
}

// Observe contabiliza uma linha já interpretada.
func (c *Collector) Observe(line Line) {
	status := line.CacheStatus()
	c.Responses.WithLabelValues(status, strconv.Itoa(line.Status)).Inc()
	c.Bytes.WithLabelValues(status).Add(float64(line.Bytes))
	c.RequestSeconds.WithLabelValues(status).Observe(line.RequestTime)
	if seconds, ok := line.UpstreamSeconds(); ok {
		c.UpstreamSeconds.Observe(seconds)
	}
}

// bytesContainsToken procura as marcas dos parâmetros do token.
func bytesContainsToken(raw []byte) bool {
	text := string(raw)
	for _, param := range []string{signer.ParamSignature + "=", signer.ParamKeyID + "=", signer.ParamExpires + "="} {
		if strings.Contains(text, param) {
			return true
		}
	}
	return false
}
