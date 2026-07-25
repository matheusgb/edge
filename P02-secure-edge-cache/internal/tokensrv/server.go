// Package tokensrv expõe a emissão e a validação de URLs assinadas por HTTP.
//
// São dois endpoints com públicos completamente diferentes:
//
//   - /sign é a "aplicação": alguém que já sabe quem é o usuário pede um link
//     temporário para um objeto. Num sistema real, este endpoint fica atrás de
//     login. Aqui ele é aberto, e isso está escrito no README como limitação.
//   - /auth é a borda: o Nginx faz uma subrequisição a cada requisição do cliente
//     e usa o código de status como resposta binária, autorizado ou não.
//
// O detalhe que dá segurança ao conjunto é o /auth rodar SEMPRE, inclusive quando
// a resposta vai sair do cache. A autorização é por requisição; o cache é do
// objeto. Se o cache pudesse dispensar a autorização, o primeiro usuário
// autorizado abriria o objeto para todo mundo.
package tokensrv

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/metrics"
	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/signer"
)

// Headers que a borda envia na subrequisição de autenticação.
const (
	// HeaderMethod é o método da requisição ORIGINAL. A subrequisição do
	// auth_request é sempre um GET, então o Nginx precisa carregar o método
	// verdadeiro num header próprio. Sem isso, um token de GET autorizaria
	// qualquer método que a borda deixasse passar.
	HeaderMethod = "X-Original-Method"
	// HeaderURI é a URI original completa, com a query onde o token viaja.
	HeaderURI = "X-Original-URI"
	// HeaderEdgePath é o caminho JÁ NORMALIZADO pelo Nginx ($uri). Ele existe
	// para o validador conferir se ele e a borda entendem o mesmo caminho.
	HeaderEdgePath = "X-Edge-Path"
)

// Server valida e emite tokens.
type Server struct {
	keys       *signer.Keyset
	metrics    *metrics.Token
	logger     *slog.Logger
	defaultTTL time.Duration
	maxTTL     time.Duration
	now        func() time.Time
}

// Options configura o serviço.
type Options struct {
	DefaultTTL time.Duration
	MaxTTL     time.Duration
	Now        func() time.Time // injetável para teste; nil usa time.Now
}

// New monta o serviço.
func New(keys *signer.Keyset, m *metrics.Token, logger *slog.Logger, opts Options) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DefaultTTL <= 0 {
		opts.DefaultTTL = 60 * time.Second
	}
	if opts.MaxTTL <= 0 {
		opts.MaxTTL = 5 * time.Minute
	}
	return &Server{
		keys: keys, metrics: m, logger: logger,
		defaultTTL: opts.DefaultTTL, maxTTL: opts.MaxTTL, now: opts.Now,
	}
}

// Handler devolve o roteador do serviço.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", s.handleAuth)
	mux.HandleFunc("GET /sign", s.handleSign)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// SignedToken é a resposta de /sign.
type SignedToken struct {
	Path      string    `json:"path"`
	Query     string    `json:"query"`
	URL       string    `json:"url"`
	KeyID     string    `json:"kid"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleSign emite um link temporário.
func (s *Server) handleSign(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target := q.Get("path")
	if target == "" || !strings.HasPrefix(target, "/") {
		http.Error(w, "parâmetro path é obrigatório e precisa começar com /", http.StatusBadRequest)
		return
	}
	method := strings.ToUpper(q.Get("method"))
	if method == "" {
		method = http.MethodGet
	}

	ttl := s.defaultTTL
	if raw := q.Get("ttl"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			http.Error(w, "parâmetro ttl inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		ttl = parsed
	}
	// O teto do TTL é uma decisão de segurança, não de conveniência. Uma URL
	// assinada é reutilizável enquanto vale: quem receber o link repassa o link.
	// Prazo curto é o que limita o estrago de um vazamento.
	if ttl <= 0 || ttl > s.maxTTL {
		http.Error(w, fmt.Sprintf("ttl precisa estar entre 1s e %s", s.maxTTL), http.StatusBadRequest)
		return
	}

	// O caminho é normalizado ANTES de assinar. Assinar a forma crua deixaria o
	// token válido só para aquela grafia exata, e a borda entrega o caminho
	// normalizado: as duas pontas precisam falar da mesma coisa.
	normalized, err := normalize(target)
	if err != nil {
		http.Error(w, "path inválido: "+err.Error(), http.StatusBadRequest)
		return
	}

	expires := s.now().Add(ttl)
	values := s.keys.Sign(method, normalized, expires)
	s.metrics.Signed.WithLabelValues(s.keys.ActiveKeyID()).Inc()

	// O log registra o caminho e a chave, nunca a assinatura. Um log com o token
	// inteiro transforma o arquivo de log num arquivo de credenciais válidas.
	s.logger.Info("token emitido",
		"path", normalized, "method", method, "kid", s.keys.ActiveKeyID(), "ttl", ttl.String())

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(SignedToken{
		Path:      normalized,
		Query:     values.Encode(),
		URL:       normalized + "?" + values.Encode(),
		KeyID:     values.Get(signer.ParamKeyID),
		ExpiresAt: expires,
	})
}

// handleAuth é o endpoint consultado pelo auth_request do Nginx.
//
// A resposta é o código de status: 204 autoriza, 403 recusa. O corpo é ignorado
// pelo Nginx, então não escrevemos nada além do necessário.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	started := s.now()
	defer func() { s.metrics.AuthDuration.Observe(time.Since(started).Seconds()) }()

	method := r.Header.Get(HeaderMethod)
	rawURI := r.Header.Get(HeaderURI)
	edgePath := r.Header.Get(HeaderEdgePath)

	if method == "" || rawURI == "" {
		s.deny(w, "", "missing_headers", "")
		return
	}

	parsed, err := url.ParseRequestURI(rawURI)
	if err != nil {
		s.deny(w, "", "bad_uri", "")
		return
	}

	normalized, err := normalize(parsed.Path)
	if err != nil {
		s.deny(w, "", "bad_path", "")
		return
	}

	// A checagem mais importante deste arquivo.
	//
	// O Nginx decide o que servir e o que guardar em cache usando $uri, o caminho
	// que ELE normalizou. O validador decide se autoriza usando o caminho que ELE
	// normalizou. Se as duas normalizações divergirem, existe uma requisição que
	// é autorizada como um caminho e servida como outro, que é exatamente a forma
	// clássica de burlar autorização em proxy.
	//
	// Não tentamos adivinhar todas as diferenças entre as regras de normalização
	// do Nginx e as do Go. Exigimos que as duas cheguem ao mesmo resultado e
	// recusamos quando discordam. Perder uma requisição legítima esquisita é
	// muito mais barato que autorizar a errada.
	if edgePath != "" && edgePath != normalized {
		s.deny(w, normalized, "path_mismatch", "")
		return
	}

	kid, err := s.keys.Verify(method, normalized, parsed.Query(), s.now())
	if err != nil {
		s.deny(w, normalized, signer.Reason(err), kid)
		return
	}

	s.metrics.Auth.WithLabelValues("allow", "ok").Inc()
	// Autorização concedida não vira log. Em volume de borda, um log por
	// requisição autorizada custa mais I/O que o próprio conteúdo, e o Nginx já
	// registra o acesso. Log aqui é para o que foge do normal.
	w.WriteHeader(http.StatusNoContent)
}

// deny recusa a requisição e registra o motivo.
func (s *Server) deny(w http.ResponseWriter, path, reason, kid string) {
	s.metrics.Auth.WithLabelValues("deny", reason).Inc()
	// Nunca logamos a URI original: é lá que o token viaja. Logamos o caminho já
	// normalizado, sem query, mais o motivo e a chave alegada.
	s.logger.Warn("autorização negada", "path", path, "reason", reason, "kid", kid)
	// A resposta também não explica o motivo. Dizer "expirado" em vez de
	// "assinatura inválida" entrega ao atacante a informação de que ele acertou
	// a assinatura, o que é justamente o que ele quer descobrir.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
}

// normalize decodifica e limpa um caminho.
//
// path.Clean resolve "." e "..", e colapsa barras repetidas. O resultado precisa
// continuar absoluto: um caminho que sai da raiz depois de limpo é tentativa de
// traversal, não engano de digitação.
func normalize(p string) (string, error) {
	if p == "" || !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("caminho precisa ser absoluto")
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "/..") {
		return "", fmt.Errorf("caminho escapa da raiz")
	}
	// path.Clean remove a barra final, que para um objeto é irrelevante. Se
	// alguém pedir um "diretório", a origem devolve 404 de qualquer forma.
	return cleaned, nil
}
