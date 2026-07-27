package memory

import "testing"

func TestNormalizeFTSRanksAndMergeHybridScores(t *testing.T) {
	fts := NormalizeFTSRanks(map[string]float64{
		"best":  -10,
		"worse": -2,
	})
	if fts["best"] != 1 || fts["worse"] != 0 {
		t.Fatalf("normalized=%v", fts)
	}
	single := NormalizeFTSRanks(map[string]float64{"only": -5})
	if single["only"] != 1 {
		t.Fatalf("single=%v", single)
	}

	merged := MergeHybridScores(
		map[string]float64{"both": 0.9, "fts": 0.8},
		map[string]float64{"both": 0.5, "vec": 1.0},
		0.3, 0.7,
	)
	if merged["fts"] != 0.8 {
		t.Fatalf("fts-only should keep full score: %v", merged)
	}
	if want := 0.7; merged["vec"] != want {
		t.Fatalf("vec-only=%v want %v", merged["vec"], want)
	}
	both := 0.3*0.9 + 0.7*0.5
	if both < 0.9 {
		both = 0.9
	}
	if merged["both"] != both {
		t.Fatalf("both=%v want %v", merged["both"], both)
	}
}

func TestIsSemanticallyDuplicateAndThreshold(t *testing.T) {
	if EffectiveSemanticDedupThreshold(nil) != DefaultSemanticDedupThreshold {
		t.Fatal("nil should default to 0.92")
	}
	high := 2.0
	if EffectiveSemanticDedupThreshold(&high) != 1 {
		t.Fatal("threshold should clamp to 1")
	}
	low := -1.0
	if EffectiveSemanticDedupThreshold(&low) != 0 {
		t.Fatal("threshold should clamp to 0")
	}

	// Identical unit vectors → L2 0 → similarity 1
	if !IsSemanticallyDuplicate([]VecNeighbor{{ChunkID: "a", Distance: 0}}, 0.92) {
		t.Fatal("identical embedding should dedup")
	}
	// Orthogonal-ish: distance 2 → similarity 0
	if IsSemanticallyDuplicate([]VecNeighbor{{ChunkID: "a", Distance: 2}}, 0.92) {
		t.Fatal("distant neighbor should allow write")
	}
	if IsSemanticallyDuplicate(nil, 0.92) {
		t.Fatal("empty neighbors fail open")
	}
	if DistanceToSimilarity(1) != 0.5 {
		t.Fatalf("mid distance similarity=%v", DistanceToSimilarity(1))
	}
}
