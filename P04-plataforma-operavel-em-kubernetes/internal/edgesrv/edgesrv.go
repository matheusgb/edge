// Package edgesrv implementa a CDN mínima do P04: um proxy reverso para
// a origem com um cache em memória de TTL curto para /object/{name}.
// /work nunca é cacheado, porque o objetivo dele é gerar carga de CPU
// medível na origem a cada requisição, não conteúdo reaproveitável.
package edgesrv

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/metrics"
)

// Config controla o comportamento da borda.
type Config struct {
	InstanceID string
	OriginURL  string
	CacheTTL   time.Duration
	Warmup     time.Duration
	FailReady  bool
}

type cacheEntry struct {
	body      []byte
	header    http.Header
	status    int
	expiresAt time.Time
}

// Server é a borda (CDN mínima).
type Server struct {
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Edge
	ready   *atomic.Bool
	client  *http.Client

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// New cria a borda. ready é compartilhado com httpx.Pair, no mesmo
// espírito do serviço de origem.
func New(cfg Config, log *slog.Logger, m *metrics.Edge, ready *atomic.Bool) *Server {
	s := &Server{
		cfg:     cfg,
		log:     log,
		metrics: m,
		ready:   ready,
		client:  &http.Client{Timeout: 10 * time.Second},
		cache:   make(map[string]cacheEntry),
	}
	go s.warmup()
	return s
}

func (s *Server) warmup() {
	if s.cfg.FailReady {
		s.log.Warn("fail-ready ativo: este pod nunca ficará pronto")
		return
	}
	time.Sleep(s.cfg.Warmup)
	s.ready.Store(true)
	s.log.Info("warmup concluído, servidor pronto", "warmup", s.cfg.Warmup)
}

// Handler monta as rotas públicas.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /object/{name}", s.instrument("object", s.handleObject))
	mux.HandleFunc("GET /work", s.instrument("work", s.handleWorkPassthrough))
	return mux
}

// AdminHandler monta as rotas administrativas.
func (s *Server) AdminHandler(promHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promHandler)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	return mux
}

func (s *Server) instrument(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.metrics.InFlight.Inc()
		defer s.metrics.InFlight.Dec()
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r)
		s.metrics.Duration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		s.metrics.Requests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
	}
}

// handleObject atende do cache local quando o TTL ainda é válido, e
// encaminha para a origem quando não. A chave de cache é o caminho, sem
// nenhum parâmetro extra, porque este projeto não lida com URL assinada
// (isso é escopo do P02).
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path

	s.mu.RLock()
	entry, ok := s.cache[key]
	s.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		s.metrics.CacheHits.Inc()
		writeEntry(w, entry, "HIT")
		return
	}
	s.metrics.CacheMisses.Inc()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.OriginURL+r.URL.Path, nil)
	if err != nil {
		http.Error(w, "requisição inválida para a origem", http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.OriginErrors.Inc()
		http.Error(w, "origem indisponível", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.metrics.OriginErrors.Inc()
		http.Error(w, "falha ao ler resposta da origem", http.StatusBadGateway)
		return
	}

	newEntry := cacheEntry{
		body:      body,
		header:    resp.Header.Clone(),
		status:    resp.StatusCode,
		expiresAt: time.Now().Add(s.cfg.CacheTTL),
	}
	if resp.StatusCode == http.StatusOK {
		s.mu.Lock()
		s.cache[key] = newEntry
		s.mu.Unlock()
	}
	writeEntry(w, newEntry, "MISS")
}

func writeEntry(w http.ResponseWriter, entry cacheEntry, cacheStatus string) {
	for k, v := range entry.header {
		if k == "Connection" {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("X-Cache", cacheStatus)
	w.WriteHeader(entry.status)
	if entry.status != http.StatusNotModified {
		w.Write(entry.body)
	}
}

// handleWorkPassthrough sempre encaminha para a origem: /work existe
// para gerar CPU na origem a cada chamada, então cachear a resposta
// destruiria o propósito do experimento de escala.
func (s *Server) handleWorkPassthrough(w http.ResponseWriter, r *http.Request) {
	s.metrics.CacheBypass.Inc()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.OriginURL+r.URL.RequestURI(), nil)
	if err != nil {
		http.Error(w, "requisição inválida para a origem", http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.OriginErrors.Inc()
		http.Error(w, "origem indisponível", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		s.metrics.OriginErrors.Inc()
		http.Error(w, "falha ao ler resposta da origem", http.StatusBadGateway)
		return
	}
	for k, v := range resp.Header {
		if k == "Connection" {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("X-Cache", "BYPASS")
	w.WriteHeader(resp.StatusCode)
	w.Write(buf.Bytes())
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.ready.Load() {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
