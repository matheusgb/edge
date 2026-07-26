package router

import (
	"sync"
	"testing"
	"time"
)

func TestEWMAReageSemPular(t *testing.T) {
	cfg := DefaultConfig()
	b := NewBackend("edge-a", "http://a", 10*time.Millisecond)

	b.Begin()
	b.Finish(cfg, 1000*time.Millisecond, false)
	primeira := b.LatencyEWMA()
	if primeira <= 10*time.Millisecond || primeira >= 500*time.Millisecond {
		t.Fatalf("uma amostra de 1s levou a média para %s: com alpha 0.2, ela deveria subir sem pular", primeira)
	}

	for range 30 {
		b.Begin()
		b.Finish(cfg, 1000*time.Millisecond, false)
	}
	if b.LatencyEWMA() < 900*time.Millisecond {
		t.Fatalf("depois de 30 amostras de 1s, a média ficou em %s", b.LatencyEWMA())
	}
}

func TestDisjuntorAbreEFechaComSucesso(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FailuresToOpen = 3
	cfg.OpenFor = 50 * time.Millisecond
	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)

	for range 2 {
		b.Begin()
		b.Finish(cfg, time.Millisecond, true)
	}
	if b.Open(time.Now()) {
		t.Fatal("abriu antes das três falhas seguidas")
	}
	b.Begin()
	b.Finish(cfg, time.Millisecond, true)
	if !b.Open(time.Now()) {
		t.Fatal("não abriu depois da terceira falha seguida")
	}

	// Depois da janela, o destino volta a ser candidato: é a sondagem.
	if b.Open(time.Now().Add(60 * time.Millisecond)) {
		t.Fatal("continuou aberto depois da janela")
	}

	// E um sucesso fecha na hora, sem esperar o relógio.
	b.Begin()
	b.Finish(cfg, time.Millisecond, true)
	b.Begin()
	b.Finish(cfg, time.Millisecond, true)
	b.Begin()
	b.Finish(cfg, time.Millisecond, true)
	b.Begin()
	b.Finish(cfg, time.Millisecond, false)
	if b.Open(time.Now()) {
		t.Fatal("um sucesso deveria ter fechado o disjuntor")
	}
}

func TestFalhaIntercaladaNaoAbre(t *testing.T) {
	// O critério é FALHAS SEGUIDAS. Um erro isolado no meio de tráfego saudável é
	// rotina, e abrir por causa dele tiraria um destino bom do ar.
	cfg := DefaultConfig()
	cfg.FailuresToOpen = 3
	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)
	for range 10 {
		b.Begin()
		b.Finish(cfg, time.Millisecond, true)
		b.Begin()
		b.Finish(cfg, time.Millisecond, false)
	}
	if b.Open(time.Now()) {
		t.Fatal("abriu com falhas intercaladas por sucesso")
	}
}

func TestCustoCresceComFilaLatenciaEErro(t *testing.T) {
	cfg := DefaultConfig()
	base := NewBackend("base", "http://base", 10*time.Millisecond)
	lento := NewBackend("lento", "http://lento", 100*time.Millisecond)
	cheio := NewBackend("cheio", "http://cheio", 10*time.Millisecond)
	cheio.Begin()
	cheio.Begin()
	cheio.Begin()
	comErro := NewBackend("erro", "http://erro", 10*time.Millisecond)
	for range 10 {
		comErro.Begin()
		comErro.Finish(cfg, 10*time.Millisecond, true)
	}

	if lento.Cost(cfg) <= base.Cost(cfg) {
		t.Error("latência maior deveria custar mais")
	}
	if cheio.Cost(cfg) <= base.Cost(cfg) {
		t.Error("fila maior deveria custar mais")
	}
	if comErro.Cost(cfg) <= base.Cost(cfg) {
		t.Error("taxa de erro deveria custar mais")
	}
}

func TestResetVoltaAoPalpiteInicial(t *testing.T) {
	cfg := DefaultConfig()
	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)
	for range 20 {
		b.Begin()
		b.Finish(cfg, time.Second, true)
	}
	b.Reset(5 * time.Millisecond)

	if b.LatencyEWMA() != 5*time.Millisecond {
		t.Errorf("latência ficou em %s depois do reset", b.LatencyEWMA())
	}
	if b.ErrorRate() != 0 {
		t.Errorf("taxa de erro ficou em %.3f depois do reset", b.ErrorRate())
	}
	if b.Open(time.Now()) {
		t.Error("o disjuntor continuou aberto depois do reset")
	}
}

func TestContadoresSaoSegurosEmParalelo(t *testing.T) {
	// O caminho quente lê e escreve estes campos de muitas goroutines. O valor
	// deste teste está em rodar com -race.
	cfg := DefaultConfig()
	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 100 {
				b.Begin()
				b.Finish(cfg, time.Duration(j)*time.Millisecond, (i+j)%7 == 0)
				_ = b.Cost(cfg)
				_ = b.Stats(cfg, time.Now())
			}
		}()
	}
	wg.Wait()

	if b.Inflight() != 0 {
		t.Fatalf("sobrou %d requisição em andamento depois de todas terminarem", b.Inflight())
	}
}

func TestCustoEnvelheceQuandoODestinoParaDeReceber(t *testing.T) {
	// Sem isto, um destino evitado nunca mais é escolhido, porque o que o tornaria
	// atraente de novo é justamente a informação que só o tráfego traz. Foi o que
	// a execução de falha controlada mostrou antes desta correção existir.
	cfg := DefaultConfig()
	cfg.AgingWindow = 50 * time.Millisecond

	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)
	b.Begin()
	b.Finish(cfg, 800*time.Millisecond, false)

	logoDepois := b.Cost(cfg)
	time.Sleep(200 * time.Millisecond)
	depoisDeQuatroJanelas := b.Cost(cfg)

	if depoisDeQuatroJanelas >= logoDepois {
		t.Fatalf("o custo não caiu com o silêncio: %.0f antes, %.0f depois", logoDepois, depoisDeQuatroJanelas)
	}
	if depoisDeQuatroJanelas > logoDepois/3 {
		t.Fatalf("o custo caiu pouco: %.0f para %.0f em quatro janelas", logoDepois, depoisDeQuatroJanelas)
	}
}

func TestDestinoNuncaUsadoNaoGanhaDesconto(t *testing.T) {
	// O palpite inicial já coloca todo mundo em pé de igualdade. Dar desconto por
	// "silêncio" a quem acabou de subir faria o roteador preferir o destino sobre
	// o qual não sabe nada.
	cfg := DefaultConfig()
	novo := NewBackend("novo", "http://novo", 10*time.Millisecond)
	usado := NewBackend("usado", "http://usado", 10*time.Millisecond)
	usado.Begin()
	usado.Finish(cfg, 10*time.Millisecond, false)

	if novo.Cost(cfg) < usado.Cost(cfg)*0.9 {
		t.Fatalf("o destino nunca usado saiu mais barato: %.0f contra %.0f", novo.Cost(cfg), usado.Cost(cfg))
	}
}

func TestMedicaoVelhaESubstituidaEmVezDeMisturada(t *testing.T) {
	// O destino ficou péssimo, sumiu do tráfego e voltou bom. A primeira resposta
	// depois do silêncio precisa valer por si: misturada a 20%, a média levaria
	// dezenas de sondagens espaçadas para descer, e cada sondagem depende de outro
	// silêncio longo antes.
	cfg := DefaultConfig()
	cfg.AgingWindow = 10 * time.Millisecond // duas janelas = 20ms de silêncio

	b := NewBackend("edge-a", "http://a", 5*time.Millisecond)
	b.Begin()
	b.Finish(cfg, 800*time.Millisecond, false)

	time.Sleep(120 * time.Millisecond)
	b.Begin()
	b.Finish(cfg, 2*time.Millisecond, false)

	if b.LatencyEWMA() > 5*time.Millisecond {
		t.Fatalf("depois do silêncio, a média ficou em %s: a amostra nova não substituiu a velha", b.LatencyEWMA())
	}
}

func TestMedicaoRecenteContinuaSendoMisturada(t *testing.T) {
	// O outro lado: sem silêncio, uma amostra sozinha não pode jogar a média
	// inteira para o valor dela, ou um outlier viraria a decisão.
	cfg := DefaultConfig()
	b := NewBackend("edge-a", "http://a", 10*time.Millisecond)
	for range 10 {
		b.Begin()
		b.Finish(cfg, 10*time.Millisecond, false)
	}
	b.Begin()
	b.Finish(cfg, 800*time.Millisecond, false)

	if b.LatencyEWMA() > 300*time.Millisecond {
		t.Fatalf("um único outlier levou a média para %s", b.LatencyEWMA())
	}
}
