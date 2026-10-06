// Package grid maintains the checkpoint mesh on the clay-layer top surface:
// the current additional-stress value and the allowable value at every
// checkpoint, plus the primitive operations to tentatively evaluate and to
// apply stress increments caused by surface load changes.
package grid

import (
	"fmt"
	"sort"

	"yardgate/internal/stress"
)

// Zone is a rectangular region with its own allowable stress.
type Zone struct {
	Name                   string
	MinX, MinY, MaxX, MaxY float64
	AllowanceKPa           float64
}

// Contains reports whether the point lies in the zone (inclusive bounds).
func (z Zone) Contains(x, y float64) bool {
	return x >= z.MinX && x <= z.MaxX && y >= z.MinY && y <= z.MaxY
}

// Grid is a regular checkpoint mesh at a fixed depth (the clay top surface).
type Grid struct {
	OriginX, OriginY float64
	Dx, Dy           float64
	Nx, Ny           int
	Z                float64 // checkpoint depth below surface, m (> 0)

	allow  []float64 // allowable stress per checkpoint, kPa
	Values []float64 // current additional stress per checkpoint, kPa
}

// New builds the mesh and resolves the allowance of every checkpoint: the
// first zone (in the order given) containing the point wins, otherwise
// defaultAllowance is used.
func New(originX, originY, dx, dy float64, nx, ny int, z float64, zones []Zone, defaultAllowance float64) (*Grid, error) {
	if z <= 0 {
		return nil, fmt.Errorf("grid: checkpoint depth must be positive, got %v", z)
	}
	if dx <= 0 || dy <= 0 || nx <= 0 || ny <= 0 {
		return nil, fmt.Errorf("grid: spacing and counts must be positive")
	}
	if defaultAllowance <= 0 {
		return nil, fmt.Errorf("grid: default allowance must be positive, got %v", defaultAllowance)
	}
	for _, zn := range zones {
		if zn.AllowanceKPa <= 0 {
			return nil, fmt.Errorf("grid: zone %q: allowance must be positive, got %v", zn.Name, zn.AllowanceKPa)
		}
		if zn.MaxX < zn.MinX || zn.MaxY < zn.MinY {
			return nil, fmt.Errorf("grid: zone %q: empty rectangle", zn.Name)
		}
	}
	g := &Grid{
		OriginX: originX, OriginY: originY,
		Dx: dx, Dy: dy, Nx: nx, Ny: ny, Z: z,
		allow:  make([]float64, nx*ny),
		Values: make([]float64, nx*ny),
	}
	for j := 0; j < ny; j++ {
		for i := 0; i < nx; i++ {
			x, y := originX+float64(i)*dx, originY+float64(j)*dy
			a := defaultAllowance
			for _, zn := range zones {
				if zn.Contains(x, y) {
					a = zn.AllowanceKPa
					break
				}
			}
			g.allow[j*nx+i] = a
		}
	}
	return g, nil
}

// XY returns the plan coordinates of checkpoint idx.
func (g *Grid) XY(idx int) (x, y float64) {
	return g.OriginX + float64(idx%g.Nx)*g.Dx, g.OriginY + float64(idx/g.Nx)*g.Dy
}

// Index returns the checkpoint index at integer coordinates (i, j).
func (g *Grid) Index(i, j int) int { return j*g.Nx + i }

// Len returns the number of checkpoints.
func (g *Grid) Len() int { return g.Nx * g.Ny }

// AllowanceAt returns the allowable stress at checkpoint idx.
func (g *Grid) AllowanceAt(idx int) float64 { return g.allow[idx] }

// PointDelta is a stress increment at one checkpoint.
type PointDelta struct {
	Index int
	Delta float64
}

// ComputePointDeltas evaluates the stress increments produced by the given
// rectangular surface loads at every checkpoint where the increment is at
// least cutoffKPa in magnitude. The result is sorted by index and is a pure
// function of the inputs, so replaying the same loads reproduces the same
// increments bit for bit.
func (g *Grid) ComputePointDeltas(loads []stress.RectLoad, d stress.Discretizer, cutoffKPa float64) []PointDelta {
	var buf []PointDelta
	for _, rl := range loads {
		if rl.Q == 0 {
			continue
		}
		R := stress.InfluenceRadius(rl.Q, g.Z, cutoffKPa)
		if R <= 0 {
			continue
		}
		i0 := maxInt(0, int((rl.CX-R-g.OriginX)/g.Dx))
		i1 := minInt(g.Nx-1, int((rl.CX+R-g.OriginX)/g.Dx)+1)
		j0 := maxInt(0, int((rl.CY-R-g.OriginY)/g.Dy))
		j1 := minInt(g.Ny-1, int((rl.CY+R-g.OriginY)/g.Dy)+1)
		for j := j0; j <= j1; j++ {
			for i := i0; i <= i1; i++ {
				x := g.OriginX + float64(i)*g.Dx
				y := g.OriginY + float64(j)*g.Dy
				dx, dy := x-rl.CX, y-rl.CY
				if dx*dx+dy*dy > R*R {
					continue
				}
				v := d.StressAt(rl, x, y, g.Z)
				if v <= -cutoffKPa || v >= cutoffKPa {
					buf = append(buf, PointDelta{Index: j*g.Nx + i, Delta: v})
				}
			}
		}
	}
	sort.Slice(buf, func(a, b int) bool { return buf[a].Index < buf[b].Index })
	if len(loads) > 1 {
		// Merge duplicate checkpoints (overlapping influence zones).
		out := buf[:0]
		for _, pd := range buf {
			if n := len(out); n > 0 && out[n-1].Index == pd.Index {
				out[n-1].Delta += pd.Delta
			} else {
				out = append(out, pd)
			}
		}
		buf = out
	}
	return buf
}

// Violation describes one checkpoint whose stress would exceed its
// allowable value.
type Violation struct {
	Index        int
	X, Y         float64
	StressKPa    float64
	AllowanceKPa float64
	ExcessKPa    float64
}

// Evaluate returns the checkpoints where applying deltas would push the
// stress above the allowable value, sorted by excess (descending) and
// truncated to at most topK entries. It does not modify the grid.
func (g *Grid) Evaluate(deltas []PointDelta, topK int) []Violation {
	var out []Violation
	for _, pd := range deltas {
		s := g.Values[pd.Index] + pd.Delta
		if s > g.allow[pd.Index] {
			x, y := g.XY(pd.Index)
			out = append(out, Violation{
				Index: pd.Index,
				X:     x, Y: y,
				StressKPa:    s,
				AllowanceKPa: g.allow[pd.Index],
				ExcessKPa:    s - g.allow[pd.Index],
			})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].ExcessKPa != out[b].ExcessKPa {
			return out[a].ExcessKPa > out[b].ExcessKPa
		}
		return out[a].Index < out[b].Index
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// Apply adds the deltas to the current values.
func (g *Grid) Apply(deltas []PointDelta) {
	for _, pd := range deltas {
		g.Values[pd.Index] += pd.Delta
	}
}

// CloneValues returns a copy of the current stress values.
func (g *Grid) CloneValues() []float64 {
	out := make([]float64, len(g.Values))
	copy(out, g.Values)
	return out
}

// Clone returns a deep copy of the grid (geometry, allowances and values).
func (g *Grid) Clone() *Grid {
	out := *g
	out.allow = make([]float64, len(g.allow))
	copy(out.allow, g.allow)
	out.Values = g.CloneValues()
	return &out
}

// EmptyClone copies geometry and allowances but returns zeroed values. It
// only reads immutable fields, so it is safe to call while other goroutines
// update the live values.
func (g *Grid) EmptyClone() *Grid {
	out := *g
	out.allow = make([]float64, len(g.allow))
	copy(out.allow, g.allow)
	out.Values = make([]float64, len(g.Values))
	return &out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
