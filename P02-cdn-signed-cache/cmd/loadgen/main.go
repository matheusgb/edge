// Comando loadgen mede o hit ratio da CDN e o alívio da origem.
//
// A carga é gerada pelo Vegeta, não por um gerador escrito aqui. O motivo é
// conceitual, não de preguiça: o Vegeta ataca a uma TAXA CONSTANTE, disparando a
// próxima requisição na hora marcada mesmo que a anterior ainda não tenha
// respondido. Um gerador ingênuo, com N clientes em laço, faz o contrário: se o
// servidor fica lento, ele naturalmente pede menos, e a lentidão some da
// medição. Esse viés tem nome, coordinated omission, e é o erro mais comum em
// medição caseira de latência.
//
// A distribuição de acesso é desigual de propósito. Tráfego real de conteúdo é
// assim: um punhado de objetos populares responde pela maior parte das
// requisições. Um cache medido com tráfego uniforme mostra um hit ratio que
// nenhuma CDN real teria.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/cdnclient"
	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/evidence"
	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/promscrape"
)

// Result é o que vai para metrics.json.
type Result struct {
	Cenario          string             `json:"cenario"`
	Prefixo          string             `json:"prefixo"`
	TaxaAlvo         int                `json:"taxa_alvo_por_segundo"`
	Duracao          string             `json:"duracao"`
	TTLDaOrigem      string             `json:"ttl_anunciado_pela_origem"`
	Objetos          int                `json:"objetos_distintos"`
	Populares        int                `json:"objetos_populares"`
	FatiaPopular     float64            `json:"fatia_do_trafego_nos_populares"`
	Oferecidas       uint64             `json:"requisicoes_oferecidas"`
	Concluidas       uint64             `json:"requisicoes_concluidas"`
	TaxaSucesso      float64            `json:"taxa_de_sucesso"`
	Codigos          map[string]int     `json:"codigos_http"`
	P50Ms            float64            `json:"p50_ms"`
	P95Ms            float64            `json:"p95_ms"`
	P99Ms            float64            `json:"p99_ms"`
	MaxMs            float64            `json:"max_ms"`
	VazaoPorSegundo  float64            `json:"vazao_por_segundo"`
	BytesRecebidos   float64            `json:"bytes_recebidos"`
	StatusDeCache    map[string]float64 `json:"status_de_cache"`
	HitRatio         float64            `json:"hit_ratio"`
	OrigemRequisicao int64              `json:"requisicoes_na_origem"`
	OrigemCorpos     int64              `json:"corpos_servidos_pela_origem"`
	OrigemRevalidada int64              `json:"revalidacoes_na_origem"`
	OrigemBytes      int64              `json:"bytes_servidos_pela_origem"`
	AlivioDaOrigem   float64            `json:"alivio_da_origem"`
	Erros            []string           `json:"erros"`
}

func main() {
	cdnURL := flag.String("cdn", "http://127.0.0.1:8080", "URL da CDN")
	tokendURL := flag.String("tokend", "http://127.0.0.1:8082", "URL do serviço de token")
	cdnMetrics := flag.String("cdn-metrics", "http://127.0.0.1:8093/metrics", "métricas derivadas do log da CDN")
	originAdmin := flag.String("origin-admin", "http://127.0.0.1:8090", "porta administrativa da origem")
	prefix := flag.String("prefix", "/objects/", "prefixo do caminho na CDN")
	rate := flag.Int("rate", 400, "requisições por segundo oferecidas")
	duration := flag.Duration("duration", 20*time.Second, "duração da medição")
	warmup := flag.Duration("warmup", 3*time.Second, "aquecimento descartado da medição")
	objectCount := flag.Int("objects", 40, "quantidade de objetos distintos no catálogo sintético")
	popular := flag.Int("popular", 4, "quantos objetos concentram a maior parte do tráfego")
	popularShare := flag.Float64("popular-share", 0.8, "fatia do tráfego que vai para os populares")
	size := flag.String("size", "64KiB", "tamanho declarado no nome dos objetos")
	ttl := flag.Duration("token-ttl", 5*time.Minute, "validade dos tokens emitidos")
	tag := flag.String("tag", "", "sufixo dos nomes; vazio gera um a partir do relógio")
	scenario := flag.String("scenario", "hit-ratio", "nome do cenário na pasta de evidência")
	evidenceDir := flag.String("evidence", "evidence", "raiz da pasta de evidência")
	timeout := flag.Duration("timeout", 20*time.Second, "timeout por requisição")
	originMaxAge := flag.Duration("origin-max-age", 0, "se > 0, ajusta o TTL anunciado pela origem antes de medir")
	flag.Parse()

	ctx := context.Background()
	if *tag == "" {
		// O sufixo muda a cada execução para garantir que o cache começa vazio
		// para estes objetos. Sem isso, a segunda rodada mediria um cache já
		// aquecido pela primeira e inflaria o hit ratio.
		*tag = fmt.Sprintf("r%d", time.Now().Unix())
	}

	client := cdnclient.New(*cdnURL, *tokendURL, *rate)

	// O TTL é variável do experimento, não configuração de ambiente. É ele que
	// decide quantas vezes cada objeto volta à origem durante a janela medida, e
	// deixá-lo fixo esconderia o principal trade-off de um cache: TTL longo dá
	// hit ratio alto e conteúdo velho; TTL curto dá o contrário.
	if *originMaxAge > 0 {
		if err := cdnclient.SetOriginMaxAge(ctx, *originAdmin, *originMaxAge); err != nil {
			fmt.Fprintln(os.Stderr, "erro ajustando o TTL da origem:", err)
			os.Exit(1)
		}
	}

	// Os tokens são emitidos ANTES da medição, uma vez por objeto. Assim o tempo
	// medido é o da CDN, não o do serviço de token. O custo de validar continua
	// dentro da medição, porque a CDN consulta o validador a cada requisição.
	targets, err := buildTargets(ctx, client, *prefix, *size, *tag, *objectCount, *ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro preparando os alvos:", err)
		os.Exit(1)
	}

	targeter := skewedTargeter(targets, *popular, *popularShare)

	// Um atacante por fase. O Vegeta guarda estado interno de uma campanha, e
	// reaproveitar a mesma instância entre o aquecimento e a medição fazia a
	// segunda campanha terminar quase imediatamente, com um punhado de amostras.
	novoAtacante := func() *vegeta.Attacker {
		return vegeta.NewAttacker(
			vegeta.Timeout(*timeout),
			vegeta.KeepAlive(true),
			vegeta.Connections(*rate),
			vegeta.MaxWorkers(uint64(*rate)),
		)
	}

	if *warmup > 0 {
		fmt.Printf("aquecendo por %s...\n", *warmup)
		aquecimento := novoAtacante()
		for range aquecimento.Attack(targeter, vegeta.Rate{Freq: *rate, Per: time.Second}, *warmup, "warmup") {
		}
		aquecimento.Stop()
	}

	// As duas leituras de "antes" precisam ser feitas depois do aquecimento e
	// imediatamente antes do ataque: tudo que acontecer entre elas e o fim da
	// medição entra na conta.
	cdnBefore, err := promscrape.Fetch(ctx, *cdnMetrics)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro lendo métricas da CDN:", err)
		os.Exit(1)
	}
	originBefore, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro lendo contadores da origem:", err)
		os.Exit(1)
	}

	fmt.Printf("medindo %s a %d req/s em %s%s...\n", *duration, *rate, *cdnURL, *prefix)
	atacante := novoAtacante()
	var m vegeta.Metrics
	for res := range atacante.Attack(targeter, vegeta.Rate{Freq: *rate, Per: time.Second}, *duration, *scenario) {
		m.Add(res)
	}
	m.Close()
	atacante.Stop()

	// O exporter lê o log da CDN com um pequeno atraso. Sem esta pausa, as
	// últimas respostas ainda não teriam virado métrica e o hit ratio sairia
	// contando menos requisições do que o cliente realmente fez.
	time.Sleep(2 * time.Second)

	cdnAfter, err := promscrape.Fetch(ctx, *cdnMetrics)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro lendo métricas da CDN:", err)
		os.Exit(1)
	}
	originAfter, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro lendo contadores da origem:", err)
		os.Exit(1)
	}

	cacheStatus := promscrape.DeltaByLabel(cdnBefore, cdnAfter, "cdn_responses_total", "cache")
	result := Result{
		Cenario:          *scenario,
		Prefixo:          *prefix,
		TaxaAlvo:         *rate,
		Duracao:          duration.String(),
		TTLDaOrigem:      ttlLabel(*originMaxAge),
		Objetos:          len(targets),
		Populares:        *popular,
		FatiaPopular:     *popularShare,
		Oferecidas:       m.Requests,
		Concluidas:       uint64(float64(m.Requests) * m.Success),
		TaxaSucesso:      m.Success,
		Codigos:          m.StatusCodes,
		P50Ms:            float64(m.Latencies.P50) / 1e6,
		P95Ms:            float64(m.Latencies.P95) / 1e6,
		P99Ms:            float64(m.Latencies.P99) / 1e6,
		MaxMs:            float64(m.Latencies.Max) / 1e6,
		VazaoPorSegundo:  m.Throughput,
		BytesRecebidos:   float64(m.BytesIn.Total),
		StatusDeCache:    cacheStatus,
		OrigemRequisicao: originAfter.Requests - originBefore.Requests,
		OrigemCorpos:     originAfter.Bodies - originBefore.Bodies,
		OrigemRevalidada: originAfter.Revalidations - originBefore.Revalidations,
		OrigemBytes:      originAfter.Bytes - originBefore.Bytes,
		Erros:            m.Errors,
	}
	result.HitRatio = hitRatio(cacheStatus)
	if result.Oferecidas > 0 {
		result.AlivioDaOrigem = 1 - float64(result.OrigemRequisicao)/float64(result.Oferecidas)
	}

	doc := evidence.New(*scenario)
	doc.Data = result
	doc.Commands = []string{strings.Join(os.Args, " ")}
	doc.Notes = "Cliente, CDN e origem dividem a mesma máquina e disputam CPU. " +
		"Os números absolutos carregam essa interferência; a comparação entre cenários é o que vale."
	doc.Summary = renderSummary(result)

	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro gravando evidência:", err)
		os.Exit(1)
	}

	fmt.Print("\n", result.Summary())
	fmt.Println("evidência em", dir)
}

// Summary imprime o resultado no terminal.
func (r Result) Summary() string { return renderSummary(r) }

func renderSummary(r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resumo: %s\n\n", r.Cenario)
	fmt.Fprintf(&b, "Carga a taxa constante de %d req/s por %s sobre %d objetos, com %d deles concentrando %.0f%% do tráfego.\n",
		r.TaxaAlvo, r.Duracao, r.Objetos, r.Populares, r.FatiaPopular*100)
	fmt.Fprintf(&b, "TTL anunciado pela origem: %s.\n\n", r.TTLDaOrigem)

	fmt.Fprintf(&b, "| Medida | Valor |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Requisições oferecidas | %d |\n", r.Oferecidas)
	fmt.Fprintf(&b, "| Requisições concluídas | %d |\n", r.Concluidas)
	fmt.Fprintf(&b, "| Taxa de sucesso | %.2f%% |\n", r.TaxaSucesso*100)
	fmt.Fprintf(&b, "| Vazão concluída | %.1f req/s |\n", r.VazaoPorSegundo)
	fmt.Fprintf(&b, "| p50 / p95 / p99 | %.2f / %.2f / %.2f ms |\n", r.P50Ms, r.P95Ms, r.P99Ms)
	fmt.Fprintf(&b, "| Máxima | %.2f ms |\n", r.MaxMs)
	fmt.Fprintf(&b, "| Bytes recebidos pelo cliente | %.1f MiB |\n", r.BytesRecebidos/(1<<20))
	fmt.Fprintf(&b, "| **Hit ratio na CDN** | **%.2f%%** |\n", r.HitRatio*100)
	fmt.Fprintf(&b, "| Requisições que chegaram à origem | %d |\n", r.OrigemRequisicao)
	fmt.Fprintf(&b, "| Corpos servidos pela origem | %d |\n", r.OrigemCorpos)
	fmt.Fprintf(&b, "| Revalidações condicionais (304) | %d |\n", r.OrigemRevalidada)
	fmt.Fprintf(&b, "| Bytes servidos pela origem | %.1f MiB |\n", float64(r.OrigemBytes)/(1<<20))
	fmt.Fprintf(&b, "| **Alívio da origem** | **%.2f%%** |\n", r.AlivioDaOrigem*100)

	fmt.Fprintf(&b, "\n## Status de cache no log da CDN\n\n| Status | Respostas |\n|---|---:|\n")
	for _, status := range sortedKeys(r.StatusDeCache) {
		fmt.Fprintf(&b, "| %s | %.0f |\n", status, r.StatusDeCache[status])
	}

	fmt.Fprintf(&b, "\n## Códigos HTTP vistos pelo cliente\n\n| Código | Respostas |\n|---|---:|\n")
	codes := make([]string, 0, len(r.Codigos))
	for code := range r.Codigos {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		fmt.Fprintf(&b, "| %s | %d |\n", code, r.Codigos[code])
	}

	if len(r.Erros) > 0 {
		fmt.Fprintf(&b, "\n## Erros\n\n")
		for _, e := range r.Erros {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return b.String()
}

// buildTargets pede um token por objeto e monta os alvos do Vegeta.
func buildTargets(ctx context.Context, client *cdnclient.Client, prefix, size, tag string, count int, ttl time.Duration) ([]vegeta.Target, error) {
	targets := make([]vegeta.Target, 0, count)
	for i := range count {
		name := fmt.Sprintf("img-%s-%s-%03d.bin", size, tag, i)
		url, err := client.SignedURL(ctx, "GET", prefix+name, ttl)
		if err != nil {
			return nil, err
		}
		targets = append(targets, vegeta.Target{Method: "GET", URL: url})
	}
	return targets, nil
}

// skewedTargeter escolhe o próximo alvo com distribuição desigual.
//
// O Vegeta chama o targeter de várias goroutines, então o sorteio precisa de
// trava. É um mutex por requisição, e não aparece na medição porque o custo dele
// é ordens de grandeza menor que uma ida à rede.
func skewedTargeter(targets []vegeta.Target, popular int, share float64) vegeta.Targeter {
	if popular <= 0 || popular > len(targets) {
		popular = len(targets)
	}
	var mu sync.Mutex
	// Semente fixa: duas execuções do mesmo cenário sorteiam a mesma sequência,
	// e a diferença entre elas passa a ser o sistema, não o sorteio.
	src := rand.New(rand.NewSource(42))

	return func(t *vegeta.Target) error {
		mu.Lock()
		defer mu.Unlock()
		var idx int
		if src.Float64() < share {
			idx = src.Intn(popular)
		} else {
			idx = popular + src.Intn(len(targets)-popular+1)
			if idx >= len(targets) {
				idx = len(targets) - 1
			}
		}
		*t = targets[idx]
		return nil
	}
}

// hitRatio considera acerto tudo que a CDN respondeu sem pedir o corpo à
// origem: HIT direto, revalidação bem-sucedida e resposta antiga servida durante
// falha da origem. MISS, EXPIRED e BYPASS custaram origem.
func hitRatio(status map[string]float64) float64 {
	var hits, total float64
	for name, count := range status {
		if name == "none" {
			// Respostas geradas pela própria CDN (403, 429, 405) não passaram
			// pelo cache e não pertencem a nenhum dos dois lados da conta.
			continue
		}
		total += count
		switch name {
		case "HIT", "REVALIDATED", "STALE", "UPDATING":
			hits += count
		}
	}
	if total == 0 {
		return 0
	}
	return hits / total
}

func ttlLabel(d time.Duration) string {
	if d <= 0 {
		return "o que já estava configurado no ambiente"
	}
	return d.String()
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
