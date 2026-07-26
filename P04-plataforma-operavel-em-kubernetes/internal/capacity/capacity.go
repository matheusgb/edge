// Package capacity implementa o modelo de capacidade do P04: converte
// a premissa de 3 milhões de requisições por minuto (50 mil rps) em uma
// estimativa de réplicas, a partir de uma capacidade por réplica
// realmente medida neste laboratório.
//
// O resultado nunca prova que o laptop sustenta a premissa. Ele separa
// o que foi medido (capacidade por réplica, neste hardware, nesta
// rodada) do que é projetado (a premissa de 50 mil rps e os fatores de
// segurança aplicados sobre ela).
package capacity

import "math"

// Input reúne as variáveis nomeadas do modelo. Nenhum valor é fixado no
// código: todos vêm de flags ou de uma medição anterior.
type Input struct {
	// PremiseRPS é a premissa de modelagem (Meta/Premissa, não Medido).
	PremiseRPS float64
	// CapacityPerReplicaRPS é Medido: o throughput sustentável de uma
	// única réplica, obtido de uma rodada de saturação real.
	CapacityPerReplicaRPS float64
	// TargetUtilization é a fração da capacidade medida que se deseja
	// usar em regime normal, deixando margem para picos.
	TargetUtilization float64
	// Headroom é um multiplicador de segurança adicional sobre o número
	// de réplicas calculado (ex.: 1.2 para 20% de folga).
	Headroom float64
	// CacheHitRate é a fração de requisições esperada para ser
	// absorvida pelo cache da borda, reduzindo a carga que chega à
	// origem. 0 é a suposição mais conservadora.
	CacheHitRate float64
	// AvgResponseBytes e BandwidthLimitBytesPerSec descrevem o limite de
	// banda por réplica; usados só para o alerta de banda, não para o
	// cálculo principal de réplicas.
	AvgResponseBytes          float64
	BandwidthLimitBytesPerSec float64
	// Zones é o número de zonas consideradas e ZoneFailureTolerance
	// indica se o dimensionamento deve sobreviver à perda de uma zona
	// (N-1).
	Zones               int
	ZoneFailureTolerant bool
}

// Result é a saída do modelo, com cada campo rotulado como medido ou
// projetado no texto do relatório, não apenas no JSON.
type Result struct {
	Input                Input   `json:"input"`
	EffectiveRPS         float64 `json:"effective_rps"`
	BaseReplicas         int     `json:"base_replicas"`
	ReplicasWithHeadroom int     `json:"replicas_with_headroom"`
	ReplicasFinal        int     `json:"replicas_final"`
	BandwidthNeededBps   float64 `json:"bandwidth_needed_bytes_per_sec"`
	BandwidthPerReplica  float64 `json:"bandwidth_per_replica_bytes_per_sec"`
	BandwidthBound       bool    `json:"bandwidth_bound"`
}

// Calculate aplica réplicas = teto(rps_premissa / capacidade_por_réplica
// / utilização_alvo), depois headroom e, se pedido, o multiplicador de
// tolerância a falha de uma zona.
func Calculate(in Input) Result {
	effectiveRPS := in.PremiseRPS * (1 - in.CacheHitRate)

	base := effectiveRPS / (in.CapacityPerReplicaRPS * in.TargetUtilization)
	baseReplicas := int(math.Ceil(base))

	withHeadroom := int(math.Ceil(float64(baseReplicas) * in.Headroom))

	final := withHeadroom
	if in.ZoneFailureTolerant && in.Zones > 1 {
		// Perder uma zona deixa Zones-1 zonas para absorver o total: o
		// número de réplicas precisa ser suficiente mesmo com uma zona
		// fora, então escala pela razão zones/(zones-1) e arredonda para
		// cima, além de garantir um múltiplo do número de zonas para
		// distribuição uniforme.
		factor := float64(in.Zones) / float64(in.Zones-1)
		final = int(math.Ceil(float64(withHeadroom) * factor))
		if rem := final % in.Zones; rem != 0 {
			final += in.Zones - rem
		}
	}

	bandwidthNeeded := effectiveRPS * in.AvgResponseBytes
	var bandwidthPerReplica float64
	if final > 0 {
		bandwidthPerReplica = bandwidthNeeded / float64(final)
	}

	return Result{
		Input:                in,
		EffectiveRPS:         effectiveRPS,
		BaseReplicas:         baseReplicas,
		ReplicasWithHeadroom: withHeadroom,
		ReplicasFinal:        final,
		BandwidthNeededBps:   bandwidthNeeded,
		BandwidthPerReplica:  bandwidthPerReplica,
		BandwidthBound:       in.BandwidthLimitBytesPerSec > 0 && bandwidthPerReplica > in.BandwidthLimitBytesPerSec,
	}
}
