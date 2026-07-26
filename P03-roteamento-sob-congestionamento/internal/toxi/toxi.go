// Package toxi injeta as falhas de rede do P03 usando o Toxiproxy.
//
// O Toxiproxy fica entre o roteador e cada edge. Todo tráfego passa por ele, e
// ele consegue degradar UM caminho sem tocar nos outros dois, que é exatamente o
// desenho do experimento: um edge doente, dois saudáveis, e a pergunta de quanto
// o roteador percebe.
//
// Uma honestidade necessária sobre vocabulário. O Toxiproxy trabalha em cima da
// conexão TCP, não do pacote IP. Ele não descarta pacote e não mexe em janela de
// congestionamento; o que ele faz é atrasar bytes, limitar taxa, parar de
// responder ou cortar a conexão. O cenário que este projeto chama de "perda de
// pacotes" é, na verdade, conexão cortada com uma certa probabilidade. O efeito
// observado no cliente é parecido, timeout e retry, e o mecanismo por baixo é
// diferente. Chamar as duas coisas pelo mesmo nome seria o tipo de imprecisão que
// o resto do laboratório evita, então o relatório usa o nome do mecanismo.
package toxi

import (
	"fmt"
	"time"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
)

// assentamento é quanto tempo esperar, depois de desligar um caminho, antes de
// mexer nas toxinas dele. Veja Clear para o porquê.
const assentamento = 1500 * time.Millisecond

// Client é o controle das falhas de rede do lab.
type Client struct {
	api *toxiproxy.Client
}

// New cria o cliente apontando para a API do Toxiproxy, normalmente :8474.
func New(endpoint string) *Client {
	return &Client{api: toxiproxy.NewClient(endpoint)}
}

// Reset devolve a rede ao estado limpo: proxies ligados, sem nenhuma toxina.
//
// Todo cenário começa chamando isto. Um experimento que herda a degradação do
// anterior produz um número que ninguém consegue explicar depois.
//
// A limpeza é caminho por caminho, e não pelo ResetState global do Toxiproxy,
// pelo motivo explicado em Clear: remover toxina com conexão presa trava a API
// inteira, e a partir daí nem para ler o estado ela responde.
func (c *Client) Reset() error {
	proxies, err := c.api.Proxies()
	if err != nil {
		return fmt.Errorf("listando proxies: %w", err)
	}
	for nome := range proxies {
		if err := c.Clear(nome); err != nil {
			return err
		}
	}
	return nil
}

// Latency adiciona latência a um caminho, com variação em torno da média.
//
// O jitter existe porque latência perfeitamente constante não acontece em rede
// nenhuma, e um destino com latência exata seria fácil demais para a média móvel
// do roteador aprender.
func (c *Client) Latency(proxy string, media, jitter time.Duration) error {
	return c.add(proxy, "latencia", "latency", "downstream", 1, toxiproxy.Attributes{
		"latency": media.Milliseconds(),
		"jitter":  jitter.Milliseconds(),
	})
}

// Bandwidth limita a taxa de um caminho, em kilobytes por segundo.
//
// Este é o cenário que separa throughput de requisições de throughput de bytes:
// o edge continua respondendo rápido no cabeçalho e leva um tempo enorme para
// entregar o corpo, e uma medição que só olha "requisições por segundo" não vê
// nada acontecendo.
func (c *Client) Bandwidth(proxy string, kbps int) error {
	return c.add(proxy, "banda", "bandwidth", "downstream", 1, toxiproxy.Attributes{
		"rate": kbps,
	})
}

// ResetPeer corta a conexão depois de um tempo, com a probabilidade dada.
//
// É o que este lab usa no lugar de perda de pacotes, pelo motivo explicado no
// comentário do pacote. toxicity 0.3 significa que três em cada dez conexões
// novas por aquele caminho são cortadas.
func (c *Client) ResetPeer(proxy string, probabilidade float32, depois time.Duration) error {
	return c.add(proxy, "reset", "reset_peer", "downstream", probabilidade, toxiproxy.Attributes{
		"timeout": depois.Milliseconds(),
	})
}

// Timeout faz o caminho parar de responder sem fechar a conexão.
//
// É a falha mais cruel das três: o cliente não recebe erro, recebe silêncio, e só
// descobre o problema quando o próprio prazo vence. Um health check por TCP
// continua feliz o tempo todo.
func (c *Client) Timeout(proxy string, depois time.Duration) error {
	return c.add(proxy, "silencio", "timeout", "downstream", 1, toxiproxy.Attributes{
		"timeout": depois.Milliseconds(),
	})
}

// Down desliga o caminho inteiro: a conexão passa a ser recusada na hora.
func (c *Client) Down(proxy string) error {
	p, err := c.api.Proxy(proxy)
	if err != nil {
		return fmt.Errorf("procurando o proxy %s: %w", proxy, err)
	}
	if err := p.Disable(); err != nil {
		return fmt.Errorf("desligando %s: %w", proxy, err)
	}
	return nil
}

// Up religa o caminho.
func (c *Client) Up(proxy string) error {
	p, err := c.api.Proxy(proxy)
	if err != nil {
		return fmt.Errorf("procurando o proxy %s: %w", proxy, err)
	}
	if err := p.Enable(); err != nil {
		return fmt.Errorf("religando %s: %w", proxy, err)
	}
	return nil
}

// Clear desliga as toxinas de um caminho, deixando os outros como estão.
//
// Desligar aqui significa levar a toxicidade a zero, e NÃO remover a toxina. A
// diferença importa e custou uma execução perdida deste laboratório.
//
// Remover uma toxina que está segurando bytes, como latência ou corte de
// conexão, faz o Toxiproxy esperar as goroutines dela terminarem enquanto segura
// o mutex da API inteira. Com tráfego passando, essa espera não termina, e o que
// se vê do lado de fora é a API parando de responder até para leitura. A partir
// daí só reiniciar o container resolve, e o experimento morre no meio. A
// execução em que isso aconteceu está guardada em evidence/.
//
// Toxicidade é a probabilidade de a toxina ser aplicada a uma conexão nova. Com
// zero, ela deixa de valer para todo mundo que chega depois, e a atualização é
// uma escrita simples que não espera goroutine nenhuma. O caminho é desligado e
// religado em volta disso para fechar as conexões que já estavam degradadas: sem
// esse corte, uma conexão persistente aberta antes continuaria lenta durante a
// fase seguinte inteira.
func (c *Client) Clear(proxy string) error {
	p, err := c.api.Proxy(proxy)
	if err != nil {
		return fmt.Errorf("procurando o proxy %s: %w", proxy, err)
	}
	toxinas, err := p.Toxics()
	if err != nil {
		return fmt.Errorf("listando toxinas de %s: %w", proxy, err)
	}

	if err := p.Disable(); err != nil {
		return fmt.Errorf("desligando %s antes de limpar: %w", proxy, err)
	}
	// A espera depois de desligar é o que faltava para o roteiro completo
	// funcionar. Desligar fecha as conexões, mas as goroutines da toxina de
	// latência ainda estão dormindo o atraso configurado, e mexer na toxina
	// enquanto centenas delas dormem faz a chamada passar do tempo limite da
	// própria API do Toxiproxy, que responde uma página HTML de timeout. Meio
	// segundo depois do maior atraso usado no lab é folga suficiente.
	time.Sleep(assentamento)
	for _, t := range toxinas {
		if _, err := p.UpdateToxic(t.Name, 0, t.Attributes); err != nil {
			return fmt.Errorf("desligando a toxina %s de %s: %w", t.Name, proxy, err)
		}
	}
	if err := p.Enable(); err != nil {
		return fmt.Errorf("religando %s: %w", proxy, err)
	}
	return nil
}

// Describe lista o estado atual de todos os caminhos, para a evidência.
//
// A evidência precisa dizer que degradação estava valendo no momento da medição.
// Reconstruir isso do comando que foi digitado é frágil; ler do próprio Toxiproxy
// é o que aconteceu de verdade.
func (c *Client) Describe() (map[string]any, error) {
	proxies, err := c.api.Proxies()
	if err != nil {
		return nil, fmt.Errorf("listando proxies: %w", err)
	}
	out := map[string]any{}
	for nome, p := range proxies {
		toxinas, err := p.Toxics()
		if err != nil {
			return nil, fmt.Errorf("listando toxinas de %s: %w", nome, err)
		}
		descricao := map[string]any{"enabled": p.Enabled, "listen": p.Listen, "upstream": p.Upstream}
		lista := make([]map[string]any, 0, len(toxinas))
		for _, t := range toxinas {
			lista = append(lista, map[string]any{
				"name": t.Name, "type": t.Type, "stream": t.Stream,
				"toxicity": t.Toxicity, "attributes": t.Attributes,
			})
		}
		descricao["toxics"] = lista
		out[nome] = descricao
	}
	return out, nil
}

// add cria ou atualiza uma toxina pelo nome.
//
// Recriar sem remover devolve erro de nome duplicado do Toxiproxy, e um cenário
// que sobe latência em degraus chamaria isto várias vezes no mesmo caminho. Por
// isso a atualização vem primeiro, e a criação é o caso em que ela falha.
func (c *Client) add(proxy, nome, tipo, stream string, toxicity float32, attrs toxiproxy.Attributes) error {
	p, err := c.api.Proxy(proxy)
	if err != nil {
		return fmt.Errorf("procurando o proxy %s: %w", proxy, err)
	}
	if _, err := p.UpdateToxic(nome, toxicity, attrs); err == nil {
		return nil
	}
	if _, err := p.AddToxic(nome, tipo, stream, toxicity, attrs); err != nil {
		return fmt.Errorf("aplicando %s em %s: %w", tipo, proxy, err)
	}
	return nil
}
