// Package loadtest gera a carga do P03 e junta as duas metades da medição.
//
// A carga é gerada pelo Vegeta, como nos projetos anteriores, e aqui ele é usado
// no modelo ABERTO: a taxa é informada, e o gerador dispara na hora marcada mesmo
// que a resposta anterior ainda não tenha voltado.
//
// Essa escolha é o coração da honestidade deste projeto. No modelo fechado, N
// clientes em laço esperam a resposta para pedir de novo, então um servidor lento
// recebe MENOS requisições e a latência média melhora sozinha. O nome disso é
// coordinated omission: o atraso do sistema medido reduz a frequência das
// amostras e esconde justamente a cauda que se queria medir. Num experimento de
// congestionamento, medir assim seria medir o gerador.
//
// A segunda metade da medição vem dos serviços. O cliente sabe quanto esperou e
// quantos erros recebeu; só o roteador sabe quantas tentativas foram feitas, para
// onde elas foram, quantos retries o orçamento negou e quantas requisições foram
// recusadas na porta. Um relatório com apenas uma das metades sempre tem uma
// pergunta sem resposta.
package loadtest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/promscrape"
)

// Config descreve um cenário de carga.
type Config struct {
	Scenario string

	// BaseURL é o roteador, que é o único endereço que o cliente conhece.
	BaseURL string

	// RouterAdmin, EdgeAdmin e OriginAdmin são de onde vêm as métricas do lado
	// do servidor. Ficam separados do BaseURL porque porta administrativa não se
	// publica junto com a porta de tráfego.
	RouterAdmin string
	EdgeAdmin   map[string]string
	OriginAdmin string

	// Rate é a taxa oferecida, em requisições por segundo. Zero significa "o
	// máximo que os workers conseguirem", que é o modelo fechado e só deve ser
	// usado quando a pergunta for sobre concorrência, não sobre taxa.
	Rate     int
	Workers  int
	Duration time.Duration
	Warmup   time.Duration
	Timeout  time.Duration

	// O catálogo do cenário. Popular concentra a maior parte dos pedidos num
	// punhado de objetos, que é como o tráfego real de conteúdo se comporta, e é
	// o que faz o cache dos edges ter efeito. Tail é a cauda longa, que gera os
	// MISS e mantém a origem no circuito.
	Popular      int
	Tail         int
	PopularShare float64
	Size         string

	// Seed fixa a sequência de objetos. Duas execuções com a mesma semente pedem
	// exatamente os mesmos objetos na mesma ordem, que é o que permite comparar
	// duas políticas de roteamento sem que a diferença venha do sorteio.
	Seed uint64
}

// Validate confere a configuração antes de gastar tempo medindo.
func (c Config) Validate() error {
	switch {
	case c.BaseURL == "":
		return errors.New("BaseURL é obrigatório")
	case c.Workers <= 0:
		return fmt.Errorf("Workers deve ser maior que zero, recebi %d", c.Workers)
	case c.Duration <= 0:
		return fmt.Errorf("Duration deve ser maior que zero, recebi %s", c.Duration)
	case c.Timeout <= 0:
		return fmt.Errorf("Timeout deve ser maior que zero, recebi %s", c.Timeout)
	case c.Popular <= 0 || c.Tail < 0:
		return fmt.Errorf("catálogo inválido: popular=%d tail=%d", c.Popular, c.Tail)
	case c.PopularShare < 0 || c.PopularShare > 1:
		return fmt.Errorf("PopularShare deve ficar entre 0 e 1, recebi %.2f", c.PopularShare)
	}
	return nil
}

// Result é o resumo de um cenário, com as duas metades separadas.
type Result struct {
	Scenario string `json:"scenario"`
	Strategy string `json:"strategy"`

	TargetRate  int     `json:"target_rate"`
	Workers     int     `json:"workers"`
	DurationSec float64 `json:"duration_sec"`

	// --- o que o cliente observou ---

	Offered   uint64 `json:"offered"`
	Completed uint64 `json:"completed"`
	Errors    uint64 `json:"errors"`

	OfferedPerSec   float64 `json:"offered_per_sec"`
	CompletedPerSec float64 `json:"completed_per_sec"`
	ErrorRate       float64 `json:"error_rate"`

	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`

	StatusCodes   map[string]int `json:"status_codes"`
	ErrorMessages []string       `json:"error_messages,omitempty"`

	// --- o que o roteador relatou ---

	// AttemptsByBackend mostra a distribuição de tráfego, que é a decisão da
	// política ficando visível.
	AttemptsByBackend map[string]float64 `json:"attempts_by_backend"`
	// AttemptsByOutcome separa ok, 5xx, timeout e erro de transporte.
	AttemptsByOutcome map[string]float64 `json:"attempts_by_outcome"`
	// FailuresByBackend é quantas tentativas cada destino estragou.
	FailuresByBackend map[string]float64 `json:"failures_by_backend"`

	RetriesGranted float64            `json:"retries_granted"`
	RetriesDenied  float64            `json:"retries_denied"`
	Rejected       map[string]float64 `json:"rejected_by_reason"`
	BudgetTokens   float64            `json:"retry_budget_tokens_end"`

	RouterBytes       float64 `json:"router_bytes"`
	RouterBytesPerSec float64 `json:"router_bytes_per_sec"`
	Goroutines        float64 `json:"router_goroutines_end"`
	OpenFDs           float64 `json:"router_open_fds_end"`

	// --- o que os edges e a origem relataram ---

	EdgeHits    map[string]float64 `json:"edge_hits"`
	EdgeMisses  map[string]float64 `json:"edge_misses"`
	EdgeOrigin  map[string]float64 `json:"edge_origin_fetches"`
	OriginCalls float64            `json:"origin_requests"`
	OriginPeak  float64            `json:"origin_inflight_peak"`
}

// Run executa um cenário e devolve o resumo.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, fmt.Errorf("configuração inválida: %w", err)
	}

	targeter := catalogTargeter(cfg)
	rate := vegeta.Rate{Freq: cfg.Rate, Per: time.Second}

	// Aquecimento: as primeiras requisições pagam abertura de conexão, cache frio
	// nos edges e caminhos do runtime esquentando. Medir isso junto contamina o
	// resultado com custos que não se repetem.
	if cfg.Warmup > 0 {
		warm := newAttacker(cfg)
		for range warm.Attack(targeter, rate, cfg.Warmup, cfg.Scenario+"-warmup") {
		}
		warm.Stop()
	}

	// As leituras "antes" vão depois do aquecimento e imediatamente antes do
	// ataque: tudo que acontecer entre elas e as leituras finais entra na conta.
	antes := scrapeAll(ctx, cfg)

	attacker := newAttacker(cfg)
	var m vegeta.Metrics
	comecou := time.Now()
	for res := range attacker.Attack(targeter, rate, cfg.Duration, cfg.Scenario) {
		m.Add(res)
	}
	m.Close()
	attacker.Stop()
	decorrido := time.Since(comecou)

	depois := scrapeAll(ctx, cfg)

	r := resultFrom(cfg, &m, decorrido)
	fillServerSide(&r, antes, depois, decorrido)
	return r, nil
}

// newAttacker monta o atacante do Vegeta.
//
// Um por fase, nunca reaproveitado: o atacante guarda estado de uma campanha, e
// reusar a mesma instância entre o aquecimento e a medição faz a segunda terminar
// quase imediatamente, com um punhado de amostras.
func newAttacker(cfg Config) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Timeout(cfg.Timeout),
		vegeta.KeepAlive(true),
		// MaxWorkers acima de Workers é o que permite o modelo aberto funcionar:
		// quando o sistema fica lento, o Vegeta precisa poder subir workers para
		// manter a taxa marcada. Fixar os dois no mesmo valor traria de volta o
		// modelo fechado e a omissão coordenada junto.
		vegeta.Workers(uint64(cfg.Workers)),
		vegeta.MaxWorkers(uint64(cfg.Workers*8)),
		// O pool de conexões ociosas tem o mesmo tamanho do teto de conexões, e
		// isso não é detalhe. Com um pool menor, toda conexão acima do pool é
		// FECHADA ao terminar em vez de reaproveitada, cada fechamento deixa uma
		// porta local em TIME_WAIT por dezenas de segundos, e num cenário de
		// milhares de requisições por segundo o gerador esgota as portas efêmeras
		// da máquina. O erro que aparece é "bind: address already in use", e ele é
		// do CLIENTE: com o pool pequeno, o experimento mede o limite do gerador e
		// chama isso de saturação do serviço.
		vegeta.Connections(cfg.Workers*8),
		vegeta.MaxConnections(cfg.Workers*8),
		// O corpo é drenado e descartado: guardar cada objeto recebido faria o
		// gerador alocar mais que os serviços medidos.
		vegeta.MaxBody(0),
	)
}

// catalogTargeter produz a sequência de objetos pedidos.
//
// A distribuição é desigual de propósito. Tráfego de conteúdo real tem um punhado
// de objetos populares respondendo pela maior parte dos pedidos, e é isso que faz
// o cache do edge valer alguma coisa. Uma distribuição uniforme sobre um catálogo
// grande transformaria todo pedido em MISS e o experimento mediria a origem, não
// o roteamento.
func catalogTargeter(cfg Config) vegeta.Targeter {
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	tamanho := cfg.Size
	if tamanho == "" {
		tamanho = "64KiB"
	}
	// PCG com semente explícita: a mesma configuração pede os mesmos objetos na
	// mesma ordem em qualquer máquina, que é o que torna a comparação entre as
	// duas políticas uma comparação de políticas.
	gen := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15))

	return func(t *vegeta.Target) error {
		if t == nil {
			return vegeta.ErrNilTarget
		}
		var nome string
		if gen.Float64() < cfg.PopularShare || cfg.Tail == 0 {
			nome = fmt.Sprintf("pop-%s-%d.bin", tamanho, gen.IntN(cfg.Popular))
		} else {
			nome = fmt.Sprintf("cauda-%s-%d.bin", tamanho, gen.IntN(cfg.Tail))
		}
		t.Method = "GET"
		t.URL = base + "/objects/" + nome
		return nil
	}
}

type snapshots struct {
	router promscrape.Snapshot
	edges  map[string]promscrape.Snapshot
	origin promscrape.Snapshot
	ok     bool
}

func scrapeAll(ctx context.Context, cfg Config) snapshots {
	s := snapshots{edges: map[string]promscrape.Snapshot{}, ok: true}
	if cfg.RouterAdmin != "" {
		snap, err := promscrape.Fetch(ctx, strings.TrimSuffix(cfg.RouterAdmin, "/")+"/metrics")
		if err != nil {
			s.ok = false
		}
		s.router = snap
	}
	for nome, url := range cfg.EdgeAdmin {
		snap, err := promscrape.Fetch(ctx, strings.TrimSuffix(url, "/")+"/metrics")
		if err != nil {
			s.ok = false
			continue
		}
		s.edges[nome] = snap
	}
	if cfg.OriginAdmin != "" {
		snap, err := promscrape.Fetch(ctx, strings.TrimSuffix(cfg.OriginAdmin, "/")+"/metrics")
		if err != nil {
			s.ok = false
		}
		s.origin = snap
	}
	return s
}

func resultFrom(cfg Config, m *vegeta.Metrics, decorrido time.Duration) Result {
	segundos := decorrido.Seconds()
	if segundos <= 0 {
		segundos = 1
	}
	concluidas := uint64(float64(m.Requests) * m.Success)

	return Result{
		Scenario:    cfg.Scenario,
		TargetRate:  cfg.Rate,
		Workers:     cfg.Workers,
		DurationSec: segundos,

		Offered:   m.Requests,
		Completed: concluidas,
		Errors:    m.Requests - concluidas,

		OfferedPerSec:   float64(m.Requests) / segundos,
		CompletedPerSec: float64(concluidas) / segundos,
		ErrorRate:       1 - m.Success,

		P50Ms: millis(m.Latencies.P50),
		P95Ms: millis(m.Latencies.P95),
		P99Ms: millis(m.Latencies.P99),
		MaxMs: millis(m.Latencies.Max),

		StatusCodes:   m.StatusCodes,
		ErrorMessages: limitar(m.Errors, 5),
	}
}

// fillServerSide completa o resultado com o que os serviços relataram.
func fillServerSide(r *Result, antes, depois snapshots, decorrido time.Duration) {
	r.AttemptsByBackend = promscrape.DeltaByLabel(antes.router, depois.router, "router_backend_attempts_total", "backend")
	r.AttemptsByOutcome = promscrape.DeltaByLabel(antes.router, depois.router, "router_backend_attempts_total", "outcome")
	r.Rejected = promscrape.DeltaByLabel(antes.router, depois.router, "router_rejected_total", "reason")

	r.FailuresByBackend = map[string]float64{}
	for backend := range r.AttemptsByBackend {
		var falhas float64
		for _, desfecho := range []string{"5xx", "timeout", "erro", "cancelado"} {
			falhas += promscrape.Delta(antes.router, depois.router, "router_backend_attempts_total",
				map[string]string{"backend": backend, "outcome": desfecho})
		}
		r.FailuresByBackend[backend] = falhas
	}

	retries := promscrape.DeltaByLabel(antes.router, depois.router, "router_retries_total", "decision")
	r.RetriesGranted = retries["concedido"]
	r.RetriesDenied = retries["negado-orcamento"]

	r.BudgetTokens = depois.router.Sum("router_retry_budget_tokens", nil)
	r.RouterBytes = promscrape.Delta(antes.router, depois.router, "router_response_bytes_total", nil)
	if s := decorrido.Seconds(); s > 0 {
		r.RouterBytesPerSec = r.RouterBytes / s
	}
	// Goroutines e descritores são gauges: o que interessa é o valor no fim da
	// janela. Goroutine crescendo sem parar é acúmulo interno; descritor no teto
	// é o primeiro recurso a limitar quem fala com a rede.
	r.Goroutines = depois.router.Sum("go_goroutines", nil)
	r.OpenFDs = depois.router.Sum("process_open_fds", nil)

	r.EdgeHits = map[string]float64{}
	r.EdgeMisses = map[string]float64{}
	r.EdgeOrigin = map[string]float64{}
	for nome, dep := range depois.edges {
		ant := antes.edges[nome]
		r.EdgeHits[nome] = promscrape.Delta(ant, dep, "edge_requests_total", map[string]string{"cache": "HIT"})
		r.EdgeMisses[nome] = promscrape.Delta(ant, dep, "edge_requests_total", map[string]string{"cache": "MISS"})
		r.EdgeOrigin[nome] = promscrape.Delta(ant, dep, "edge_origin_requests_total", nil)
	}

	r.OriginCalls = promscrape.Delta(antes.origin, depois.origin, "origin_requests_total", nil)
	// O pico, e não a leitura instantânea: a avalanche de uma recuperação dura
	// menos de um segundo, e quem chega depois dela encontra zero.
	r.OriginPeak = depois.origin.Sum("origin_inflight_peak", nil)
}

func millis(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

// limitar corta a lista de mensagens de erro.
//
// O Vegeta guarda uma entrada por erro distinto, e num cenário de outage isso
// vira dezenas de linhas quase iguais dentro do metrics.json. As primeiras
// bastam para identificar o tipo de falha.
func limitar(msgs []string, n int) []string {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[:n]
}

// Share transforma contagem por destino em percentual, para a tabela do resumo.
func Share(contagem map[string]float64) map[string]float64 {
	var total float64
	for _, v := range contagem {
		total += v
	}
	out := map[string]float64{}
	if total == 0 {
		return out
	}
	for k, v := range contagem {
		out[k] = v / total * 100
	}
	return out
}
