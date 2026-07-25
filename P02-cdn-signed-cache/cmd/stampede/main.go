// Comando stampede reproduz o cache stampede e mede o efeito do lock.
//
// O cenário: um objeto popular expira (ou nunca esteve no cache) e, no mesmo
// instante, cem clientes pedem por ele. Sem coordenação, a CDN abre cem
// conexões com a origem para buscar o MESMO objeto. É o pior momento possível
// para multiplicar carga, porque ele acontece justamente quando o conteúdo está
// em alta.
//
// O experimento roda o mesmo disparo em dois caminhos da CDN, um com
// proxy_cache_lock ligado e outro com ele desligado, e compara quantas chamadas
// chegaram à origem. A comparação é o resultado; o número isolado não prova nada.
//
// A largada simultânea usa errgroup mais um canal fechado como pistola de
// partida. Sem essa sincronização, os clientes chegariam espalhados no tempo, o
// primeiro já teria populado o cache e não haveria stampede nenhum.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/cdnclient"
	"github.com/matheusgb/edge-lab/p02-cdn-signed-cache/internal/evidence"
)

// Variante é um disparo contra um caminho da CDN.
type Variante struct {
	Nome              string         `json:"nome"`
	Caminho           string         `json:"caminho"`
	LockLigado        bool           `json:"proxy_cache_lock"`
	Clientes          int            `json:"clientes"`
	Objeto            string         `json:"objeto"`
	Codigos           map[string]int `json:"codigos_http"`
	StatusDeCache     map[string]int `json:"status_de_cache"`
	OrigemRequisicoes int64          `json:"requisicoes_na_origem"`
	OrigemCorpos      int64          `json:"corpos_servidos_pela_origem"`
	OrigemBytes       int64          `json:"bytes_servidos_pela_origem"`
	P50Ms             float64        `json:"p50_ms"`
	P99Ms             float64        `json:"p99_ms"`
	MaxMs             float64        `json:"max_ms"`
	Erros             []string       `json:"erros"`
}

// Resultado é o pacote completo do experimento.
type Resultado struct {
	Clientes       int        `json:"clientes"`
	LatenciaOrigem string     `json:"latencia_artificial_na_origem"`
	Tamanho        string     `json:"tamanho_do_objeto"`
	Variantes      []Variante `json:"variantes"`
	Amplificacao   float64    `json:"amplificacao_evitada"`
}

func main() {
	cdnURL := flag.String("cdn", "http://127.0.0.1:8080", "URL da CDN")
	tokendURL := flag.String("tokend", "http://127.0.0.1:8082", "URL do serviço de token")
	originAdmin := flag.String("origin-admin", "http://127.0.0.1:8090", "porta administrativa da origem")
	clients := flag.Int("clients", 100, "clientes simultâneos pedindo o mesmo objeto")
	size := flag.String("size", "1MiB", "tamanho declarado no nome do objeto")
	originLatency := flag.Duration("origin-latency", 300*time.Millisecond, "latência artificial na origem durante o disparo")
	scenario := flag.String("scenario", "cache-stampede", "nome do cenário na pasta de evidência")
	evidenceDir := flag.String("evidence", "evidence", "raiz da pasta de evidência")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout por requisição")
	flag.Parse()

	ctx := context.Background()
	client := cdnclient.New(*cdnURL, *tokendURL, *clients)

	// A latência artificial abre a janela do stampede. Com a origem respondendo
	// em microssegundos, o primeiro cliente termina antes dos outros chegarem e o
	// problema não aparece nem sem lock, o que daria uma conclusão falsa.
	if err := cdnclient.SetOriginLatency(ctx, *originAdmin, *originLatency); err != nil {
		fmt.Fprintln(os.Stderr, "erro configurando latência da origem:", err)
		os.Exit(1)
	}
	defer func() {
		if err := cdnclient.SetOriginLatency(context.Background(), *originAdmin, 0); err != nil {
			fmt.Fprintln(os.Stderr, "aviso: não consegui restaurar a latência da origem:", err)
		}
	}()

	tag := time.Now().Unix()
	resultado := Resultado{Clientes: *clients, LatenciaOrigem: originLatency.String(), Tamanho: *size}

	variantes := []struct {
		nome    string
		prefixo string
		lock    bool
	}{
		{"com-lock", "/objects/", true},
		{"sem-lock", "/nolock/", false},
	}

	for _, v := range variantes {
		// Cada variante usa um objeto NOVO. Reaproveitar o nome faria a segunda
		// variante encontrar o objeto já em cache e medir outra coisa.
		objeto := fmt.Sprintf("hot-%s-%s-%d.bin", *size, v.nome, tag)
		out, err := dispararVariante(ctx, client, *originAdmin, v.nome, v.prefixo+objeto, v.lock, *clients, *timeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "erro na variante", v.nome, ":", err)
			os.Exit(1)
		}
		out.Objeto = objeto
		resultado.Variantes = append(resultado.Variantes, out)
		fmt.Printf("%-9s -> %d chamadas na origem\n", v.nome, out.OrigemRequisicoes)
	}

	if len(resultado.Variantes) == 2 && resultado.Variantes[0].OrigemRequisicoes > 0 {
		resultado.Amplificacao = float64(resultado.Variantes[1].OrigemRequisicoes) /
			float64(resultado.Variantes[0].OrigemRequisicoes)
	}

	doc := evidence.New(*scenario)
	doc.Data = resultado
	doc.Commands = []string{strings.Join(os.Args, " ")}
	doc.Notes = fmt.Sprintf("Origem com %s de latência artificial durante o disparo, para abrir a janela do stampede.", originLatency)
	doc.Summary = render(resultado)

	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro gravando evidência:", err)
		os.Exit(1)
	}
	fmt.Print("\n", doc.Summary)
	fmt.Println("evidência em", dir)
}

// dispararVariante lança todos os clientes ao mesmo tempo contra um caminho.
func dispararVariante(ctx context.Context, client *cdnclient.Client, originAdmin, nome, caminho string, lock bool, clients int, timeout time.Duration) (Variante, error) {
	out := Variante{
		Nome: nome, Caminho: caminho, LockLigado: lock, Clientes: clients,
		Codigos: map[string]int{}, StatusDeCache: map[string]int{},
	}

	// Um token só, compartilhado pelos cem clientes. É o caso real: o link
	// assinado circula, e o objeto no cache é o mesmo para todos.
	url, err := client.SignedURL(ctx, "GET", caminho, 5*time.Minute)
	if err != nil {
		return out, err
	}

	before, err := cdnclient.FetchCounters(ctx, originAdmin)
	if err != nil {
		return out, err
	}

	type resposta struct {
		code     int
		cache    string
		duration time.Duration
		err      error
	}
	respostas := make([]resposta, clients)
	largada := make(chan struct{})

	group, gctx := errgroup.WithContext(ctx)
	for i := range clients {
		group.Go(func() error {
			<-largada // todos param aqui até a pistola de partida
			started := time.Now()
			reqCtx, cancel := context.WithTimeout(gctx, timeout)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
			if err != nil {
				respostas[i] = resposta{err: err}
				return nil
			}
			resp, err := client.HTTP.Do(req)
			if err != nil {
				respostas[i] = resposta{err: err, duration: time.Since(started)}
				return nil
			}
			defer resp.Body.Close()
			// O corpo precisa ser lido até o fim: parar no header mediria o tempo
			// até o primeiro byte, e o que interessa é a entrega do objeto.
			_, _ = io.Copy(io.Discard, resp.Body)
			respostas[i] = resposta{
				code:     resp.StatusCode,
				cache:    resp.Header.Get("X-Cache-Status"),
				duration: time.Since(started),
			}
			return nil
		})
	}

	close(largada)
	if err := group.Wait(); err != nil {
		return out, err
	}

	after, err := cdnclient.FetchCounters(ctx, originAdmin)
	if err != nil {
		return out, err
	}
	out.OrigemRequisicoes = after.Requests - before.Requests
	out.OrigemCorpos = after.Bodies - before.Bodies
	out.OrigemBytes = after.Bytes - before.Bytes

	duracoes := make([]time.Duration, 0, clients)
	for _, r := range respostas {
		if r.err != nil {
			out.Erros = append(out.Erros, r.err.Error())
			continue
		}
		out.Codigos[fmt.Sprint(r.code)]++
		status := r.cache
		if status == "" || status == "-" {
			status = "none"
		}
		out.StatusDeCache[status]++
		duracoes = append(duracoes, r.duration)
	}
	sort.Slice(duracoes, func(i, j int) bool { return duracoes[i] < duracoes[j] })
	out.P50Ms = percentil(duracoes, 0.50)
	out.P99Ms = percentil(duracoes, 0.99)
	out.MaxMs = percentil(duracoes, 1)
	return out, nil
}

func percentil(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return float64(sorted[idx].Nanoseconds()) / 1e6
}

func render(r Resultado) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resumo: cache stampede\n\n")
	fmt.Fprintf(&b, "%d clientes pedem o MESMO objeto ausente no mesmo instante, com a origem a %s de latência.\n\n",
		r.Clientes, r.LatenciaOrigem)

	fmt.Fprintf(&b, "| Variante | Lock | Chamadas na origem | Corpos servidos | MiB da origem | p50 ms | p99 ms |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---:|---:|---:|\n")
	for _, v := range r.Variantes {
		fmt.Fprintf(&b, "| %s | %v | %d | %d | %.1f | %.1f | %.1f |\n",
			v.Nome, v.LockLigado, v.OrigemRequisicoes, v.OrigemCorpos,
			float64(v.OrigemBytes)/(1<<20), v.P50Ms, v.P99Ms)
	}

	if r.Amplificacao > 0 {
		fmt.Fprintf(&b, "\nSem lock a origem recebeu **%.1f vezes** mais chamadas para entregar o mesmo objeto.\n", r.Amplificacao)
	}

	for _, v := range r.Variantes {
		fmt.Fprintf(&b, "\n## %s (%s)\n\n| Status de cache | Respostas |\n|---|---:|\n", v.Nome, v.Caminho)
		for _, k := range chavesOrdenadas(v.StatusDeCache) {
			fmt.Fprintf(&b, "| %s | %d |\n", k, v.StatusDeCache[k])
		}
		fmt.Fprintf(&b, "\n| Código HTTP | Respostas |\n|---|---:|\n")
		for _, k := range chavesOrdenadas(v.Codigos) {
			fmt.Fprintf(&b, "| %s | %d |\n", k, v.Codigos[k])
		}
		if len(v.Erros) > 0 {
			fmt.Fprintf(&b, "\nErros: %d\n", len(v.Erros))
		}
	}
	return b.String()
}

func chavesOrdenadas(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
