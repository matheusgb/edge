// Package originsrv implementa a origem mínima do P04: um serviço Go
// que serve objetos sintéticos e um endpoint de trabalho limitado por
// CPU, usado para dar ao HPA algo real para medir.
//
// O endpoint /work existe porque um HPA baseado em CPU precisa de uma
// carga que realmente consuma CPU por requisição; servir bytes estáticos
// sozinho não cria pressão suficiente para observar escala em um laptop.
package originsrv

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/metrics"
)

// Config controla o comportamento do servidor de origem.
type Config struct {
	InstanceID string
	Warmup     time.Duration
	FailReady  bool
	MaxWork    int
	ObjectSize int
}

// Server é a origem mínima.
type Server struct {
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Origin
	ready   *atomic.Bool
}

// New cria o servidor de origem. ready é compartilhado com httpx.Pair
// para que o shutdown gracioso e o warmup controlem o mesmo estado.
func New(cfg Config, log *slog.Logger, m *metrics.Origin, ready *atomic.Bool) *Server {
	s := &Server{cfg: cfg, log: log, metrics: m, ready: ready}
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
	mux.HandleFunc("GET /work", s.instrument("work", s.handleWork))
	return mux
}

// AdminHandler monta as rotas administrativas: métricas, healthz, readyz.
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

// handleObject serve um objeto sintético determinístico. O nome do
// objeto vira a semente do conteúdo, então o mesmo nome sempre produz o
// mesmo corpo e o mesmo ETag, o que permite validar o cache da borda.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "nome do objeto obrigatório", http.StatusBadRequest)
		return
	}
	body := syntheticObject(name, s.cfg.ObjectSize)
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=5")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Origin-Instance", s.cfg.InstanceID)

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

// handleWork executa um laço de hashing SHA-256 de n iterações, para
// consumir CPU de forma proporcional e previsível ao parâmetro
// recebido. n é limitado por MaxWork para que uma requisição maliciosa
// ou mal formada não monopolize um núcleo indefinidamente.
func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 {
		n = 10_000
	}
	if n > s.cfg.MaxWork {
		n = s.cfg.MaxWork
	}

	sum := doWork(uint64(n))
	s.metrics.WorkIterations.Add(float64(n))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Origin-Instance", s.cfg.InstanceID)
	fmt.Fprintf(w, "iterations=%d checksum=%x\n", n, sum)
}

func doWork(n uint64) [32]byte {
	var buf [8]byte
	h := sha256.New()
	for i := uint64(0); i < n; i++ {
		binary.BigEndian.PutUint64(buf[:], i)
		h.Write(buf[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
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

func syntheticObject(name string, size int) []byte {
	if size <= 0 {
		size = 4096
	}
	seed := sha256.Sum256([]byte(name))
	out := make([]byte, size)
	block := seed[:]
	for i := 0; i < size; i += len(block) {
		copy(out[i:], block)
		block = hashOf(block)
	}
	return out
}

func hashOf(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
