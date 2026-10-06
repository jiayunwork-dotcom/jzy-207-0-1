// Package stress implements the classical Boussinesq solution for the
// vertical additional stress in an elastic half-space, plus the exact
// closed-form solution for a uniformly loaded rectangle (used as the
// convergence reference for the load discretization and in tests).
//
// Model: the ground is a homogeneous, isotropic, linearly elastic
// half-space. A surface point load P (kN) applied at the origin produces at
// depth z (m) and horizontal distance r (m) the vertical additional stress
//
//	sigma_z = 3P / (2*pi*z^2) * (1 + (r/z)^2)^(-5/2)        [kPa = kN/m^2]
//
// The kernel has two properties the service relies on:
//
//   - Superposition: the stress of several loads is the sum of the
//     individual stresses (linearity of the elastic solution).
//   - Load conservation: the integral of sigma_z over any horizontal plane
//     z = const > 0 equals the total applied surface load.
package stress

import (
	"math"

	"yardstress/internal/geom"
)

// PointLoad returns the vertical additional stress (kPa) at depth z (m)
// and horizontal distance r (m) from a surface point load P (kN).
// z must be positive; r must be non-negative.
func PointLoad(P, r, z float64) float64 {
	rz := r / z
	t := 1 + rz*rz
	// (1+(r/z)^2)^(5/2) = t^2 * sqrt(t); written out to avoid math.Pow.
	return 3 * P / (2 * math.Pi * z * z) / (t * t * math.Sqrt(t))
}

// PointLoadAt returns the vertical additional stress at plan position
// (px, py) and depth z due to a point load P applied at (lx, ly).
func PointLoadAt(P, lx, ly, px, py, z float64) float64 {
	dx, dy := px-lx, py-ly
	return PointLoad(P, math.Hypot(dx, dy), z)
}

// RectangleCorner returns the influence factor I for the vertical stress
// below the corner of a rectangle m x n (side lengths normalised by depth),
// uniformly loaded with unit pressure: sigma_z = q * I.
//
// This is the exact closed form obtained by integrating the Boussinesq
// kernel over the rectangle (see e.g. Das, Principles of Soil Mechanics).
// Consistency checks: as m,n -> inf, I -> 1/4 (a quarter of the plane); as
// m,n -> 0 with load P = q*m*n*z^2, q*I -> 3P/(2*pi*z^2) (Boussinesq).
func RectangleCorner(m, n float64) float64 {
	if m <= 0 || n <= 0 {
		return 0
	}
	mn := m * n
	root := math.Sqrt(m*m + n*n + 1)
	num := 2 * mn * root
	term1 := num / (m*m + n*n + mn*mn + 1) * (m*m + n*n + 2) / (m*m + n*n + 1)
	// Atan2 handles the branch: for m^2+n^2+1 < m^2*n^2 the angle lies in
	// (pi/2, pi), which the textbook "+pi" rule expresses.
	ang := math.Atan2(num, m*m+n*n-mn*mn+1)
	return (term1 + ang) / (4 * math.Pi)
}

// RectangleUniform returns the exact vertical additional stress (kPa) at
// depth z below plan point (px, py) caused by uniform pressure q (kPa) over
// the rectangle rc. The point may lie inside or outside the rectangle; the
// result is assembled by inclusion-exclusion of four corner rectangles.
func RectangleUniform(q float64, rc geom.Rect, px, py, z float64) float64 {
	ax, bx := rc.MinX-px, rc.MaxX-px
	ay, by := rc.MinY-py, rc.MaxY-py
	// c2(u, v) is the influence of the rectangle spanned between the origin
	// and (u, v); the signs implement the inclusion-exclusion so that
	// rect[ax,bx]x[ay,by] = c2(bx,by) - c2(ax,by) - c2(bx,ay) + c2(ax,ay).
	c2 := func(u, v float64) float64 {
		if u == 0 || v == 0 {
			return 0
		}
		s := 1.0
		if (u < 0) != (v < 0) {
			s = -1
		}
		return s * RectangleCorner(math.Abs(u)/z, math.Abs(v)/z)
	}
	return q * (c2(bx, by) - c2(ax, by) - c2(bx, ay) + c2(ax, ay))
}
