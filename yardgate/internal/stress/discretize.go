package stress

import "math"

// RectLoad is a uniformly distributed vertical load of total magnitude Q (kN)
// spread over a horizontal rectangle centered at (CX, CY) with side lengths
// A (along x) and B (along y), in meters. Q may be negative (load removal).
type RectLoad struct {
	CX, CY float64
	A, B   float64
	Q      float64
}

// PointLoad is a concentrated load P (kN) applied at surface point (X, Y).
type PointLoad struct {
	X, Y float64
	P    float64
}

// Subdivide splits a rectangular uniform load into m×n equal point loads
// placed at sub-cell centers. The sum of the point loads equals rl.Q.
//
// As m,n → ∞ the discrete superposition converges to the exact integral of
// the Boussinesq kernel over the rectangle; the discretization error of the
// m×n approximation is O((s/z)²) where s is the sub-cell size and z the
// evaluation depth (the first-order moment terms cancel by symmetry, the
// leading error term is the second moment of the cell).
func Subdivide(rl RectLoad, m, n int) []PointLoad {
	if m < 1 {
		m = 1
	}
	if n < 1 {
		n = 1
	}
	out := make([]PointLoad, 0, m*n)
	dx := rl.A / float64(m)
	dy := rl.B / float64(n)
	p := rl.Q / float64(m*n)
	x0 := rl.CX - rl.A/2
	y0 := rl.CY - rl.B/2
	for i := 0; i < m; i++ {
		for j := 0; j < n; j++ {
			out = append(out, PointLoad{
				X: x0 + (float64(i)+0.5)*dx,
				Y: y0 + (float64(j)+0.5)*dy,
				P: p,
			})
		}
	}
	return out
}

// Discretizer approximates rectangular uniform loads by point-load
// superposition. Far away from the rectangle a single equivalent point load
// is used; close to it the rectangle is subdivided so that no sub-cell is
// larger than MaxCellSize.
type Discretizer struct {
	// MaxCellSize is the target maximum sub-cell side length (m) used near
	// the load. Must be > 0.
	MaxCellSize float64
	// FarFactor: evaluation points farther than FarFactor × (rectangle
	// diagonal) from the rectangle center use a single point load. Must be
	// >= 1.
	FarFactor float64
}

// StressAt returns the vertical additional stress (kPa) at point (x, y) and
// depth z (m) caused by the rectangular load rl.
func (d Discretizer) StressAt(rl RectLoad, x, y, z float64) float64 {
	if rl.Q == 0 {
		return 0
	}
	diag := math.Hypot(rl.A, rl.B)
	r := math.Hypot(x-rl.CX, y-rl.CY)
	if diag > 0 && r > d.FarFactor*diag {
		return VerticalStressPointLoad(rl.Q, r, z)
	}
	m := int(math.Ceil(rl.A / d.MaxCellSize))
	n := int(math.Ceil(rl.B / d.MaxCellSize))
	pts := Subdivide(rl, m, n)
	sum := 0.0
	for _, p := range pts {
		sum += VerticalStressPointLoad(p.P, math.Hypot(x-p.X, y-p.Y), z)
	}
	return sum
}
