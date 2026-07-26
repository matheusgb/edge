// Package origin implementa o servidor de origem com os dois modos do lab.
//
// A pergunta do P01 é: por que carregar o objeto inteiro em memória e transmiti-lo
// em fluxo produzem comportamentos diferentes sob concorrência?
//
// Para que a comparação seja honesta, os dois modos precisam ser IGUAIS em tudo
// menos na variável estudada. Conseguimos isso entregando os dois pelo mesmo
// http.ServeContent, mudando só o que passamos como fonte de leitura:
//
//   - buffered: lemos o arquivo inteiro para um []byte e servimos um bytes.Reader;
//   - streamed: abrimos o arquivo e servimos o próprio *os.File.
//
// Como os dois são io.ReadSeeker, o ServeContent aplica exatamente a mesma
// semântica de Range, ETag e requisição condicional nos dois casos. A única
// diferença é de onde os bytes saem, e é justamente essa diferença que o lab mede.
package origin

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/matheusgb/edge/p01-hot-path-http/internal/catalog"
	"github.com/matheusgb/edge/p01-hot-path-http/internal/metrics"
)

// Mode é a estratégia de leitura do objeto.
type Mode string

const (
	// ModeBuffered lê o objeto inteiro para a memória antes de responder.
	ModeBuffered Mode = "buffered"
	// ModeStreamed transmite o objeto direto do arquivo, sem cópia completa.
	ModeStreamed Mode = "streamed"
)

// ParseMode valida uma string de modo.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeBuffered:
		return ModeBuffered, nil
	case ModeStreamed:
		return ModeStreamed, nil
	default:
		return "", fmt.Errorf("modo inválido %q: use %q ou %q", s, ModeBuffered, ModeStreamed)
	}
}

// Server atende os objetos do catálogo.
type Server struct {
	catalog     *catalog.Catalog
	metrics     *metrics.Metrics
	logger      *slog.Logger
	defaultMode Mode
}

// New monta o servidor.
func New(c *catalog.Catalog, m *metrics.Metrics, logger *slog.Logger, defaultMode Mode) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{catalog: c, metrics: m, logger: logger, defaultMode: defaultMode}
}

// Handler devolve o roteador público (sem métricas e sem pprof, que ficam na
// porta administrativa).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects/{name}", s.serveObject)
	mux.HandleFunc("HEAD /objects/{name}", s.serveObject)
	mux.HandleFunc("GET /catalog", s.serveCatalog)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// serveObject atende GET e HEAD de um objeto.
func (s *Server) serveObject(w http.ResponseWriter, r *http.Request) {
	mode := s.defaultMode
	// O modo pode ser trocado por requisição. Isso permite que a matriz de carga
	// compare os dois modos no MESMO processo, com o mesmo cache de página do
	// sistema operacional e o mesmo estado de heap, removendo diferenças de
	// ambiente que poderiam ser confundidas com diferença de estratégia.
	if raw := r.URL.Query().Get("mode"); raw != "" {
		parsed, err := ParseMode(raw)
		if err != nil {
			s.metrics.Errors.WithLabelValues(string(mode), "bad_mode").Inc()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mode = parsed
	}

	label := string(mode)
	s.metrics.InFlight.WithLabelValues(label).Inc()
	defer s.metrics.InFlight.WithLabelValues(label).Dec()

	started := time.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}

	obj, err := s.catalog.Get(r.PathValue("name"))
	if err != nil {
		reason := "read_error"
		status := http.StatusInternalServerError
		if errors.Is(err, catalog.ErrNotFound) {
			reason, status = "not_found", http.StatusNotFound
		}
		s.metrics.Errors.WithLabelValues(label, reason).Inc()
		http.Error(rec, http.StatusText(status), status)
		s.observe(label, r.Method, rec, started, r)
		return
	}

	// O ETag precisa estar no header ANTES do ServeContent: é ele que compara o
	// valor com If-None-Match e If-Range para decidir entre 200, 304 e 206.
	w.Header().Set("Etag", obj.ETag)
	w.Header().Set("X-Origin-Mode", label)

	if err := s.serveWithMode(rec, r, obj, mode); err != nil {
		s.metrics.Errors.WithLabelValues(label, "read_error").Inc()
		s.logger.Error("falha ao servir objeto",
			"object", obj.Name, "mode", label, "err", err)
		// Se nada foi escrito ainda, dá para responder 500 honestamente. Se o
		// corpo já começou, o status foi enviado e só resta cortar a conexão,
		// por isso o erro fica registrado na métrica, não no status.
		if !rec.wroteHeader {
			http.Error(rec, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	}

	s.observe(label, r.Method, rec, started, r)
}

// serveWithMode é o único ponto onde os dois modos divergem.
func (s *Server) serveWithMode(w http.ResponseWriter, r *http.Request, obj catalog.Object, mode Mode) error {
	info, err := os.Stat(obj.Path)
	if err != nil {
		return fmt.Errorf("stat de %s: %w", obj.Path, err)
	}

	switch mode {
	case ModeBuffered:
		// os.ReadFile aloca um []byte do tamanho INTEIRO do objeto, por requisição.
		// Com 200 requisições simultâneas de 16 MiB, são ~3,2 GiB de heap vivo ao
		// mesmo tempo. Esse é o custo que o lab quer tornar visível: o coletor de
		// lixo passa a trabalhar muito mais, e isso aparece na CPU e no p99.
		data, err := os.ReadFile(obj.Path)
		if err != nil {
			return fmt.Errorf("lendo %s: %w", obj.Path, err)
		}
		http.ServeContent(w, r, obj.Name, info.ModTime(), bytes.NewReader(data))
		return nil

	case ModeStreamed:
		// O arquivo aberto é lido em pedaços por io.Copy, com um buffer de 32 KiB
		// reaproveitado. A memória por requisição fica constante, independente do
		// tamanho do objeto. Em troca, há mais idas ao kernel (syscalls de read).
		f, err := os.Open(obj.Path)
		if err != nil {
			return fmt.Errorf("abrindo %s: %w", obj.Path, err)
		}
		defer f.Close()
		http.ServeContent(w, r, obj.Name, info.ModTime(), f)
		return nil

	default:
		return fmt.Errorf("modo não tratado: %q", mode)
	}
}

// serveCatalog lista os objetos disponíveis, para o gerador de carga descobrir
// o que existe sem precisar de configuração duplicada.
func (s *Server) serveCatalog(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, obj := range s.catalog.List() {
		fmt.Fprintf(w, "%s\t%d\t%s\n", obj.Name, obj.Size, obj.ETag)
	}
}

// observe registra as métricas de uma requisição concluída.
func (s *Server) observe(label, method string, rec *recorder, started time.Time, r *http.Request) {
	s.metrics.Duration.WithLabelValues(label).Observe(time.Since(started).Seconds())
	s.metrics.Requests.WithLabelValues(label, method, strconv.Itoa(rec.status)).Inc()
	s.metrics.ResponseBytes.WithLabelValues(label).Add(float64(rec.written))

	// Um contexto cancelado aqui significa que o cliente foi embora antes do fim.
	// Sem esta contagem, uma desistência em massa apareceria como "menos carga",
	// e não como o que é: carga oferecida que não virou carga concluída.
	if r.Context().Err() != nil {
		s.metrics.Cancellations.WithLabelValues(label).Inc()
	}
}

// recorder embrulha o ResponseWriter para capturar status e bytes escritos.
//
// O ServeContent escreve direto no ResponseWriter, então esta é a única forma de
// saber o que de fato saiu, inclusive numa resposta 206 de Range, em que o corpo
// é menor que o objeto.
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

// Flush repassa o flush. Sem isso, o embrulho esconderia o http.Flusher do
// ResponseWriter original e mudaria o comportamento de escrita, exatamente o
// tipo de diferença acidental que invalidaria a comparação entre os modos.
func (rec *recorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
