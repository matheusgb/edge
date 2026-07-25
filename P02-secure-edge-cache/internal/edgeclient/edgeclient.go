// Package edgeclient é o cliente que os experimentos usam para falar com o lab.
//
// Ele existe para que nenhuma ferramenta de experimento precise conhecer o
// segredo de assinatura. Elas pedem um link temporário ao serviço de token, do
// mesmo jeito que uma aplicação real pediria, e usam o link. Se um experimento
// assinasse por conta própria, ele estaria testando a própria cópia da regra, e
// não o serviço que a borda consulta.
package edgeclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/originsrv"
	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/tokensrv"
)

// Client fala com o serviço de token e com a borda.
type Client struct {
	TokendURL string
	EdgeURL   string
	HTTP      *http.Client
}

// New monta o cliente com um http.Client próprio.
//
// O padrão da biblioteca mantém só duas conexões ociosas por host, e um
// experimento com centenas de requisições simultâneas passaria o tempo abrindo
// e fechando conexão, medindo handshake em vez de cache.
func New(edgeURL, tokendURL string, concurrency int) *Client {
	if concurrency < 8 {
		concurrency = 8
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = concurrency * 2
	transport.MaxIdleConnsPerHost = concurrency * 2
	transport.MaxConnsPerHost = concurrency * 2
	transport.DisableCompression = true

	return &Client{
		TokendURL: strings.TrimSuffix(tokendURL, "/"),
		EdgeURL:   strings.TrimSuffix(edgeURL, "/"),
		HTTP:      &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// Sign pede um link assinado para um caminho.
func (c *Client) Sign(ctx context.Context, method, path string, ttl time.Duration) (tokensrv.SignedToken, error) {
	q := url.Values{
		"path":   {path},
		"method": {method},
		"ttl":    {ttl.String()},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.TokendURL+"/sign?"+q.Encode(), nil)
	if err != nil {
		return tokensrv.SignedToken{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return tokensrv.SignedToken{}, fmt.Errorf("pedindo token para %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tokensrv.SignedToken{}, fmt.Errorf("token para %s: %s", path, resp.Status)
	}
	var token tokensrv.SignedToken
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return tokensrv.SignedToken{}, fmt.Errorf("resposta de token ilegível: %w", err)
	}
	return token, nil
}

// FetchCounters lê os contadores da porta administrativa da origem.
//
// É esse número que responde à pergunta do projeto: de todas as requisições que
// o cliente fez à borda, quantas realmente custaram trabalho à origem?
func FetchCounters(ctx context.Context, adminURL string) (originsrv.Counters, error) {
	var counters originsrv.Counters
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(adminURL, "/")+"/admin/counters", nil)
	if err != nil {
		return counters, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return counters, fmt.Errorf("lendo contadores da origem: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return counters, fmt.Errorf("contadores da origem: %s", resp.Status)
	}
	return counters, json.NewDecoder(resp.Body).Decode(&counters)
}

// SetOriginLatency e SetOriginFailure controlam a degradação da origem durante
// um experimento. Ficam aqui para que os comandos não repitam o mesmo PUT.
func SetOriginLatency(ctx context.Context, adminURL string, d time.Duration) error {
	return adminPut(ctx, fmt.Sprintf("%s/admin/latency?d=%s", strings.TrimSuffix(adminURL, "/"), d))
}

// SetOriginMaxAge muda o TTL anunciado pela origem nas próximas respostas.
func SetOriginMaxAge(ctx context.Context, adminURL string, d time.Duration) error {
	return adminPut(ctx, fmt.Sprintf("%s/admin/max-age?d=%s", strings.TrimSuffix(adminURL, "/"), d))
}

// SetOriginFailure liga ou desliga o modo de falha. status 0 desliga.
func SetOriginFailure(ctx context.Context, adminURL string, status int, window time.Duration) error {
	url := fmt.Sprintf("%s/admin/fail?status=%d", strings.TrimSuffix(adminURL, "/"), status)
	if window > 0 {
		url += "&for=" + window.String()
	}
	return adminPut(ctx, url)
}

func adminPut(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s devolveu %s", url, resp.Status)
	}
	return nil
}

// SignedURL devolve a URL completa, já apontando para a borda.
func (c *Client) SignedURL(ctx context.Context, method, path string, ttl time.Duration) (string, error) {
	token, err := c.Sign(ctx, method, path, ttl)
	if err != nil {
		return "", err
	}
	return c.EdgeURL + token.URL, nil
}
