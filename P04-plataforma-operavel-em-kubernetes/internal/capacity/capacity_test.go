package capacity

import "testing"

func TestCalculateArredondaParaCima(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:            50_000,
		CapacityPerReplicaRPS: 3_000,
		TargetUtilization:     0.7,
		Headroom:              1.0,
	})
	// 50000 / (3000 * 0.7) = 23.809... -> teto = 24
	if res.BaseReplicas != 24 {
		t.Fatalf("esperava 24 réplicas base, obteve %d", res.BaseReplicas)
	}
}

func TestCalculateAplicaHeadroom(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:            10_000,
		CapacityPerReplicaRPS: 1_000,
		TargetUtilization:     1.0,
		Headroom:              1.2,
	})
	// base = 10, com headroom 1.2 -> teto(12) = 12
	if res.BaseReplicas != 10 {
		t.Fatalf("esperava 10 réplicas base, obteve %d", res.BaseReplicas)
	}
	if res.ReplicasWithHeadroom != 12 {
		t.Fatalf("esperava 12 réplicas com headroom, obteve %d", res.ReplicasWithHeadroom)
	}
}

func TestCalculateCacheHitReduzCargaEfetiva(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:            10_000,
		CapacityPerReplicaRPS: 1_000,
		TargetUtilization:     1.0,
		Headroom:              1.0,
		CacheHitRate:          0.8,
	})
	if diff := res.EffectiveRPS - 2_000; diff > 0.001 || diff < -0.001 {
		t.Fatalf("esperava rps efetivo de ~2000 com 80%% de cache hit, obteve %f", res.EffectiveRPS)
	}
	if res.BaseReplicas != 2 {
		t.Fatalf("esperava 2 réplicas base, obteve %d", res.BaseReplicas)
	}
}

func TestCalculateZonaTolerantAMultiploDeZonas(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:            9_000,
		CapacityPerReplicaRPS: 1_000,
		TargetUtilization:     1.0,
		Headroom:              1.0,
		Zones:                 3,
		ZoneFailureTolerant:   true,
	})
	if res.ReplicasFinal%3 != 0 {
		t.Fatalf("esperava múltiplo de 3 zonas, obteve %d", res.ReplicasFinal)
	}
	// base=9, headroom=9, perder 1 zona de 3: fator 3/2=1.5 -> teto(13.5)=14 -> arredonda para múltiplo de 3 -> 15
	if res.ReplicasFinal != 15 {
		t.Fatalf("esperava 15 réplicas finais, obteve %d", res.ReplicasFinal)
	}
}

func TestCalculateBandwidthBound(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:                1_000,
		CapacityPerReplicaRPS:     100,
		TargetUtilization:         1.0,
		Headroom:                  1.0,
		AvgResponseBytes:          1_000_000,
		BandwidthLimitBytesPerSec: 1_000,
	})
	if !res.BandwidthBound {
		t.Fatal("esperava que o cenário fosse limitado por banda")
	}
}

func TestCalculateSemLimiteDeBandaNaoMarcaBound(t *testing.T) {
	res := Calculate(Input{
		PremiseRPS:            1_000,
		CapacityPerReplicaRPS: 100,
		TargetUtilization:     1.0,
		Headroom:              1.0,
	})
	if res.BandwidthBound {
		t.Fatal("sem limite de banda configurado, não deveria marcar bandwidth_bound")
	}
}
