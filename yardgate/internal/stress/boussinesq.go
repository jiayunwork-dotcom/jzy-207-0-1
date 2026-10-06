// Package stress computes vertical additional stress in an elastic
// half-space due to surface loads, using the classical Boussinesq point-load
// solution and superposition.
package stress

import "math"

// VerticalStressPointLoad returns the vertical additional stress σz (kPa)
// at horizontal distance r (m) and depth z (m) below the ground surface,
// caused by a concentrated load P (kN) applied at the surface:
//
//	σz = 3·P·z³ / ( 2π·(r²+z²)^(5/2) )
//
// Reference values (used in tests): P=100 kN, z=2 m → 11.94 kPa at r=0,
// 3.91 kPa at r=1.5 m.
func VerticalStressPointLoad(P, r, z float64) float64 {
	z2 := z * z
	s := r*r + z2
	return 3 * P * z2 * z / (2 * math.Pi * s * s * math.Sqrt(s))
}

// InfluenceRadius returns the horizontal distance beyond which a surface
// load of magnitude |P| (kN) contributes less than eps kPa at depth z.
// Derived by inverting the point-load solution.
func InfluenceRadius(P, z, eps float64) float64 {
	P = math.Abs(P)
	if P == 0 || eps <= 0 {
		return 0
	}
	// (r²+z²)^(5/2) = 3·P·z³ / (2π·eps)
	v := math.Pow(3*P*z*z*z/(2*math.Pi*eps), 2.0/5.0)
	if v <= z*z {
		return 0
	}
	return math.Sqrt(v - z*z)
}
