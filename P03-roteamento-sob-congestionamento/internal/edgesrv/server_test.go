package edgesrv

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/metrics"
)

// origemFalsa conta quantas buscas chegaram até ela, que é o número que mede o
// alívio dado pelo cache e pela coalescência.
type origemFalsa struct {
	*httptest.Server
	buscas atomic.Int64
}

func novaOrigem(t *testing.T, h http.HandlerFunc) *origemFalsa {
	t.Helper()
	o := &origemFalsa{}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.buscas.Add(1)
		h(w, r)
	}))
	t.Cleanup(o.Close)
	return o
}

func novoEdge(t *testing.T, origem string, capacidade int, ttl time.Duration) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New("edge-teste", origem, capacidade, ttl, &http.Client{Timeout: 2 * time.Second}, metrics.NewEdge(), nil)
	if err != nil {
		t.Fatalf("montando o edge: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func servirObjeto(conteudo string, maxAge int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
		w.Header().Set("Etag", `"abc"`)
		_, _ = w.Write([]byte(conteudo))
	}
}

func pegar(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("pedindo %s: %v", url, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	return resp, string(corpo)
}

func TestSegundoPedidoVemDoCache(t *testing.T) {
	origem := novaOrigem(t, servirObjeto("conteúdo", 60))
	_, edge := novoEdge(t, origem.URL, 8, time.Minute)

	resp, corpo := pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	if resp.Header.Get(HeaderCache) != "MISS" || corpo != "conteúdo" {
		t.Fatalf("primeira: cache=%s corpo=%q", resp.Header.Get(HeaderCache), corpo)
	}

	resp, corpo = pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	if resp.Header.Get(HeaderCache) != "HIT" || corpo != "conteúdo" {
		t.Fatalf("segunda: cache=%s corpo=%q", resp.Header.Get(HeaderCache), corpo)
	}
	if origem.buscas.Load() != 1 {
		t.Fatalf("a origem recebeu %d buscas para dois pedidos", origem.buscas.Load())
	}
}

func TestPedidosSimultaneosViramUmaBuscaSo(t *testing.T) {
	// É o cache stampede do P02 aparecendo aqui dentro do processo. Sem
	// coalescência, todo objeto popular que expira vira uma rajada na origem.
	solta := make(chan struct{})
	origem := novaOrigem(t, func(w http.ResponseWriter, _ *http.Request) {
		<-solta
		servirObjeto("conteúdo", 60)(w, nil)
	})
	edgeSrv, edge := novoEdge(t, origem.URL, 8, time.Minute)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(edge.URL + "/objects/obj-1KiB-1.bin")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}

	// Espera todas chegarem antes de soltar a origem.
	time.Sleep(200 * time.Millisecond)
	close(solta)
	wg.Wait()

	if n := origem.buscas.Load(); n != 1 {
		t.Fatalf("50 pedidos simultâneos viraram %d buscas na origem", n)
	}
	if c := edgeSrv.Counters(); c.Coalesced == 0 {
		t.Fatal("nenhuma requisição foi contada como aproveitando a busca em andamento")
	}
}

func TestPrazoPropagadoLimitaABuscaNaOrigem(t *testing.T) {
	// O edge recebe 100ms de prazo e a origem demora 2s. Ele precisa desistir
	// dentro do prazo, e não gastar dois segundos produzindo uma resposta que já
	// não tem para quem ir.
	origem := novaOrigem(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			servirObjeto("tarde demais", 60)(w, r)
		case <-r.Context().Done():
		}
	})
	_, edge := novoEdge(t, origem.URL, 8, time.Minute)

	req, _ := http.NewRequest(http.MethodGet, edge.URL+"/objects/obj-1KiB-1.bin", nil)
	req.Header.Set(HeaderDeadline, "100")

	comecou := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pedindo: %v", err)
	}
	defer resp.Body.Close()
	decorrido := time.Since(comecou)

	if decorrido > time.Second {
		t.Fatalf("o edge esperou %s com prazo de 100ms", decorrido)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %s, queria 504", resp.Status)
	}
}

func TestOrigemComErroDevolve502(t *testing.T) {
	origem := novaOrigem(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "caí", http.StatusInternalServerError)
	})
	_, edge := novoEdge(t, origem.URL, 8, time.Minute)

	resp, _ := pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %s, queria 502", resp.Status)
	}
}

func TestTTLDaOrigemVence(t *testing.T) {
	origem := novaOrigem(t, servirObjeto("conteúdo", 0))
	_, edge := novoEdge(t, origem.URL, 8, time.Hour)

	// max-age=0 na origem manda mais que o TTL de uma hora do edge: quem sabe se
	// o objeto pode ser reaproveitado é quem o produziu.
	pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	resp, _ := pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	if resp.Header.Get(HeaderCache) != "MISS" {
		t.Fatal("serviu do cache um objeto que a origem declarou não reaproveitável")
	}
	if origem.buscas.Load() != 2 {
		t.Fatalf("a origem recebeu %d buscas", origem.buscas.Load())
	}
}

func TestCachePequenoDescartaOMaisAntigo(t *testing.T) {
	// Cache pequeno é escolha do lab: é o que faz deslocar carga para outro edge
	// ter preço visível.
	origem := novaOrigem(t, servirObjeto("conteúdo", 600))
	_, edge := novoEdge(t, origem.URL, 2, time.Hour)

	for i := range 3 {
		pegar(t, fmt.Sprintf("%s/objects/obj-1KiB-%d.bin", edge.URL, i))
	}
	// O primeiro saiu para o terceiro entrar.
	resp, _ := pegar(t, edge.URL+"/objects/obj-1KiB-0.bin")
	if resp.Header.Get(HeaderCache) != "MISS" {
		t.Fatal("o objeto mais antigo continuou no cache de duas entradas")
	}
}

func TestRevalidacaoCondicionalDevolve304(t *testing.T) {
	origem := novaOrigem(t, servirObjeto("conteúdo", 600))
	_, edge := novoEdge(t, origem.URL, 8, time.Hour)
	pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")

	req, _ := http.NewRequest(http.MethodGet, edge.URL+"/objects/obj-1KiB-1.bin", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pedindo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status %s, queria 304", resp.Status)
	}
	corpo, _ := io.ReadAll(resp.Body)
	if len(corpo) != 0 {
		t.Fatalf("304 veio com %d bytes de corpo", len(corpo))
	}
}

func TestPurgeEsvaziaOCache(t *testing.T) {
	origem := novaOrigem(t, servirObjeto("conteúdo", 600))
	edgeSrv, edge := novoEdge(t, origem.URL, 8, time.Hour)
	pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")

	admin := httptest.NewServer(edgeSrv.AdminHandler(http.NotFoundHandler()))
	defer admin.Close()
	resp, err := http.Post(admin.URL+"/admin/purge", "", nil)
	if err != nil {
		t.Fatalf("esvaziando: %v", err)
	}
	resp.Body.Close()

	depois, _ := pegar(t, edge.URL+"/objects/obj-1KiB-1.bin")
	if depois.Header.Get(HeaderCache) != "MISS" {
		t.Fatal("o cache continuou quente depois do purge")
	}
}

func TestMaxAge(t *testing.T) {
	casos := map[string]time.Duration{
		"public, max-age=60":         60 * time.Second,
		"max-age=0":                  0,
		" max-age=5 , s-maxage=99":   5 * time.Second,
		"no-store":                   -1,
		"public, max-age=nao-numero": -1,
	}
	for entrada, quer := range casos {
		got, ok := maxAge(entrada)
		if quer == -1 {
			if ok {
				t.Errorf("maxAge(%q) aceitou um valor que deveria recusar", entrada)
			}
			continue
		}
		if !ok || got != quer {
			t.Errorf("maxAge(%q) = %s (ok=%v), queria %s", entrada, got, ok, quer)
		}
	}
}
