package router

import (
	"sync/atomic"
	"time"
)

// Limiter é o teto de requisições simultâneas dentro do roteador.
//
// Ele existe para responder a uma pergunta desconfortável: o que o serviço faz
// quando chega mais trabalho do que ele consegue terminar? Sem teto, a resposta
// padrão do Go é aceitar tudo. Cada requisição vira uma goroutine, cada goroutine
// segura um socket e um buffer, e a memória cresce enquanto a latência de todo
// mundo piora junto. O sistema não recusa ninguém e não atende ninguém.
//
// Com teto, o excesso recebe 503 e Retry-After na hora. É uma resposta ruim, e é
// muito melhor que a alternativa: quem entrou continua sendo atendido no prazo, e
// quem foi recusado sabe disso em milissegundos em vez de descobrir depois de um
// timeout de trinta segundos. Isso é backpressure: empurrar a pressão de volta
// para quem gera a carga, em vez de acumulá-la aqui dentro.
type Limiter struct {
	vagas chan struct{}

	// retryAfter é o que se diz a quem foi recusado. Não é adivinhação de quando
	// vai melhorar; é um pedido explícito de espera, para que o cliente não volte
	// imediatamente e piore o que causou a recusa.
	retryAfter time.Duration

	admitidos atomic.Int64
	recusados atomic.Int64
	emUso     atomic.Int64
}

// NewLimiter cria o limitador com um teto de requisições simultâneas.
func NewLimiter(max int, retryAfter time.Duration) *Limiter {
	if max <= 0 {
		max = 1
	}
	return &Limiter{vagas: make(chan struct{}, max), retryAfter: retryAfter}
}

// Acquire tenta ocupar uma vaga sem esperar. Devolve a função que libera.
//
// A tentativa é sem espera de propósito. Uma fila de espera aqui só moveria o
// acúmulo de lugar: as requisições ficariam paradas gastando socket e memória,
// e boa parte delas venceria o prazo do cliente antes de ser atendida. Trabalho
// que ninguém vai mais esperar é trabalho jogado fora.
func (l *Limiter) Acquire() (func(), bool) {
	select {
	case l.vagas <- struct{}{}:
		l.admitidos.Add(1)
		l.emUso.Add(1)
		var uma bool
		return func() {
			if uma {
				return
			}
			uma = true
			l.emUso.Add(-1)
			<-l.vagas
		}, true
	default:
		l.recusados.Add(1)
		return func() {}, false
	}
}

// RetryAfter é o tempo sugerido a quem foi recusado.
func (l *Limiter) RetryAfter() time.Duration { return l.retryAfter }

// InFlight é quantas vagas estão ocupadas agora.
func (l *Limiter) InFlight() int64 { return l.emUso.Load() }

// Capacity é o teto configurado.
func (l *Limiter) Capacity() int { return cap(l.vagas) }

// Counters devolve admitidos e recusados desde o início.
func (l *Limiter) Counters() (admitidos, recusados int64) {
	return l.admitidos.Load(), l.recusados.Load()
}
