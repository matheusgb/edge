package router

import (
	"testing"
	"time"
)

func destinos() []*Backend {
	return []*Backend{
		NewBackend("edge-a", "http://a", 5*time.Millisecond),
		NewBackend("edge-b", "http://b", 5*time.Millisecond),
		NewBackend("edge-c", "http://c", 5*time.Millisecond),
	}
}

func TestRoundRobinIgnoraOEstadoDosDestinos(t *testing.T) {
	// Esta é a propriedade que faz o round-robin ser a referência do experimento:
	// mesmo com um destino claramente pior, ele continua entregando um terço.
	bs := destinos()
	cfg := DefaultConfig()
	for range 50 {
		bs[0].Begin()
		bs[0].Finish(cfg, 2*time.Second, true)
	}

	rr := NewRoundRobin()
	contagem := map[string]int{}
	for range 300 {
		contagem[rr.Pick(bs, time.Now()).Name]++
	}
	for _, b := range bs {
		if contagem[b.Name] != 100 {
			t.Errorf("%s recebeu %d de 300, queria 100: o rodízio deveria ser cego ao estado",
				b.Name, contagem[b.Name])
		}
	}
}

func TestAdaptativaPrefereOMaisBarato(t *testing.T) {
	bs := destinos()
	cfg := DefaultConfig()

	// edge-a fica lento sem falhar: é o caso que health check não pega.
	for range 20 {
		for _, b := range bs {
			b.Begin()
		}
		bs[0].Finish(cfg, 500*time.Millisecond, false)
		bs[1].Finish(cfg, 5*time.Millisecond, false)
		bs[2].Finish(cfg, 5*time.Millisecond, false)
	}

	a := NewAdaptive(cfg)
	contagem := map[string]int{}
	for range 3000 {
		contagem[a.Pick(bs, time.Now()).Name]++
	}
	if contagem["edge-a"] >= contagem["edge-b"] || contagem["edge-a"] >= contagem["edge-c"] {
		t.Fatalf("o destino lento não foi evitado: %v", contagem)
	}
}

func TestAdaptativaVoltaAUsarODestinoQuandoOsOutrosEnchem(t *testing.T) {
	// A política não bane ninguém. Ela compara custos, e o custo dos saudáveis
	// sobe quando a fila deles cresce. É isso que impede o destino evitado de
	// virar um exilado permanente: em algum momento ele volta a ser a melhor
	// opção, e a resposta dele atualiza o que o roteador sabe.
	bs := destinos()
	cfg := DefaultConfig()
	for range 20 {
		for _, b := range bs {
			b.Begin()
		}
		bs[0].Finish(cfg, 100*time.Millisecond, false)
		bs[1].Finish(cfg, 10*time.Millisecond, false)
		bs[2].Finish(cfg, 10*time.Millisecond, false)
	}

	a := NewAdaptive(cfg)
	antes := 0
	for range 2000 {
		if a.Pick(bs, time.Now()).Name == "edge-a" {
			antes++
		}
	}

	// Os dois saudáveis ficam com 50 requisições em andamento cada.
	for range 50 {
		bs[1].Begin()
		bs[2].Begin()
	}

	depois := 0
	for range 2000 {
		if a.Pick(bs, time.Now()).Name == "edge-a" {
			depois++
		}
	}
	if depois <= antes {
		t.Fatalf("o destino lento recebeu %d antes e %d depois de os saudáveis encherem", antes, depois)
	}
}

func TestAdaptativaEscolheEntreDoisSorteados(t *testing.T) {
	// Com o sorteio fixado, a escolha vira uma afirmação verificável em vez de
	// uma tendência estatística.
	bs := destinos()
	cfg := DefaultConfig()
	bs[0].Begin()
	bs[0].Finish(cfg, time.Second, false) // caro
	bs[1].Begin()
	bs[1].Finish(cfg, 50*time.Millisecond, false)

	a := NewAdaptive(cfg)
	sorteios := []int{0, 0} // sorteia o índice 0 e, depois do ajuste, o índice 1
	i := 0
	a.escolher = func(int) int {
		v := sorteios[i%len(sorteios)]
		i++
		return v
	}
	if escolhido := a.Pick(bs, time.Now()); escolhido.Name != "edge-b" {
		t.Fatalf("escolheu %s entre edge-a caro e edge-b barato", escolhido.Name)
	}
}

func TestAdaptativaPulaDisjuntorAberto(t *testing.T) {
	bs := destinos()
	cfg := DefaultConfig()
	cfg.FailuresToOpen = 3
	for range 3 {
		bs[0].Begin()
		bs[0].Finish(cfg, 10*time.Millisecond, true)
	}
	if !bs[0].Open(time.Now()) {
		t.Fatal("o disjuntor deveria ter aberto depois de três falhas seguidas")
	}

	a := NewAdaptive(cfg)
	for range 200 {
		if a.Pick(bs, time.Now()).Name == "edge-a" {
			t.Fatal("mandou tráfego para um destino com disjuntor aberto")
		}
	}
}

func TestAdaptativaNaoDesligaTudoQuandoTodosEstaoAbertos(t *testing.T) {
	// Recusar tudo aqui transformaria degradação em blecaute. A requisição precisa
	// ir para o menos ruim e servir de sondagem.
	bs := destinos()
	cfg := DefaultConfig()
	cfg.FailuresToOpen = 1
	for _, b := range bs {
		b.Begin()
		b.Finish(cfg, 10*time.Millisecond, true)
	}
	a := NewAdaptive(cfg)
	if a.Pick(bs, time.Now()) == nil {
		t.Fatal("com todos os disjuntores abertos, a política devolveu nil em vez do menos ruim")
	}
}

func TestPickSemCandidatos(t *testing.T) {
	agora := time.Now()
	if NewRoundRobin().Pick(nil, agora) != nil {
		t.Error("round-robin devolveu destino a partir de uma lista vazia")
	}
	if NewAdaptive(DefaultConfig()).Pick(nil, agora) != nil {
		t.Error("adaptativa devolveu destino a partir de uma lista vazia")
	}
}

func BenchmarkPick(b *testing.B) {
	// A escolha acontece uma vez por tentativa, no caminho quente. Se ela custasse
	// microssegundos, o roteador seria o gargalo que veio medir.
	bs := destinos()
	cfg := DefaultConfig()
	agora := time.Now()

	b.Run("round-robin", func(b *testing.B) {
		rr := NewRoundRobin()
		for b.Loop() {
			_ = rr.Pick(bs, agora)
		}
	})

	b.Run("adaptativa", func(b *testing.B) {
		a := NewAdaptive(cfg)
		for b.Loop() {
			_ = a.Pick(bs, agora)
		}
	})
}
