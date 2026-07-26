package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/metrics"
)

// Headers que o roteador propaga para o resto do caminho.
const (
	// HeaderCorrelation acompanha a requisição do cliente até a origem. É o que
	// permite pegar uma requisição lenta no log do roteador e achar a mesma
	// requisição no log do edge, sem depender de horário de relógio.
	HeaderCorrelation = "X-Correlation-Id"

	// HeaderDeadline diz ao destino quanto tempo ainda resta.
	//
	// Sem isso, o edge que recebe uma requisição já quase vencida vai até o fim
	// buscando na origem, gastando conexão e CPU para produzir uma resposta que
	// ninguém vai mais ler. Com isso, ele desiste cedo. É a mesma ideia do
	// context.Context, atravessando o limite do processo, que é onde o contexto
	// do Go não vai sozinho.
	HeaderDeadline = "X-Deadline-Ms"

	// Headers de diagnóstico da resposta, úteis no experimento e no curl.
	HeaderBackend  = "X-Router-Backend"
	HeaderAttempts = "X-Router-Attempts"
	HeaderStrategy = "X-Router-Strategy"
)

// Options configura o roteador.
type Options struct {
	Backends []*Backend
	Strategy Strategy
	Config   Config
	Budget   *RetryBudget
	Limiter  *Limiter

	// AttemptTimeout é o prazo de UMA tentativa. Ele é separado do prazo total
	// porque são perguntas diferentes: quanto tempo vale a pena esperar por este
	// destino, e quanto tempo o cliente aceita esperar pela resposta.
	AttemptTimeout time.Duration

	// RequestTimeout é o prazo total do cliente, o que manda em tudo.
	RequestTimeout time.Duration

	// MaxAttempts inclui a primeira tentativa. 2 significa uma tentativa e um
	// retry.
	MaxAttempts int

	// MinSliceLeft é o resto de prazo abaixo do qual não vale mais tentar. Sem
	// esse piso, o roteador gastaria uma tentativa inteira em 3ms de prazo, o que
	// é garantia de timeout e de trabalho jogado fora no destino.
	MinSliceLeft time.Duration

	// PalpiteInicial é a latência atribuída a um destino que ainda não foi
	// medido, e para onde o reset devolve os destinos.
	PalpiteInicial time.Duration

	Client  *http.Client
	Logger  *slog.Logger
	Metrics *metrics.Router
	Tracer  trace.Tracer
}

// Server é o roteador.
type Server struct {
	opts Options

	// A política é um ponteiro atômico para a interface, e não um atomic.Value.
	// O Value exige que todos os valores guardados tenham o MESMO tipo concreto,
	// e guardar um *RoundRobin depois de um *Adaptive faz o programa entrar em
	// pânico na troca, que é justamente o que o experimento faz a cada bateria.
	strategy atomic.Pointer[Strategy]

	started time.Time
}

// New monta o roteador.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	if opts.MinSliceLeft <= 0 {
		opts.MinSliceLeft = 20 * time.Millisecond
	}
	if opts.PalpiteInicial <= 0 {
		opts.PalpiteInicial = 5 * time.Millisecond
	}
	s := &Server{opts: opts, started: time.Now()}
	s.SetStrategy(opts.Strategy)
	return s
}

// Strategy devolve a política em uso.
func (s *Server) Strategy() Strategy { return *s.strategy.Load() }

// SetStrategy troca a política em tempo de execução.
//
// A troca existe por causa do experimento: rodar as duas políticas no mesmo
// processo, com os mesmos containers e a mesma rede, elimina de uma vez todas as
// diferenças de ambiente entre as duas metades da comparação.
func (s *Server) SetStrategy(st Strategy) { s.strategy.Store(&st) }

// Handler devolve o roteador público.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects/{name}", s.handleObject)
	mux.HandleFunc("HEAD /objects/{name}", s.handleObject)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// handleObject é o caminho quente inteiro: admissão, escolha, tentativa, retry.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	correlacao := r.Header.Get(HeaderCorrelation)
	if correlacao == "" {
		correlacao = novoID()
	}
	w.Header().Set(HeaderCorrelation, correlacao)

	// Admissão vem antes de tudo. Uma requisição que vai ser recusada não deve
	// custar contexto, span nem escolha de destino.
	liberar, entrou := s.opts.Limiter.Acquire()
	if !entrou {
		s.opts.Metrics.Rejected.WithLabelValues("concorrencia").Inc()
		w.Header().Set("Retry-After", strconv.Itoa(int(s.opts.Limiter.RetryAfter().Seconds()+0.999)))
		s.finish(w, r, inicio, http.StatusServiceUnavailable, "excesso de carga: tente de novo\n")
		s.opts.Logger.Warn("recusado na porta",
			"reason", "concorrencia",
			"correlation_id", correlacao,
			"inflight", s.opts.Limiter.InFlight(),
			"capacity", s.opts.Limiter.Capacity())
		return
	}
	defer liberar()

	ctx, cancel := context.WithTimeout(r.Context(), s.opts.RequestTimeout)
	defer cancel()

	ctx, span := s.tracer().Start(ctx, "router.route")
	defer span.End()
	span.SetAttributes(
		attribute.String("correlation_id", correlacao),
		attribute.String("router.strategy", s.Strategy().Name()),
	)

	// Uma requisição do cliente, um depósito. Depositar por tentativa deixaria o
	// retry pagando o próprio retry, e o orçamento nunca secaria.
	s.opts.Budget.Deposit()

	var tentados []*Backend
	var ultimoErro error
	var ultimoStatus int

	for tentativa := 1; tentativa <= s.opts.MaxAttempts; tentativa++ {
		restante := time.Until(prazo(ctx))
		if restante < s.opts.MinSliceLeft {
			s.opts.Metrics.Rejected.WithLabelValues("prazo").Inc()
			ultimoErro = context.DeadlineExceeded
			break
		}

		destino := s.Strategy().Pick(exceto(s.opts.Backends, tentados), time.Now())
		if destino == nil {
			// Sem candidato novo: todos os destinos já foram tentados nesta
			// requisição. Insistir no mesmo que acabou de falhar tem chance baixa
			// de mudar o resultado e custo certo.
			break
		}
		tentados = append(tentados, destino)

		if tentativa > 1 {
			// O orçamento é consultado depois de escolher o destino e antes de
			// gastar a rede, e a decisão é registrada nos dois casos: sem a série
			// de negados, um gráfico de retry baixo é ambíguo entre "não precisou"
			// e "não deixaram".
			if !s.opts.Budget.Withdraw() {
				s.opts.Metrics.Retries.WithLabelValues("negado-orcamento").Inc()
				s.opts.Logger.Warn("retry negado pelo orçamento",
					"correlation_id", correlacao,
					"attempt", tentativa,
					"tokens", s.opts.Budget.Tokens())
				break
			}
			s.opts.Metrics.Retries.WithLabelValues("concedido").Inc()
		}

		res, err := s.attempt(ctx, w, r, destino, correlacao, tentativa)
		if err == nil {
			s.opts.Metrics.Bytes.WithLabelValues(destino.Name).Add(float64(res.bytes))
			span.SetAttributes(
				attribute.String("router.backend", destino.Name),
				attribute.Int("router.attempts", tentativa),
			)
			// A resposta já foi copiada para o cliente dentro de attempt: uma vez
			// que o primeiro byte saiu, não existe mais retry possível.
			s.observe(inicio, res.status)
			return
		}

		ultimoErro = err
		ultimoStatus = res.status
	}

	status, motivo := desfecho(ultimoErro, ultimoStatus)
	span.SetStatus(codes.Error, motivo)
	s.opts.Logger.Error("requisição sem resposta boa",
		"correlation_id", correlacao,
		"reason", motivo,
		"attempts", len(tentados),
		"elapsed_ms", float64(time.Since(inicio).Microseconds())/1000)
	w.Header().Set(HeaderAttempts, strconv.Itoa(len(tentados)))
	w.Header().Set(HeaderStrategy, s.Strategy().Name())
	s.finish(w, r, inicio, status, motivo+"\n")
}

// resultado de uma tentativa.
type resultado struct {
	status int
	bytes  int64
}

// attempt faz uma tentativa contra um destino e, quando ela dá certo, copia a
// resposta para o cliente.
//
// Copiar aqui dentro, e não no chamador, é o que garante que só existe retry
// enquanto nada foi escrito: quem escreveu já não volta atrás.
func (s *Server) attempt(ctx context.Context, w http.ResponseWriter, r *http.Request, destino *Backend, correlacao string, tentativa int) (resultado, error) {
	restante := time.Until(prazo(ctx))
	fatia := s.opts.AttemptTimeout
	if restante < fatia {
		// O prazo do cliente manda. Uma tentativa com timeout maior que o prazo
		// restante é uma promessa que o roteador não pode cumprir.
		fatia = restante
	}

	ctx, cancel := context.WithTimeout(ctx, fatia)
	defer cancel()

	ctx, span := s.tracer().Start(ctx, "router.attempt")
	defer span.End()
	span.SetAttributes(
		attribute.String("backend", destino.Name),
		attribute.Int("attempt", tentativa),
		attribute.Float64("backend.cost", destino.Cost(s.opts.Config)),
		attribute.Int64("backend.inflight", destino.Inflight()),
	)

	url := destino.URL + r.URL.Path
	req, err := http.NewRequestWithContext(ctx, r.Method, url, nil)
	if err != nil {
		return resultado{}, err
	}
	req.Header.Set(HeaderCorrelation, correlacao)
	req.Header.Set(HeaderDeadline, strconv.FormatInt(fatia.Milliseconds(), 10))
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		req.Header.Set("If-None-Match", inm)
	}

	destino.Begin()
	comecou := time.Now()
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		duracao := time.Since(comecou)
		destino.Finish(s.opts.Config, duracao, true)
		s.recordAttempt(destino, duracao, classificar(err))
		span.SetStatus(codes.Error, err.Error())
		return resultado{}, err
	}
	defer resp.Body.Close()

	// 5xx conta como falha do destino; 4xx não. Um 404 é resposta correta sobre
	// um objeto que não existe, e tratá-lo como doença mandaria a carga para
	// longe do destino certo pelo motivo errado.
	if resp.StatusCode >= 500 {
		duracao := time.Since(comecou)
		destino.Finish(s.opts.Config, duracao, true)
		s.recordAttempt(destino, duracao, "5xx")
		// O corpo do erro é drenado e descartado: sem isso, a conexão não volta
		// para o pool e cada 5xx custaria um handshake novo, justamente quando o
		// sistema está pior.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		span.SetStatus(codes.Error, resp.Status)
		return resultado{status: resp.StatusCode}, fmt.Errorf("destino %s respondeu %s", destino.Name, resp.Status)
	}

	// A partir daqui a resposta é do cliente. A latência do destino é medida até
	// o cabeçalho, e não até o último byte do corpo, porque o tempo de corpo
	// depende do tamanho do objeto: somá-lo faria a média móvel do destino subir
	// só porque alguém pediu um objeto maior.
	duracao := time.Since(comecou)
	destino.Finish(s.opts.Config, duracao, false)
	s.recordAttempt(destino, duracao, "ok")
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	w.Header().Set(HeaderBackend, destino.Name)
	w.Header().Set(HeaderAttempts, strconv.Itoa(tentativa))
	w.Header().Set(HeaderStrategy, s.Strategy().Name())

	n, err := copiar(w, resp)
	if err != nil {
		// Falha no meio do corpo. Não existe retry a partir daqui, porque parte
		// da resposta já saiu; o que dá para fazer é registrar e deixar o
		// destino aprender com o erro.
		span.SetStatus(codes.Error, err.Error())
		s.opts.Logger.Warn("corpo interrompido no meio",
			"correlation_id", correlacao, "backend", destino.Name, "bytes", n, "err", err)
	}
	return resultado{status: resp.StatusCode, bytes: n}, nil
}

// copiar repassa cabeçalhos e corpo do destino para o cliente.
//
// A lista de cabeçalhos é explícita. Repassar tudo às cegas levaria adiante
// coisas que pertencem à conexão entre roteador e edge, como Connection e
// Transfer-Encoding, e Content-Length de uma resposta que o roteador pode acabar
// não conseguindo copiar inteira.
func copiar(w http.ResponseWriter, resp *http.Response) (int64, error) {
	for _, nome := range []string{"Content-Type", "Content-Length", "Etag", "Last-Modified", "Cache-Control", "X-Cache", "X-Edge", "X-Origin-Generated-At"} {
		if v := resp.Header.Get(nome); v != "" {
			w.Header().Set(nome, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	return io.Copy(w, resp.Body)
}

// recordAttempt publica os medidores de uma tentativa.
func (s *Server) recordAttempt(destino *Backend, d time.Duration, desfecho string) {
	s.opts.Metrics.Attempts.WithLabelValues(destino.Name, desfecho).Inc()
	s.opts.Metrics.AttemptDuration.WithLabelValues(destino.Name).Observe(d.Seconds())
}

// observe registra a resposta bem-sucedida do ponto de vista do cliente.
func (s *Server) observe(inicio time.Time, status int) {
	code := strconv.Itoa(status)
	s.opts.Metrics.Requests.WithLabelValues(code).Inc()
	s.opts.Metrics.Duration.WithLabelValues(code).Observe(time.Since(inicio).Seconds())
}

// finish responde um erro e registra a resposta.
func (s *Server) finish(w http.ResponseWriter, _ *http.Request, inicio time.Time, status int, corpo string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, corpo)
	code := strconv.Itoa(status)
	s.opts.Metrics.Requests.WithLabelValues(code).Inc()
	s.opts.Metrics.Duration.WithLabelValues(code).Observe(time.Since(inicio).Seconds())
}

func (s *Server) tracer() trace.Tracer {
	if s.opts.Tracer == nil {
		// Sem coletor configurado, o tracer no-op deixa o código do handler igual
		// nos dois casos: nada de "if tracing habilitado" espalhado pelo caminho
		// quente.
		return noop.NewTracerProvider().Tracer("router")
	}
	return s.opts.Tracer
}

// AdminHandler devolve o roteador administrativo.
func (s *Server) AdminHandler(metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metricsHandler)

	mux.HandleFunc("GET /admin/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.State())
	})

	mux.HandleFunc("PUT /admin/strategy", func(w http.ResponseWriter, r *http.Request) {
		nome := r.URL.Query().Get("name")
		switch nome {
		case "round-robin":
			s.SetStrategy(NewRoundRobin())
		case "adaptativa":
			s.SetStrategy(NewAdaptive(s.opts.Config))
		default:
			http.Error(w, "política desconhecida: use round-robin ou adaptativa", http.StatusBadRequest)
			return
		}
		s.opts.Logger.Info("política de roteamento trocada", "strategy", nome)
		fmt.Fprintf(w, "strategy=%s\n", nome)
	})

	// O reset apaga o que os destinos ensinaram ao roteador e devolve o orçamento
	// de retry ao estado inicial. É o que permite comparar duas políticas nas
	// mesmas condições, sem reiniciar container no meio do experimento.
	mux.HandleFunc("POST /admin/reset", func(w http.ResponseWriter, _ *http.Request) {
		for _, b := range s.opts.Backends {
			b.Reset(s.opts.PalpiteInicial)
		}
		s.opts.Budget.Reset()
		s.opts.Logger.Info("estado do roteador zerado")
		fmt.Fprintln(w, "estado zerado")
	})

	return mux
}

// State é a foto do roteador, para evidência e diagnóstico.
type State struct {
	Strategy        string  `json:"strategy"`
	Backends        []Stats `json:"backends"`
	BudgetTokens    float64 `json:"retry_budget_tokens"`
	RetriesGranted  int64   `json:"retries_granted"`
	RetriesDenied   int64   `json:"retries_denied"`
	Inflight        int64   `json:"inflight"`
	Capacity        int     `json:"inflight_capacity"`
	Admitted        int64   `json:"admitted_total"`
	Rejected        int64   `json:"rejected_total"`
	UptimeSeconds   float64 `json:"uptime_seconds"`
	AttemptTimeout  string  `json:"attempt_timeout"`
	RequestTimeout  string  `json:"request_timeout"`
	MaxAttemptsUsed int     `json:"max_attempts"`
}

// State devolve a foto atual.
func (s *Server) State() State {
	agora := time.Now()
	stats := make([]Stats, 0, len(s.opts.Backends))
	for _, b := range s.opts.Backends {
		stats = append(stats, b.Stats(s.opts.Config, agora))
	}
	concedidos, negados := s.opts.Budget.Counters()
	admitidos, recusados := s.opts.Limiter.Counters()
	return State{
		Strategy:        s.Strategy().Name(),
		Backends:        stats,
		BudgetTokens:    s.opts.Budget.Tokens(),
		RetriesGranted:  concedidos,
		RetriesDenied:   negados,
		Inflight:        s.opts.Limiter.InFlight(),
		Capacity:        s.opts.Limiter.Capacity(),
		Admitted:        admitidos,
		Rejected:        recusados,
		UptimeSeconds:   agora.Sub(s.started).Seconds(),
		AttemptTimeout:  s.opts.AttemptTimeout.String(),
		RequestTimeout:  s.opts.RequestTimeout.String(),
		MaxAttemptsUsed: s.opts.MaxAttempts,
	}
}

// PublishGauges copia o estado interno para as gauges do Prometheus.
//
// As gauges são publicadas por amostragem, e não a cada requisição, porque o
// valor delas é uma foto do agora: escrever em gauge no hot path só adicionaria
// contenção para produzir o mesmo gráfico.
func (s *Server) PublishGauges() {
	agora := time.Now()
	for _, b := range s.opts.Backends {
		st := b.Stats(s.opts.Config, agora)
		s.opts.Metrics.BackendInflight.WithLabelValues(b.Name).Set(float64(st.Inflight))
		s.opts.Metrics.BackendLatency.WithLabelValues(b.Name).Set(st.LatencyMS / 1000)
		s.opts.Metrics.BackendErrorRate.WithLabelValues(b.Name).Set(st.ErrorRate)
		s.opts.Metrics.BackendCost.WithLabelValues(b.Name).Set(st.Cost)
		aberto := 0.0
		if st.Open {
			aberto = 1
		}
		s.opts.Metrics.BackendOpen.WithLabelValues(b.Name).Set(aberto)
	}
	s.opts.Metrics.BudgetTokens.Set(s.opts.Budget.Tokens())
	s.opts.Metrics.LimiterInflight.Set(float64(s.opts.Limiter.InFlight()))
	s.opts.Metrics.LimiterCapacity.Set(float64(s.opts.Limiter.Capacity()))
}

// SampleGauges publica as gauges periodicamente até o contexto acabar.
func (s *Server) SampleGauges(ctx context.Context, intervalo time.Duration) {
	tick := time.NewTicker(intervalo)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.PublishGauges()
		}
	}
}

// exceto devolve os destinos que ainda não foram tentados nesta requisição.
func exceto(todos, tentados []*Backend) []*Backend {
	if len(tentados) == 0 {
		return todos
	}
	out := make([]*Backend, 0, len(todos))
	for _, b := range todos {
		usado := false
		for _, t := range tentados {
			if t == b {
				usado = true
				break
			}
		}
		if !usado {
			out = append(out, b)
		}
	}
	return out
}

// prazo devolve o instante em que o contexto vence.
func prazo(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(time.Hour)
}

// classificar transforma o erro de transporte em rótulo de métrica.
//
// Timeout é separado de recusa de propósito: os dois viram "falhou" para o
// aprendizado do destino, mas contam histórias diferentes no dashboard. Recusa é
// um destino que não está lá; timeout é um destino que está lá e não responde, e
// é este segundo caso que o projeto investiga.
func classificar(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelado"
	default:
		return "erro"
	}
}

// desfecho escolhe o status para o cliente quando nenhuma tentativa deu certo.
func desfecho(err error, ultimoStatus int) (int, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "prazo esgotado"
	case ultimoStatus >= 500:
		return http.StatusBadGateway, "destino respondeu erro"
	case err != nil:
		return http.StatusBadGateway, "nenhum destino respondeu"
	default:
		return http.StatusBadGateway, "sem destino disponível"
	}
}

// novoID gera o identificador de correlação.
func novoID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
