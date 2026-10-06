package admission

import (
	"sync"

	"yardgate/internal/grid"
)

// tileTable partitions the checkpoint grid into square tiles, each guarded
// by its own mutex. Jobs lock exactly the tiles their stress footprint
// touches, so jobs with disjoint footprints proceed in parallel while
// overlapping jobs serialize.
type tileTable struct {
	locks []sync.Mutex
	size  int // checkpoints per tile side
	ntx   int // tiles along x
	nty   int // tiles along y
}

func newTileTable(g *grid.Grid, tileSize int) *tileTable {
	if tileSize < 1 {
		tileSize = 1
	}
	ntx := (g.Nx + tileSize - 1) / tileSize
	nty := (g.Ny + tileSize - 1) / tileSize
	return &tileTable{
		locks: make([]sync.Mutex, ntx*nty),
		size:  tileSize,
		ntx:   ntx,
		nty:   nty,
	}
}

// rect returns the sorted, deduplicated ids of the tiles intersecting the
// rectangle [minx,maxx] x [miny,maxy] (grid coordinates).
func (t *tileTable) rect(g *grid.Grid, minx, miny, maxx, maxy float64) []int {
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	i0 := clamp(int((minx-g.OriginX)/g.Dx), 0, g.Nx-1)
	i1 := clamp(int((maxx-g.OriginX)/g.Dx), 0, g.Nx-1)
	j0 := clamp(int((miny-g.OriginY)/g.Dy), 0, g.Ny-1)
	j1 := clamp(int((maxy-g.OriginY)/g.Dy), 0, g.Ny-1)
	tx0, tx1 := i0/t.size, i1/t.size
	ty0, ty1 := j0/t.size, j1/t.size
	var ids []int
	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			ids = append(ids, ty*t.ntx+tx)
		}
	}
	return ids
}

// mergeTileIDs sorts and deduplicates tile id lists.
func mergeTileIDs(lists ...[]int) []int {
	seen := map[int]bool{}
	var out []int
	for _, l := range lists {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	// insertion sort by id (lists are short); guarantees a global lock order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// lockAll locks the tiles in ascending id order (deadlock-free).
func (t *tileTable) lockAll(ids []int) {
	for _, id := range ids {
		t.locks[id].Lock()
	}
}

// unlockAll unlocks in reverse order.
func (t *tileTable) unlockAll(ids []int) {
	for i := len(ids) - 1; i >= 0; i-- {
		t.locks[ids[i]].Unlock()
	}
}
