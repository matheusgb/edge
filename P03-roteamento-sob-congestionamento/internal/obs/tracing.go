// Package obs liga os traces do projeto.
//
// Métrica responde "quantas requisições ficaram lentas". Trace responde "por onde
// passou ESTA requisição lenta, e quanto tempo ela gastou em cada etapa". No P03
// a segunda pergunta é indispensável: quando o cliente vê 900ms, a métrica não
// diz se foram três tentativas de 300ms, uma tentativa lenta ou espera na fila do
// roteador. O trace mostra os spans lado a lado e a resposta fica evidente.
//
// O trace também é o único lugar onde o identificador de correlação pode virar
// atributo sem medo: cardinalidade alta é problema de métrica, e é exatamente o
// caso de uso de um sistema de traces.
package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Tracing liga o exportador OTLP e devolve o tracer e a função de encerramento.
//
// Endpoint vazio devolve um tracer no-op, e isso é proposital: os testes e o
// `go run` local não devem exigir um coletor de traces no ar. O código do
// caminho quente fica igual nos dois casos.
func Tracing(ctx context.Context, endpoint, servico string, taxa float64) (trace.Tracer, func(context.Context) error, error) {
	if endpoint == "" {
		return noop.NewTracerProvider().Tracer(servico), func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		// Sem TLS porque o coletor está na rede do Compose, ao lado. Num ambiente
		// que atravessa rede de verdade, isto teria que mudar.
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("criando exportador OTLP: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", servico),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("descrevendo o serviço: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		// Amostragem é decisão de custo, e ela é do experimento: durante uma
		// falha controlada de dois minutos, amostrar tudo é barato e a cauda
		// inteira fica registrada. Em carga sustentada, 100% de trace custaria
		// mais CPU no processo medido do que o próprio tráfego.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(taxa))),
	)
	otel.SetTracerProvider(provider)
	// O propagador é o que faz o span do edge virar filho do span do roteador em
	// vez de um trace solto. Sem ele, cada processo produz uma árvore própria e a
	// requisição inteira nunca aparece junta.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return provider.Tracer(servico), provider.Shutdown, nil
}
