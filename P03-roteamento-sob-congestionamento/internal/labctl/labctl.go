// Package labctl agrupa os comandos de controle do laboratório.
//
// São as chamadas administrativas que todo experimento faz antes de medir:
// esvaziar cache, zerar o que o roteador aprendeu, escolher a política, esperar
// tudo responder. Elas ficam aqui, e não copiadas em cada comando, porque um
// experimento que esquece uma delas produz um número que parece bom e não
// significa nada.
package labctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Lab descreve os endereços administrativos do ambiente.
type Lab struct {
	RouterAdmin string
	EdgeAdmin   map[string]string
	OriginAdmin string
	Client      *http.Client
}

// New monta o controle com um cliente de prazo curto: comando administrativo que
// demora é sintoma, não algo para se esperar pacientemente no meio de um roteiro.
func New(routerAdmin string, edges map[string]string, originAdmin string) *Lab {
	return &Lab{
		RouterAdmin: strings.TrimSuffix(routerAdmin, "/"),
		EdgeAdmin:   edges,
		OriginAdmin: strings.TrimSuffix(originAdmin, "/"),
		Client:      &http.Client{Timeout: 5 * time.Second},
	}
}

// WaitReady espera todo mundo responder antes de começar a medir.
func (l *Lab) WaitReady(ctx context.Context, prazo time.Duration) error {
	alvos := []string{l.RouterAdmin + "/admin/state", l.OriginAdmin + "/admin/counters"}
	for _, url := range l.EdgeAdmin {
		alvos = append(alvos, strings.TrimSuffix(url, "/")+"/admin/counters")
	}

	limite := time.Now().Add(prazo)
	for _, url := range alvos {
		for {
			if err := l.get(ctx, url, nil); err == nil {
				break
			}
			if time.Now().After(limite) {
				return fmt.Errorf("%s não respondeu dentro de %s", url, prazo)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return nil
}

// PurgeEdges esvazia o cache dos três edges.
//
// Sem isso, o segundo cenário de uma bateria começa com o cache quente do
// primeiro, e a comparação entre eles vira uma comparação entre estados de cache.
func (l *Lab) PurgeEdges(ctx context.Context) error {
	for nome, url := range l.EdgeAdmin {
		if err := l.post(ctx, strings.TrimSuffix(url, "/")+"/admin/purge"); err != nil {
			return fmt.Errorf("esvaziando o cache de %s: %w", nome, err)
		}
	}
	return nil
}

// ResetRouter apaga o que os destinos ensinaram e devolve o orçamento de retry.
func (l *Lab) ResetRouter(ctx context.Context) error {
	return l.post(ctx, l.RouterAdmin+"/admin/reset")
}

// ResetOriginPeak zera a marca d'água de simultaneidade da origem.
func (l *Lab) ResetOriginPeak(ctx context.Context) error {
	return l.post(ctx, l.OriginAdmin+"/admin/reset-peak")
}

// SetStrategy troca a política do roteador.
func (l *Lab) SetStrategy(ctx context.Context, nome string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		l.RouterAdmin+"/admin/strategy?name="+nome, nil)
	if err != nil {
		return err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return fmt.Errorf("trocando a política para %s: %w", nome, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trocando a política para %s: %s", nome, resp.Status)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RouterState lê a foto do roteador em forma genérica, para a evidência.
func (l *Lab) RouterState(ctx context.Context) (map[string]any, error) {
	var estado map[string]any
	if err := l.get(ctx, l.RouterAdmin+"/admin/state", &estado); err != nil {
		return nil, err
	}
	return estado, nil
}

// BackendSnapshot é o que o roteador acha de um destino num instante.
type BackendSnapshot struct {
	Name      string  `json:"name"`
	Inflight  int64   `json:"inflight"`
	LatencyMS float64 `json:"latency_ewma_ms"`
	ErrorRate float64 `json:"error_rate"`
	Cost      float64 `json:"cost"`
	Open      bool    `json:"circuit_open"`
	Attempts  int64   `json:"attempts_total"`
	Failures  int64   `json:"failures_total"`
}

// RouterSnapshot é a foto tipada do roteador.
type RouterSnapshot struct {
	At             time.Time         `json:"at"`
	Strategy       string            `json:"strategy"`
	Backends       []BackendSnapshot `json:"backends"`
	RetriesGranted int64             `json:"retries_granted"`
	RetriesDenied  int64             `json:"retries_denied"`
	BudgetTokens   float64           `json:"retry_budget_tokens"`
	Inflight       int64             `json:"inflight"`
	Rejected       int64             `json:"rejected_total"`
}

// Router lê a foto tipada do roteador.
func (l *Lab) Router(ctx context.Context) (RouterSnapshot, error) {
	var s RouterSnapshot
	if err := l.get(ctx, l.RouterAdmin+"/admin/state", &s); err != nil {
		return RouterSnapshot{}, err
	}
	s.At = time.Now()
	return s, nil
}

// Sample coleta fotos do roteador em intervalos regulares até o contexto acabar.
//
// A série temporal existe para responder uma pergunta que a média da janela não
// responde: em quanto tempo a política PERCEBEU o destino doente. Com só o total
// da janela, uma política que reagiu em um segundo e outra que reagiu em quinze
// podem terminar com distribuições parecidas.
func (l *Lab) Sample(ctx context.Context, intervalo time.Duration) []RouterSnapshot {
	var fotos []RouterSnapshot
	tick := time.NewTicker(intervalo)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fotos
		case <-tick.C:
			if s, err := l.Router(ctx); err == nil {
				fotos = append(fotos, s)
			}
		}
	}
}

// EdgeCounters lê os contadores de cada edge.
func (l *Lab) EdgeCounters(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	for nome, url := range l.EdgeAdmin {
		var c map[string]any
		if err := l.get(ctx, strings.TrimSuffix(url, "/")+"/admin/counters", &c); err != nil {
			return nil, fmt.Errorf("lendo contadores de %s: %w", nome, err)
		}
		out[nome] = c
	}
	return out, nil
}

// OriginCounters lê os contadores da origem.
func (l *Lab) OriginCounters(ctx context.Context) (map[string]any, error) {
	var c map[string]any
	if err := l.get(ctx, l.OriginAdmin+"/admin/counters", &c); err != nil {
		return nil, err
	}
	return c, nil
}

// OriginFail liga ou desliga o modo de falha da origem. status 0 desliga.
func (l *Lab) OriginFail(ctx context.Context, status int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/admin/fail?status=%d", l.OriginAdmin, status), nil)
	if err != nil {
		return err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mudando o modo de falha da origem: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mudando o modo de falha da origem: %s", resp.Status)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Prepare deixa o ambiente pronto para um cenário: cache vazio, roteador sem
// memória, origem sem pico e política escolhida.
func (l *Lab) Prepare(ctx context.Context, politica string) error {
	if err := l.PurgeEdges(ctx); err != nil {
		return err
	}
	if err := l.ResetRouter(ctx); err != nil {
		return err
	}
	if err := l.ResetOriginPeak(ctx); err != nil {
		return err
	}
	if politica != "" {
		if err := l.SetStrategy(ctx, politica); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lab) get(ctx context.Context, url string, destino any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s devolveu %s", url, resp.Status)
	}
	if destino == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(destino)
}

func (l *Lab) post(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s devolveu %s", url, resp.Status)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
