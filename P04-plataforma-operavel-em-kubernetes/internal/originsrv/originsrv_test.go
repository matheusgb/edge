package originsrv

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/metrics"
)

func newTestServer(t testing.TB, cfg Config) (*Server, *atomic.Bool) {
	t.Helper()
	var ready atomic.Bool
	m := metrics.NewOrigin()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, log, m, &ready), &ready
}

func TestHealthzSempreOK(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 100, ObjectSize: 128})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	s.AdminHandler(http.NotFoundHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200, obteve %d", w.Code)
	}
}

func TestReadyzAntesDoWarmup(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 100, ObjectSize: 128})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	s.AdminHandler(http.NotFoundHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperava 503 antes do warmup, obteve %d", w.Code)
	}
}

func TestReadyzDepoisDoWarmup(t *testing.T) {
	s, ready := newTestServer(t, Config{Warmup: time.Millisecond, MaxWork: 100, ObjectSize: 128})
	deadline := time.Now().Add(time.Second)
	for !ready.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	s.AdminHandler(http.NotFoundHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200 depois do warmup, obteve %d", w.Code)
	}
}

func TestFailReadyNuncaFicaPronto(t *testing.T) {
	_, ready := newTestServer(t, Config{Warmup: time.Millisecond, FailReady: true, MaxWork: 100, ObjectSize: 128})
	time.Sleep(20 * time.Millisecond)
	if ready.Load() {
		t.Fatal("fail-ready deveria manter o servidor sempre not-ready")
	}
}

func TestObjectDeterministico(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 100, ObjectSize: 256})
	h := s.Handler()

	req1 := httptest.NewRequest(http.MethodGet, "/object/foo.bin", nil)
	req1.SetPathValue("name", "foo.bin")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/object/foo.bin", nil)
	req2.SetPathValue("name", "foo.bin")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)

	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("esperava 200 em ambas, obteve %d e %d", w1.Code, w2.Code)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatal("o mesmo nome de objeto deveria produzir o mesmo conteúdo")
	}
	if w1.Header().Get("ETag") != w2.Header().Get("ETag") {
		t.Fatal("o mesmo nome de objeto deveria produzir o mesmo ETag")
	}
	if len(w1.Body.Bytes()) != 256 {
		t.Fatalf("esperava objeto de 256 bytes, obteve %d", len(w1.Body.Bytes()))
	}
}

func TestObjectNomesDiferentesContéudoDiferente(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 100, ObjectSize: 256})
	h := s.Handler()

	reqA := httptest.NewRequest(http.MethodGet, "/object/a.bin", nil)
	reqA.SetPathValue("name", "a.bin")
	wA := httptest.NewRecorder()
	h.ServeHTTP(wA, reqA)

	reqB := httptest.NewRequest(http.MethodGet, "/object/b.bin", nil)
	reqB.SetPathValue("name", "b.bin")
	wB := httptest.NewRecorder()
	h.ServeHTTP(wB, reqB)

	if wA.Body.String() == wB.Body.String() {
		t.Fatal("nomes diferentes deveriam produzir conteúdo diferente")
	}
}

func TestObjectCondicionalIfNoneMatch(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 100, ObjectSize: 64})
	h := s.Handler()

	req1 := httptest.NewRequest(http.MethodGet, "/object/c.bin", nil)
	req1.SetPathValue("name", "c.bin")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	etag := w1.Header().Get("ETag")

	req2 := httptest.NewRequest(http.MethodGet, "/object/c.bin", nil)
	req2.SetPathValue("name", "c.bin")
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNotModified {
		t.Fatalf("esperava 304 com If-None-Match, obteve %d", w2.Code)
	}
}

func TestWorkRespeitaMaxWork(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 10, ObjectSize: 64})
	req := httptest.NewRequest(http.MethodGet, "/work?n=1000000", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200, obteve %d", w.Code)
	}
}

func TestWorkNInvalidoUsaDefault(t *testing.T) {
	s, _ := newTestServer(t, Config{Warmup: time.Hour, MaxWork: 1000, ObjectSize: 64})
	req := httptest.NewRequest(http.MethodGet, "/work?n=abc", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("esperava 200 mesmo com n inválido, obteve %d", w.Code)
	}
}
