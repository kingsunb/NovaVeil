package relay

import "testing"

func TestCloneRouteStateDeepCopiesEmergencyMaps(t *testing.T) {
	original := RouteState{
		GroupID:           7,
		Cooldowns:         map[int]int64{1: 111},
		Levels:            map[int]int{2: 3},
		HalfOpens:         map[int]int64{4: 555},
		PostCommitStrikes: map[int]int{6: 8},
		emergencyCounts:   map[int]int{1: 2},
		emergencyBlocks:   map[int]int64{3: 999},
	}
	cloned := cloneRouteState(&original)

	original.emergencyCounts[9] = 10
	original.emergencyBlocks[12] = 13
	original.Cooldowns[14] = 15

	if cloned.emergencyCounts[9] != 0 {
		t.Fatalf("emergencyCounts must be deep-copied, got %+v", cloned.emergencyCounts)
	}
	if cloned.emergencyBlocks[12] != 0 {
		t.Fatalf("emergencyBlocks must be deep-copied, got %+v", cloned.emergencyBlocks)
	}
	if cloned.Cooldowns[14] != 0 {
		t.Fatalf("Cooldowns must stay independent, got %+v", cloned.Cooldowns)
	}
	if cloned.emergencyCounts[1] != 2 || cloned.emergencyBlocks[3] != 999 {
		t.Fatalf("cloned emergency maps must preserve original values: %+v %+v", cloned.emergencyCounts, cloned.emergencyBlocks)
	}
}
