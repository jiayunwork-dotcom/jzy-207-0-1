// Package grid maintains the checkpoint grid on the clay-layer top surface:
// geometry, current additional-stress values, per-point allowable values,
// and the striped ("tile") locks used by the admission engine to serialise
// jobs whose influence zones overlap.
package grid

import (
	"math"
	"sort"
	"sync"

	"yardstress/internal/geom"
)

// Zone assigns an allowable vertical additional stress (kPa) to a
// rectangular region of the yard plane.
type Zone struct {
	Rect  geom.Rect
	Value float64
}

// Grid is a regular checkpoint mesh at a fixed depth (clay top).
//
// Values and Allow are laid out row-major with x fastest:
// idx = j*Nx + i for checkpoint (X0+i*Dx, Y0+j*Dy).
//
// The grid is partitioned into square tiles of TileSize checkpoints; each
// tile owns a mutex. The engine locks exactly the tiles a job can
// influence, in ascending tile id, so jobs with disjoint influence zones
// never wait on each other while overlapping jobs serialise.
type Grid struct {
	X0, Y0   float64
	Dx, Dy   float64
	Nx, Ny   int
	Depth    float64
	TileSize int

	Values []float64 // current vertical additional stress, kPa
	Allow  []float64 // allowable vertical additional stress, kPa

	ntx, nty int
	tiles    []sync.Mutex
}

// New builds a grid. allow must have Nx*Ny entries (see Allowables).
func New(x0, y0, dx, dy float64, nx, ny int, depth float64, allow []float64, tileSize int) *Grid {
	if tileSize < 1 {
		tileSize = 1
	}
	ntx := (nx + tileSize - 1) / tileSize
	nty := (ny + tileSize - 1) / tileSize
	return &Grid{
		X0: x0, Y0: y0, Dx: dx, Dy: dy,
		Nx: nx, Ny: ny, Depth: depth, TileSize: tileSize,
		Values: make([]float64, nx*ny),
		Allow:  allow,
		ntx:    ntx, nty: nty,
		tiles: make([]sync.Mutex, ntx*nty),
	}
}

// Allowables evaluates the zone list at every checkpoint; the first
// matching zone wins, otherwise def applies.
func Allowables(g *Grid, def float64, zones []Zone) []float64 {
	out := make([]float64, g.Nx*g.Ny)
	for idx := range out {
		x, y := g.XY(idx)
		v := def
		for _, z := range zones {
			if z.Rect.Contains(x, y) {
				v = z.Value
				break
			}
		}
		out[idx] = v
	}
	return out
}

// Idx returns the linear index of checkpoint (i, j).
func (g *Grid) Idx(i, j int) int { return j*g.Nx + i }

// XY returns the plan coordinates of checkpoint idx.
func (g *Grid) XY(idx int) (float64, float64) {
	i, j := idx%g.Nx, idx/g.Nx
	return g.X0 + float64(i)*g.Dx, g.Y0 + float64(j)*g.Dy
}

// Len returns the number of checkpoints.
func (g *Grid) Len() int { return g.Nx * g.Ny }

// Range maps a bounding box to the inclusive checkpoint index range
// [i0,i1]x[j0,j1] it covers. ok is false when the box misses the grid.
func (g *Grid) Range(minX, minY, maxX, maxY float64) (i0, i1, j0, j1 int, ok bool) {
	const eps = 1e-9
	i0 = int(math.Ceil((minX - g.X0 - eps) / g.Dx))
	i1 = int(math.Floor((maxX - g.X0 + eps) / g.Dx))
	j0 = int(math.Ceil((minY - g.Y0 - eps) / g.Dy))
	j1 = int(math.Floor((maxY - g.Y0 + eps) / g.Dy))
	if i0 < 0 {
		i0 = 0
	}
	if j0 < 0 {
		j0 = 0
	}
	if i1 > g.Nx-1 {
		i1 = g.Nx - 1
	}
	if j1 > g.Ny-1 {
		j1 = g.Ny - 1
	}
	if i0 > i1 || j0 > j1 {
		return 0, 0, 0, 0, false
	}
	return i0, i1, j0, j1, true
}

// TilesForRange returns the sorted, de-duplicated ids of the tiles
// intersecting the checkpoint range.
func (g *Grid) TilesForRange(i0, i1, j0, j1 int) []int {
	ts := g.TileSize
	var ids []int
	for tj := j0 / ts; tj <= j1/ts; tj++ {
		for ti := i0 / ts; ti <= i1/ts; ti++ {
			ids = append(ids, tj*g.ntx+ti)
		}
	}
	sort.Ints(ids)
	return ids
}

// LockTiles locks the given tiles in ascending id order and returns an
// unlock function that releases them in reverse order. Callers must
// accumulate and sort tile ids themselves (TilesForRange already sorts);
// passing the same canonical order to every goroutine is what makes the
// locking scheme deadlock-free.
func (g *Grid) LockTiles(ids []int) func() {
	for _, id := range ids {
		g.tiles[id].Lock()
	}
	return func() {
		for k := len(ids) - 1; k >= 0; k-- {
			g.tiles[ids[k]].Unlock()
		}
	}
}

// LockAll locks every tile; used for consistent full-grid reads.
func (g *Grid) LockAll() func() {
	ids := make([]int, len(g.tiles))
	for i := range ids {
		ids[i] = i
	}
	return g.LockTiles(ids)
}

// CloneBlank returns a grid with identical geometry and allowable values
// but zeroed stress values and fresh tile locks. Used as a scratch pad for
// replay and what-if analysis.
func (g *Grid) CloneBlank() *Grid {
	return New(g.X0, g.Y0, g.Dx, g.Dy, g.Nx, g.Ny, g.Depth, g.Allow, g.TileSize)
}
