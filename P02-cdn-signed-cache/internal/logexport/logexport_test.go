package logexport

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const linhaHit = `{"time":"2026-07-25T21:00:00+00:00","host":"edge","method":"GET","uri":"/objects/img-64KiB-a.bin","status":200,"cache":"HIT","upstream_time":"-","request_time":0.001,"bytes":65536}`

const linhaMiss = `{"time":"2026-07-25T21:00:01+00:00","host":"edge","method":"GET","uri":"/objects/img-64KiB-a.bin","status":200,"cache":"MISS","upstream_time":"0.030","request_time":0.032,"bytes":65536}`

const linhaNegada = `{"time":"2026-07-25T21:00:02+00:00","host":"edge","method":"GET","uri":"/objects/img-64KiB-a.bin","status":403,"cache":"-","upstream_time":"-","request_time":0.001,"bytes":0}`

func TestParseELeituraDosCampos(t *testing.T) {
	line, err := Parse([]byte(linhaMiss))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if line.CacheStatus() != "MISS" || line.Status != 200 || line.Bytes != 65536 {
		t.Fatalf("linha lida errado: %+v", line)
	}
	seconds, ok := line.UpstreamSeconds()
	if !ok || seconds != 0.030 {
		t.Fatalf("UpstreamSeconds = %v, %v", seconds, ok)
	}
}

// "-" é o que o Nginx escreve quando a requisição nem consultou o cache. Tratar
// isso como um status de cache criaria uma categoria fantasma no hit ratio.
func TestRespostaGeradaPelaCDNNaoTemStatusDeCache(t *testing.T) {
	line, err := Parse([]byte(linhaNegada))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := line.CacheStatus(); got != "none" {
		t.Fatalf("CacheStatus = %q, esperava none", got)
	}
	if _, ok := line.UpstreamSeconds(); ok {
		t.Fatal("uma resposta que não foi à origem não deveria ter tempo de origem")
	}
}

func TestUpstreamSecondsSomaTentativas(t *testing.T) {
	line := Line{UpstreamTime: "0.010, 0.020"}
	seconds, ok := line.UpstreamSeconds()
	if !ok || seconds != 0.030 {
		t.Fatalf("UpstreamSeconds = %v, %v; esperava 0.03", seconds, ok)
	}
}

func TestCollectorContaPorStatusDeCache(t *testing.T) {
	c := NewCollector()
	c.Feed([]byte(linhaHit))
	c.Feed([]byte(linhaHit))
	c.Feed([]byte(linhaMiss))
	c.Feed([]byte(linhaNegada))

	if got := testutil.ToFloat64(c.Responses.WithLabelValues("HIT", "200")); got != 2 {
		t.Fatalf("HIT 200 = %v, esperava 2", got)
	}
	if got := testutil.ToFloat64(c.Responses.WithLabelValues("MISS", "200")); got != 1 {
		t.Fatalf("MISS 200 = %v, esperava 1", got)
	}
	if got := testutil.ToFloat64(c.Responses.WithLabelValues("none", "403")); got != 1 {
		t.Fatalf("none 403 = %v, esperava 1", got)
	}
	if got := testutil.ToFloat64(c.Bytes.WithLabelValues("HIT")); got != 2*65536 {
		t.Fatalf("bytes de HIT = %v", got)
	}
}

func TestLinhaIlegivelViraMetricaEmVezDeSumir(t *testing.T) {
	c := NewCollector()
	c.Feed([]byte("isto não é json"))
	if got := testutil.ToFloat64(c.Malformed); got != 1 {
		t.Fatalf("malformed = %v, esperava 1", got)
	}
}

// O leitor vigia o próprio log: se alguém mexer no log_format e a query voltar,
// o vazamento acende um contador em vez de passar despercebido.
func TestVazamentoDeTokenNoLogEhContado(t *testing.T) {
	c := NewCollector()
	comToken := strings.Replace(linhaHit, `"/objects/img-64KiB-a.bin"`, `"/objects/img-64KiB-a.bin?kid=k1&exp=1&sig=deadbeef"`, 1)
	c.Feed([]byte(comToken))
	if got := testutil.ToFloat64(c.TokenLeaks); got != 1 {
		t.Fatalf("token leaks = %v, esperava 1", got)
	}

	c2 := NewCollector()
	c2.Feed([]byte(linhaHit))
	if got := testutil.ToFloat64(c2.TokenLeaks); got != 0 {
		t.Fatalf("token leaks = %v numa linha limpa, esperava 0", got)
	}
}
