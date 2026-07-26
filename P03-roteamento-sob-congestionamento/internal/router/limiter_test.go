package router

import (
	"sync"
	"testing"
	"time"
)

func TestLimiterRecusaAlemDoTeto(t *testing.T) {
	l := NewLimiter(3, time.Second)
	var liberar []func()
	for range 3 {
		fim, ok := l.Acquire()
		if !ok {
			t.Fatal("recusou dentro do teto")
		}
		liberar = append(liberar, fim)
	}

	if _, ok := l.Acquire(); ok {
		t.Fatal("admitiu a quarta requisição com teto de três")
	}

	// A recusa é imediata, e não uma espera. Fila aqui só moveria o acúmulo de
	// lugar, com as requisições vencendo o prazo do cliente enquanto esperam.
	liberar[0]()
	if _, ok := l.Acquire(); !ok {
		t.Fatal("não admitiu depois de uma vaga ser liberada")
	}
}

func TestLiberarDuasVezesNaoCriaVaga(t *testing.T) {
	// Um defer duplicado num handler poderia liberar a mesma vaga duas vezes e
	// furar o teto em silêncio, que é o pior jeito de um limite falhar.
	l := NewLimiter(1, time.Second)
	fim, ok := l.Acquire()
	if !ok {
		t.Fatal("não admitiu a primeira")
	}
	fim()
	fim()

	if _, ok := l.Acquire(); !ok {
		t.Fatal("não admitiu depois da liberação")
	}
	if _, ok := l.Acquire(); ok {
		t.Fatal("a liberação repetida criou uma vaga extra")
	}
}

func TestLimiterEmParalelo(t *testing.T) {
	const teto = 8
	l := NewLimiter(teto, time.Second)

	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fim, ok := l.Acquire()
			if !ok {
				return
			}
			if n := l.InFlight(); n > teto {
				t.Errorf("ocupação passou do teto: %d", n)
			}
			time.Sleep(time.Millisecond)
			fim()
		}()
	}
	wg.Wait()

	if l.InFlight() != 0 {
		t.Fatalf("sobraram %d vagas ocupadas", l.InFlight())
	}
	admitidos, recusados := l.Counters()
	if admitidos+recusados != 200 {
		t.Fatalf("perdeu decisões: %d admitidas + %d recusadas", admitidos, recusados)
	}
}
