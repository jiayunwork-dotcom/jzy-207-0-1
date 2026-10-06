package grid

import (
	"math"
	"testing"

	"yardgate/internal/stress"
)

func testDisc() stress.Discretizer {
	return stress.Discretizer{MaxCellSize: 0.5, FarFactor: 4}
}

func TestNewGridValidation(t *testing.T) {
	if _, err := New(0, 0, 1, 1, 4, 4, 0, nil, 10); err == nil {
		t.Error("zero depth should be rejected")
	}
	if _, err := New(0, 0, 1, 1, 4, 4, -2, nil, 10); err == nil {
		t.Error("negative depth should be rejected")
	}
	if _, err := New(0, 0, 1, 1, 4, 4, 2, nil, 0); err == nil {
		t.Error("zero default allowance should be rejected")
	}
	if _, err := New(0, 0, 1, 1, 4, 4, 2, []Zone{{Name: "z", AllowanceKPa: -5}}, 10); err == nil {
		t.Error("negative zone allowance should be rejected")
	}
	if _, err := New(0, 0, 0, 1, 4, 4, 2, nil, 10); err == nil {
		t.Error("zero spacing should be rejected")
	}
}

func TestAllowanceZones(t *testing.T) {
	g, err := New(0, 0, 1, 1, 4, 4, 2, []Zone{
		{Name: "soft", MinX: 0, MinY: 0, MaxX: 1, MaxY: 1, AllowanceKPa: 5},
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if a := g.AllowanceAt(g.Index(0, 0)); a != 5 {
		t.Errorf("in zone: got %v", a)
	}
	if a := g.AllowanceAt(g.Index(1, 1)); a != 5 {
		t.Errorf("zone boundary inclusive: got %v", a)
	}
	if a := g.AllowanceAt(g.Index(3, 3)); a != 10 {
		t.Errorf("outside zone: got %v", a)
	}
}

func TestComputeApplyEvaluate(t *testing.T) {
	g, err := New(-10, -10, 1, 1, 21, 21, 2, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	// A very small footing approximates a point load: ~11.94 kPa at center.
	load := stress.RectLoad{CX: 0, CY: 0, A: 0.1, B: 0.1, Q: 100}
	deltas := g.ComputePointDeltas([]stress.RectLoad{load}, testDisc(), 1e-9)
	if len(deltas) == 0 {
		t.Fatal("expected non-empty deltas")
	}
	// Center checkpoint (0,0) must see ~11.94 kPa.
	center := g.Index(10, 10)
	found := false
	for _, pd := range deltas {
		if pd.Index == center {
			found = true
			if math.Abs(pd.Delta-11.94) > 0.05 {
				t.Fatalf("center delta = %.4f, want ~11.94", pd.Delta)
			}
		}
	}
	if !found {
		t.Fatal("center checkpoint not affected")
	}
	// With allowance 10 the center would violate; evaluation must report it,
	// sorted by excess.
	g.allow[center] = 10
	viol := g.Evaluate(deltas, 5)
	if len(viol) == 0 || viol[0].Index != center {
		t.Fatalf("expected center violation first, got %+v", viol)
	}
	for i := 1; i < len(viol); i++ {
		if viol[i-1].ExcessKPa < viol[i].ExcessKPa {
			t.Fatal("violations not sorted by excess")
		}
	}
	// Apply and check the value landed.
	g.Apply(deltas)
	if math.Abs(g.Values[center]-11.94) > 0.05 {
		t.Fatalf("center value after apply = %.4f", g.Values[center])
	}
}

// Superposition at grid level: applying two loads together must give the
// same grid as applying them one after another.
func TestGridSuperposition(t *testing.T) {
	mk := func() *Grid {
		g, err := New(-10, -10, 1, 1, 21, 21, 2, nil, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	l1 := stress.RectLoad{CX: -3, CY: 2, A: 2, B: 1.5, Q: 300}
	l2 := stress.RectLoad{CX: 4, CY: -1, A: 1, B: 1, Q: 150}

	ga, gb := mk(), mk()
	d := testDisc()
	ga.Apply(ga.ComputePointDeltas([]stress.RectLoad{l1, l2}, d, 1e-12))
	gb.Apply(gb.ComputePointDeltas([]stress.RectLoad{l1}, d, 1e-12))
	gb.Apply(gb.ComputePointDeltas([]stress.RectLoad{l2}, d, 1e-12))
	for i := range ga.Values {
		if ga.Values[i] != gb.Values[i] {
			t.Fatalf("checkpoint %d: together %v, separately %v", i, ga.Values[i], gb.Values[i])
		}
	}
}
