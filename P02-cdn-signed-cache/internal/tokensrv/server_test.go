package tokensrv

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/metrics"
	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/signer"
)

const (
	secretA = "chave-de-teste-aaaaaaaaaaaaaaaaaaaa"
	secretB = "chave-de-teste-bbbbbbbbbbbbbbbbbbbb"
)

func newServer(t *testing.T, logTo *bytes.Buffer) *Server {
	t.Helper()
	keys, err := signer.NewKeyset("k2", map[string]string{"k1": secretA, "k2": secretB})
	if err != nil {
		t.Fatalf("NewKeyset: %v", err)
	}
	var logger *slog.Logger
	if logTo != nil {
		logger = slog.New(slog.NewJSONHandler(logTo, &slog.HandlerOptions{Level: slog.LevelDebug}))
	} else {
		logger = slog.New(slog.NewJSONHandler(new(bytes.Buffer), nil))
	}
	return New(keys, metrics.NewToken(), logger, Options{DefaultTTL: 60 * time.Second, MaxTTL: 5 * time.Minute})
}

// sign pede um token pelo endpoint HTTP, como as ferramentas de experimento fazem.
func sign(t *testing.T, srv *Server, path, method, ttl string) SignedToken {
	t.Helper()
	q := url.Values{"path": {path}, "method": {method}}
	if ttl != "" {
		q.Set("ttl", ttl)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sign?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/sign devolveu %d: %s", rec.Code, rec.Body.String())
	}
	var token SignedToken
	if err := json.Unmarshal(rec.Body.Bytes(), &token); err != nil {
		t.Fatalf("resposta de /sign não é JSON: %v", err)
	}
	return token
}

// authRequest simula a subrequisição que o Nginx faz.
func authRequest(srv *Server, method, uri, edgePath string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/auth", nil)
	r.Header.Set(HeaderMethod, method)
	r.Header.Set(HeaderURI, uri)
	if edgePath != "" {
		r.Header.Set(HeaderCDNPath, edgePath)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestAuthAutorizaTokenValido(t *testing.T) {
	srv := newServer(t, nil)
	token := sign(t, srv, "/objects/img-64KiB-a.bin", "GET", "")

	rec := authRequest(srv, "GET", token.URL, token.Path)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("código = %d, esperava 204", rec.Code)
	}
}

func TestAuthRecusaVetores(t *testing.T) {
	srv := newServer(t, nil)
	token := sign(t, srv, "/objects/img-64KiB-a.bin", "GET", "")

	cases := []struct {
		nome     string
		method   string
		uri      string
		edgePath string
	}{
		{"sem token", "GET", "/objects/img-64KiB-a.bin", "/objects/img-64KiB-a.bin"},
		{"assinatura alterada", "GET", strings.Replace(token.URL, "sig=", "sig=00", 1), token.Path},
		{"outro objeto com o mesmo token", "GET", "/objects/outro.bin?" + token.Query, "/objects/outro.bin"},
		{"outro método", "HEAD", token.URL, token.Path},
		{"headers ausentes", "", "", ""},
		{"uri sem barra inicial", "GET", "objects/a.bin?" + token.Query, ""},
	}

	for _, tc := range cases {
		t.Run(tc.nome, func(t *testing.T) {
			if rec := authRequest(srv, tc.method, tc.uri, tc.edgePath); rec.Code != http.StatusForbidden {
				t.Fatalf("código = %d, esperava 403", rec.Code)
			}
		})
	}
}

// Se o caminho que a CDN vai servir não é o mesmo que o validador autorizou,
// a decisão não vale. Este é o teste que fecha a porta do bypass por
// normalização divergente.
func TestAuthRecusaQuandoCDNEValidadorDiscordam(t *testing.T) {
	srv := newServer(t, nil)
	token := sign(t, srv, "/objects/img-64KiB-a.bin", "GET", "")

	rec := authRequest(srv, "GET", token.URL, "/objects/outro-objeto.bin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("código = %d, esperava 403", rec.Code)
	}
}

// O caminho é normalizado dos dois lados, então uma grafia com ".." que resolve
// para o mesmo objeto continua sendo o mesmo objeto, e é autorizada.
func TestAuthNormalizaCaminhoEquivalente(t *testing.T) {
	srv := newServer(t, nil)
	token := sign(t, srv, "/objects/img-64KiB-a.bin", "GET", "")

	uri := "/objects/sub/../img-64KiB-a.bin?" + token.Query
	if rec := authRequest(srv, "GET", uri, "/objects/img-64KiB-a.bin"); rec.Code != http.StatusNoContent {
		t.Fatalf("código = %d, esperava 204", rec.Code)
	}
}

func TestAuthRecusaTokenExpirado(t *testing.T) {
	srv := newServer(t, nil)
	token := sign(t, srv, "/objects/a.bin", "GET", "1s")

	// Avança o relógio do serviço em vez de dormir: teste que espera relógio de
	// parede fica lento e intermitente.
	srv.now = func() time.Time { return time.Now().Add(2 * time.Second) }

	if rec := authRequest(srv, "GET", token.URL, token.Path); rec.Code != http.StatusForbidden {
		t.Fatalf("código = %d, esperava 403", rec.Code)
	}
}

func TestSignRecusaTTLAcimaDoTeto(t *testing.T) {
	srv := newServer(t, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sign?path=/objects/a.bin&ttl=1h", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("código = %d, esperava 400", rec.Code)
	}
}

// Critério de conclusão do projeto: nenhum token aparece em log. O teste olha a saída
// real do logger em vez de confiar na leitura do código.
func TestLogsNaoVazamToken(t *testing.T) {
	var logs bytes.Buffer
	srv := newServer(t, &logs)
	token := sign(t, srv, "/objects/img-64KiB-a.bin", "GET", "")

	authRequest(srv, "GET", token.URL, token.Path)                           // autorizado
	authRequest(srv, "GET", token.URL, "/objects/outro.bin")                 // negado
	authRequest(srv, "GET", "/objects/a.bin?"+token.Query, "/objects/a.bin") // negado

	assinatura := strings.SplitN(strings.Split(token.Query, "sig=")[1], "&", 2)[0]
	if strings.Contains(logs.String(), assinatura) {
		t.Fatalf("a assinatura vazou para o log:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "sig=") {
		t.Fatalf("a query com o token vazou para o log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "autorização negada") {
		t.Fatal("as negativas deveriam ter sido registradas")
	}
}

func TestRotacaoDeChaveAceitaTokenAntigo(t *testing.T) {
	srv := newServer(t, nil)
	keys, err := signer.NewKeyset("k1", map[string]string{"k1": secretA, "k2": secretB})
	if err != nil {
		t.Fatal(err)
	}
	// Token emitido pela chave antiga, quando ela ainda era a ativa.
	values := keys.Sign("GET", "/objects/a.bin", time.Now().Add(time.Minute))
	uri := "/objects/a.bin?" + values.Encode()

	// O serviço atual assina com k2, mas continua aceitando k1.
	if rec := authRequest(srv, "GET", uri, "/objects/a.bin"); rec.Code != http.StatusNoContent {
		t.Fatalf("código = %d, esperava 204", rec.Code)
	}
}
