package discretize

import (
	"math"
	"testing"

	"yardstress/internal/geom"
	"yardstress/internal/stress"
)

// The sub-loads must sum to the total stack weight.
func TestPatchConservesLoad(t *testing.T) {
	r := geom.Rect{MinX: 10, MinY: 4, MaxX: 22.5, MaxY: 7}
	for _, n := range [][2]int{{1, 1}, {3, 2}, {5, 4}, {16, 16}} {
		loads := PatchN(r, 1500, n[0], n[1])
		sum := 0.0
		for _, l := range loads {
			sum += l.P
		}
		if rel := math.Abs(sum-1500) / 1500; rel > 1e-12 {
			t.Errorf("n=%v: total load %.12f, want 1500", n, sum)
		}
	}
}

// Refining the subdivision must converge to the exact uniformly loaded
// rectangle solution, with the second-order rate of the midpoint rule
// (error ~ (h/z)^2, i.e. ~4x smaller per doubling).
func TestConvergenceToExactRectangle(t *testing.T) {
	r := geom.Rect{MinX: 0, MinY: 0, MaxX: 12.5, MaxY: 3}
	const W = 1500.0 // kN
	q := W / r.Area()
	z := 12.0
	points := [][2]float64{
		{6.25, 1.5},  // below centre
		{12.5, 1.5},  // below edge midpoint
		{16, 6},      // outside, off the corner
		{6.25, -2.5}, // outside, off the long side
	}
	for _, p := range points {
		exact := stress.RectangleUniform(q, r, p[0], p[1], z)
		var prevErr float64
		var errs []float64
		for k := 1; k <= 32; k *= 2 {
			loads := PatchN(r, W, k, k)
			got := 0.0
			for _, l := range loads {
				got += stress.PointLoadAt(l.P, l.X, l.Y, p[0], p[1], z)
			}
			err := math.Abs(got - exact)
			errs = append(errs, err)
			if k >= 4 && err > prevErr*1.2 {
				t.Fatalf("point %v: error not decreasing at k=%d: %g -> %g", p, k, prevErr, err)
			}
			prevErr = err
		}
		// Log the convergence table (used in the README error analysis).
		t.Logf("point %v exact=%.6f", p, exact)
		for i, k := 0, 1; k <= 32; i, k = i+1, k*2 {
			t.Logf("  %4d x %-4d subcells: abs err %.3e kPa", k, k, errs[i])
		}
		// Final refinement must be close; and the last refinement step must
		// show roughly the quadratic rate (factor ~4, accept 2..8).
		last := errs[len(errs)-1]
		if rel := last / math.Abs(exact); rel > 1e-3 {
			t.Errorf("point %v: rel err at 32x32 = %.2e, want < 1e-3", p, rel)
		}
		rate := errs[len(errs)-2] / last
		if rate < 2 || rate > 8 {
			t.Errorf("point %v: last refinement rate %.2f, want ~4 (second order)", p, rate)
		}
	}
}

// InfluenceRadius must invert the kernel: at the radius the stress equals
// the cutoff, beyond it it is smaller.
func TestInfluenceRadius(t *testing.T) {
	const P, z, cut = 1000.0, 12.0, 1e-3
	r := InfluenceRadius(P, z, cut)
	if r <= 0 {
		t.Fatal("radius must be positive")
	}
	if got := stress.PointLoad(P, r, z); math.Abs(got-cut)/cut > 1e-9 {
		t.Errorf("stress at radius = %g, want cutoff %g", got, cut)
	}
	if got := stress.PointLoad(P, 2*r, z); got >= cut {
		t.Errorf("stress beyond radius = %g >= cutoff %g", got, cut)
	}
	if InfluenceRadius(1e-9, z, cut) != 0 {
		t.Error("tiny load should have zero influence radius")
	}
}
