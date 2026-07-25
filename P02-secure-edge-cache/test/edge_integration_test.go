//go:build integration

// Teste de integração do caminho completo: cliente, borda, validador e origem.
//
// Os testes unitários provam que cada peça faz a sua parte. Eles não conseguem
// provar o que este arquivo prova, porque as propriedades interessantes do
// projeto moram entre as peças: a chave de cache é do Nginx, a decisão de
// autorização é do serviço Go, e o valor está em elas concordarem.
//
// A orquestração é o próprio docker compose, chamado por os/exec. Ele já é a
// ferramenta que define o ambiente do projeto; embrulhar isso num SDK traria o
// cliente Docker inteiro para dentro do go.mod só para executar dois comandos.
//
//	go test -tags=integration ./test/... -v
//
// Para iterar rápido com o ambiente já de pé, use EDGE_LAB_SKIP_COMPOSE=1.
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	edgeURL   = "http://127.0.0.1:8080"
	tokendURL = "http://127.0.0.1:8082"
	originAPI = "http://127.0.0.1:8090"
)

// TestMain sobe o ambiente uma vez para todos os testes do pacote.
func TestMain(m *testing.M) {
	if os.Getenv("EDGE_LAB_SKIP_COMPOSE") == "" {
		if err := compose("up", "-d", "--build", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, "não consegui subir o ambiente:", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	code := 1
	func() {
		// O ambiente é derrubado com os volumes: cache e log da borda são estado
		// de uma execução, e deixá-los para trás faria a próxima rodada começar
		// com um cache quente que ninguém pediu.
		defer func() {
			if os.Getenv("EDGE_LAB_SKIP_COMPOSE") == "" {
				_ = compose("down", "--volumes", "--remove-orphans")
			}
		}()
		if err := esperarSaude(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "ambiente não ficou saudável:", err)
			return
		}
		code = m.Run()
	}()
	os.Exit(code)
}

func compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = ".."
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func esperarSaude(ctx context.Context) error {
	alvos := []string{edgeURL + "/healthz", tokendURL + "/healthz", originAPI + "/admin/counters"}
	deadline := time.Now().Add(90 * time.Second)
	for _, alvo := range alvos {
		for {
			if time.Now().After(deadline) {
				return fmt.Errorf("%s não respondeu a tempo", alvo)
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, alvo, nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil
}

// assinar pede um link temporário, como uma aplicação faria.
func assinar(t *testing.T, path string) string {
	return assinarComTTL(t, path, "5m")
}

func assinarComTTL(t *testing.T, path, ttl string) string {
	t.Helper()
	resp, err := http.Get(tokendURL + "/sign?path=" + path + "&method=GET&ttl=" + ttl)
	if err != nil {
		t.Fatalf("pedindo token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/sign devolveu %s", resp.Status)
	}
	var token struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		t.Fatalf("resposta de /sign ilegível: %v", err)
	}
	return edgeURL + token.URL
}

func get(t *testing.T, url string) (int, string, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("X-Cache-Status"), body
}

func contadores(t *testing.T) map[string]int64 {
	t.Helper()
	resp, err := http.Get(originAPI + "/admin/counters")
	if err != nil {
		t.Fatalf("contadores: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]int64
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("contadores ilegíveis: %v", err)
	}
	return out
}

func objeto(sufixo string) string {
	return fmt.Sprintf("/objects/it-64KiB-%d-%s.bin", time.Now().UnixNano(), sufixo)
}

func TestCaminhoFelizGuardaNoCache(t *testing.T) {
	url := assinar(t, objeto("feliz"))

	status, cache, body := get(t, url)
	if status != http.StatusOK {
		t.Fatalf("primeira requisição: status %d", status)
	}
	if cache != "MISS" {
		t.Fatalf("primeira requisição: cache %q, esperava MISS", cache)
	}
	if len(body) != 64<<10 {
		t.Fatalf("corpo com %d bytes, esperava 65536", len(body))
	}

	status, cache, _ = get(t, url)
	if status != http.StatusOK || cache != "HIT" {
		t.Fatalf("segunda requisição: status %d cache %q, esperava 200 HIT", status, cache)
	}
}

// A propriedade central do projeto: o token não entra na chave de cache, então
// usuários diferentes, com tokens diferentes, compartilham a MESMA entrada.
func TestTokensDiferentesCompartilhamAEntrada(t *testing.T) {
	path := objeto("compartilhado")
	// TTLs diferentes garantem tokens diferentes. Dois pedidos com o mesmo TTL no
	// mesmo segundo produzem a MESMA assinatura, porque a assinatura é função de
	// método, caminho, expiração e chave, e nada mais. Isso é propriedade do
	// esquema, não coincidência: não há aleatoriedade a esconder num HMAC.
	primeiroUsuario := assinarComTTL(t, path, "5m")
	segundoUsuario := assinarComTTL(t, path, "4m")
	if primeiroUsuario == segundoUsuario {
		t.Fatal("os dois tokens saíram iguais; o teste não provaria nada")
	}

	antes := contadores(t)
	if status, cache, _ := get(t, primeiroUsuario); status != http.StatusOK || cache != "MISS" {
		t.Fatalf("primeiro usuário: status %d cache %q", status, cache)
	}
	if status, cache, _ := get(t, segundoUsuario); status != http.StatusOK || cache != "HIT" {
		t.Fatalf("segundo usuário: status %d cache %q, esperava HIT", status, cache)
	}
	depois := contadores(t)

	if delta := depois["requests"] - antes["requests"]; delta != 1 {
		t.Fatalf("a origem recebeu %d chamadas, esperava 1", delta)
	}
}

// Autorização vale por requisição, mesmo quando o conteúdo sai do cache. Se o
// auth_request rodasse só no MISS, o primeiro usuário autorizado abriria o
// objeto para o mundo inteiro.
func TestCacheQuenteNaoDispensaAutorizacao(t *testing.T) {
	path := objeto("quente")
	url := assinar(t, path)
	if status, _, _ := get(t, url); status != http.StatusOK {
		t.Fatalf("preparação: status %d", status)
	}

	status, _, _ := get(t, edgeURL+path) // agora sem token nenhum
	if status != http.StatusForbidden {
		t.Fatalf("status %d, esperava 403 mesmo com o objeto em cache", status)
	}
}

// Stampede: cem clientes pedem o mesmo objeto ausente ao mesmo tempo, e a origem
// precisa receber uma chamada só.
func TestStampedeChegaUmaVezNaOrigem(t *testing.T) {
	url := assinar(t, objeto("stampede"))

	// Latência artificial abre a janela: sem ela, o primeiro cliente termina
	// antes dos outros chegarem e não existe stampede para conter.
	putAdmin(t, originAPI+"/admin/latency?d=300ms")
	defer putAdmin(t, originAPI+"/admin/latency?d=0s")

	antes := contadores(t)

	const clientes = 100
	largada := make(chan struct{})
	var wg sync.WaitGroup
	codigos := make([]int, clientes)
	wg.Add(clientes)
	for i := range clientes {
		go func() {
			defer wg.Done()
			<-largada
			resp, err := http.Get(url)
			if err != nil {
				codigos[i] = -1
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			codigos[i] = resp.StatusCode
		}()
	}
	close(largada)
	wg.Wait()

	depois := contadores(t)
	chamadas := depois["requests"] - antes["requests"]
	if chamadas != 1 {
		t.Fatalf("a origem recebeu %d chamadas durante o stampede, esperava 1", chamadas)
	}
	for i, code := range codigos {
		if code != http.StatusOK {
			t.Fatalf("cliente %d recebeu %d", i, code)
		}
	}
}

func TestVetoresNegativosNoCaminhoCompleto(t *testing.T) {
	path := objeto("negativos")
	url := assinar(t, path)

	casos := []struct {
		nome     string
		url      string
		host     string
		esperado int
	}{
		{"sem token", edgeURL + path, "", http.StatusForbidden},
		{"assinatura alterada", strings.Replace(url, "sig=", "sig=ff", 1), "", http.StatusForbidden},
		{"token de outro objeto", edgeURL + objeto("outro") + "?" + queryDe(url), "", http.StatusForbidden},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("requisição: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.esperado {
				t.Fatalf("status %d, esperava %d", resp.StatusCode, tc.esperado)
			}
		})
	}
}

// O Host desconhecido é recusado antes de qualquer coisa, então o cliente vê a
// conexão fechada em vez de um status.
func TestHostDesconhecidoNaoEhAtendido(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, edgeURL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "atacante.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return // conexão encerrada: é o comportamento esperado do 444
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("status %d: um Host desconhecido não deveria ser atendido", resp.StatusCode)
	}
}

// O log da borda alimenta as métricas, e nenhuma linha pode conter token.
func TestMetricasDaBordaNaoAcusamVazamento(t *testing.T) {
	url := assinar(t, objeto("log"))
	get(t, url)
	time.Sleep(2 * time.Second)

	resp, err := http.Get("http://127.0.0.1:8093/metrics")
	if err != nil {
		t.Fatalf("métricas da borda: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, linha := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(linha, "edge_log_token_leaks_total") && !strings.HasSuffix(strings.TrimSpace(linha), " 0") {
			t.Fatalf("o log da borda registrou token: %q", linha)
		}
	}
}

func putAdmin(t *testing.T, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s devolveu %s", url, resp.Status)
	}
}

func queryDe(url string) string {
	_, q, _ := strings.Cut(url, "?")
	return q
}
