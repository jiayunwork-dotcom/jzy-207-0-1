package stress

import (
	"math"
	"testing"
)

// Verification values given by the geotechnical consultant:
// 100 kN point load, 2 m depth: 11.94 kPa directly below, 3.91 kPa at r=1.5 m.
func TestBoussinesqCheckValues(t *testing.T) {
	got := VerticalStressPointLoad(100, 0, 2)
	if math.Abs(got-11.94) > 0.01 {
		t.Fatalf("below load: got %.4f kPa, want ~11.94", got)
	}
	got = VerticalStressPointLoad(100, 1.5, 2)
	if math.Abs(got-3.91) > 0.01 {
		t.Fatalf("offset 1.5m: got %.4f kPa, want ~3.91", got)
	}
}

// Superposition: the stress of several loads acting together equals the sum
// of the stresses of each load acting alone.
func TestSuperposition(t *testing.T) {
	loads := []PointLoad{{X: 0, Y: 0, P: 120}, {X: 3, Y: -4, P: 80}, {X: -2, Y: 1.5, P: 55}}
	z := 2.0
	for _, pt := range [][2]float64{{0, 0}, {1.5, 2.5}, {-3, 1}, {10, 10}} {
		sum := 0.0
		for _, l := range loads {
			sum += VerticalStressPointLoad(l.P, math.Hypot(pt[0]-l.X, pt[1]-l.Y), z)
		}
		combined := 0.0
		for _, l := range loads { // independent second evaluation path
			r := math.Sqrt((pt[0]-l.X)*(pt[0]-l.X) + (pt[1]-l.Y)*(pt[1]-l.Y))
			combined += 3 * l.P * z * z * z / (2 * math.Pi * math.Pow(r*r+z*z, 2.5))
		}
		if math.Abs(sum-combined) > 1e-12 {
			t.Fatalf("superposition mismatch at %v: %v vs %v", pt, sum, combined)
		}
	}
}

// The integral of σz over any horizontal plane equals the total surface load.
func TestPlaneIntegralEqualsLoad(t *testing.T) {
	const P = 100.0
	const z = 2.0
	// Numerically integrate over a large plane; the tail beyond 100 m is
	// negligible at this depth.
	const half = 100.0
	const dx = 0.25
	sum := 0.0
	for x := -half + dx/2; x < half; x += dx {
		for y := -half + dx/2; y < half; y += dx {
			sum += VerticalStressPointLoad(P, math.Hypot(x, y), z)
		}
	}
	integral := sum * dx * dx
	if math.Abs(integral-P) > 0.002*P {
		t.Fatalf("plane integral = %.4f kN, want %v (±0.2%%)", integral, P)
	}
}

// Subdivision must conserve the total load.
func TestSubdivideConservesLoad(t *testing.T) {
	rl := RectLoad{CX: 1, CY: -2, A: 6, B: 2.4, Q: 900}
	for _, mn := range [][2]int{{1, 1}, {3, 2}, {7, 5}, {16, 16}} {
		sum := 0.0
		for _, p := range Subdivide(rl, mn[0], mn[1]) {
			sum += p.P
		}
		if math.Abs(sum-rl.Q) > 1e-9 {
			t.Fatalf("subdivide %v: total %v, want %v", mn, sum, rl.Q)
		}
	}
}

// The discrete approximation must converge to a single limit as the
// subdivision is refined, with error decreasing roughly quadratically
// (second-order in cell size) once the cell size is well below the depth.
func TestDiscretizationConvergence(t *testing.T) {
	rl := RectLoad{CX: 0, CY: 0, A: 6, B: 2.4, Q: 900}
	z := 2.0
	eval := []struct{ x, y float64 }{{0, 0}, {2.0, 1.0}, {3.5, 0.5}, {0, 4.0}}

	stressAt := func(m, n int, x, y float64) float64 {
		s := 0.0
		for _, p := range Subdivide(rl, m, n) {
			s += VerticalStressPointLoad(p.P, math.Hypot(x-p.X, y-p.Y), z)
		}
		return s
	}

	for _, pt := range eval {
		// Levels m = 16, 32, 64, 128 are inside the asymptotic regime
		// (cell size <= 0.375 m << z = 2 m): successive differences must
		// shrink by a factor of ~4 per refinement (second order).
		var diffs []float64
		prev := stressAt(16, 16, pt.x, pt.y)
		for _, m := range []int{32, 64, 128} {
			v := stressAt(m, m, pt.x, pt.y)
			diffs = append(diffs, math.Abs(v-prev))
			prev = v
		}
		for i := 1; i < len(diffs); i++ {
			if ratio := diffs[i-1] / diffs[i]; ratio < 3.0 {
				t.Fatalf("pt %v: error contraction ratio %.2f < 3 (diffs %v)", pt, ratio, diffs)
			}
		}
		// Richardson extrapolation of the last two levels gives the limit;
		// the finest level must already be within 1e-3 kPa of it.
		v64 := stressAt(64, 64, pt.x, pt.y)
		v128 := stressAt(128, 128, pt.x, pt.y)
		limit := v128 + (v128-v64)/3
		if math.Abs(v128-limit) > 1e-3 {
			t.Fatalf("pt %v: not converged, |v128-limit| = %g kPa", pt, math.Abs(v128-limit))
		}
		// The crude whole-block approximation heads toward the same limit.
		v1 := stressAt(1, 1, pt.x, pt.y)
		v2 := stressAt(2, 2, pt.x, pt.y)
		if math.Abs(v2-limit) >= math.Abs(v1-limit) && math.Abs(v1-limit) > 1e-9 {
			t.Fatalf("pt %v: refinement does not approach the limit (v1=%.4f v2=%.4f limit=%.4f)", pt, v1, v2, limit)
		}
	}
}

// The Discretizer must agree with brute-force fine subdivision near the load
// and with a single point load far away.
func TestDiscretizerMatchesFineSubdivision(t *testing.T) {
	d := Discretizer{MaxCellSize: 0.5, FarFactor: 4}
	rl := RectLoad{CX: 0, CY: 0, A: 6, B: 2.4, Q: 900}
	z := 2.0

	ref := func(x, y float64) float64 {
		s := 0.0
		for _, p := range Subdivide(rl, 64, 64) {
			s += VerticalStressPointLoad(p.P, math.Hypot(x-p.X, y-p.Y), z)
		}
		return s
	}

	// Near field: discretizer subdivides; must be close to the 64x64 reference.
	// With 0.5 m cells at z = 2 m the second-order error is ~0.5% of the peak.
	for _, pt := range [][2]float64{{0, 0}, {2, 1}, {3, 1.2}} {
		got, want := d.StressAt(rl, pt[0], pt[1], z), ref(pt[0], pt[1])
		if math.Abs(got-want) > 0.25 {
			t.Fatalf("near field %v: got %.5f, ref %.5f", pt, got, want)
		}
	}
	// Far field: single-point approximation; error is O((diag/r)^2), here a
	// few percent at r ≈ 50 m for a 6 m x 2.4 m footing.
	x, y := 40.0, 30.0
	got, want := d.StressAt(rl, x, y, z), ref(x, y)
	if math.Abs(got-want)/want > 0.05 {
		t.Fatalf("far field: got %.6f, ref %.6f", got, want)
	}
}
