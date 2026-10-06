package events

import "testing"

func TestDeltas(t *testing.T) {
	place := Event{Type: Place, StackID: "S1", Weight: 100}
	if d := place.Deltas(); len(d) != 1 || d[0].Weight != 100 || d[0].StackID != "S1" {
		t.Fatalf("place deltas: %+v", d)
	}
	pick := Event{Type: Pick, StackID: "S1", Weight: 40}
	if d := pick.Deltas(); len(d) != 1 || d[0].Weight != -40 {
		t.Fatalf("pick deltas: %+v", d)
	}
	restow := Event{Type: Restow, FromID: "S1", ToID: "S2", Weight: 60}
	d := restow.Deltas()
	if len(d) != 2 || d[0].Weight != -60 || d[1].Weight != 60 {
		t.Fatalf("restow deltas: %+v", d)
	}
	// Restow within one stack is load-neutral.
	same := Event{Type: Restow, FromID: "S1", ToID: "S1", Weight: 60}
	if len(same.Deltas()) != 0 {
		t.Fatal("same-stack restow must produce no ground-load delta")
	}
	corr := Event{Type: Correction, DeltaList: []Delta{{StackID: "S1", Weight: 25}}}
	if d := corr.Deltas(); len(d) != 1 || d[0].Weight != 25 {
		t.Fatalf("correction deltas: %+v", d)
	}
}

func TestEffectiveJobsFoldsCorrections(t *testing.T) {
	log := []Event{
		{Seq: 1, Type: Place, StackID: "S1", Weight: 100, Tiers: 1},
		{Seq: 2, Type: Place, StackID: "S2", Weight: 200, Tiers: 1},
		{Seq: 3, Type: Correction, TargetSeq: 1, NewWeight: 150, DeltaList: []Delta{{StackID: "S1", Weight: 50}}},
		{Seq: 4, Type: Pick, StackID: "S2", Weight: 200, Tiers: 1},
		{Seq: 5, Type: Correction, TargetSeq: 1, NewWeight: 180, DeltaList: []Delta{{StackID: "S1", Weight: 30}}},
	}
	eff := EffectiveJobs(log)
	if len(eff) != 3 {
		t.Fatalf("effective jobs: %+v", eff)
	}
	// Last correction wins.
	if eff[0].Weight != 180 {
		t.Fatalf("job 1 effective weight = %v, want 180", eff[0].Weight)
	}
	if eff[1].Weight != 200 || eff[2].Weight != 200 {
		t.Fatalf("untouched jobs changed: %+v", eff)
	}
	// Corrections themselves disappear from the job stream.
	for _, e := range eff {
		if e.Type == Correction {
			t.Fatal("correction leaked into effective jobs")
		}
	}
	if w, ok := EffectiveWeight(log, 1); !ok || w != 180 {
		t.Fatalf("EffectiveWeight(1) = %v %v", w, ok)
	}
	if _, ok := EffectiveWeight(log, 99); ok {
		t.Fatal("EffectiveWeight(99) should not be found")
	}
}

func TestApplyToStacks(t *testing.T) {
	states := map[string]StackState{}
	ApplyToStacks(states, Event{Type: Place, StackID: "S1", Weight: 100, Tiers: 2})
	ApplyToStacks(states, Event{Type: Restow, FromID: "S1", ToID: "S2", Weight: 40, Tiers: 1})
	ApplyToStacks(states, Event{Type: Correction, DeltaList: []Delta{{StackID: "S1", Weight: 10}}})
	if s := states["S1"]; s.Tiers != 1 || s.Weight != 70 {
		t.Fatalf("S1 = %+v, want tiers 1 weight 70", s)
	}
	if s := states["S2"]; s.Tiers != 1 || s.Weight != 40 {
		t.Fatalf("S2 = %+v, want tiers 1 weight 40", s)
	}
}
