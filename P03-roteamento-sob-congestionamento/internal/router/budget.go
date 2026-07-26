package router

import "sync"

// RetryBudget limita quantas tentativas extras o roteador pode gastar.
//
// Retry sem orçamento é o mecanismo que transforma degradação em queda. A conta é
// direta: se cada requisição pode ser tentada três vezes, um sistema com falha
// generalizada recebe o triplo da carga exatamente no momento em que já não dá
// conta da carga original. O nome disso é amplificação de retry, e é o motivo de
// vários incidentes públicos terem durado mais do que a falha que os começou.
//
// O orçamento é um balde de fichas, no mesmo espírito do que o gRPC faz. Cada
// requisição que chega deposita uma fração de ficha; cada retry saca uma ficha
// inteira. Enquanto a taxa de falha for baixa, o balde enche mais rápido do que
// esvazia e todo retry é permitido. Quando a falha se generaliza, o balde seca em
// segundos e o roteador simplesmente para de insistir.
//
// A propriedade que importa: o teto é proporcional ao tráfego, não absoluto. Não
// existe número mágico de "quantos retries por segundo" para acertar, e o mesmo
// serviço com dez vezes mais carga não precisa de nova configuração.
//
// Este mecanismo é escrito à mão, e não importado, porque o comportamento dele é
// uma das perguntas do projeto: o relatório precisa mostrar o balde secando.
type RetryBudget struct {
	mu     sync.Mutex
	fichas float64

	// max é o tamanho do balde. Ele define por quanto tempo um pico de falhas
	// pode ser absorvido antes do orçamento acabar.
	max float64

	// ratio é quanto cada requisição deposita. 0.1 significa "no regime
	// permanente, no máximo 10% das requisições podem virar retry".
	ratio float64

	concedidos int64
	negados    int64
}

// NewRetryBudget cria o orçamento. ratio 0.1 e max 100 são um ponto de partida
// razoável: até 10% de retry no permanente, com folga para uma rajada curta.
func NewRetryBudget(ratio float64, max float64) *RetryBudget {
	if ratio <= 0 {
		ratio = 0.1
	}
	if max <= 0 {
		max = 100
	}
	return &RetryBudget{
		// O balde começa cheio pela metade. Cheio deixaria os primeiros segundos
		// de vida do processo sem limite nenhum; vazio negaria retry legítimo
		// antes de qualquer tráfego ter ensinado alguma coisa.
		fichas: max / 2,
		max:    max,
		ratio:  ratio,
	}
}

// Deposit registra uma requisição do cliente. Chamado uma vez por requisição,
// não uma vez por tentativa: quem paga o orçamento é a demanda que chega, e
// contar tentativa aqui deixaria o retry financiando o próprio retry.
func (b *RetryBudget) Deposit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fichas += b.ratio
	if b.fichas > b.max {
		b.fichas = b.max
	}
}

// Withdraw tenta pagar por um retry. Devolve false quando o orçamento acabou.
func (b *RetryBudget) Withdraw() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fichas < 1 {
		b.negados++
		return false
	}
	b.fichas--
	b.concedidos++
	return true
}

// Reset devolve o balde ao estado inicial, para começar um cenário limpo.
func (b *RetryBudget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fichas = b.max / 2
	b.concedidos = 0
	b.negados = 0
}

// Tokens é quanto resta no balde, para métrica e evidência.
func (b *RetryBudget) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fichas
}

// Counters devolve quantos retries foram concedidos e negados.
func (b *RetryBudget) Counters() (concedidos, negados int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.concedidos, b.negados
}
