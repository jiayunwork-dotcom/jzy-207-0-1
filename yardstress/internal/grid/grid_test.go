package grid

import (
	"testing"

	"yardstress/internal/geom"
)

func testGrid() *Grid {
	g := New(0, 0, 2, 2, 10, 6, 12, nil, 4)
	g.Allow = make([]float64, g.Len())
	for i := range g.Allow {
		g.Allow[i] = 50
	}
	return g
}

func TestIndexRoundTrip(t *testing.T) {
	g := testGrid()
	for j := 0; j < g.Ny; j++ {
		for i := 0; i < g.Nx; i++ {
			idx := g.Idx(i, j)
			x, y := g.XY(idx)
			if x != float64(i)*2 || y != float64(j)*2 {
				t.Fatalf("idx %d -> (%v,%v), want (%v,%v)", idx, x, y, float64(i)*2, float64(j)*2)
			}
		}
	}
}

func TestRangeClampsAndMisses(t *testing.T) {
	g := testGrid()
	i0, i1, j0, j1, ok := g.Range(3, 3, 9, 9)
	if !ok || i0 != 2 || i1 != 4 || j0 != 2 || j1 != 4 {
		t.Errorf("range = %d..%d x %d..%d ok=%v, want 2..4 x 2..4", i0, i1, j0, j1, ok)
	}
	// Fully outside.
	if _, _, _, _, ok := g.Range(100, 100, 120, 120); ok {
		t.Error("expected miss for far-away box")
	}
	// Partially outside clamps.
	i0, i1, _, _, ok = g.Range(-50, 0, 4, 4)
	if !ok || i0 != 0 || i1 != 2 {
		t.Errorf("clamped range = %d..%d ok=%v, want 0..2", i0, i1, ok)
	}
}

func TestAllowableZonesFirstMatchWins(t *testing.T) {
	g := testGrid()
	zones := []Zone{
		{Rect: geom.Rect{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}, Value: 30},
		{Rect: geom.Rect{MinX: 4, MinY: 0, MaxX: 8, MaxY: 10}, Value: 20},
	}
	allow := Allowables(g, 50, zones)
	// (0,0) only in first zone.
	if v := allow[g.Idx(0, 0)]; v != 30 {
		t.Errorf("(0,0): got %v want 30", v)
	}
	// (6,0) in both: first match wins.
	if v := allow[g.Idx(3, 0)]; v != 30 {
		t.Errorf("(6,0): got %v want 30 (first match)", v)
	}
	// (16,0) in no zone: default.
	if v := allow[g.Idx(8, 0)]; v != 50 {
		t.Errorf("(16,0): got %v want 50 (default)", v)
	}
}

func TestTilesForRangeCoversExactly(t *testing.T) {
	g := testGrid() // 10x6 points, tile size 4 -> 3x2 tiles
	tiles := g.TilesForRange(0, 9, 0, 5)
	if len(tiles) != 6 {
		t.Fatalf("full range should touch all 6 tiles, got %v", tiles)
	}
	tiles = g.TilesForRange(0, 3, 0, 3)
	if len(tiles) != 1 || tiles[0] != 0 {
		t.Fatalf("corner range should touch tile 0 only, got %v", tiles)
	}
	// Adjacent ranges share no tile.
	a := g.TilesForRange(0, 3, 0, 5)
	b := g.TilesForRange(4, 9, 0, 5)
	for _, x := range a {
		for _, y := range b {
			if x == y {
				t.Fatalf("ranges share tile %d", x)
			}
		}
	}
}
