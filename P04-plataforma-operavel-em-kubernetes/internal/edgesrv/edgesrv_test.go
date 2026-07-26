package edgesrv

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/metrics"
)

func newTestServer(t *testing.T, originURL string, ttl time.Duration) (*Server, *atomic.Bool) {
	t.Helper()
	var ready atomic.Bool
	m := metrics.NewEdge()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{OriginURL: originURL, CacheTTL: ttl, Warmup: time.Hour}
	return New(cfg, log, m, &ready), &ready
}

func TestCacheHitDepoisDoPrimeiroMiss(t *testing.T) {
	var originHits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originHits, 1)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "conteúdo")
	}))
	defer origin.Close()

	s, _ := newTestServer(t, origin.URL, time.Minute)
	h := s.Handler()

	req1 := httptest.NewRequest(http.MethodGet, "/object/x.bin", nil)
	req1.SetPathValue("name", "x.bin")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if got := w1.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("esperava MISS na primeira chamada, obteve %s", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/object/x.bin", nil)
	req2.SetPathValue("name", "x.bin")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if got := w2.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("esperava HIT na segunda chamada, obteve %s", got)
	}

	if hits := atomic.LoadInt32(&originHits); hits != 1 {
		t.Fatalf("esperava exatamente 1 chamada à origem, obteve %d", hits)
	}
}

func TestCacheExpiraAposTTL(t *testing.T) {
	var originHits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originHits, 1)
		fmt.Fprint(w, "v")
	}))
	defer origin.Close()

	s, _ := newTestServer(t, origin.URL, 10*time.Millisecond)
	h := s.Handler()

	req1 := httptest.NewRequest(http.MethodGet, "/object/y.bin", nil)
	req1.SetPathValue("name", "y.bin")
	h.ServeHTTP(httptest.NewRecorder(), req1)

	time.Sleep(30 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodGet, "/object/y.bin", nil)
	req2.SetPathValue("name", "y.bin")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if got := w2.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("esperava MISS após expirar o TTL, obteve %s", got)
	}
	if hits := atomic.LoadInt32(&originHits); hits != 2 {
		t.Fatalf("esperava 2 chamadas à origem após expirar o cache, obteve %d", hits)
	}
}

func TestWorkNuncaEhCacheadoSempreBypass(t *testing.T) {
	var originHits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originHits, 1)
		fmt.Fprint(w, "iterations=1 checksum=aa")
	}))
	defer origin.Close()

	s, _ := newTestServer(t, origin.URL, time.Minute)
	h := s.Handler()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/work?n=1", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("X-Cache"); got != "BYPASS" {
			t.Fatalf("esperava BYPASS em /work, obteve %s", got)
		}
	}
	if hits := atomic.LoadInt32(&originHits); hits != 3 {
		t.Fatalf("esperava 3 chamadas à origem (sem cache), obteve %d", hits)
	}
}

func TestOrigemIndisponivelRetorna502(t *testing.T) {
	s, _ := newTestServer(t, "http://127.0.0.1:1", time.Minute)
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/object/z.bin", nil)
	req.SetPathValue("name", "z.bin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("esperava 502 com origem indisponível, obteve %d", w.Code)
	}
}

func TestReadyzAntesDoWarmup(t *testing.T) {
	s, _ := newTestServer(t, "http://example.invalid", time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	s.AdminHandler(http.NotFoundHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperava 503 antes do warmup, obteve %d", w.Code)
	}
}
