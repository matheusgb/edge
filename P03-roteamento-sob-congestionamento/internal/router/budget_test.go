package router

import (
	"sync"
	"testing"
)

func TestOrcamentoLimitaRetryAoRatio(t *testing.T) {
	// A afirmação que o projeto faz: no regime permanente, o retry não passa da
	// fração configurada do tráfego. É o que impede a amplificação.
	b := NewRetryBudget(0.1, 10)

	// Esvazia o balde inicial para medir só o regime permanente.
	for b.Withdraw() {
	}

	concedidos := 0
	for range 1000 {
		b.Deposit()
		if b.Withdraw() {
			concedidos++
		}
	}
	if concedidos > 110 || concedidos < 90 {
		t.Fatalf("com ratio 0.1 em 1000 requisições, concedeu %d retries", concedidos)
	}
}

func TestOrcamentoSecaSobFalhaGeneralizada(t *testing.T) {
	// Uma requisição, muitos pedidos de retry: é o que acontece quando tudo falha
	// ao mesmo tempo. O balde precisa acabar rápido.
	b := NewRetryBudget(0.1, 20)
	b.Deposit()

	concedidos := 0
	for range 1000 {
		if b.Withdraw() {
			concedidos++
		}
	}
	if concedidos > 21 {
		t.Fatalf("concedeu %d retries a partir de um balde de 20 fichas", concedidos)
	}
	if b.Tokens() >= 1 {
		t.Fatalf("o balde deveria estar seco, restaram %.2f fichas", b.Tokens())
	}
}

func TestOrcamentoNaoPassaDoTeto(t *testing.T) {
	// Sem teto, um período longo de calmaria acumularia crédito infinito e o
	// primeiro incidente teria retry sem limite.
	b := NewRetryBudget(0.5, 10)
	for range 10_000 {
		b.Deposit()
	}
	if b.Tokens() > 10 {
		t.Fatalf("o balde passou do teto: %.2f fichas", b.Tokens())
	}
}

func TestOrcamentoEmParalelo(t *testing.T) {
	b := NewRetryBudget(0.1, 100)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				b.Deposit()
				b.Withdraw()
				_ = b.Tokens()
			}
		}()
	}
	wg.Wait()

	concedidos, negados := b.Counters()
	if concedidos+negados != 32*1000 {
		t.Fatalf("perdeu decisões: %d concedidas + %d negadas", concedidos, negados)
	}
}

func TestResetDevolveOBaldePelaMetade(t *testing.T) {
	b := NewRetryBudget(0.1, 50)
	for b.Withdraw() {
	}
	b.Reset()
	if b.Tokens() != 25 {
		t.Fatalf("depois do reset o balde tem %.2f fichas, queria 25", b.Tokens())
	}
	concedidos, negados := b.Counters()
	if concedidos != 0 || negados != 0 {
		t.Fatalf("o reset não zerou os contadores: %d concedidas, %d negadas", concedidos, negados)
	}
}
