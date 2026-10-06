// Package discretize turns the load of a container stack into the point
// loads that the Boussinesq kernel consumes.
//
// Chosen scheme: the stack load is assumed to spread uniformly over the
// stack footprint (the standard geotechnical assumption for a loaded
// pavement area; corner-casting concentrations are smoothed out by the
// pavement and are not modelled). The footprint is subdivided into a
// regular nx-by-ny grid of sub-rectangles whose side length does not exceed
// Params.MaxCell, and each sub-rectangle is replaced by a point load at its
// centroid carrying its share of the total weight (midpoint quadrature of
// the Boussinesq kernel over the footprint).
//
// Error and convergence: the midpoint rule is second order, so the
// discretization error at a checkpoint scales like O((h/z)^2) where h is
// the sub-cell side and z the checkpoint depth, and it vanishes as the
// subdivision is refined. The exact limit is the closed-form uniformly
// loaded rectangle (stress.RectangleUniform); the discretize package tests
// demonstrate quadratic convergence to that limit. See README for measured
// error magnitudes.
package discretize

import (
	"math"

	"yardstress/internal/geom"
)

// Params controls the discretization.
type Params struct {
	// MaxCell is the target maximum side length (m) of a sub-rectangle.
	// Smaller values are more accurate and more expensive.
	MaxCell float64
	// Cutoff (kPa) is the per-point contribution threshold below which a
	// point load's influence on a checkpoint is neglected. It bounds the
	// influence radius and therefore the locking footprint of a job.
	Cutoff float64
}

// PointLoad is a concentrated load P (kN, signed) applied at (X, Y).
type PointLoad struct {
	X, Y, P float64
}

// PatchN subdivides rect into nx*ny equal sub-rectangles and returns one
// centroid point load per sub-rectangle, each carrying weight/(nx*ny).
// The sum of the returned loads equals weight (up to floating-point
// rounding of the division).
func PatchN(r geom.Rect, weight float64, nx, ny int) []PointLoad {
	if nx < 1 {
		nx = 1
	}
	if ny < 1 {
		ny = 1
	}
	cw := r.Width() / float64(nx)
	ch := r.Height() / float64(ny)
	pw := weight / float64(nx*ny)
	out := make([]PointLoad, 0, nx*ny)
	for i := 0; i < nx; i++ {
		for j := 0; j < ny; j++ {
			out = append(out, PointLoad{
				X: r.MinX + (float64(i)+0.5)*cw,
				Y: r.MinY + (float64(j)+0.5)*ch,
				P: pw,
			})
		}
	}
	return out
}

// Patch subdivides rect so that no sub-cell side exceeds p.MaxCell.
func Patch(r geom.Rect, weight float64, p Params) []PointLoad {
	nx := int(math.Ceil(r.Width() / p.MaxCell))
	ny := int(math.Ceil(r.Height() / p.MaxCell))
	return PatchN(r, weight, nx, ny)
}

// InfluenceRadius returns the horizontal distance beyond which a point load
// P (kN) contributes less than cutoff (kPa) at depth z (m), obtained by
// inverting the Boussinesq kernel:
//
//	r = z * sqrt( (3P/(2*pi*cutoff*z^2))^(2/5) - 1 )
//
// Returns 0 when the load is below cutoff even directly underneath.
func InfluenceRadius(P, z, cutoff float64) float64 {
	P = math.Abs(P)
	if P <= 0 || cutoff <= 0 || z <= 0 {
		return 0
	}
	v := 3 * P / (2 * math.Pi * cutoff * z * z)
	if v <= 1 {
		return 0
	}
	return z * math.Sqrt(math.Pow(v, 0.4)-1)
}
