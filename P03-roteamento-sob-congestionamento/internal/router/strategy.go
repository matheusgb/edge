package router

import (
	"math/rand/v2"
	"sync/atomic"
	"time"
)

// Strategy escolhe para qual destino vai a próxima tentativa.
//
// A interface é pequena de propósito: é ela que permite rodar o mesmo cenário
// determinístico com as duas políticas, trocando só a implementação, e é ela que
// os testes usam para verificar a escolha sem subir container nenhum.
type Strategy interface {
	// Name é o rótulo que aparece em métrica, log e evidência.
	Name() string

	// Pick devolve o destino escolhido entre os candidatos, ou nil quando não há
	// nenhum aceitável. Os candidatos já vêm sem os destinos que esta requisição
	// tentou antes, porque repetir o destino que acabou de falhar é gastar o
	// orçamento de retry para receber o mesmo erro.
	Pick(candidatos []*Backend, agora time.Time) *Backend
}

// RoundRobin distribui em rodízio, ignorando tudo o que se sabe sobre o destino.
//
// Esta é a política de referência do experimento, e a ingenuidade dela é o ponto:
// ela representa o roteador que só sabe se o destino aceitou a conexão. Quando um
// edge fica lento sem cair, ela continua entregando a ele exatamente um terço da
// carga, e é isso que o relatório mede.
type RoundRobin struct {
	proximo atomic.Uint64
}

// NewRoundRobin cria a política de rodízio.
func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

// Name identifica a política.
func (r *RoundRobin) Name() string { return "round-robin" }

// Pick devolve o próximo da fila circular.
func (r *RoundRobin) Pick(candidatos []*Backend, _ time.Time) *Backend {
	if len(candidatos) == 0 {
		return nil
	}
	i := r.proximo.Add(1) - 1
	return candidatos[i%uint64(len(candidatos))]
}

// Adaptive escolhe pelo custo estimado, entre dois candidatos sorteados.
//
// O sorteio de dois e a escolha do melhor entre eles é conhecido como "power of
// two choices". Pegar sempre o de menor custo global parece melhor e não é: a
// informação que o roteador tem é de alguns milissegundos atrás, e todos os
// clientes que decidirem ao mesmo tempo mandam a rajada inteira para o mesmo
// destino, que fica lento e recebe a próxima rajada do concorrente que acabou de
// ficar rápido. O sorteio quebra essa sincronia com uma perda pequena de
// qualidade da escolha.
//
// Com três destinos a diferença é modesta, mas a propriedade é a mesma, e trocar
// de política aqui não deveria depender do tamanho do lab.
type Adaptive struct {
	cfg Config

	// escolher existe para o teste: um sorteio determinístico transforma
	// "provavelmente escolhe o melhor" em uma afirmação verificável.
	escolher func(n int) int
}

// NewAdaptive cria a política adaptativa.
func NewAdaptive(cfg Config) *Adaptive {
	return &Adaptive{cfg: cfg, escolher: rand.IntN}
}

// Name identifica a política.
func (a *Adaptive) Name() string { return "adaptativa" }

// Pick escolhe o destino de menor custo entre dois sorteados, pulando destinos
// com o disjuntor aberto.
func (a *Adaptive) Pick(candidatos []*Backend, agora time.Time) *Backend {
	if len(candidatos) == 0 {
		return nil
	}

	fechados := make([]*Backend, 0, len(candidatos))
	for _, b := range candidatos {
		if !b.Open(agora) {
			fechados = append(fechados, b)
		}
	}
	// Todos abertos significa que o sistema inteiro está mal, e recusar tudo aqui
	// transformaria degradação em blecaute. Nesse caso a requisição vai para o
	// menos ruim e recebe uma chance de ser a sondagem que fecha o disjuntor.
	if len(fechados) == 0 {
		return a.menorCusto(candidatos)
	}
	if len(fechados) == 1 {
		return fechados[0]
	}

	i := a.escolher(len(fechados))
	j := a.escolher(len(fechados) - 1)
	if j >= i {
		j++ // sorteia o segundo entre os que sobraram, sem repetir o primeiro
	}
	if fechados[j].Cost(a.cfg) < fechados[i].Cost(a.cfg) {
		return fechados[j]
	}
	return fechados[i]
}

func (a *Adaptive) menorCusto(candidatos []*Backend) *Backend {
	melhor := candidatos[0]
	for _, b := range candidatos[1:] {
		if b.Cost(a.cfg) < melhor.Cost(a.cfg) {
			melhor = b
		}
	}
	return melhor
}
