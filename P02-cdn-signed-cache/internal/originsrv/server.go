// Package originsrv implementa o servidor de origem do P02.
//
// A origem aqui é deliberadamente simples: ela não tem cache, não tem token e não
// sabe nada sobre usuários. O papel dela no lab é ser o recurso CARO que a CDN
// precisa proteger, e ser honesta o bastante para contar quantas chamadas de fato
// chegaram até ela.
//
// Duas decisões merecem atenção.
//
// A primeira é o segredo compartilhado. Numa CDN real a origem fica numa rede
// privada, alcançável só pela CDN. Aqui as duas rodam na mesma rede do Compose,
// então exigimos um header que só a CDN envia. Não é autenticação forte, é
// defesa em profundidade: se alguém publicar a porta da origem por engano, o
// tráfego direto ainda é recusado e o contador de falhas acende.
//
// A segunda são os controles de degradação (latência artificial e modo de falha).
// Eles existem porque os experimentos precisam de uma origem lenta ou fora do ar
// sob demanda, e reiniciar container no meio de uma medição introduz variáveis
// que não têm nada a ver com a pergunta.
package originsrv

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/metrics"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/objects"
)

// HeaderCDNAuth é o segredo que a CDN envia e a origem exige nas rotas
// protegidas.
//
// O valor está na forma canônica do Go (primeira letra de cada parte em
// maiúscula), que é a chave usada no mapa de headers do net/http. Escrever
// "X-CDN-Auth" aqui compilaria e funcionaria no Get, mas quebraria a comparação
// por chave do mapa em handleEcho, e o segredo passaria a ser ecoado.
const HeaderCDNAuth = "X-Cdn-Auth"

// Counters é o resumo que os experimentos leem para saber quanto tráfego passou
// pela CDN sem tocar a origem.
//
// Existe um /metrics em formato Prometheus ao lado deste endpoint. Este aqui é
// conveniência de laboratório: uma ferramenta de experimento que só quer dois
// números não deveria precisar de um parser de texto de exposição.
type Counters struct {
	Requests      int64 `json:"requests"`
	Bodies        int64 `json:"bodies"`
	Revalidations int64 `json:"revalidations"`
	AuthFailures  int64 `json:"auth_failures"`
	Bytes         int64 `json:"bytes"`
}

// Server atende os objetos da origem.
type Server struct {
	metrics *metrics.Origin
	logger  *slog.Logger
	secret  string

	// Estado controlado pela porta administrativa. São atômicos porque o handler
	// os lê em várias goroutines enquanto o operador do experimento os escreve.
	maxAgeSeconds atomic.Int64
	latencyNanos  atomic.Int64
	failStatus    atomic.Int64
	failUntil     atomic.Int64 // unix nano; 0 = sem prazo

	requests atomic.Int64
	bodies   atomic.Int64
	revals   atomic.Int64
	authBad  atomic.Int64
	bytesOut atomic.Int64
}

// New monta a origem.
func New(m *metrics.Origin, logger *slog.Logger, secret string, maxAge time.Duration, latency time.Duration) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{metrics: m, logger: logger, secret: secret}
	s.maxAgeSeconds.Store(int64(maxAge.Seconds()))
	s.latencyNanos.Store(int64(latency))
	return s
}

// Handler devolve o roteador público.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Duas famílias de rota, com a mesma implementação e políticas diferentes:
	// /public/ é conteúdo aberto, /objects/ é conteúdo que só a CDN autenticada
	// pode buscar. Ter as duas lado a lado deixa visível o que o token muda.
	mux.HandleFunc("GET /public/{name}", s.handleObject("public", false))
	mux.HandleFunc("HEAD /public/{name}", s.handleObject("public", false))
	mux.HandleFunc("GET /objects/{name}", s.handleObject("protected", true))
	mux.HandleFunc("HEAD /objects/{name}", s.handleObject("protected", true))
	mux.HandleFunc("GET /debug/echo", s.handleEcho)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// handleObject atende um objeto do catálogo sintético.
func (s *Server) handleObject(route string, protected bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		s.requests.Add(1)

		defer func() {
			s.metrics.Duration.WithLabelValues(route).Observe(time.Since(started).Seconds())
			s.metrics.Requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
			switch rec.status {
			case http.StatusOK:
				// Bytes contam só corpo de objeto entregue. Somar o corpo de uma
				// mensagem de erro aqui faria "bytes servidos pela origem", que é
				// a medida do alívio do cache, subir por causa de recusas.
				s.metrics.Bytes.WithLabelValues(route).Add(float64(rec.written))
				s.bytesOut.Add(rec.written)
				s.metrics.Bodies.WithLabelValues(route).Inc()
				s.bodies.Add(1)
			case http.StatusNotModified:
				s.revals.Add(1)
			}
		}()

		if protected && !s.authorized(r) {
			s.authBad.Add(1)
			s.metrics.AuthFailures.WithLabelValues(route).Inc()
			// A origem NÃO diz o que faltou. Uma mensagem detalhada aqui ensinaria
			// quem está sondando qual header inventar na próxima tentativa.
			http.Error(rec, "forbidden", http.StatusForbidden)
			return
		}

		if !s.degrade(rec, r) {
			return
		}

		name := r.PathValue("name")
		content, err := objects.Content(name)
		if err != nil {
			http.Error(rec, "not found", http.StatusNotFound)
			return
		}

		// Cache-Control é o que a CDN obedece para decidir por quanto tempo pode
		// reutilizar a resposta. Quem manda no TTL é a origem, e isso é proposital:
		// só ela sabe se o objeto pode ser compartilhado e por quanto tempo.
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", s.maxAgeSeconds.Load()))
		w.Header().Set("Etag", objects.ETag(name))
		w.Header().Set("Content-Type", "application/octet-stream")
		// Marca de tempo da origem: é o que permite provar, no experimento de
		// stale, que a resposta antiga veio do cache e não de uma origem viva.
		w.Header().Set("X-Origin-Generated-At", time.Now().UTC().Format(time.RFC3339Nano))

		// modTime fixo. Um horário variável mudaria o Last-Modified a cada
		// requisição e faria toda revalidação condicional falhar sem motivo.
		http.ServeContent(rec, r, name, time.Unix(0, 0).UTC(), bytes.NewReader(content))
	}
}

// handleEcho devolve os headers que a origem recebeu.
//
// É o instrumento do experimento de poisoning: em vez de acreditar que a CDN
// removeu um header, a evidência mostra o que a origem viu.
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.authBad.Add(1)
		s.metrics.AuthFailures.WithLabelValues("echo").Inc()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	received := map[string]string{"__host": r.Host, "__uri": r.URL.RequestURI()}
	for name, values := range r.Header {
		// O segredo compartilhado é o único header que a origem se recusa a
		// ecoar: um endpoint de diagnóstico não pode virar um vazamento.
		if http.CanonicalHeaderKey(name) == HeaderCDNAuth {
			received[name] = "(omitido)"
			continue
		}
		received[name] = values[0]
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(received)
}

// authorized confere o segredo compartilhado em tempo constante.
func (s *Server) authorized(r *http.Request) bool {
	if s.secret == "" {
		return true // sem segredo configurado, o lab roda em modo aberto
	}
	got := r.Header.Get(HeaderCDNAuth)
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.secret)) == 1
}

// degrade aplica latência artificial e modo de falha. Devolve false quando já
// respondeu e o handler deve parar.
func (s *Server) degrade(w http.ResponseWriter, r *http.Request) bool {
	if d := time.Duration(s.latencyNanos.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			// O cliente desistiu durante a espera. Responder agora seria escrever
			// numa conexão que já foi embora.
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
//
// Ele fica numa porta separada, como no P01, e pelo mesmo motivo: métricas,
// perfis e controles de experimento não podem estar na porta que atende tráfego.
func (s *Server) AdminHandler(metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metricsHandler)

	mux.HandleFunc("GET /admin/counters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Counters())
	})

	mux.HandleFunc("PUT /admin/latency", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil {
			http.Error(w, "parâmetro d inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.latencyNanos.Store(int64(d))
		s.logger.Info("latência artificial alterada", "latency", d.String())
		fmt.Fprintf(w, "latency=%s\n", d)
	})

	// O TTL é ajustável em tempo de execução por causa do experimento de stale:
	// para observar uma entrada VENCIDA sendo servida, o objeto precisa ser
	// guardado com um prazo curto, e esperar o TTL de produção passar tornaria o
	// experimento lento sem ensinar nada a mais.
	mux.HandleFunc("PUT /admin/max-age", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil || d < 0 {
			http.Error(w, "parâmetro d inválido", http.StatusBadRequest)
			return
		}
		s.maxAgeSeconds.Store(int64(d.Seconds()))
		s.logger.Info("max-age alterado", "max_age", d.String())
		fmt.Fprintf(w, "max-age=%s\n", d)
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

	return mux
}

// Counters devolve a foto atual dos contadores.
func (s *Server) Counters() Counters {
	return Counters{
		Requests:      s.requests.Load(),
		Bodies:        s.bodies.Load(),
		Revalidations: s.revals.Load(),
		AuthFailures:  s.authBad.Load(),
		Bytes:         s.bytesOut.Load(),
	}
}

// recorder captura status e bytes escritos, como no P01: o ServeContent escreve
// direto no ResponseWriter e não conta nada por conta própria.
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
