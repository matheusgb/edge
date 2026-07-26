package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/metrics"
)

// destinoFalso é um edge de mentira, controlado pelo teste.
type destinoFalso struct {
	*httptest.Server
	chamadas atomic.Int64
	prazos   chan string // o valor do header de prazo que ele recebeu
}

func novoDestino(t *testing.T, h http.HandlerFunc) *destinoFalso {
	t.Helper()
	d := &destinoFalso{prazos: make(chan string, 64)}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.chamadas.Add(1)
		select {
		case d.prazos <- r.Header.Get(HeaderDeadline):
		default:
		}
		h(w, r)
	}))
	t.Cleanup(d.Close)
	return d
}

func novoRoteador(t *testing.T, opts Options, destinos ...*destinoFalso) *httptest.Server {
	t.Helper()
	for i, d := range destinos {
		opts.Backends = append(opts.Backends, NewBackend("edge-"+strconv.Itoa(i), d.URL, 5*time.Millisecond))
	}
	if opts.Strategy == nil {
		opts.Strategy = NewRoundRobin()
	}
	if opts.Budget == nil {
		opts.Budget = NewRetryBudget(1, 1000)
	}
	if opts.Limiter == nil {
		opts.Limiter = NewLimiter(100, time.Second)
	}
	if opts.AttemptTimeout == 0 {
		opts.AttemptTimeout = 500 * time.Millisecond
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 2 * time.Second
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 2
	}
	opts.Config = DefaultConfig()
	opts.Metrics = metrics.NewRouter()

	srv := httptest.NewServer(New(opts).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func pedir(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("pedindo %s: %v", url, err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	return resp, string(corpo)
}

func TestRetryVaiParaOutroDestino(t *testing.T) {
	// Repetir no destino que acabou de falhar é gastar orçamento para receber o
	// mesmo erro.
	ruim := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "estou mal", http.StatusInternalServerError)
	})
	bom := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("conteúdo"))
	})

	roteador := novoRoteador(t, Options{Strategy: NewRoundRobin()}, ruim, bom)

	resp, corpo := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s, queria 200", resp.Status)
	}
	if corpo != "conteúdo" {
		t.Fatalf("corpo %q", corpo)
	}
	if resp.Header.Get(HeaderAttempts) != "2" {
		t.Fatalf("tentativas %q, queria 2", resp.Header.Get(HeaderAttempts))
	}
	if ruim.chamadas.Load() != 1 || bom.chamadas.Load() != 1 {
		t.Fatalf("chamadas: ruim=%d bom=%d", ruim.chamadas.Load(), bom.chamadas.Load())
	}
}

func TestSemRetryDepoisDoCorpoComecar(t *testing.T) {
	// O status 200 já foi para o cliente; o que vier depois não dá para desfazer.
	// A garantia aqui é que o roteador não tenta outro destino e concatena duas
	// respostas na mesma conexão.
	quebrado := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metade"))
		// Fecha sem escrever os 100 bytes prometidos.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	})
	outro := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("resposta inteira"))
	})

	roteador := novoRoteador(t, Options{Strategy: NewRoundRobin()}, quebrado, outro)

	resp, err := http.Get(roteador.URL + "/objects/obj-1KiB-1.bin")
	if err != nil {
		t.Fatalf("pedindo: %v", err)
	}
	corpo, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if outro.chamadas.Load() != 0 {
		t.Fatal("tentou outro destino depois do corpo já ter começado a sair")
	}
	if string(corpo) == "resposta inteira" {
		t.Fatal("o cliente recebeu a resposta do segundo destino emendada na do primeiro")
	}
}

func TestOrcamentoNegaRetry(t *testing.T) {
	ruim := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "estou mal", http.StatusInternalServerError)
	})
	outro := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "também estou mal", http.StatusInternalServerError)
	})

	// Balde minúsculo: o primeiro retry esgota o orçamento.
	roteador := novoRoteador(t, Options{
		Strategy: NewRoundRobin(),
		Budget:   NewRetryBudget(0.001, 2),
	}, ruim, outro)

	for range 20 {
		resp, _ := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status %s, queria 502", resp.Status)
		}
	}

	total := ruim.chamadas.Load() + outro.chamadas.Load()
	// 20 requisições, orçamento para pouquíssimos retries: sem o orçamento seriam
	// 40 tentativas.
	if total > 25 {
		t.Fatalf("%d tentativas para 20 requisições: o orçamento não conteve a amplificação", total)
	}
}

func TestPrazoEPropagadoEDiminui(t *testing.T) {
	destino := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	roteador := novoRoteador(t, Options{
		AttemptTimeout: 300 * time.Millisecond,
		RequestTimeout: 2 * time.Second,
	}, destino)

	resp, _ := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s", resp.Status)
	}

	prazo := <-destino.prazos
	ms, err := strconv.Atoi(prazo)
	if err != nil {
		t.Fatalf("header de prazo %q não é um número", prazo)
	}
	if ms <= 0 || ms > 300 {
		t.Fatalf("prazo propagado de %dms; deveria caber no prazo de uma tentativa", ms)
	}
}

func TestPrazoDoClienteMandaNaTentativa(t *testing.T) {
	// Quando resta menos prazo do que o timeout de uma tentativa, quem manda é o
	// prazo do cliente: prometer 800ms com 100ms restantes seria mentira.
	destino := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	roteador := novoRoteador(t, Options{
		AttemptTimeout: 5 * time.Second,
		RequestTimeout: 200 * time.Millisecond,
	}, destino)

	pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	prazo := <-destino.prazos
	ms, _ := strconv.Atoi(prazo)
	if ms > 200 {
		t.Fatalf("propagou %dms com prazo total de 200ms", ms)
	}
}

func TestTimeoutDaTentativaViraOutraTentativa(t *testing.T) {
	lento := novoDestino(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	rapido := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("rápido"))
	})

	roteador := novoRoteador(t, Options{
		Strategy:       NewRoundRobin(),
		AttemptTimeout: 100 * time.Millisecond,
		RequestTimeout: 3 * time.Second,
	}, lento, rapido)

	resp, corpo := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	if resp.StatusCode != http.StatusOK || corpo != "rápido" {
		t.Fatalf("status %s, corpo %q", resp.Status, corpo)
	}
}

func TestPrazoTotalEstouradoDevolve504(t *testing.T) {
	lento := novoDestino(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	roteador := novoRoteador(t, Options{
		AttemptTimeout: 500 * time.Millisecond,
		RequestTimeout: 200 * time.Millisecond,
		MaxAttempts:    3,
	}, lento)

	resp, _ := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %s, queria 504", resp.Status)
	}
}

func TestExcessoRecebe503ComRetryAfter(t *testing.T) {
	solto := make(chan struct{})
	preso := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		<-solto
		_, _ = w.Write([]byte("ok"))
	})

	roteador := novoRoteador(t, Options{
		Limiter:        NewLimiter(1, 2*time.Second),
		RequestTimeout: 5 * time.Second,
	}, preso)

	// A primeira ocupa a única vaga e fica presa lá dentro.
	go func() { _, _ = http.Get(roteador.URL + "/objects/obj-1KiB-1.bin") }()
	esperar(t, func() bool { return preso.chamadas.Load() == 1 })

	resp, _ := pedir(t, roteador.URL+"/objects/obj-1KiB-2.bin")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %s, queria 503", resp.Status)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("recusou sem dizer quando tentar de novo")
	}
	close(solto)
}

func TestNaoTentaDeNovoEm404(t *testing.T) {
	// 404 é resposta correta sobre um objeto que não existe. Tentar de novo em
	// outro destino gastaria orçamento para receber o mesmo 404.
	a := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	b := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	roteador := novoRoteador(t, Options{Strategy: NewRoundRobin()}, a, b)

	resp, _ := pedir(t, roteador.URL+"/objects/nao-existe.bin")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %s, queria 404", resp.Status)
	}
	if total := a.chamadas.Load() + b.chamadas.Load(); total != 1 {
		t.Fatalf("%d tentativas para um 404", total)
	}
}

func TestCorrelacaoEPropagadaEDevolvida(t *testing.T) {
	recebido := make(chan string, 1)
	destino := novoDestino(t, func(w http.ResponseWriter, r *http.Request) {
		recebido <- r.Header.Get(HeaderCorrelation)
		_, _ = w.Write([]byte("ok"))
	})
	roteador := novoRoteador(t, Options{}, destino)

	req, _ := http.NewRequest(http.MethodGet, roteador.URL+"/objects/obj-1KiB-1.bin", nil)
	req.Header.Set(HeaderCorrelation, "abc123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("pedindo: %v", err)
	}
	defer resp.Body.Close()

	if got := <-recebido; got != "abc123" {
		t.Fatalf("o destino recebeu %q", got)
	}
	if got := resp.Header.Get(HeaderCorrelation); got != "abc123" {
		t.Fatalf("a resposta devolveu %q", got)
	}
}

func TestCorrelacaoEGeradaQuandoNaoVem(t *testing.T) {
	destino := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	roteador := novoRoteador(t, Options{}, destino)

	resp, _ := pedir(t, roteador.URL+"/objects/obj-1KiB-1.bin")
	if resp.Header.Get(HeaderCorrelation) == "" {
		t.Fatal("nenhum identificador de correlação foi gerado")
	}
}

func TestTrocaDePoliticaEmTempoDeExecucao(t *testing.T) {
	destino := novoDestino(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	opts := Options{
		Strategy: NewRoundRobin(),
		Backends: []*Backend{NewBackend("edge-0", destino.URL, 5*time.Millisecond)},
		Config:   DefaultConfig(),
		Budget:   NewRetryBudget(1, 100),
		Limiter:  NewLimiter(10, time.Second),
		Metrics:  metrics.NewRouter(),
	}
	s := New(opts)
	admin := httptest.NewServer(s.AdminHandler(http.NotFoundHandler()))
	defer admin.Close()

	req, _ := http.NewRequest(http.MethodPut, admin.URL+"/admin/strategy?name=adaptativa", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trocando a política: %v", err)
	}
	resp.Body.Close()

	if s.Strategy().Name() != "adaptativa" {
		t.Fatalf("a política ficou em %s", s.Strategy().Name())
	}
}

func esperar(t *testing.T, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(limite) {
			t.Fatal("condição não aconteceu dentro de 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
