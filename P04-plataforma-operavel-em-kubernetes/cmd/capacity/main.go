// Command capacity converte a capacidade por réplica medida em uma
// estimativa de réplicas para a premissa de 50 mil rps, separando
// claramente o que é medido do que é projetado.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/capacity"
)

func main() {
	premiseRPS := flag.Float64("premise-rps", 50_000, "premissa de requisições por segundo (3 milhões/min)")
	capacityPerReplica := flag.Float64("capacity-per-replica-rps", 0, "capacidade medida por réplica, em rps (obrigatório)")
	targetUtilization := flag.Float64("target-utilization", 0.7, "fração alvo da capacidade medida em regime normal")
	headroom := flag.Float64("headroom", 1.2, "multiplicador de folga sobre o número de réplicas")
	cacheHitRate := flag.Float64("cache-hit-rate", 0, "fração de requisições esperada absorvida pelo cache da borda")
	avgResponseBytes := flag.Float64("avg-response-bytes", 65536, "tamanho médio de resposta, em bytes")
	bandwidthLimit := flag.Float64("bandwidth-limit-bytes-per-sec", 0, "limite de banda por réplica, em bytes/s (0 desliga o alerta)")
	zones := flag.Int("zones", 3, "número de zonas consideradas")
	zoneTolerant := flag.Bool("zone-failure-tolerant", true, "dimensionar para sobreviver à perda de uma zona (N-1)")
	flag.Parse()

	if *capacityPerReplica <= 0 {
		fmt.Fprintln(os.Stderr, "uso: capacity -capacity-per-replica-rps=<medido> [outras flags]")
		fmt.Fprintln(os.Stderr, "capacity-per-replica-rps é obrigatório e deve vir de uma medição real (ex.: evidence/escala/).")
		os.Exit(2)
	}

	in := capacity.Input{
		PremiseRPS:                *premiseRPS,
		CapacityPerReplicaRPS:     *capacityPerReplica,
		TargetUtilization:         *targetUtilization,
		Headroom:                  *headroom,
		CacheHitRate:              *cacheHitRate,
		AvgResponseBytes:          *avgResponseBytes,
		BandwidthLimitBytesPerSec: *bandwidthLimit,
		Zones:                     *zones,
		ZoneFailureTolerant:       *zoneTolerant,
	}
	res := capacity.Calculate(in)

	fmt.Println("# Modelo de capacidade")
	fmt.Println()
	fmt.Printf("Medido: capacidade por réplica = %.0f rps (rodada de saturação local).\n", in.CapacityPerReplicaRPS)
	fmt.Printf("Premissa: %.0f rps (3 milhões de requisições por minuto).\n", in.PremiseRPS)
	fmt.Printf("Projetado: rps efetivo após cache hit de %.0f%% = %.0f rps.\n", in.CacheHitRate*100, res.EffectiveRPS)
	fmt.Printf("Projetado: réplicas base (utilização alvo %.0f%%) = %d.\n", in.TargetUtilization*100, res.BaseReplicas)
	fmt.Printf("Projetado: réplicas com headroom (%.2fx) = %d.\n", in.Headroom, res.ReplicasWithHeadroom)
	if in.ZoneFailureTolerant {
		fmt.Printf("Projetado: réplicas finais tolerantes à perda de 1 de %d zonas = %d.\n", in.Zones, res.ReplicasFinal)
	} else {
		fmt.Printf("Projetado: réplicas finais (sem tolerância a falha de zona) = %d.\n", res.ReplicasFinal)
	}
	if in.BandwidthLimitBytesPerSec > 0 {
		if res.BandwidthBound {
			fmt.Printf("Alerta: banda necessária por réplica (%.0f B/s) excede o limite informado (%.0f B/s).\n",
				res.BandwidthPerReplica, in.BandwidthLimitBytesPerSec)
		} else {
			fmt.Printf("Banda necessária por réplica: %.0f B/s, dentro do limite informado.\n", res.BandwidthPerReplica)
		}
	}
	fmt.Println()
	fmt.Println("Este número não prova que o laptop deste laboratório sustenta a premissa de 50 mil rps;")
	fmt.Println("ele projeta quantas réplicas seriam necessárias SE a capacidade medida por réplica se mantivesse.")
	fmt.Println()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(res)
}
