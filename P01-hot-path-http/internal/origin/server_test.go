package origin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/catalog"
	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/metrics"
)

const testObject = "obj-64KiB.bin"

// setupTestServer sobe um servidor real sobre um catálogo temporário.
//
// Usamos httptest.Server, e não chamadas diretas ao handler, porque parte do que
// o lab estuda (Range, conexões, cancelamento) só acontece de verdade com um
// socket no meio.
func setupTestServer(t *testing.T, mode Mode) (*httptest.Server, *metrics.Metrics, []byte) {
	t.Helper()

	dir := t.TempDir()
	if _, err := catalog.Generate(dir, []int64{64 << 10}); err != nil {
		t.Fatalf("gerando catálogo: %v", err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatalf("carregando catálogo: %v", err)
	}

	obj, err := cat.Get(testObject)
	if err != nil {
		t.Fatalf("objeto de teste ausente: %v", err)
	}
	want, err := os.ReadFile(obj.Path)
	if err != nil {
		t.Fatalf("lendo objeto de teste: %v", err)
	}

	m := metrics.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(cat, m, logger, mode).Handler())
	t.Cleanup(srv.Close)

	return srv, m, want
}

// TestModosDevolvemConteudoIdentico é o gate central do P01: se os dois modos não
// entregam exatamente os mesmos bytes, a comparação de desempenho não significa nada.
func TestModosDevolvemConteudoIdentico(t *testing.T) {
	t.Parallel()

	srv, _, want := setupTestServer(t, ModeStreamed)

	var corpos [][]byte
	var etags []string
	for _, mode := range []Mode{ModeBuffered, ModeStreamed} {
		resp, err := srv.Client().Get(srv.URL + "/objects/" + testObject + "?mode=" + string(mode))
		if err != nil {
			t.Fatalf("modo %s: %v", mode, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("modo %s: lendo corpo: %v", mode, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("modo %s: esperava 200, recebi %d", mode, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Origin-Mode"); got != string(mode) {
			t.Errorf("modo %s: header X-Origin-Mode veio %q", mode, got)
		}
		if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(want)) {
			t.Errorf("modo %s: Content-Length %q, esperava %d", mode, got, len(want))
		}
		corpos = append(corpos, body)
		etags = append(etags, resp.Header.Get("Etag"))
	}

	if string(corpos[0]) != string(corpos[1]) {
		t.Error("buffered e streamed devolveram conteúdos diferentes")
	}
	if string(corpos[0]) != string(want) {
		t.Error("o conteúdo servido não bate com o objeto em disco")
	}
	if etags[0] != etags[1] {
		t.Errorf("ETag divergiu entre os modos: %s vs %s", etags[0], etags[1])
	}
	if etags[0] == "" {
		t.Error("ETag ausente na resposta")
	}
}

func TestRangeFuncionaNosDoisModos(t *testing.T) {
	t.Parallel()

	srv, _, want := setupTestServer(t, ModeStreamed)

	for _, mode := range []Mode{ModeBuffered, ModeStreamed} {
		t.Run(string(mode), func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet,
				srv.URL+"/objects/"+testObject+"?mode="+string(mode), nil)
			if err != nil {
				t.Fatalf("montando requisição: %v", err)
			}
			req.Header.Set("Range", "bytes=100-199")

			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("requisição: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("esperava 206, recebi %d", resp.StatusCode)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("lendo corpo: %v", err)
			}
			if len(body) != 100 {
				t.Fatalf("esperava 100 bytes, recebi %d", len(body))
			}
			if string(body) != string(want[100:200]) {
				t.Error("o intervalo devolvido não corresponde ao trecho do objeto")
			}
			if cr := resp.Header.Get("Content-Range"); !strings.HasPrefix(cr, "bytes 100-199/") {
				t.Errorf("Content-Range inesperado: %q", cr)
			}
		})
	}
}

func TestRequisicaoCondicionalDevolve304(t *testing.T) {
	t.Parallel()

	srv, _, _ := setupTestServer(t, ModeBuffered)

	resp, err := srv.Client().Get(srv.URL + "/objects/" + testObject)
	if err != nil {
		t.Fatalf("primeira requisição: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	etag := resp.Header.Get("Etag")

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/objects/"+testObject, nil)
	if err != nil {
		t.Fatalf("montando requisição: %v", err)
	}
	req.Header.Set("If-None-Match", etag)

	cond, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("requisição condicional: %v", err)
	}
	defer cond.Body.Close()

	if cond.StatusCode != http.StatusNotModified {
		t.Fatalf("esperava 304 para If-None-Match igual, recebi %d", cond.StatusCode)
	}
	body, _ := io.ReadAll(cond.Body)
	if len(body) != 0 {
		t.Errorf("304 não pode ter corpo, veio com %d bytes", len(body))
	}
}

func TestHeadNaoEnviaCorpoMasInformaTamanho(t *testing.T) {
	t.Parallel()

	srv, _, want := setupTestServer(t, ModeStreamed)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/objects/"+testObject, nil)
	if err != nil {
		t.Fatalf("montando requisição: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("requisição HEAD: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperava 200, recebi %d", resp.StatusCode)
	}
	if got := resp.ContentLength; got != int64(len(want)) {
		t.Errorf("Content-Length %d, esperava %d", got, len(want))
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD não pode ter corpo, veio com %d bytes", len(body))
	}
}

func TestObjetoInexistenteDevolve404(t *testing.T) {
	t.Parallel()

	srv, m, _ := setupTestServer(t, ModeStreamed)

	resp, err := srv.Client().Get(srv.URL + "/objects/nao-existe.bin")
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("esperava 404, recebi %d", resp.StatusCode)
	}
	if got := counterValue(t, m.Errors, "streamed", "not_found"); got != 1 {
		t.Errorf("contador de not_found = %v, esperava 1", got)
	}
}

func TestModoInvalidoDevolve400(t *testing.T) {
	t.Parallel()

	srv, _, _ := setupTestServer(t, ModeStreamed)

	resp, err := srv.Client().Get(srv.URL + "/objects/" + testObject + "?mode=turbo")
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("esperava 400 para modo inválido, recebi %d", resp.StatusCode)
	}
}

func TestMetricasContamRequisicoesEBytes(t *testing.T) {
	t.Parallel()

	srv, m, want := setupTestServer(t, ModeBuffered)

	resp, err := srv.Client().Get(srv.URL + "/objects/" + testObject)
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := counterValue(t, m.Requests, "buffered", "GET", "200"); got != 1 {
		t.Errorf("requisições contadas = %v, esperava 1", got)
	}
	if got := counterValue(t, m.ResponseBytes, "buffered"); got != float64(len(want)) {
		t.Errorf("bytes contados = %v, esperava %d", got, len(want))
	}
	// Com a requisição encerrada, nada pode continuar marcado como em andamento.
	if got := gaugeValue(t, m.InFlight, "buffered"); got != 0 {
		t.Errorf("in-flight = %v após a resposta, esperava 0", got)
	}
}

func TestCancelamentoDoClienteEhContabilizado(t *testing.T) {
	t.Parallel()

	srv, m, _ := setupTestServer(t, ModeStreamed)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/objects/"+testObject, nil)
	if err != nil {
		t.Fatalf("montando requisição: %v", err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	// Desiste depois dos headers e antes de drenar o corpo: é a forma de simular
	// o cliente que fecha a aba no meio do download.
	cancel()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// O servidor observa o cancelamento de forma assíncrona; o teste só exige que
	// a requisição não tenha ficado presa no contador de in-flight.
	waitFor(t, func() bool { return gaugeValue(t, m.InFlight, "streamed") == 0 })
}

func TestCatalogListaObjetos(t *testing.T) {
	t.Parallel()

	srv, _, _ := setupTestServer(t, ModeStreamed)

	resp, err := srv.Client().Get(srv.URL + "/catalog")
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lendo corpo: %v", err)
	}
	if !strings.Contains(string(body), testObject) {
		t.Errorf("o catálogo não listou %s: %q", testObject, string(body))
	}
}

func TestParseModeRejeitaValorDesconhecido(t *testing.T) {
	t.Parallel()

	if _, err := ParseMode("buffered"); err != nil {
		t.Errorf("buffered deveria ser válido: %v", err)
	}
	if _, err := ParseMode("streamed"); err != nil {
		t.Errorf("streamed deveria ser válido: %v", err)
	}
	if _, err := ParseMode(""); err == nil {
		t.Error("modo vazio deveria ser rejeitado")
	}
	if _, err := ParseMode("cached"); err == nil {
		t.Error("modo desconhecido deveria ser rejeitado")
	}
}

// --- auxiliares de leitura de métricas ---

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	var m dto.Metric
	if err := vec.WithLabelValues(labels...).Write(&m); err != nil {
		t.Fatalf("lendo contador %v: %v", labels, err)
	}
	return m.GetCounter().GetValue()
}

func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	var m dto.Metric
	if err := vec.WithLabelValues(labels...).Write(&m); err != nil {
		t.Fatalf("lendo gauge %v: %v", labels, err)
	}
	return m.GetGauge().GetValue()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("condição não foi satisfeita a tempo")
}
