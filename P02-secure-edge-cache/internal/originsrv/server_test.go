package originsrv

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/metrics"
	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/objects"
)

const secret = "segredo-de-teste-compartilhado"

func newServer() *Server {
	return New(metrics.NewOrigin(), slog.New(slog.NewTextHandler(io.Discard, nil)), secret, 60*time.Second, 0)
}

func do(srv *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestObjetoProtegidoExigeSegredoDaBorda(t *testing.T) {
	srv := newServer()

	if rec := do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("sem segredo: código = %d, esperava 403", rec.Code)
	}
	if rec := do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", map[string]string{HeaderEdgeAuth: "errado"}); rec.Code != http.StatusForbidden {
		t.Fatalf("segredo errado: código = %d, esperava 403", rec.Code)
	}

	rec := do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", map[string]string{HeaderEdgeAuth: secret})
	if rec.Code != http.StatusOK {
		t.Fatalf("com segredo: código = %d, esperava 200", rec.Code)
	}
	if got := rec.Body.Len(); got != 1<<10 {
		t.Fatalf("corpo = %d bytes, esperava 1024", got)
	}
	if rec.Header().Get("Etag") != objects.ETag("img-1KiB-a.bin") {
		t.Fatal("ETag ausente ou diferente do esperado")
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestObjetoPublicoNaoExigeSegredo(t *testing.T) {
	if rec := do(newServer(), http.MethodGet, "/public/img-1KiB-a.bin", nil); rec.Code != http.StatusOK {
		t.Fatalf("código = %d, esperava 200", rec.Code)
	}
}

// A revalidação condicional é o que permite à borda renovar um objeto sem baixar
// o corpo de novo. Sem 304, cada expiração de TTL viraria transferência cheia.
func TestRevalidacaoCondicionalDevolve304(t *testing.T) {
	srv := newServer()
	name := "img-1KiB-a.bin"
	rec := do(srv, http.MethodGet, "/objects/"+name, map[string]string{
		HeaderEdgeAuth:  secret,
		"If-None-Match": objects.ETag(name),
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("código = %d, esperava 304", rec.Code)
	}
	if c := srv.Counters(); c.Revalidations != 1 || c.Bodies != 0 {
		t.Fatalf("contadores = %+v, esperava 1 revalidação e 0 corpos", c)
	}
}

func TestNomeInvalidoDevolve404(t *testing.T) {
	rec := do(newServer(), http.MethodGet, "/objects/nome%20com%20espaco.bin", map[string]string{HeaderEdgeAuth: secret})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("código = %d, esperava 404", rec.Code)
	}
}

func TestEchoNaoDevolveOSegredo(t *testing.T) {
	rec := do(newServer(), http.MethodGet, "/debug/echo", map[string]string{
		HeaderEdgeAuth:     secret,
		"X-Forwarded-Host": "atacante.example",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("código = %d, esperava 200", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta não é JSON: %v", err)
	}
	if got[HeaderEdgeAuth] != "(omitido)" {
		t.Fatalf("o endpoint de diagnóstico devolveu o segredo: %q", got[HeaderEdgeAuth])
	}
	if got["X-Forwarded-Host"] != "atacante.example" {
		t.Fatal("o echo deveria mostrar os headers realmente recebidos")
	}
}

func TestContadoresSeparamCorpoDeRevalidacao(t *testing.T) {
	srv := newServer()
	for range 3 {
		do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", map[string]string{HeaderEdgeAuth: secret})
	}
	do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", nil) // negada

	c := srv.Counters()
	if c.Requests != 4 || c.Bodies != 3 || c.AuthFailures != 1 {
		t.Fatalf("contadores = %+v", c)
	}
	if c.Bytes != 3<<10 {
		t.Fatalf("bytes = %d, esperava 3072", c.Bytes)
	}
}

func TestModoDeFalhaELatencia(t *testing.T) {
	srv := newServer()
	admin := srv.AdminHandler(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/fail?status=503", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/fail devolveu %d", rec.Code)
	}
	if got := do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", map[string]string{HeaderEdgeAuth: secret}); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("código = %d, esperava 503", got.Code)
	}

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/fail?status=0", nil))
	if got := do(srv, http.MethodGet, "/objects/img-1KiB-a.bin", map[string]string{HeaderEdgeAuth: secret}); got.Code != http.StatusOK {
		t.Fatalf("código = %d, esperava 200 depois de desligar a falha", got.Code)
	}

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/latency?d=xyz", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("latência inválida devia dar 400, deu %d", rec.Code)
	}
}
