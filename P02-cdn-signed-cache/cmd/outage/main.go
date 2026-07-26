// Comando outage mede o que a CDN faz quando a origem cai.
//
// A pergunta: um objeto que já esteve no cache continua sendo entregue enquanto a
// origem está fora do ar? E por quanto tempo?
//
// O experimento tem uma sequência fixa, e cada passo existe por um motivo:
//
//  1. a origem passa a anunciar um TTL curto, para que a entrada vença rápido e o
//     experimento não precise esperar o TTL de produção;
//  2. o objeto é buscado uma vez, populando o cache;
//  3. o TTL volta ao normal e a origem entra em modo de falha;
//  4. depois do vencimento, várias requisições são feitas: a CDN precisa
//     entregar a cópia VENCIDA em vez de propagar o erro;
//  5. a origem volta, e a última requisição confirma que o conteúdo foi renovado.
//
// O que a evidência precisa mostrar, além do 200: que o conteúdo entregue durante
// a queda é o ANTIGO, e que a CDN não transformou uma origem fora do ar em uma
// tempestade de tentativas.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/cdnclient"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/evidence"
)

// Passo é uma requisição do roteiro, com o que ela observou.
type Passo struct {
	Nome           string `json:"nome"`
	Momento        string `json:"momento"`
	Status         int    `json:"status"`
	CacheStatus    string `json:"cache_status"`
	GeradoEm       string `json:"origem_gerou_em"`
	ConteudoAntigo bool   `json:"conteudo_antigo"`
	Bytes          int64  `json:"bytes"`
	Erro           string `json:"erro,omitempty"`
}

// Resultado é o pacote do experimento.
type Resultado struct {
	Objeto              string  `json:"objeto"`
	TTLCurto            string  `json:"ttl_curto"`
	StatusDaFalha       int     `json:"status_da_falha"`
	RequisicoesNaQueda  int     `json:"requisicoes_durante_a_queda"`
	ChamadasNaOrigem    int64   `json:"chamadas_na_origem_durante_a_queda"`
	TentativasPorPedido float64 `json:"tentativas_por_pedido"`
	ServidasComStale    int     `json:"respostas_servidas_do_cache_vencido"`
	RecuperouApos       int     `json:"recuperou_apos_n_requisicoes"`
	RevalidacoesNaVolta int64   `json:"revalidacoes_304_na_volta"`
	CorposNaVolta       int64   `json:"corpos_baixados_de_novo_na_volta"`
	ErrosPropagados     int     `json:"erros_propagados_ao_cliente"`
	Passos              []Passo `json:"passos"`
}

func main() {
	cdnURL := flag.String("cdn", "http://127.0.0.1:8080", "URL da CDN")
	tokendURL := flag.String("tokend", "http://127.0.0.1:8082", "URL do serviço de token")
	originAdmin := flag.String("origin-admin", "http://127.0.0.1:8090", "porta administrativa da origem")
	shortTTL := flag.Duration("short-ttl", 3*time.Second, "TTL curto usado para popular o cache")
	normalTTL := flag.Duration("normal-ttl", 60*time.Second, "TTL restaurado depois de popular")
	failStatus := flag.Int("fail-status", 503, "status que a origem devolve durante a queda")
	requests := flag.Int("requests", 10, "requisições feitas durante a queda")
	interval := flag.Duration("interval", 500*time.Millisecond, "intervalo entre as requisições da queda")
	scenario := flag.String("scenario", "origem-indisponivel", "nome do cenário na pasta de evidência")
	evidenceDir := flag.String("evidence", "evidence", "raiz da pasta de evidência")
	flag.Parse()

	ctx := context.Background()
	client := cdnclient.New(*cdnURL, *tokendURL, 8)

	objeto := fmt.Sprintf("stale-64KiB-%d.bin", time.Now().Unix())
	caminho := "/objects/" + objeto
	url, err := client.SignedURL(ctx, "GET", caminho, 10*time.Minute)
	if err != nil {
		falhar("erro pedindo token", err)
	}

	resultado := Resultado{
		Objeto: objeto, TTLCurto: shortTTL.String(), StatusDaFalha: *failStatus,
		RequisicoesNaQueda: *requests,
	}

	// Passo 1 e 2: TTL curto e uma busca que popula o cache.
	if err := cdnclient.SetOriginMaxAge(ctx, *originAdmin, *shortTTL); err != nil {
		falhar("erro ajustando o TTL da origem", err)
	}
	primeira := requisitar(client, url, "1-popula-o-cache")
	resultado.Passos = append(resultado.Passos, primeira)
	if primeira.Status != http.StatusOK {
		falhar("o cache não foi populado", fmt.Errorf("status %d", primeira.Status))
	}
	original := primeira.GeradoEm

	// Passo 3: TTL de volta ao normal (só afeta respostas futuras) e origem cai.
	if err := cdnclient.SetOriginMaxAge(ctx, *originAdmin, *normalTTL); err != nil {
		falhar("erro restaurando o TTL da origem", err)
	}
	if err := cdnclient.SetOriginFailure(ctx, *originAdmin, *failStatus, 0); err != nil {
		falhar("erro derrubando a origem", err)
	}
	defer func() {
		if err := cdnclient.SetOriginFailure(context.Background(), *originAdmin, 0, 0); err != nil {
			fmt.Fprintln(os.Stderr, "aviso: não consegui restaurar a origem:", err)
		}
	}()

	// Espera o vencimento. Enquanto a entrada está fresca, a CDN nem consulta a
	// origem, e o experimento estaria medindo um HIT comum.
	time.Sleep(*shortTTL + time.Second)

	antes, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		falhar("erro lendo contadores da origem", err)
	}

	// Passo 4: as requisições durante a queda.
	for i := range *requests {
		passo := requisitar(client, url, fmt.Sprintf("2-durante-a-queda-%02d", i+1))
		passo.ConteudoAntigo = passo.GeradoEm != "" && passo.GeradoEm == original
		if passo.Status == http.StatusOK {
			resultado.ServidasComStale++
		} else {
			resultado.ErrosPropagados++
		}
		resultado.Passos = append(resultado.Passos, passo)
		time.Sleep(*interval)
	}

	depois, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		falhar("erro lendo contadores da origem", err)
	}
	resultado.ChamadasNaOrigem = depois.Requests - antes.Requests
	if *requests > 0 {
		resultado.TentativasPorPedido = float64(resultado.ChamadasNaOrigem) / float64(*requests)
	}

	// Passo 5: origem de volta.
	//
	// A recuperação não é instantânea, e isso é comportamento, não defeito. Com
	// proxy_cache_background_update, a requisição que descobre a origem viva
	// ainda recebe a cópia vencida e dispara a atualização em segundo plano; quem
	// chega depois é que encontra a entrada renovada.
	//
	// O sinal de recuperação NÃO é o conteúdo mudar. O objeto deste lab é
	// determinístico, então o ETag continua o mesmo, a revalidação condicional
	// devolve 304 e o Nginx renova o frescor da cópia que já tinha, headers
	// inclusive. O sinal certo é a entrada voltar a HIT e a origem registrar a
	// revalidação.
	if err := cdnclient.SetOriginFailure(ctx, *originAdmin, 0, 0); err != nil {
		falhar("erro restaurando a origem", err)
	}
	antesDaVolta, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		falhar("erro lendo contadores da origem", err)
	}
	for i := range 5 {
		time.Sleep(time.Second)
		passo := requisitar(client, url, fmt.Sprintf("3-origem-de-volta-%02d", i+1))
		passo.ConteudoAntigo = passo.GeradoEm != "" && passo.GeradoEm == original
		resultado.Passos = append(resultado.Passos, passo)
		if passo.Status == http.StatusOK && strings.EqualFold(passo.CacheStatus, "HIT") {
			resultado.RecuperouApos = i + 1
			break
		}
	}
	depoisDaVolta, err := cdnclient.FetchCounters(ctx, *originAdmin)
	if err != nil {
		falhar("erro lendo contadores da origem", err)
	}
	resultado.RevalidacoesNaVolta = depoisDaVolta.Revalidations - antesDaVolta.Revalidations
	resultado.CorposNaVolta = depoisDaVolta.Bodies - antesDaVolta.Bodies

	doc := evidence.New(*scenario)
	doc.Data = resultado
	doc.Commands = []string{strings.Join(os.Args, " ")}
	doc.Notes = "A origem foi derrubada pelo modo de falha da porta administrativa, não por parada de container. " +
		"O caminho de erro exercitado é http_" + fmt.Sprint(*failStatus) + "; uma parada real também exercita timeout e recusa de conexão."
	doc.Summary = render(resultado, original)

	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		falhar("erro gravando evidência", err)
	}
	fmt.Print(doc.Summary)
	fmt.Println("evidência em", dir)

	if resultado.ServidasComStale == 0 {
		fmt.Fprintln(os.Stderr, "nenhuma resposta foi servida do cache vencido: a política de stale não funcionou")
		os.Exit(1)
	}
}

func requisitar(client *cdnclient.Client, url, nome string) Passo {
	passo := Passo{Nome: nome, Momento: time.Now().UTC().Format(time.RFC3339)}
	resp, err := client.HTTP.Get(url)
	if err != nil {
		passo.Erro = err.Error()
		return passo
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	passo.Status = resp.StatusCode
	passo.CacheStatus = resp.Header.Get("X-Cache-Status")
	passo.GeradoEm = resp.Header.Get("X-Origin-Generated-At")
	passo.Bytes = n
	return passo
}

func falhar(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}

func render(r Resultado, original string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resumo: origem indisponível\n\n")
	fmt.Fprintf(&b, "Objeto `%s`, guardado com TTL de %s e pedido de novo com a origem devolvendo %d.\n\n",
		r.Objeto, r.TTLCurto, r.StatusDaFalha)

	fmt.Fprintf(&b, "| Medida | Valor |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Requisições durante a queda | %d |\n", r.RequisicoesNaQueda)
	fmt.Fprintf(&b, "| Respostas servidas do cache vencido | %d |\n", r.ServidasComStale)
	fmt.Fprintf(&b, "| Erros propagados ao cliente | %d |\n", r.ErrosPropagados)
	fmt.Fprintf(&b, "| Chamadas que chegaram à origem | %d |\n", r.ChamadasNaOrigem)
	fmt.Fprintf(&b, "| Tentativas por pedido | %.2f |\n", r.TentativasPorPedido)
	if r.RecuperouApos > 0 {
		fmt.Fprintf(&b, "| Requisições até a entrada voltar a HIT | %d |\n", r.RecuperouApos)
	} else {
		fmt.Fprintf(&b, "| Requisições até a entrada voltar a HIT | não voltou na janela observada |\n")
	}
	fmt.Fprintf(&b, "| Revalidações 304 na volta | %d |\n", r.RevalidacoesNaVolta)
	fmt.Fprintf(&b, "| Corpos baixados de novo na volta | %d |\n", r.CorposNaVolta)

	fmt.Fprintf(&b, "\n## Roteiro\n\n| Passo | Status | Cache | Gerado na origem em | Conteúdo antigo | Bytes |\n|---|---:|---|---|---|---:|\n")
	for _, p := range r.Passos {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %v | %d |\n",
			p.Nome, p.Status, vazio(p.CacheStatus), vazio(p.GeradoEm), p.ConteudoAntigo, p.Bytes)
	}

	fmt.Fprintf(&b, "\nA cópia original foi gerada em `%s`. As respostas com **conteúdo antigo = true**\n", original)
	fmt.Fprintf(&b, "carregam exatamente aquela cópia, o que prova que vieram do cache e não de uma\n")
	fmt.Fprintf(&b, "origem viva.\n\n")
	fmt.Fprintf(&b, "Depois da volta o conteúdo continua o mesmo, e isso está certo: o objeto é\n")
	fmt.Fprintf(&b, "determinístico, o ETag não mudou, a revalidação condicional devolveu 304 e o\n")
	fmt.Fprintf(&b, "Nginx renovou o frescor da cópia guardada em vez de baixar tudo de novo. O sinal\n")
	fmt.Fprintf(&b, "de recuperação é a entrada voltar a HIT com revalidação registrada na origem,\n")
	fmt.Fprintf(&b, "não o corpo mudar.\n\n")
	fmt.Fprintf(&b, "**O risco:** servir conteúdo vencido é uma escolha, não um bônus. Enquanto a\n")
	fmt.Fprintf(&b, "origem está fora, o cliente recebe uma versão que pode estar errada, e sem saber\n")
	fmt.Fprintf(&b, "disso. Para uma imagem, é o melhor negócio. Para um preço ou um saldo, pode ser\n")
	fmt.Fprintf(&b, "pior que um erro honesto. Quem decide é quem conhece o conteúdo.\n")
	return b.String()
}

func vazio(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
