// Package yard models the container yard: blocks, bays, rows and the slots
// they form, together with static validation (positive dimensions, no
// overlapping slots).
package yard

import (
	"fmt"
	"sort"
)

// Block describes a rectangular container block with a regular grid of
// slots. Slot (bay i, row j), 0-based, occupies the rectangle
// [OriginX + i*BayWidth, OriginX + (i+1)*BayWidth] x
// [OriginY + j*RowWidth, OriginY + (j+1)*RowWidth].
type Block struct {
	ID       string  `json:"id" yaml:"id"`
	OriginX  float64 `json:"origin_x" yaml:"origin_x"`
	OriginY  float64 `json:"origin_y" yaml:"origin_y"`
	Bays     int     `json:"bays" yaml:"bays"`
	Rows     int     `json:"rows" yaml:"rows"`
	BayWidth float64 `json:"bay_width" yaml:"bay_width"`
	RowWidth float64 `json:"row_width" yaml:"row_width"`
	MaxTiers int     `json:"max_tiers" yaml:"max_tiers"`
}

// Slot is a single stacking position with fixed plan position and size.
type Slot struct {
	ID       string // "block/bay/row"
	BlockID  string
	Bay, Row int
	CX, CY   float64 // center, m
	A, B     float64 // side lengths along x and y, m
	MaxTiers int
}

// Yard is the static yard layout plus a slot index.
type Yard struct {
	Blocks []Block
	slots  map[string]*Slot
}

// SlotID builds the canonical slot identifier.
func SlotID(block string, bay, row int) string {
	return fmt.Sprintf("%s/%d/%d", block, bay, row)
}

// Build validates the block definitions and generates all slots.
func Build(blocks []Block) (*Yard, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("yard: no blocks defined")
	}
	seen := map[string]bool{}
	y := &Yard{Blocks: blocks, slots: map[string]*Slot{}}
	var all []*Slot
	for _, b := range blocks {
		if b.ID == "" {
			return nil, fmt.Errorf("yard: block with empty id")
		}
		if seen[b.ID] {
			return nil, fmt.Errorf("yard: duplicate block id %q", b.ID)
		}
		seen[b.ID] = true
		if b.Bays <= 0 || b.Rows <= 0 {
			return nil, fmt.Errorf("yard: block %q: bays and rows must be positive", b.ID)
		}
		if b.BayWidth <= 0 || b.RowWidth <= 0 {
			return nil, fmt.Errorf("yard: block %q: slot dimensions must be positive (bay_width=%v, row_width=%v)", b.ID, b.BayWidth, b.RowWidth)
		}
		if b.MaxTiers <= 0 {
			return nil, fmt.Errorf("yard: block %q: max_tiers must be positive", b.ID)
		}
		for i := 0; i < b.Bays; i++ {
			for j := 0; j < b.Rows; j++ {
				s := &Slot{
					ID:       SlotID(b.ID, i, j),
					BlockID:  b.ID,
					Bay:      i,
					Row:      j,
					CX:       b.OriginX + (float64(i)+0.5)*b.BayWidth,
					CY:       b.OriginY + (float64(j)+0.5)*b.RowWidth,
					A:        b.BayWidth,
					B:        b.RowWidth,
					MaxTiers: b.MaxTiers,
				}
				y.slots[s.ID] = s
				all = append(all, s)
			}
		}
	}
	if err := checkOverlap(all); err != nil {
		return nil, err
	}
	return y, nil
}

// checkOverlap rejects layouts where any two slot rectangles overlap by
// more than a hair (touching edges is allowed; an epsilon absorbs
// floating-point rounding on shared edges).
func checkOverlap(slots []*Slot) error {
	const eps = 1e-9
	sorted := make([]*Slot, len(slots))
	copy(sorted, slots)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CX < sorted[j].CX })
	for i := 0; i < len(sorted); i++ {
		a := sorted[i]
		for j := i + 1; j < len(sorted); j++ {
			b := sorted[j]
			if b.CX-b.B/2 >= a.CX+a.A/2-eps {
				break // no more x-overlap possible
			}
			xOverlap := minF(a.CX+a.A/2, b.CX+b.B/2) - maxF(a.CX-a.A/2, b.CX-b.B/2)
			yOverlap := minF(a.CY+a.B/2, b.CY+b.B/2) - maxF(a.CY-a.B/2, b.CY-b.B/2)
			if xOverlap > eps && yOverlap > eps {
				return fmt.Errorf("yard: slots %s and %s overlap", a.ID, b.ID)
			}
		}
	}
	return nil
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Slot looks up a slot by block id, bay and row.
func (y *Yard) Slot(block string, bay, row int) (*Slot, bool) {
	s, ok := y.slots[SlotID(block, bay, row)]
	return s, ok
}

// SlotByID looks up a slot by its canonical id.
func (y *Yard) SlotByID(id string) (*Slot, bool) {
	s, ok := y.slots[id]
	return s, ok
}

// Slots returns all slots in deterministic order (by id).
func (y *Yard) Slots() []*Slot {
	out := make([]*Slot, 0, len(y.slots))
	for _, s := range y.slots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
