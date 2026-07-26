// Comando matrix roda a matriz de carga do P03.
//
// São nove cenários, cada um com uma pergunta própria: capacidade sem falha,
// carga sustentada, rajada, estresse, banda limitada, latência, conexão cortada,
// queda e volta. Todos usam a mesma política de roteamento, a mesma semente de
// catálogo e a mesma preparação, então o que muda entre eles é só a condição de
// rede, que é o que se quer comparar.
//
// A matriz não decide qual política é melhor. Essa é a pergunta do comando
// failover, que roda o mesmo roteiro duas vezes.
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

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/evidence"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/labctl"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/loadtest"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/toxi"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:9080", "endereço público do roteador")
	routerAdmin := flag.String("router-admin", "http://127.0.0.1:9090", "administrativa do roteador")
	edgesRaw := flag.String("edges", "edge-a=http://127.0.0.1:9091,edge-b=http://127.0.0.1:9092,edge-c=http://127.0.0.1:9093", "administrativas dos edges")
	originAdmin := flag.String("origin-admin", "http://127.0.0.1:9094", "administrativa da origem")
	toxiAddr := flag.String("toxiproxy", "http://127.0.0.1:8474", "API do Toxiproxy")

	politica := flag.String("strategy", "adaptativa", "política usada em toda a matriz")
	rate := flag.Int("rate", 400, "taxa base, em requisições por segundo")
	duracao := flag.Duration("duration", 20*time.Second, "janela de medição de cada cenário")
	warmup := flag.Duration("warmup", 5*time.Second, "aquecimento antes de cada janela")
	timeout := flag.Duration("timeout", 5*time.Second, "prazo do cliente no gerador")
	workers := flag.Int("workers", 64, "workers iniciais do gerador")
	somente := flag.String("only", "", "roda apenas os cenários listados, separados por vírgula")
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

	cenarios := montarCenarios(*rate)
	if *somente != "" {
		cenarios = filtrar(cenarios, strings.Split(*somente, ","))
		if len(cenarios) == 0 {
			falhar("nenhum cenário casou com -only=%s", *somente)
		}
	}

	doc := evidence.New("matriz-de-carga")
	doc.Commands = []string{
		fmt.Sprintf("go run ./cmd/matrix -strategy=%s -rate=%d -duration=%s -warmup=%s",
			*politica, *rate, *duracao, *warmup),
	}

	var resultados []loadtest.Result
	for _, c := range cenarios {
		fmt.Printf("\n=== %s: %s\n", c.nome, c.pergunta)

		// Toda medição começa do mesmo estado: rede limpa, cache vazio, roteador
		// sem memória do cenário anterior.
		if err := rede.Reset(); err != nil {
			falhar("limpando a rede: %v", err)
		}
		if err := lab.Prepare(ctx, *politica); err != nil {
			falhar("preparando o ambiente: %v", err)
		}
		if err := lab.OriginFail(ctx, 0); err != nil {
			falhar("religando a origem: %v", err)
		}

		if c.antes != nil {
			if err := c.antes(ctx, rede, lab); err != nil {
				falhar("aplicando a condição de %s: %v", c.nome, err)
			}
		}

		cfg := loadtest.Config{
			Scenario:     c.nome,
			BaseURL:      *base,
			RouterAdmin:  *routerAdmin,
			EdgeAdmin:    edges,
			OriginAdmin:  *originAdmin,
			Rate:         c.rate,
			Workers:      *workers,
			Duration:     c.duracao(*duracao),
			Warmup:       *warmup,
			Timeout:      *timeout,
			Popular:      10,
			Tail:         200,
			PopularShare: 0.8,
			Size:         c.tamanho,
			// A mesma semente em todos os cenários: a sequência de objetos pedidos
			// é idêntica, e a diferença entre duas linhas da tabela é a condição de
			// rede, não o sorteio.
			Seed: 42,
		}

		// Alguns cenários mudam a condição no meio da janela. A mudança roda numa
		// goroutine própria porque a medição não pode parar para esperar por ela.
		if c.durante != nil {
			go c.durante(ctx, rede, lab, cfg.Duration)
		}

		r, err := loadtest.Run(ctx, cfg)
		if err != nil {
			falhar("cenário %s: %v", c.nome, err)
		}
		r.Strategy = *politica
		resultados = append(resultados, r)
		imprimir(r)
	}

	// A rede volta ao normal no fim: deixar toxina de pé é a forma mais rápida de
	// fazer o próximo experimento produzir um número inexplicável.
	if err := rede.Reset(); err != nil {
		fmt.Fprintf(os.Stderr, "aviso: não consegui limpar a rede: %v\n", err)
	}
	if err := lab.OriginFail(ctx, 0); err != nil {
		fmt.Fprintf(os.Stderr, "aviso: não consegui religar a origem: %v\n", err)
	}

	if estado, err := rede.Describe(); err == nil {
		doc.Network = estado
	}
	doc.Data = map[string]any{"strategy": *politica, "base_rate": *rate, "results": resultados}
	doc.Summary = resumo(*politica, *rate, cenarios, resultados)
	doc.Notes = notas
	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		falhar("gravando evidência: %v", err)
	}
	fmt.Printf("\nevidência em %s\n", dir)
}

// cenario descreve uma linha da matriz.
type cenario struct {
	nome     string
	pergunta string
	rate     int
	tamanho  string
	fator    float64 // multiplica a janela padrão; 0 usa a janela inteira

	// antes aplica a condição de rede antes de medir.
	antes func(context.Context, *toxi.Client, *labctl.Lab) error
	// durante muda a condição no meio da janela.
	durante func(context.Context, *toxi.Client, *labctl.Lab, time.Duration)
}

func (c cenario) duracao(padrao time.Duration) time.Duration {
	if c.fator <= 0 {
		return padrao
	}
	return time.Duration(float64(padrao) * c.fator)
}

func montarCenarios(base int) []cenario {
	return []cenario{
		{
			nome:     "baseline",
			pergunta: "qual a latência e o throughput sem nenhuma falha",
			rate:     base / 4,
			tamanho:  "64KiB",
		},
		{
			nome:     "load",
			pergunta: "o sistema sustenta a taxa alvo abaixo da saturação",
			rate:     base,
			tamanho:  "64KiB",
		},
		{
			nome:     "spike",
			pergunta: "o que a rajada provoca, e o backpressure aparece",
			rate:     base * 6,
			tamanho:  "64KiB",
			// Rajada é curta por definição. Uma "rajada" de vinte segundos é
			// carga sustentada com outro nome.
			fator: 0.25,
		},
		{
			nome:     "stress",
			pergunta: "qual recurso limita primeiro quando a taxa passa do teto",
			rate:     base * 10,
			tamanho:  "64KiB",
			fator:    0.5,
		},
		{
			nome:     "bandwidth",
			pergunta: "o que acontece quando o link de um edge fica estreito",
			rate:     base / 2,
			// Objeto maior de propósito: banda limitada quase não aparece em
			// objeto de 1KiB, porque o gargalo continua sendo o custo por
			// requisição.
			tamanho: "256KiB",
			antes: func(_ context.Context, rede *toxi.Client, _ *labctl.Lab) error {
				return rede.Bandwidth("edge-a", 512)
			},
		},
		{
			nome:     "latency",
			pergunta: "um edge saudável e lento continua recebendo a mesma carga",
			rate:     base,
			tamanho:  "64KiB",
			antes: func(_ context.Context, rede *toxi.Client, _ *labctl.Lab) error {
				return rede.Latency("edge-a", 300*time.Millisecond, 50*time.Millisecond)
			},
		},
		{
			nome:     "conexao-cortada",
			pergunta: "timeout, retry e recuperação quando a conexão cai no meio",
			rate:     base,
			tamanho:  "64KiB",
			antes: func(_ context.Context, rede *toxi.Client, _ *labctl.Lab) error {
				// Trinta por cento das conexões com edge-a são cortadas depois de
				// 100ms. Não é perda de pacote; é o que o Toxiproxy sabe fazer, e
				// o relatório usa o nome do mecanismo.
				return rede.ResetPeer("edge-a", 0.3, 100*time.Millisecond)
			},
		},
		{
			nome:     "outage",
			pergunta: "um edge fora do ar, e depois a origem também",
			rate:     base,
			tamanho:  "64KiB",
			antes: func(_ context.Context, rede *toxi.Client, _ *labctl.Lab) error {
				return rede.Down("edge-a")
			},
			durante: func(ctx context.Context, _ *toxi.Client, lab *labctl.Lab, janela time.Duration) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(janela / 2):
				}
				// Metade da janela com um edge fora; a outra metade com a origem
				// fora também. É a diferença entre perder capacidade e perder a
				// fonte do conteúdo: o cache dos edges saudáveis ainda responde.
				_ = lab.OriginFail(ctx, 503)
			},
		},
		{
			nome:     "recovery",
			pergunta: "a volta causa nova avalanche na origem",
			rate:     base,
			tamanho:  "64KiB",
			antes: func(ctx context.Context, rede *toxi.Client, lab *labctl.Lab) error {
				if err := rede.Down("edge-a"); err != nil {
					return err
				}
				return lab.OriginFail(ctx, 503)
			},
			durante: func(ctx context.Context, rede *toxi.Client, lab *labctl.Lab, janela time.Duration) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(janela / 3):
				}
				// Tudo volta ao mesmo tempo, que é o pior caso: os três edges
				// estão com cache frio e a origem acabou de acordar.
				_ = rede.Up("edge-a")
				_ = lab.OriginFail(ctx, 0)
			},
		},
	}
}

func filtrar(todos []cenario, nomes []string) []cenario {
	quer := map[string]bool{}
	for _, n := range nomes {
		quer[strings.TrimSpace(n)] = true
	}
	var out []cenario
	for _, c := range todos {
		if quer[c.nome] {
			out = append(out, c)
		}
	}
	return out
}

func imprimir(r loadtest.Result) {
	fmt.Printf("  oferecida %.0f/s, concluída %.0f/s, erro %.2f%%, p50 %.1fms, p99 %.1fms\n",
		r.OfferedPerSec, r.CompletedPerSec, r.ErrorRate*100, r.P50Ms, r.P99Ms)
	fmt.Printf("  distribuição %s\n", formatarShare(r.AttemptsByBackend))
	fmt.Printf("  retries concedidos %.0f, negados %.0f, recusas %v\n",
		r.RetriesGranted, r.RetriesDenied, r.Rejected)
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

func resumo(politica string, base int, cenarios []cenario, resultados []loadtest.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Matriz de carga (política %s)\n\n", politica)
	fmt.Fprintf(&b, "Taxa base %d req/s. Gerador em modelo aberto: a taxa é disparada na hora\n", base)
	fmt.Fprintf(&b, "marcada, mesmo que a resposta anterior não tenha voltado.\n\n")

	fmt.Fprintf(&b, "## O que o cliente observou\n\n")
	fmt.Fprintf(&b, "| Cenário | Alvo req/s | Oferecida/s | Concluída/s | Erro | p50 ms | p95 ms | p99 ms | max ms |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range resultados {
		fmt.Fprintf(&b, "| %s | %d | %.0f | %.0f | %.2f%% | %.1f | %.1f | %.1f | %.1f |\n",
			r.Scenario, r.TargetRate, r.OfferedPerSec, r.CompletedPerSec, r.ErrorRate*100,
			r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs)
	}

	fmt.Fprintf(&b, "\n## O que o roteador relatou\n\n")
	fmt.Fprintf(&b, "| Cenário | Distribuição das tentativas | Retry concedido | Retry negado | Recusado | Goroutines | Descritores |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---|---:|---:|\n")
	for _, r := range resultados {
		fmt.Fprintf(&b, "| %s | %s | %.0f | %.0f | %s | %.0f | %.0f |\n",
			r.Scenario, formatarShare(r.AttemptsByBackend),
			r.RetriesGranted, r.RetriesDenied, formatarMapa(r.Rejected),
			r.Goroutines, r.OpenFDs)
	}

	fmt.Fprintf(&b, "\n## O que os edges e a origem relataram\n\n")
	fmt.Fprintf(&b, "| Cenário | HIT | MISS | Buscas na origem | Requisições na origem | Simultaneidade na origem no fim |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|\n")
	for _, r := range resultados {
		fmt.Fprintf(&b, "| %s | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
			r.Scenario, somar(r.EdgeHits), somar(r.EdgeMisses), somar(r.EdgeOrigin),
			r.OriginCalls, r.OriginPeak)
	}

	fmt.Fprintf(&b, "\n## A pergunta de cada cenário\n\n")
	for _, c := range cenarios {
		fmt.Fprintf(&b, "- **%s**: %s\n", c.nome, c.pergunta)
	}
	return b.String()
}

func formatarMapa(m map[string]float64) string {
	if len(m) == 0 {
		return "nenhum"
	}
	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	partes := make([]string, 0, len(chaves))
	for _, k := range chaves {
		partes = append(partes, fmt.Sprintf("%s %.0f", k, m[k]))
	}
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

const notas = `O gerador de carga divide a máquina com os containers medidos. Parte da
latência da cauda é disputa por CPU entre quem mede e quem é medido, e não
capacidade do serviço.

O cenário chamado "conexao-cortada" corta conexões TCP com probabilidade; ele
não descarta pacotes IP. O Toxiproxy trabalha acima da camada de transporte, e
usar o nome "perda de pacotes" aqui seria impreciso.`
