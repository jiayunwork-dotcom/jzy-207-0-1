package yard

import (
	"strings"
	"testing"
)

func TestBuildValidYard(t *testing.T) {
	y, err := Build([]Block{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 3, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "B", OriginX: 30, OriginY: 0, Bays: 2, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(y.Slots()); got != 3*2+2*2 {
		t.Fatalf("slot count = %d", got)
	}
	s, ok := y.Slot("A", 2, 1)
	if !ok {
		t.Fatal("slot A/2/1 not found")
	}
	if s.CX != 15 || s.CY != 3.75 || s.A != 6 || s.B != 2.5 || s.MaxTiers != 5 {
		t.Fatalf("bad slot geometry: %+v", s)
	}
	if _, ok := y.Slot("A", 3, 0); ok {
		t.Fatal("out-of-range bay should not exist")
	}
}

func TestRejectNonPositiveDimensions(t *testing.T) {
	cases := []Block{
		{ID: "A", Bays: 1, Rows: 1, BayWidth: 0, RowWidth: 2.5, MaxTiers: 5},
		{ID: "A", Bays: 1, Rows: 1, BayWidth: 6, RowWidth: -1, MaxTiers: 5},
		{ID: "A", Bays: 0, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "A", Bays: 1, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 0},
	}
	for _, b := range cases {
		if _, err := Build([]Block{b}); err == nil {
			t.Errorf("block %+v should be rejected", b)
		}
	}
}

func TestRejectDuplicateBlockIDs(t *testing.T) {
	b := Block{ID: "A", Bays: 1, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5}
	if _, err := Build([]Block{b, b}); err == nil {
		t.Fatal("duplicate block ids should be rejected")
	}
}

func TestRejectOverlappingSlots(t *testing.T) {
	// Block B overlaps block A (A spans x in [0,12)).
	_, err := Build([]Block{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 2, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "B", OriginX: 10, OriginY: 0, Bays: 2, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
	// Touching edges is fine.
	_, err = Build([]Block{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 2, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "B", OriginX: 12, OriginY: 0, Bays: 2, Rows: 1, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
	})
	if err != nil {
		t.Fatalf("edge-touching blocks should be allowed: %v", err)
	}
}
