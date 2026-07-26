// Package originsrv implementa a origem do P03.
//
// A origem aqui é simples de propósito: ela existe para ser o recurso caro que os
// edges protegem, e para poder ficar fora do ar sob comando. O que ela tem de
// especial em relação à origem do P02 é o contador de requisições simultâneas: no
// cenário de recuperação, a pergunta não é só se a origem voltou, é se todos os
// edges voltaram a procurá-la ao mesmo tempo.
package originsrv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/metrics"
	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/objects"
)

// Counters é o resumo lido pelos experimentos.
type Counters struct {
	Requests  int64 `json:"requests"`
	Bodies    int64 `json:"bodies"`
	Errors    int64 `json:"errors"`
	Bytes     int64 `json:"bytes"`
	MaxInFlig int64 `json:"max_inflight"`
}

// Server atende os objetos da origem.
type Server struct {
	metrics *metrics.Origin
	logger  *slog.Logger

	maxAgeSeconds atomic.Int64
	latencyNanos  atomic.Int64
	failStatus    atomic.Int64
	failUntil     atomic.Int64 // unix nano; 0 = sem prazo

	requests atomic.Int64
	bodies   atomic.Int64
	errs     atomic.Int64
	bytesOut atomic.Int64
	inflight atomic.Int64
	maxFlig  atomic.Int64
}

// New monta a origem.
func New(m *metrics.Origin, logger *slog.Logger, maxAge, latency time.Duration) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{metrics: m, logger: logger}
	s.maxAgeSeconds.Store(int64(maxAge.Seconds()))
	s.latencyNanos.Store(int64(latency))
	return s
}

// Handler devolve o roteador público.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects/{name}", s.handleObject)
	mux.HandleFunc("HEAD /objects/{name}", s.handleObject)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	s.requests.Add(1)

	// A marca d'água de simultaneidade é o número que responde "a recuperação
	// causou avalanche?". Um pico de dez buscas simultâneas depois de um outage
	// tem significado bem diferente de um pico de trezentas.
	atual := s.inflight.Add(1)
	for {
		maior := s.maxFlig.Load()
		if atual <= maior {
			break
		}
		if s.maxFlig.CompareAndSwap(maior, atual) {
			s.metrics.InflightPeak.Set(float64(atual))
			break
		}
	}
	s.metrics.Inflight.Set(float64(atual))

	defer func() {
		restante := s.inflight.Add(-1)
		s.metrics.Inflight.Set(float64(restante))
		s.metrics.Duration.Observe(time.Since(started).Seconds())
		s.metrics.Requests.WithLabelValues(strconv.Itoa(rec.status)).Inc()
		switch {
		case rec.status == http.StatusOK:
			s.metrics.Bytes.Add(float64(rec.written))
			s.bytesOut.Add(rec.written)
			s.bodies.Add(1)
		case rec.status >= 500:
			s.errs.Add(1)
		}
	}()

	if !s.degrade(rec, r) {
		return
	}

	name := r.PathValue("name")
	content, err := objects.Content(name)
	if err != nil {
		http.Error(rec, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", s.maxAgeSeconds.Load()))
	w.Header().Set("Etag", objects.ETag(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Origin-Generated-At", time.Now().UTC().Format(time.RFC3339Nano))

	// modTime fixo: um horário variável mudaria o Last-Modified a cada
	// requisição e faria toda revalidação condicional falhar sem motivo.
	http.ServeContent(rec, r, name, time.Unix(0, 0).UTC(), bytes.NewReader(content))
}

// degrade aplica latência artificial e modo de falha. Devolve false quando já
// respondeu e o handler deve parar.
func (s *Server) degrade(w http.ResponseWriter, r *http.Request) bool {
	if d := time.Duration(s.latencyNanos.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			// O cliente desistiu durante a espera, provavelmente porque o prazo
			// propagado pelo roteador venceu. Responder agora seria escrever numa
			// conexão que já foi embora.
			return false
		}
	}
	if status := int(s.failStatus.Load()); status != 0 {
		if until := s.failUntil.Load(); until != 0 && time.Now().UnixNano() > until {
			s.failStatus.Store(0)
			s.failUntil.Store(0)
		} else {
			http.Error(w, http.StatusText(status), status)
			return false
		}
	}
	return true
}

// AdminHandler devolve o roteador administrativo.
func (s *Server) AdminHandler(metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metricsHandler)

	mux.HandleFunc("GET /admin/counters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Counters())
	})

	mux.HandleFunc("PUT /admin/latency", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil || d < 0 {
			http.Error(w, "parâmetro d inválido", http.StatusBadRequest)
			return
		}
		s.latencyNanos.Store(int64(d))
		s.logger.Info("latência artificial alterada", "latency", d.String())
		fmt.Fprintf(w, "latency=%s\n", d)
	})

	mux.HandleFunc("PUT /admin/fail", func(w http.ResponseWriter, r *http.Request) {
		status, err := strconv.Atoi(r.URL.Query().Get("status"))
		if err != nil || (status != 0 && (status < 400 || status > 599)) {
			http.Error(w, "parâmetro status inválido: use 0 ou um código 4xx/5xx", http.StatusBadRequest)
			return
		}
		var until int64
		if raw := r.URL.Query().Get("for"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				http.Error(w, "parâmetro for inválido: "+err.Error(), http.StatusBadRequest)
				return
			}
			until = time.Now().Add(d).UnixNano()
		}
		s.failStatus.Store(int64(status))
		s.failUntil.Store(until)
		s.logger.Warn("modo de falha alterado", "status", status, "until_unix_nano", until)
		fmt.Fprintf(w, "fail=%d\n", status)
	})

	// Zerar a marca d'água entre cenários evita que o pico de um experimento
	// apareça no relatório do seguinte.
	mux.HandleFunc("POST /admin/reset-peak", func(w http.ResponseWriter, _ *http.Request) {
		s.maxFlig.Store(0)
		s.metrics.InflightPeak.Set(0)
		fmt.Fprintln(w, "pico zerado")
	})

	return mux
}

// Counters devolve a foto atual.
func (s *Server) Counters() Counters {
	return Counters{
		Requests:  s.requests.Load(),
		Bodies:    s.bodies.Load(),
		Errors:    s.errs.Load(),
		Bytes:     s.bytesOut.Load(),
		MaxInFlig: s.maxFlig.Load(),
	}
}

// recorder captura status e bytes escritos, porque o ServeContent escreve direto
// no ResponseWriter e não conta nada por conta própria.
type recorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (rec *recorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.status = status
	rec.wroteHeader = true
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(p []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	n, err := rec.ResponseWriter.Write(p)
	rec.written += int64(n)
	return n, err
}

func (rec *recorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
