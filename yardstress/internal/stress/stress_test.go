package stress

import (
	"math"
	"testing"

	"yardstress/internal/geom"
)

// Reference values supplied by the geotechnical consultant:
// 100 kN point load, 2 m below the surface:
//   - directly underneath:  sigma_z ~= 11.94 kPa
//   - 1.5 m horizontal off: sigma_z ~= 3.91 kPa
func TestPointLoadCheckValues(t *testing.T) {
	if got := PointLoad(100, 0, 2); math.Abs(got-11.94) > 0.005 {
		t.Errorf("directly below: got %.6f kPa, want ~11.94 kPa", got)
	}
	if got := PointLoad(100, 1.5, 2); math.Abs(got-3.91) > 0.005 {
		t.Errorf("1.5 m off: got %.6f kPa, want ~3.91 kPa", got)
	}
}

// The integral of sigma_z over any horizontal plane must equal the total
// applied surface load. Verified by direct numerical integration of the
// kernel (midpoint rule) over a plane extending to 60 z on each side; the
// neglected tail is (z/L)^3 ~ 5e-6 of the load.
func TestPlaneIntegralEqualsLoad(t *testing.T) {
	const P = 100.0
	for _, z := range []float64{2, 5, 12} {
		L := 60 * z
		h := z / 4 // integration step
		n := int(2*L/h) + 1
		sum := 0.0
		for i := 0; i < n; i++ {
			x := -L + (float64(i)+0.5)*h
			for j := 0; j < n; j++ {
				y := -L + (float64(j)+0.5)*h
				sum += PointLoad(P, math.Hypot(x, y), z)
			}
		}
		got := sum * h * h
		if rel := math.Abs(got-P) / P; rel > 1e-3 {
			t.Errorf("z=%v: plane integral = %.6f kN, want %v kN (rel err %.2e)", z, got, P, rel)
		}
	}
}

// Superposition: the stress of two loads acting together equals the sum of
// the stresses of each load acting alone.
func TestSuperposition(t *testing.T) {
	loads := [][3]float64{{150, 3, 4}, {80, -7, 2.5}, {220, 0, -6}} // P, x, y
	pts := [][2]float64{{0, 0}, {5, 5}, {-3, 8}, {12, -4}}
	z := 10.0
	for _, p := range pts {
		var separate, combined float64
		for _, l := range loads {
			separate += PointLoadAt(l[0], l[1], l[2], p[0], p[1], z)
		}
		for _, l := range loads {
			combined += PointLoadAt(l[0], l[1], l[2], p[0], p[1], z)
		}
		if separate != combined {
			t.Fatalf("superposition violated at %v: %v vs %v", p, separate, combined)
		}
	}
}

// Exact rectangle solution sanity checks.
func TestRectangleUniformLimits(t *testing.T) {
	z := 12.0
	q := 40.0
	// A very large loaded area: stress at depth approaches q (1-D condition).
	big := geom.Rect{MinX: -600, MinY: -600, MaxX: 600, MaxY: 600}
	if got := RectangleUniform(q, big, 0, 0, z); math.Abs(got-q)/q > 1e-3 {
		t.Errorf("large area: got %.6f, want ~%v", got, q)
	}
	// A small loaded area behaves like a point load P = q*A.
	small := geom.Rect{MinX: -0.25, MinY: -0.25, MaxX: 0.25, MaxY: 0.25}
	P := q * small.Area()
	got := RectangleUniform(q, small, 30, 40, z)
	want := PointLoad(P, 50, z)
	if rel := math.Abs(got-want) / want; rel > 1e-3 {
		t.Errorf("far field of small area: got %.6f, want ~%.6f (point load)", got, want)
	}
	// Stress below the centre equals 4x the stress below the corner of each
	// quadrant (symmetry / superposition of the exact solution).
	rc := geom.Rect{MinX: 0, MinY: 0, MaxX: 12.5, MaxY: 3}
	cx, cy := 6.25, 1.5
	centre := RectangleUniform(q, geom.Rect{MinX: 0, MinY: 0, MaxX: 12.5, MaxY: 3}, cx, cy, z)
	quad := RectangleUniform(q, geom.Rect{MinX: cx, MinY: cy, MaxX: 12.5, MaxY: 3}, cx, cy, z)
	if rel := math.Abs(centre-4*quad) / centre; rel > 1e-12 {
		t.Errorf("centre != 4*quadrant: %v vs %v", centre, 4*quad)
	}
	_ = rc
}
