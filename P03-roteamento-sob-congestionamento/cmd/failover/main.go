// Comando failover roda a falha controlada do P03.
//
// O roteiro é sempre o mesmo: edge-a recebe latência crescente e depois passa a
// ter conexões cortadas, enquanto edge-b e edge-c continuam saudáveis. O roteiro
// roda duas vezes, uma com round-robin e uma com a política adaptativa, no mesmo
// processo e nos mesmos containers.
//
// O critério de sucesso não é "a adaptativa ganhou". É se ela preservou o sistema
// inteiro: deslocar toda a carga para os dois edges saudáveis e derrubar os dois
// no lugar do primeiro seria falha, não vitória. Por isso o relatório mede também
// o que aconteceu com quem estava bem.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/evidence"
	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/labctl"
	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/loadtest"
	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/toxi"
)

// doente é o edge que recebe a degradação. Os outros dois são o controle.
const doente = "edge-a"

func main() {
	base := flag.String("base", "http://127.0.0.1:9080", "endereço público do roteador")
	routerAdmin := flag.String("router-admin", "http://127.0.0.1:9090", "administrativa do roteador")
	edgesRaw := flag.String("edges", "edge-a=http://127.0.0.1:9091,edge-b=http://127.0.0.1:9092,edge-c=http://127.0.0.1:9093", "administrativas dos edges")
	originAdmin := flag.String("origin-admin", "http://127.0.0.1:9094", "administrativa da origem")
	toxiAddr := flag.String("toxiproxy", "http://127.0.0.1:8474", "API do Toxiproxy")

	rate := flag.Int("rate", 400, "taxa oferecida, em requisições por segundo")
	fase := flag.Duration("phase", 20*time.Second, "janela de medição de cada fase")
	warmup := flag.Duration("warmup", 3*time.Second, "aquecimento antes de cada fase")
	timeout := flag.Duration("timeout", 5*time.Second, "prazo do cliente no gerador")
	workers := flag.Int("workers", 64, "workers iniciais do gerador")
	evidenceDir := flag.String("evidence", "evidence", "diretório raiz da evidência")
	flag.Parse()

	edges, err := parseMap(*edgesRaw)
	if err != nil {
		falhar("lista de edges inválida: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lab := labctl.New(*routerAdmin, edges, *originAdmin)
	if err := lab.WaitReady(ctx, 30*time.Second); err != nil {
		falhar("ambiente não está pronto: %v", err)
	}
	rede := toxi.New(*toxiAddr)

	politicas := []string{"round-robin", "adaptativa"}
	fases := montarFases()

	execucoes := map[string][]execucao{}
	for _, politica := range politicas {
		fmt.Printf("\n########## política: %s\n", politica)

		if err := rede.Reset(); err != nil {
			falhar("limpando a rede: %v", err)
		}
		// A preparação acontece uma vez por política, e não por fase: o que a
		// política aprendeu na fase anterior faz parte do que se quer observar.
		if err := lab.Prepare(ctx, politica); err != nil {
			falhar("preparando o ambiente: %v", err)
		}

		for _, f := range fases {
			fmt.Printf("\n=== %s: %s\n", f.nome, f.descricao)
			if err := f.aplicar(rede); err != nil {
				falhar("aplicando %s: %v", f.nome, err)
			}

			// A amostragem do estado do roteador roda durante a janela inteira,
			// inclusive o aquecimento: é durante o aquecimento que a política
			// percebe a degradação recém-aplicada.
			amostraCtx, pararAmostra := context.WithCancel(ctx)
			fotosCh := make(chan []labctl.RouterSnapshot, 1)
			go func() { fotosCh <- lab.Sample(amostraCtx, 250*time.Millisecond) }()

			r, err := loadtest.Run(ctx, loadtest.Config{
				Scenario:     f.nome,
				BaseURL:      *base,
				RouterAdmin:  *routerAdmin,
				EdgeAdmin:    edges,
				OriginAdmin:  *originAdmin,
				Rate:         *rate,
				Workers:      *workers,
				Duration:     *fase,
				Warmup:       *warmup,
				Timeout:      *timeout,
				Popular:      10,
				Tail:         200,
				PopularShare: 0.8,
				Size:         "64KiB",
				// Mesma semente nas duas políticas: os mesmos objetos, na mesma
				// ordem, com a mesma degradação. O que sobra de diferença é a
				// política.
				Seed: 42,
			})
			pararAmostra()
			fotos := <-fotosCh
			if err != nil {
				falhar("fase %s: %v", f.nome, err)
			}
			r.Strategy = politica

			execucoes[politica] = append(execucoes[politica], execucao{
				Fase:      f.nome,
				Resultado: r,
				Deteccao:  deteccao(fotos),
				Fotos:     resumirFotos(fotos),
			})
			imprimir(r)
		}
	}

	if err := rede.Reset(); err != nil {
		fmt.Fprintf(os.Stderr, "aviso: não consegui limpar a rede: %v\n", err)
	}

	doc := evidence.New("falha-controlada")
	doc.Commands = []string{
		fmt.Sprintf("go run ./cmd/failover -rate=%d -phase=%s -warmup=%s", *rate, *fase, *warmup),
	}
	if estado, err := rede.Describe(); err == nil {
		doc.Network = estado
	}
	doc.Data = map[string]any{"rate": *rate, "phase": fase.String(), "runs": execucoes}
	doc.Summary = resumo(*rate, *fase, fases, politicas, execucoes)
	doc.Notes = notas
	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		falhar("gravando evidência: %v", err)
	}
	fmt.Printf("\nevidência em %s\n", dir)
}

// execucao é uma fase rodada com uma política.
type execucao struct {
	Fase      string          `json:"fase"`
	Resultado loadtest.Result `json:"resultado"`
	Deteccao  *float64        `json:"deteccao_seg"`
	Fotos     []fotoResumida  `json:"fotos"`
}

// fotoResumida é o estado do roteador num instante, já reduzido ao que a
// evidência precisa. Guardar a foto inteira a cada 250ms encheria o metrics.json
// de repetição sem acrescentar informação.
type fotoResumida struct {
	Segundo   float64            `json:"t"`
	Share     map[string]float64 `json:"share_pct"`
	CostRatio float64            `json:"custo_doente_sobre_saudavel"`
	Open      bool               `json:"disjuntor_aberto"`
}

// fase é um degrau do roteiro.
type fase struct {
	nome      string
	descricao string
	aplicar   func(*toxi.Client) error
}

func montarFases() []fase {
	return []fase{
		{
			nome:      "sem-falha",
			descricao: "os três edges saudáveis, para ter a linha de base",
			aplicar:   func(rede *toxi.Client) error { return rede.Clear(doente) },
		},
		{
			nome:      "latencia-50ms",
			descricao: "edge-a começa a ficar lento, ainda respondendo tudo",
			aplicar:   func(rede *toxi.Client) error { return rede.Latency(doente, 50*time.Millisecond, 10*time.Millisecond) },
		},
		{
			nome:      "latencia-200ms",
			descricao: "edge-a piora, e o prazo por tentativa começa a apertar",
			aplicar:   func(rede *toxi.Client) error { return rede.Latency(doente, 200*time.Millisecond, 30*time.Millisecond) },
		},
		{
			nome:      "latencia-800ms",
			descricao: "edge-a passa do prazo de uma tentativa, sem nunca ter caído",
			aplicar:   func(rede *toxi.Client) error { return rede.Latency(doente, 800*time.Millisecond, 100*time.Millisecond) },
		},
		{
			nome:      "conexao-cortada",
			descricao: "além de lento, edge-a passa a cortar 30% das conexões",
			aplicar: func(rede *toxi.Client) error {
				if err := rede.Latency(doente, 800*time.Millisecond, 100*time.Millisecond); err != nil {
					return err
				}
				return rede.ResetPeer(doente, 0.3, 100*time.Millisecond)
			},
		},
		{
			nome:      "recuperacao",
			descricao: "edge-a volta ao normal, e a pergunta é quanto tempo até ser usado de novo",
			aplicar:   func(rede *toxi.Client) error { return rede.Clear(doente) },
		},
	}
}

// deteccao é quantos segundos a política levou para tirar a maior parte da carga
// do edge doente, contados do início da amostragem.
//
// O critério é a fatia do doente nas tentativas do último intervalo ficar abaixo
// de 15% em três amostras seguidas. Com três destinos, o rodízio dá 33% a cada
// um, então 15% significa que a política está evitando ativamente aquele
// caminho. Round-robin nunca chega lá, e é isso que o campo vazio conta.
//
// A exigência de três amostras seguidas não é preciosismo: numa janela de 250ms
// o sorteio da política adaptativa produz oscilação, e uma amostra isolada
// abaixo do corte acontece mesmo com os três destinos saudáveis. A primeira
// versão deste cálculo aceitava uma amostra só e relatou "detecção em 2,5s" numa
// fase em que não havia nada para detectar.
func deteccao(fotos []labctl.RouterSnapshot) *float64 {
	if len(fotos) < 4 {
		return nil
	}
	const confirmacoes = 3
	seguidas := 0
	inicio := fotos[0].At
	anterior := fotos[0]
	for _, f := range fotos[1:] {
		var total, doenteDelta float64
		for _, b := range f.Backends {
			antes := tentativasDe(anterior, b.Name)
			delta := float64(b.Attempts - antes)
			if delta < 0 {
				delta = 0
			}
			total += delta
			if b.Name == doente {
				doenteDelta = delta
			}
		}
		anterior = f
		if total < 10 {
			// Intervalo com pouquíssimo tráfego: a fatia calculada em cima de
			// três ou quatro tentativas é ruído, não decisão.
			continue
		}
		if doenteDelta/total >= 0.15 {
			seguidas = 0
			continue
		}
		seguidas++
		if seguidas >= confirmacoes {
			segundos := f.At.Sub(inicio).Seconds()
			return &segundos
		}
	}
	return nil
}

func tentativasDe(f labctl.RouterSnapshot, nome string) int64 {
	for _, b := range f.Backends {
		if b.Name == nome {
			return b.Attempts
		}
	}
	return 0
}

func resumirFotos(fotos []labctl.RouterSnapshot) []fotoResumida {
	if len(fotos) < 2 {
		return nil
	}
	inicio := fotos[0].At
	out := make([]fotoResumida, 0, len(fotos)-1)
	anterior := fotos[0]
	for _, f := range fotos[1:] {
		share := map[string]float64{}
		var total float64
		deltas := map[string]float64{}
		var custoDoente, custoSaudavel float64
		var saudaveis int
		aberto := false
		for _, b := range f.Backends {
			d := float64(b.Attempts - tentativasDe(anterior, b.Name))
			if d < 0 {
				d = 0
			}
			deltas[b.Name] = d
			total += d
			if b.Name == doente {
				custoDoente = b.Cost
				aberto = b.Open
			} else {
				custoSaudavel += b.Cost
				saudaveis++
			}
		}
		for nome, d := range deltas {
			if total > 0 {
				share[nome] = d / total * 100
			}
		}
		razao := 0.0
		if saudaveis > 0 && custoSaudavel > 0 {
			razao = custoDoente / (custoSaudavel / float64(saudaveis))
		}
		out = append(out, fotoResumida{
			Segundo:   f.At.Sub(inicio).Seconds(),
			Share:     share,
			CostRatio: razao,
			Open:      aberto,
		})
		anterior = f
	}
	return out
}

func imprimir(r loadtest.Result) {
	fmt.Printf("  concluída %.0f/s, erro %.2f%%, p50 %.1fms, p99 %.1fms, max %.1fms\n",
		r.CompletedPerSec, r.ErrorRate*100, r.P50Ms, r.P99Ms, r.MaxMs)
	fmt.Printf("  distribuição %s\n", formatarShare(r.AttemptsByBackend))
	fmt.Printf("  retries concedidos %.0f, negados %.0f\n", r.RetriesGranted, r.RetriesDenied)
}

func resumo(rate int, janela time.Duration, fases []fase, politicas []string, execucoes map[string][]execucao) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Falha controlada: round-robin contra a política adaptativa\n\n")
	fmt.Fprintf(&b, "Taxa oferecida %d req/s, janela de %s por fase, gerador em modelo aberto.\n", rate, janela)
	fmt.Fprintf(&b, "O edge degradado é o %s; edge-b e edge-c ficam saudáveis o tempo todo.\n\n", doente)

	fmt.Fprintf(&b, "## Fases do roteiro\n\n")
	for _, f := range fases {
		fmt.Fprintf(&b, "- **%s**: %s\n", f.nome, f.descricao)
	}

	fmt.Fprintf(&b, "\n## O que o cliente observou\n\n")
	fmt.Fprintf(&b, "| Fase | Política | Concluída/s | Erro | p50 ms | p95 ms | p99 ms | max ms |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, f := range fases {
		for _, p := range politicas {
			e, ok := acharFase(execucoes[p], f.nome)
			if !ok {
				continue
			}
			r := e.Resultado
			fmt.Fprintf(&b, "| %s | %s | %.0f | %.2f%% | %.1f | %.1f | %.1f | %.1f |\n",
				f.nome, p, r.CompletedPerSec, r.ErrorRate*100, r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs)
		}
	}

	fmt.Fprintf(&b, "\n## Para onde o tráfego foi, e o que custou\n\n")
	fmt.Fprintf(&b, "| Fase | Política | Distribuição das tentativas | Falhas no doente | Retry concedido | Retry negado | Detecção |\n")
	fmt.Fprintf(&b, "|---|---|---|---:|---:|---:|---:|\n")
	for _, f := range fases {
		for _, p := range politicas {
			e, ok := acharFase(execucoes[p], f.nome)
			if !ok {
				continue
			}
			r := e.Resultado
			det := "não detectou"
			if e.Deteccao != nil {
				det = fmt.Sprintf("%.1fs", *e.Deteccao)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.0f | %.0f | %.0f | %s |\n",
				f.nome, p, formatarShare(r.AttemptsByBackend), r.FailuresByBackend[doente],
				r.RetriesGranted, r.RetriesDenied, det)
		}
	}

	fmt.Fprintf(&b, "\n## O que aconteceu com quem estava saudável\n\n")
	fmt.Fprintf(&b, "A política adaptativa só é melhor se preservar o sistema inteiro. Deslocar\n")
	fmt.Fprintf(&b, "toda a carga e derrubar os edges restantes seria falha, não vitória.\n\n")
	fmt.Fprintf(&b, "| Fase | Política | HIT nos edges | MISS nos edges | Buscas na origem | Simultaneidade na origem | Goroutines no roteador |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---:|---:|---:|\n")
	for _, f := range fases {
		for _, p := range politicas {
			e, ok := acharFase(execucoes[p], f.nome)
			if !ok {
				continue
			}
			r := e.Resultado
			fmt.Fprintf(&b, "| %s | %s | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
				f.nome, p, somar(r.EdgeHits), somar(r.EdgeMisses), somar(r.EdgeOrigin),
				r.OriginPeak, r.Goroutines)
		}
	}
	return b.String()
}

func acharFase(execs []execucao, nome string) (execucao, bool) {
	for _, e := range execs {
		if e.Fase == nome {
			return e, true
		}
	}
	return execucao{}, false
}

func formatarShare(contagem map[string]float64) string {
	share := loadtest.Share(contagem)
	nomes := make([]string, 0, len(share))
	for n := range share {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	partes := make([]string, 0, len(nomes))
	for _, n := range nomes {
		partes = append(partes, fmt.Sprintf("%s %.1f%%", n, share[n]))
	}
	// A separação é por vírgula, e não por barra vertical: a distribuição entra
	// numa célula de tabela markdown, e uma barra ali dentro parte a linha em
	// colunas novas. A primeira versão do relatório saiu com as colunas
	// desalinhadas exatamente por isso.
	return strings.Join(partes, ", ")
}

func somar(m map[string]float64) float64 {
	var total float64
	for _, v := range m {
		total += v
	}
	return total
}

func parseMap(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		nome, url, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || nome == "" || url == "" {
			return nil, fmt.Errorf("item inválido %q: use nome=url", item)
		}
		out[nome] = url
	}
	return out, nil
}

func falhar(formato string, args ...any) {
	fmt.Fprintf(os.Stderr, formato+"\n", args...)
	os.Exit(1)
}

const notas = `As duas políticas rodam no mesmo processo e nos mesmos containers, com a mesma
semente de catálogo e a mesma sequência de degradação. O roteador é zerado entre
as políticas, mas não entre as fases: o que a política aprendeu na fase anterior
faz parte do comportamento que se quer observar.

O tempo de detecção é aproximado por amostragem a cada 250ms do estado do
roteador, e mede quando a fatia do edge doente caiu abaixo de 15% das tentativas.
Ele não é o instante exato da primeira decisão diferente.

O gerador divide a máquina com os containers medidos.`
