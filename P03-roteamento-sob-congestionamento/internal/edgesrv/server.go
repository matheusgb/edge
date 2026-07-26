// Package edgesrv implementa um servidor de borda do P03.
//
// Cada edge guarda um cache pequeno e INDEPENDENTE dos outros. Isso é
// deliberado: caches independentes são o que existe numa CDN real, onde cada
// ponto de presença tem o seu, e é o que torna a decisão do roteador visível.
// Com um cache compartilhado, mandar a requisição para o edge A ou para o B daria
// quase no mesmo, e o experimento perderia o efeito que quer medir, que é o preço
// de deslocar tráfego para um destino que não tem aquele objeto.
//
// O edge também respeita o prazo que o roteador propaga. Um edge que ignora o
// prazo continua buscando na origem uma resposta que já não tem para quem ir, e
// esse trabalho perdido é o que mantém um sistema saturado saturado.
package edgesrv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/metrics"
)

// Headers do contrato entre roteador e edge.
const (
	HeaderCorrelation = "X-Correlation-Id"
	HeaderDeadline    = "X-Deadline-Ms"
	HeaderCache       = "X-Cache"
	HeaderEdge        = "X-Edge"
)

// entry é um objeto guardado no cache.
type entry struct {
	body    []byte
	etag    string
	guarded time.Time
	ttl     time.Duration
}

func (e entry) fresh(agora time.Time) bool { return agora.Sub(e.guarded) < e.ttl }

// Server é um edge.
type Server struct {
	nome    string
	origem  string
	cliente *http.Client
	cache   *lru.Cache[string, entry]
	grupo   singleflight.Group
	ttl     time.Duration
	metrics *metrics.Edge
	logger  *slog.Logger

	hits      atomic.Int64
	misses    atomic.Int64
	origin    atomic.Int64
	coalesced atomic.Int64
	erros     atomic.Int64
}

// New monta um edge com cache de no máximo `capacidade` objetos.
//
// A capacidade é pequena de propósito. Um cache que cabe tudo nunca erra, e um
// cache que nunca erra esconde o custo de deslocar carga entre destinos, que é
// exatamente o que este projeto quer enxergar.
func New(nome, origem string, capacidade int, ttl time.Duration, cliente *http.Client, m *metrics.Edge, logger *slog.Logger) (*Server, error) {
	c, err := lru.New[string, entry](capacidade)
	if err != nil {
		return nil, fmt.Errorf("criando cache: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cliente == nil {
		cliente = http.DefaultClient
	}
	return &Server{nome: nome, origem: origem, cliente: cliente, cache: c, ttl: ttl, metrics: m, logger: logger}, nil
}

// Handler devolve o roteador público do edge.
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
	inicio := time.Now()
	nome := r.PathValue("name")

	ctx := s.comPrazo(r)
	w.Header().Set(HeaderEdge, s.nome)

	if e, ok := s.cache.Get(nome); ok && e.fresh(time.Now()) {
		s.hits.Add(1)
		s.responder(w, r, e, "HIT", inicio)
		return
	}
	s.misses.Add(1)

	// singleflight resolve, dentro do processo, o mesmo problema que o
	// proxy_cache_lock resolvia no P02: cem clientes pedindo o mesmo objeto
	// ausente viram UMA busca na origem, e os outros noventa e nove esperam o
	// resultado dela. Sem isso, todo objeto popular que expira vira uma rajada na
	// origem, e o cenário de recuperação deste projeto seria uma avalanche
	// causada pelo próprio lab.
	valor, err, compartilhado := s.grupo.Do(nome, func() (any, error) {
		return s.buscarNaOrigem(ctx, nome, r.Header.Get(HeaderCorrelation))
	})
	if compartilhado {
		s.coalesced.Add(1)
		s.metrics.Coalesced.Inc()
	}
	if err != nil {
		s.erros.Add(1)
		status := http.StatusBadGateway
		if ctx.Err() != nil {
			// O prazo venceu enquanto se buscava. 504 é a resposta honesta: o
			// problema foi tempo, não conteúdo.
			status = http.StatusGatewayTimeout
		}
		s.metrics.Requests.WithLabelValues("MISS", strconv.Itoa(status)).Inc()
		s.metrics.Duration.WithLabelValues("MISS").Observe(time.Since(inicio).Seconds())
		s.logger.Warn("busca na origem falhou",
			"correlation_id", r.Header.Get(HeaderCorrelation),
			"object", nome, "err", err, "status", status)
		http.Error(w, "origem indisponível", status)
		return
	}

	e := valor.(entry)
	s.cache.Add(nome, e)
	s.metrics.Entries.Set(float64(s.cache.Len()))
	s.responder(w, r, e, "MISS", inicio)
}

// comPrazo aplica o prazo que o roteador propagou.
func (s *Server) comPrazo(r *http.Request) context.Context {
	ctx := r.Context()
	raw := r.Header.Get(HeaderDeadline)
	if raw == "" {
		return ctx
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return ctx
	}
	// O contexto vira filho do contexto da requisição, então cliente desistindo
	// e prazo vencendo cancelam pelo mesmo caminho.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	// O cancel roda quando o handler termina, via AfterFunc no contexto do
	// request. Guardar o cancel numa variável e esquecer de chamá-lo é vazamento
	// clássico; aqui o próprio contexto pai cuida disso.
	context.AfterFunc(r.Context(), cancel)
	return ctx
}

// buscarNaOrigem faz a busca que o cache não conseguiu evitar.
func (s *Server) buscarNaOrigem(ctx context.Context, nome, correlacao string) (entry, error) {
	comecou := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.origem+"/objects/"+nome, nil)
	if err != nil {
		return entry{}, err
	}
	req.Header.Set(HeaderCorrelation, correlacao)

	resp, err := s.cliente.Do(req)
	if err != nil {
		s.metrics.OriginRequests.WithLabelValues("erro").Inc()
		return entry{}, err
	}
	defer resp.Body.Close()
	s.origin.Add(1)
	s.metrics.OriginRequests.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
	s.metrics.OriginDuration.Observe(time.Since(comecou).Seconds())

	if resp.StatusCode != http.StatusOK {
		return entry{}, fmt.Errorf("origem respondeu %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return entry{}, err
	}
	ttl := s.ttl
	// A origem manda no TTL, como no P02. O edge só usa o próprio valor quando a
	// origem não disser nada.
	if v := resp.Header.Get("Cache-Control"); v != "" {
		if d, ok := maxAge(v); ok {
			ttl = d
		}
	}
	return entry{body: body, etag: resp.Header.Get("Etag"), guarded: time.Now(), ttl: ttl}, nil
}

// responder escreve o objeto para o cliente.
func (s *Server) responder(w http.ResponseWriter, r *http.Request, e entry, cache string, inicio time.Time) {
	w.Header().Set(HeaderCache, cache)
	w.Header().Set("Content-Type", "application/octet-stream")
	if e.etag != "" {
		w.Header().Set("Etag", e.etag)
	}

	// Revalidação condicional: quando o cliente já tem a versão, 304 evita
	// mandar o corpo de novo. Num cenário de banda limitada, é a diferença entre
	// caber e não caber no link.
	if r.Header.Get("If-None-Match") == e.etag && e.etag != "" {
		w.WriteHeader(http.StatusNotModified)
		s.metrics.Requests.WithLabelValues(cache, "304").Inc()
		s.metrics.Duration.WithLabelValues(cache).Observe(time.Since(inicio).Seconds())
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(e.body)))
	w.WriteHeader(http.StatusOK)
	n := 0
	if r.Method != http.MethodHead {
		escrito, _ := w.Write(e.body)
		n = escrito
	}
	s.metrics.Requests.WithLabelValues(cache, "200").Inc()
	s.metrics.Duration.WithLabelValues(cache).Observe(time.Since(inicio).Seconds())
	s.metrics.Bytes.WithLabelValues(cache).Add(float64(n))
}

// Counters é o resumo que os experimentos leem.
type Counters struct {
	Edge      string `json:"edge"`
	Hits      int64  `json:"hits"`
	Misses    int64  `json:"misses"`
	Origin    int64  `json:"origin_fetches"`
	Coalesced int64  `json:"coalesced"`
	Errors    int64  `json:"errors"`
	Entries   int    `json:"entries"`
}

// Counters devolve a foto atual.
func (s *Server) Counters() Counters {
	return Counters{
		Edge:      s.nome,
		Hits:      s.hits.Load(),
		Misses:    s.misses.Load(),
		Origin:    s.origin.Load(),
		Coalesced: s.coalesced.Load(),
		Errors:    s.erros.Load(),
		Entries:   s.cache.Len(),
	}
}

// AdminHandler devolve o roteador administrativo do edge.
func (s *Server) AdminHandler(metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metricsHandler)
	mux.HandleFunc("GET /admin/counters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Counters())
	})
	// Esvaziar o cache é o que permite começar um cenário do zero sem reiniciar
	// container: reinício mudaria também o pool de conexões e o aquecimento do
	// processo, e a medição seguinte carregaria essas diferenças junto.
	mux.HandleFunc("POST /admin/purge", func(w http.ResponseWriter, _ *http.Request) {
		s.cache.Purge()
		s.metrics.Entries.Set(0)
		s.logger.Info("cache esvaziado")
		fmt.Fprintln(w, "cache esvaziado")
	})
	return mux
}

// maxAge lê o max-age de um Cache-Control.
func maxAge(valor string) (time.Duration, bool) {
	for _, parte := range strings.Split(valor, ",") {
		segundos, ok := strings.CutPrefix(strings.TrimSpace(parte), "max-age=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(segundos)
		if err != nil || n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	return 0, false
}
