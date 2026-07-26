// Comando negative roda os testes negativos da CDN.
//
// Teste negativo é o que prova que algo NÃO acontece: o token expirado não passa,
// o Host inventado não é atendido, o header forjado não chega à origem, a query
// string não fabrica entradas novas no cache. São eles que sustentam as
// afirmações de segurança do projeto, porque um caminho feliz funcionando não
// diz nada sobre o que a CDN recusa.
//
// Cada vetor declara o que espera ANTES de rodar. Um vetor que "passa" porque a
// expectativa foi ajustada depois do resultado não prova coisa alguma.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/cdnclient"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/evidence"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/signer"
)

// Vetor é um caso de teste negativo com a expectativa declarada.
type Vetor struct {
	Nome      string `json:"nome"`
	Categoria string `json:"categoria"`
	Descricao string `json:"descricao"`
	Esperado  string `json:"esperado"`
	Obtido    string `json:"obtido"`
	Passou    bool   `json:"passou"`
}

type resposta struct {
	status int
	cache  string
	corpo  string
	err    error
}

func main() {
	cdnURL := flag.String("cdn", "http://127.0.0.1:8080", "URL da CDN")
	tokendURL := flag.String("tokend", "http://127.0.0.1:8082", "URL do serviço de token")
	scenario := flag.String("scenario", "testes-negativos", "nome do cenário na pasta de evidência")
	evidenceDir := flag.String("evidence", "evidence", "raiz da pasta de evidência")
	flag.Parse()

	ctx := context.Background()
	client := cdnclient.New(*cdnURL, *tokendURL, 8)
	runner := &runner{client: client, edge: strings.TrimSuffix(*cdnURL, "/")}

	tag := time.Now().Unix()
	vetores := []Vetor{
		runner.semToken(ctx, tag),
		runner.assinaturaAlterada(ctx, tag),
		runner.chaveDesconhecida(ctx, tag),
		runner.tokenDeOutroObjeto(ctx, tag),
		runner.tokenExpirado(ctx, tag),
		runner.metodoNaoPermitido(ctx, tag),
		runner.corpoGrande(ctx, tag),
		runner.hostDesconhecido(ctx, tag),
		runner.queryNaoEntraNaChave(ctx, tag),
		runner.caminhoCodificadoNaoDuplicaEntrada(ctx, tag),
		runner.headerForjadoNaoChegaNaOrigem(ctx),
		runner.tokenNaoChegaNaOrigem(ctx),
		runner.replayDentroDaValidade(ctx, tag),
	}

	falhas := 0
	for _, v := range vetores {
		marca := "OK  "
		if !v.Passou {
			marca = "FALHA"
			falhas++
		}
		fmt.Printf("%-5s %-38s esperado: %-28s obtido: %s\n", marca, v.Nome, v.Esperado, v.Obtido)
	}

	doc := evidence.New(*scenario)
	doc.Data = map[string]any{"vetores": vetores, "falhas": falhas}
	doc.Commands = []string{strings.Join(os.Args, " ")}
	doc.Notes = "Cada vetor declara a expectativa antes de rodar. Falha aqui é falha de segurança do lab, não ruído de medição."
	doc.Summary = render(vetores, falhas)

	dir, err := evidence.Save(*evidenceDir, doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro gravando evidência:", err)
		os.Exit(1)
	}
	fmt.Println("\nevidência em", dir)

	if falhas > 0 {
		fmt.Fprintf(os.Stderr, "\n%d vetor(es) falharam\n", falhas)
		os.Exit(1)
	}
}

type runner struct {
	client *cdnclient.Client
	edge   string
}

// objeto devolve um nome novo, garantindo cache frio para aquele vetor.
func objeto(tag int64, sufixo string) string {
	return fmt.Sprintf("/objects/neg-1KiB-%d-%s.bin", tag, sufixo)
}

func (r *runner) do(ctx context.Context, method, url string, headers map[string]string, host string, body []byte) resposta {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return resposta{err: err}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := r.client.HTTP.Do(req)
	if err != nil {
		return resposta{err: err}
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return resposta{status: resp.StatusCode, cache: resp.Header.Get("X-Cache-Status"), corpo: string(corpo)}
}

func (r *runner) semToken(ctx context.Context, tag int64) Vetor {
	got := r.do(ctx, http.MethodGet, r.edge+objeto(tag, "sem-token"), nil, "", nil)
	return veredito(Vetor{
		Nome:      "sem-token",
		Categoria: "token",
		Descricao: "requisição a objeto protegido sem nenhum token",
		Esperado:  "403",
	}, got, got.status == http.StatusForbidden)
}

func (r *runner) assinaturaAlterada(ctx context.Context, tag int64) Vetor {
	path := objeto(tag, "sig-alterada")
	url, err := r.client.SignedURL(ctx, "GET", path, time.Minute)
	if err != nil {
		return erroDeSetup("assinatura-alterada", "token", err)
	}
	// Troca o último caractere da assinatura: o token continua bem formado, só
	// deixa de fechar com a chave.
	alterada := url[:len(url)-1] + trocaUltimo(url[len(url)-1])
	got := r.do(ctx, http.MethodGet, alterada, nil, "", nil)
	return veredito(Vetor{
		Nome:      "assinatura-alterada",
		Categoria: "token",
		Descricao: "um caractere trocado na assinatura HMAC",
		Esperado:  "403",
	}, got, got.status == http.StatusForbidden)
}

func (r *runner) chaveDesconhecida(ctx context.Context, tag int64) Vetor {
	path := objeto(tag, "kid-desconhecido")
	url, err := r.client.SignedURL(ctx, "GET", path, time.Minute)
	if err != nil {
		return erroDeSetup("chave-desconhecida", "token", err)
	}
	got := r.do(ctx, http.MethodGet, trocaParam(url, signer.ParamKeyID, "chave-que-nao-existe"), nil, "", nil)
	return veredito(Vetor{
		Nome:      "chave-desconhecida",
		Categoria: "token",
		Descricao: "token apontando para um identificador de chave inexistente",
		Esperado:  "403",
	}, got, got.status == http.StatusForbidden)
}

func (r *runner) tokenDeOutroObjeto(ctx context.Context, tag int64) Vetor {
	token, err := r.client.Sign(ctx, "GET", objeto(tag, "origem-do-token"), time.Minute)
	if err != nil {
		return erroDeSetup("token-de-outro-objeto", "token", err)
	}
	alvo := r.edge + objeto(tag, "alvo-do-ataque") + "?" + token.Query
	got := r.do(ctx, http.MethodGet, alvo, nil, "", nil)
	return veredito(Vetor{
		Nome:      "token-de-outro-objeto",
		Categoria: "token",
		Descricao: "token válido de um objeto, colado na URL de outro",
		Esperado:  "403",
	}, got, got.status == http.StatusForbidden)
}

func (r *runner) tokenExpirado(ctx context.Context, tag int64) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", objeto(tag, "expirado"), time.Second)
	if err != nil {
		return erroDeSetup("token-expirado", "token", err)
	}
	// Espera o prazo passar. É o único vetor que precisa de relógio de parede:
	// a expiração está dentro da assinatura e não dá para forjar por fora.
	time.Sleep(2 * time.Second)
	got := r.do(ctx, http.MethodGet, url, nil, "", nil)
	return veredito(Vetor{
		Nome:      "token-expirado",
		Categoria: "token",
		Descricao: "token de 1s usado 2s depois de emitido",
		Esperado:  "403",
	}, got, got.status == http.StatusForbidden)
}

func (r *runner) metodoNaoPermitido(ctx context.Context, tag int64) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", objeto(tag, "metodo"), time.Minute)
	if err != nil {
		return erroDeSetup("metodo-nao-permitido", "CDN", err)
	}
	got := r.do(ctx, http.MethodPost, url, nil, "", []byte("x"))
	ok := got.status == http.StatusForbidden || got.status == http.StatusMethodNotAllowed
	return veredito(Vetor{
		Nome:      "metodo-nao-permitido",
		Categoria: "CDN",
		Descricao: "POST em rota de leitura, mesmo com token válido de GET",
		Esperado:  "403 ou 405",
	}, got, ok)
}

func (r *runner) corpoGrande(ctx context.Context, tag int64) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", objeto(tag, "corpo-grande"), time.Minute)
	if err != nil {
		return erroDeSetup("corpo-grande", "CDN", err)
	}
	got := r.do(ctx, http.MethodGet, url, nil, "", bytes.Repeat([]byte("A"), 1<<20))
	ok := got.status == http.StatusRequestEntityTooLarge || got.err != nil
	return veredito(Vetor{
		Nome:      "corpo-grande",
		Categoria: "CDN",
		Descricao: "requisição com 1 MiB de corpo contra o limite de client_max_body_size",
		Esperado:  "413 ou conexão recusada",
	}, got, ok)
}

func (r *runner) hostDesconhecido(ctx context.Context, tag int64) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", objeto(tag, "host"), time.Minute)
	if err != nil {
		return erroDeSetup("host-desconhecido", "poisoning", err)
	}
	got := r.do(ctx, http.MethodGet, url, nil, "atacante.example", nil)
	// O default_server responde 444, que é "feche a conexão sem responder". Do
	// lado do cliente isso aparece como erro de transporte, não como status.
	ok := got.err != nil || got.status >= 400
	return veredito(Vetor{
		Nome:      "host-desconhecido",
		Categoria: "poisoning",
		Descricao: "Host inventado, tentando criar uma entrada de cache paralela",
		Esperado:  "conexão encerrada ou 4xx",
	}, got, ok)
}

func (r *runner) queryNaoEntraNaChave(ctx context.Context, tag int64) Vetor {
	path := objeto(tag, "query")
	url, err := r.client.SignedURL(ctx, "GET", path, time.Minute)
	if err != nil {
		return erroDeSetup("query-nao-entra-na-chave", "poisoning", err)
	}
	if primeira := r.do(ctx, http.MethodGet, url, nil, "", nil); primeira.status != http.StatusOK {
		return veredito(Vetor{
			Nome: "query-nao-entra-na-chave", Categoria: "poisoning",
			Descricao: "primeira requisição precisa popular o cache",
			Esperado:  "200 na primeira",
		}, primeira, false)
	}
	got := r.do(ctx, http.MethodGet, url+"&cachebuster=1", nil, "", nil)
	ok := got.status == http.StatusOK && strings.EqualFold(got.cache, "HIT")
	return veredito(Vetor{
		Nome:      "query-nao-entra-na-chave",
		Categoria: "poisoning",
		Descricao: "parâmetro extra na query não deve criar uma segunda entrada de cache",
		Esperado:  "200 com X-Cache-Status: HIT",
	}, got, ok)
}

func (r *runner) caminhoCodificadoNaoDuplicaEntrada(ctx context.Context, tag int64) Vetor {
	path := objeto(tag, "encoded")
	url, err := r.client.SignedURL(ctx, "GET", path, time.Minute)
	if err != nil {
		return erroDeSetup("caminho-codificado", "poisoning", err)
	}
	if primeira := r.do(ctx, http.MethodGet, url, nil, "", nil); primeira.status != http.StatusOK {
		return veredito(Vetor{
			Nome: "caminho-codificado", Categoria: "poisoning",
			Descricao: "primeira requisição precisa popular o cache",
			Esperado:  "200 na primeira",
		}, primeira, false)
	}
	// /objects/sub/%2e%2e/nome.bin resolve para /objects/nome.bin depois de
	// decodificar e limpar. Se a CDN e o validador normalizarem igual, isto é
	// o mesmo objeto e a mesma entrada de cache. Se normalizarem diferente, ou
	// vira 403 (o validador recusa) ou vira uma entrada duplicada (falha).
	nome := path[len("/objects/"):]
	codificado := r.edge + "/objects/sub/%2e%2e/" + nome + "?" + query(url)
	got := r.do(ctx, http.MethodGet, codificado, nil, "", nil)
	ok := (got.status == http.StatusOK && strings.EqualFold(got.cache, "HIT")) || got.status == http.StatusForbidden
	return veredito(Vetor{
		Nome:      "caminho-codificado",
		Categoria: "poisoning",
		Descricao: "travessia codificada que resolve para o mesmo objeto",
		Esperado:  "403, ou 200 na MESMA entrada de cache (HIT)",
	}, got, ok)
}

func (r *runner) headerForjadoNaoChegaNaOrigem(ctx context.Context) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", "/_echo", time.Minute)
	if err != nil {
		return erroDeSetup("header-forjado", "poisoning", err)
	}
	got := r.do(ctx, http.MethodGet, url, map[string]string{
		"X-Forwarded-Host": "atacante.example",
		"X-Real-IP":        "10.0.0.1",
		"Cookie":           "sessao=roubada",
		"Authorization":    "Bearer forjado",
	}, "", nil)
	ok := got.status == http.StatusOK &&
		!strings.Contains(got.corpo, "atacante.example") &&
		!strings.Contains(got.corpo, "sessao=roubada") &&
		!strings.Contains(got.corpo, "Bearer forjado")
	return veredito(Vetor{
		Nome:      "header-forjado",
		Categoria: "poisoning",
		Descricao: "headers não confiáveis do cliente não podem chegar à origem",
		Esperado:  "origem sem X-Forwarded-Host, Cookie e Authorization do cliente",
	}, got, ok)
}

func (r *runner) tokenNaoChegaNaOrigem(ctx context.Context) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", "/_echo", time.Minute)
	if err != nil {
		return erroDeSetup("token-nao-chega-na-origem", "token", err)
	}
	got := r.do(ctx, http.MethodGet, url, nil, "", nil)
	var visto map[string]string
	_ = json.Unmarshal([]byte(got.corpo), &visto)
	uri := visto["__uri"]
	ok := got.status == http.StatusOK && uri != "" &&
		!strings.Contains(uri, signer.ParamSignature+"=") &&
		!strings.Contains(uri, signer.ParamKeyID+"=")
	return veredito(Vetor{
		Nome:      "token-nao-chega-na-origem",
		Categoria: "token",
		Descricao: "a CDN encaminha o caminho sem a query onde o token viaja",
		Esperado:  "URI na origem sem sig= nem kid=",
	}, resposta{status: got.status, cache: got.cache, corpo: uri}, ok)
}

// replayDentroDaValidade documenta um limite conhecido, não um defeito.
func (r *runner) replayDentroDaValidade(ctx context.Context, tag int64) Vetor {
	url, err := r.client.SignedURL(ctx, "GET", objeto(tag, "replay"), time.Minute)
	if err != nil {
		return erroDeSetup("replay-dentro-da-validade", "limite conhecido", err)
	}
	primeira := r.do(ctx, http.MethodGet, url, nil, "", nil)
	segunda := r.do(ctx, http.MethodGet, url, nil, "", nil)
	ok := primeira.status == http.StatusOK && segunda.status == http.StatusOK
	return veredito(Vetor{
		Nome:      "replay-dentro-da-validade",
		Categoria: "limite conhecido",
		Descricao: "o MESMO link funciona de novo enquanto não expira: quem recebe o link, repassa o link",
		Esperado:  "200 nas duas (limite aceito e documentado)",
	}, segunda, ok)
}

// redigir remove query string de qualquer texto que vá para a tela ou para a
// evidência.
//
// Uma mensagem de erro do cliente HTTP carrega a URL inteira, e a URL inteira
// carrega o token. Sem isto, a ferramenta que existe para provar que o token não
// vaza seria justamente quem o escreveria em arquivo versionado.
func redigir(texto string) string {
	return queryRe.ReplaceAllString(texto, "?(token omitido)")
}

var queryRe = regexp.MustCompile(`\?[^\s"]*`)

func veredito(v Vetor, got resposta, ok bool) Vetor {
	v.Passou = ok
	switch {
	case got.err != nil:
		v.Obtido = "erro de transporte: " + redigir(got.err.Error())
	case got.cache != "":
		v.Obtido = fmt.Sprintf("%d (cache: %s)", got.status, got.cache)
	default:
		v.Obtido = fmt.Sprint(got.status)
	}
	return v
}

func erroDeSetup(nome, categoria string, err error) Vetor {
	return Vetor{
		Nome: nome, Categoria: categoria,
		Descricao: "preparação do vetor falhou",
		Esperado:  "preparação bem-sucedida",
		Obtido:    redigir(err.Error()),
		Passou:    false,
	}
}

func trocaUltimo(c byte) string {
	if c == 'a' {
		return "b"
	}
	return "a"
}

func trocaParam(rawURL, param, valor string) string {
	base, q, _ := strings.Cut(rawURL, "?")
	partes := strings.Split(q, "&")
	for i, parte := range partes {
		if strings.HasPrefix(parte, param+"=") {
			partes[i] = param + "=" + valor
		}
	}
	return base + "?" + strings.Join(partes, "&")
}

func query(rawURL string) string {
	_, q, _ := strings.Cut(rawURL, "?")
	return q
}

func render(vetores []Vetor, falhas int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resumo: testes negativos da CDN\n\n")
	fmt.Fprintf(&b, "%d vetores, %d falha(s).\n\n", len(vetores), falhas)
	fmt.Fprintf(&b, "| Vetor | Categoria | O que tenta | Esperado | Obtido | Resultado |\n|---|---|---|---|---|---|\n")
	for _, v := range vetores {
		resultado := "passou"
		if !v.Passou {
			resultado = "**FALHOU**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			v.Nome, v.Categoria, v.Descricao, v.Esperado, v.Obtido, resultado)
	}
	fmt.Fprintf(&b, "\nO vetor `replay-dentro-da-validade` não é um defeito: é a propriedade de uma URL\n")
	fmt.Fprintf(&b, "assinada. O controle contra ele é o prazo curto, não a assinatura.\n")
	return b.String()
}
