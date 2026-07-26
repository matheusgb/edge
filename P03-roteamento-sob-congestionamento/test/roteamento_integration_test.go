//go:build integration

// Teste de integração do caminho completo: cliente, roteador, Toxiproxy, edges e
// origem.
//
// Os testes unitários provam que cada peça faz a sua parte, com destinos de
// mentira e sem rede. Eles não conseguem provar o que este arquivo prova, porque
// as propriedades interessantes do projeto moram ENTRE as peças: o prazo que
// atravessa três processos, a degradação que acontece no caminho e não no
// destino, e a diferença entre as duas políticas quando existe rede de verdade
// no meio.
//
// A orquestração é do testcontainers, que sobe o docker-compose do projeto,
// espera e derruba tudo no fim. Escrever isso à mão significaria reimplementar
// espera de saúde, limpeza em caso de pânico e remoção de volume.
//
//	go test -tags=integration ./test/... -v
//
// Para iterar rápido com o ambiente já de pé, use EDGE_SKIP_COMPOSE=1.
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/compose"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/labctl"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/toxi"
)

const (
	routerURL   = "http://127.0.0.1:9080"
	routerAdmin = "http://127.0.0.1:9090"
	originAdmin = "http://127.0.0.1:9094"
	toxiAddr    = "http://127.0.0.1:8474"
)

var edges = map[string]string{
	"edge-a": "http://127.0.0.1:9091",
	"edge-b": "http://127.0.0.1:9092",
	"edge-c": "http://127.0.0.1:9093",
}

var (
	lab  *labctl.Lab
	rede *toxi.Client
)

// TestMain sobe o ambiente uma vez para todos os testes do pacote.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var stack compose.ComposeStack
	if os.Getenv("EDGE_SKIP_COMPOSE") == "" {
		criada, err := compose.NewDockerCompose(filepath.Join("..", "docker-compose.yml"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "não consegui preparar o compose:", err)
			os.Exit(1)
		}
		stack = criada
		if err := stack.Up(ctx, compose.Wait(true)); err != nil {
			fmt.Fprintln(os.Stderr, "não consegui subir o ambiente:", err)
			os.Exit(1)
		}
	}

	lab = labctl.New(routerAdmin, edges, originAdmin)
	rede = toxi.New(toxiAddr)

	code := 1
	func() {
		defer func() {
			if stack != nil {
				if err := stack.Down(context.Background(), compose.RemoveOrphans(true), compose.RemoveVolumes(true)); err != nil {
					fmt.Fprintln(os.Stderr, "aviso ao derrubar o ambiente:", err)
				}
			}
		}()
		if err := lab.WaitReady(ctx, 90*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "ambiente não ficou saudável:", err)
			return
		}
		code = m.Run()
	}()
	os.Exit(code)
}

// preparar devolve o ambiente ao estado limpo antes de cada teste.
func preparar(t *testing.T, politica string) {
	t.Helper()
	ctx := t.Context()
	if err := rede.Reset(); err != nil {
		t.Fatalf("limpando a rede: %v", err)
	}
	if err := lab.OriginFail(ctx, 0); err != nil {
		t.Fatalf("religando a origem: %v", err)
	}
	if err := lab.Prepare(ctx, politica); err != nil {
		t.Fatalf("preparando o ambiente: %v", err)
	}
	t.Cleanup(func() {
		_ = rede.Reset()
		_ = lab.OriginFail(context.Background(), 0)
	})
}

func pedir(t *testing.T, objeto string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, routerURL+"/objects/"+objeto, nil)
	if err != nil {
		t.Fatalf("montando a requisição: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pedindo %s: %v", objeto, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	return resp, corpo
}

func TestCaminhoFeliz(t *testing.T) {
	preparar(t, "adaptativa")

	resp, corpo := pedir(t, "obj-64KiB-1.bin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s", resp.Status)
	}
	if len(corpo) != 64<<10 {
		t.Fatalf("recebi %d bytes, queria %d", len(corpo), 64<<10)
	}
	if resp.Header.Get("X-Router-Backend") == "" {
		t.Error("a resposta não diz qual destino a atendeu")
	}
	if resp.Header.Get("X-Correlation-Id") == "" {
		t.Error("a resposta não traz identificador de correlação")
	}
	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("primeiro pedido veio com X-Cache=%s", resp.Header.Get("X-Cache"))
	}
}

func TestOsTresEdgesEntregamOMesmoConteudo(t *testing.T) {
	// A premissa que sustenta trocar de destino no meio da carga: qualquer edge
	// devolve exatamente os mesmos bytes.
	preparar(t, "round-robin")

	var primeiro []byte
	vistos := map[string]bool{}
	for range 9 {
		resp, corpo := pedir(t, "obj-1KiB-7.bin")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %s", resp.Status)
		}
		vistos[resp.Header.Get("X-Edge")] = true
		if primeiro == nil {
			primeiro = corpo
			continue
		}
		if string(corpo) != string(primeiro) {
			t.Fatalf("o edge %s devolveu conteúdo diferente", resp.Header.Get("X-Edge"))
		}
	}
	if len(vistos) < 3 {
		t.Fatalf("o rodízio não passou pelos três edges: %v", vistos)
	}
}

func TestEdgeForaDoArNaoDerrubaARequisicao(t *testing.T) {
	preparar(t, "adaptativa")
	if err := rede.Down("edge-a"); err != nil {
		t.Fatalf("derrubando edge-a: %v", err)
	}

	for range 30 {
		resp, _ := pedir(t, "obj-1KiB-3.bin")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %s com dois edges saudáveis disponíveis", resp.Status)
		}
		if resp.Header.Get("X-Edge") == "edge-a" {
			t.Fatal("uma resposta veio do edge que está fora do ar")
		}
	}
}

func TestEdgeLentoPerdeTrafegoComAAdaptativa(t *testing.T) {
	preparar(t, "adaptativa")
	if err := rede.Latency("edge-a", 700*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("deixando edge-a lento: %v", err)
	}

	// Um aquecimento para a política aprender com o tráfego real.
	for range 30 {
		pedir(t, "obj-1KiB-4.bin")
	}

	contagem := map[string]int{}
	for range 60 {
		resp, _ := pedir(t, "obj-1KiB-4.bin")
		contagem[resp.Header.Get("X-Edge")]++
	}
	if contagem["edge-a"] > 12 {
		t.Fatalf("o edge lento ficou com %d de 60 respostas: %v", contagem["edge-a"], contagem)
	}
}

func TestRoundRobinContinuaMandandoParaOEdgeLento(t *testing.T) {
	// É a outra metade da comparação, e ela é o que dá sentido à primeira: sem
	// este teste, a queda de tráfego no edge lento poderia ser efeito da rede.
	preparar(t, "round-robin")
	if err := rede.Latency("edge-a", 700*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("deixando edge-a lento: %v", err)
	}

	estado, err := lab.Router(t.Context())
	if err != nil {
		t.Fatalf("lendo o estado do roteador: %v", err)
	}
	antes := tentativas(estado, "edge-a")

	for range 60 {
		pedir(t, "obj-1KiB-5.bin")
	}

	estado, err = lab.Router(t.Context())
	if err != nil {
		t.Fatalf("lendo o estado do roteador: %v", err)
	}
	doente := int(tentativas(estado, "edge-a") - antes)
	if doente < 15 {
		t.Fatalf("com round-robin, o edge lento recebeu só %d tentativas em ~60 requisições", doente)
	}
}

func TestOrcamentoDeRetryContemAAmplificacao(t *testing.T) {
	// Todos os caminhos cortando conexão: sem orçamento, cada requisição viraria
	// duas tentativas e o roteador dobraria a carga em cima de um sistema que já
	// está mal.
	preparar(t, "adaptativa")
	for _, nome := range []string{"edge-a", "edge-b", "edge-c"} {
		if err := rede.ResetPeer(nome, 1, 10*time.Millisecond); err != nil {
			t.Fatalf("cortando as conexões de %s: %v", nome, err)
		}
	}

	for range 100 {
		pedir(t, "obj-1KiB-6.bin")
	}

	estado, err := lab.Router(t.Context())
	if err != nil {
		t.Fatalf("lendo o estado do roteador: %v", err)
	}
	if estado.RetriesGranted > 60 {
		t.Fatalf("concedeu %d retries para 100 requisições com tudo falhando", estado.RetriesGranted)
	}
	if estado.RetriesDenied == 0 {
		t.Fatal("nenhum retry foi negado: o orçamento não entrou em ação")
	}
}

func TestOrigemForaDoArAindaEServidaDoCache(t *testing.T) {
	preparar(t, "adaptativa")

	// Aquece o mesmo objeto nos três edges.
	for range 30 {
		pedir(t, "obj-1KiB-8.bin")
	}
	if err := lab.OriginFail(t.Context(), 503); err != nil {
		t.Fatalf("derrubando a origem: %v", err)
	}

	ok := 0
	for range 30 {
		resp, _ := pedir(t, "obj-1KiB-8.bin")
		if resp.StatusCode == http.StatusOK {
			ok++
		}
	}
	if ok < 25 {
		t.Fatalf("só %d de 30 respostas vieram do cache com a origem fora do ar; edges: %s",
			ok, contadoresDosEdges(t))
	}
}

func TestPrazoAtravessaOsTresProcessos(t *testing.T) {
	// A origem fica lenta e o objeto é novo, então o edge precisa buscá-lo. O
	// prazo do cliente é o que decide quando todo mundo desiste.
	preparar(t, "adaptativa")
	if err := putOrigem(t, "/admin/latency?d=5s"); err != nil {
		t.Fatalf("deixando a origem lenta: %v", err)
	}
	defer func() { _ = putOrigem(t, "/admin/latency?d=5ms") }()

	comecou := time.Now()
	resp, _ := pedir(t, "obj-1KiB-99.bin")
	decorrido := time.Since(comecou)

	if resp.StatusCode == http.StatusOK {
		t.Fatal("recebeu 200 com a origem levando 5s e prazo de 2s")
	}
	if decorrido > 4*time.Second {
		t.Fatalf("o cliente esperou %s: o prazo não foi respeitado", decorrido)
	}
}

func TestMetricasExplicamAMesmaRequisicao(t *testing.T) {
	// Métrica, log e trace precisam falar da mesma coisa. Aqui a checagem é a
	// mais simples possível: o roteador contou a tentativa que a resposta diz ter
	// acontecido.
	preparar(t, "adaptativa")

	antes, err := lab.Router(t.Context())
	if err != nil {
		t.Fatalf("lendo o estado: %v", err)
	}
	resp, _ := pedir(t, "obj-1KiB-11.bin")
	destino := resp.Header.Get("X-Router-Backend")

	depois, err := lab.Router(t.Context())
	if err != nil {
		t.Fatalf("lendo o estado: %v", err)
	}
	if tentativas(depois, destino) <= tentativas(antes, destino) {
		t.Fatalf("a resposta veio de %s, mas o contador daquele destino não subiu", destino)
	}
}

func tentativas(s labctl.RouterSnapshot, nome string) int64 {
	for _, b := range s.Backends {
		if b.Name == nome {
			return b.Attempts
		}
	}
	return 0
}

func putOrigem(t *testing.T, caminho string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, originAdmin+caminho, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		corpo, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s devolveu %s: %s", caminho, resp.Status, corpo)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// contadoresDosEdges é usado nas mensagens de falha, para o diagnóstico não
// depender de olhar o container depois que o teste já terminou.
func contadoresDosEdges(t *testing.T) string {
	t.Helper()
	c, err := lab.EdgeCounters(t.Context())
	if err != nil {
		return "(indisponível)"
	}
	blob, _ := json.Marshal(c)
	return string(blob)
}
