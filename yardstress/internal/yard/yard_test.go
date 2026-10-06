package yard

import (
	"testing"
)

func validBlocks() []BlockCfg {
	return []BlockCfg{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 3, Rows: 2, BayWidth: 12.5, RowWidth: 3, MaxTiers: 5},
		{ID: "B", OriginX: 40, OriginY: 0, Bays: 2, Rows: 2, BayWidth: 12.5, RowWidth: 3, MaxTiers: 4},
	}
}

func TestNewGeneratesStacks(t *testing.T) {
	y, errs := New(validBlocks())
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got, want := len(y.Stacks), 3*2+2*2; got != want {
		t.Fatalf("got %d stacks, want %d", got, want)
	}
	s := y.Get("A-B02-R01")
	if s == nil {
		t.Fatal("stack A-B02-R01 missing")
	}
	if s.Rect.MinX != 12.5 || s.Rect.MaxX != 25 || s.Rect.MinY != 0 || s.Rect.MaxY != 3 {
		t.Errorf("wrong footprint: %+v", s.Rect)
	}
	if s.MaxTiers != 5 {
		t.Errorf("wrong max tiers: %d", s.MaxTiers)
	}
}

func TestNewRejectsBadDimensions(t *testing.T) {
	blocks := validBlocks()
	blocks[0].BayWidth = 0
	blocks[1].RowWidth = -1
	blocks[0].MaxTiers = 0
	_, errs := New(blocks)
	for _, f := range []string{"blocks[0].bay_width", "blocks[1].row_width", "blocks[0].max_tiers"} {
		if !errs.Has(f) {
			t.Errorf("expected error for %s, got: %v", f, errs)
		}
	}
}

func TestNewRejectsOverlappingStacks(t *testing.T) {
	blocks := validBlocks()
	blocks[1].OriginX = 10 // block B now overlaps block A
	_, errs := New(blocks)
	found := false
	for _, e := range errs {
		if e.Message != "" && containsOverlap(e.Message) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected overlap error, got: %v", errs)
	}
}

func TestNewRejectsDuplicateBlockIDs(t *testing.T) {
	blocks := validBlocks()
	blocks[1].ID = "A"
	_, errs := New(blocks)
	if !errs.Has("blocks[1].id") {
		t.Fatalf("expected duplicate id error, got: %v", errs)
	}
}

func containsOverlap(s string) bool {
	for i := 0; i+7 <= len(s); i++ {
		if s[i:i+7] == "overlap" {
			return true
		}
	}
	return false
}
