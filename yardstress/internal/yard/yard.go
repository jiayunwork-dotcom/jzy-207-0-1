// Package yard models the static layout of the container yard: blocks
// (箱区) made of bays (贝位) and rows (排); the intersection of a bay and a
// row is a stack position (堆位) with a fixed plan footprint and a tier
// limit. Dynamic per-stack state (current tiers and weight) lives in the
// admission engine, not here.
package yard

import (
	"fmt"

	"yardstress/internal/ferr"
	"yardstress/internal/geom"
)

// BlockCfg describes one block of the yard.
type BlockCfg struct {
	ID       string  `yaml:"id" json:"id"`
	OriginX  float64 `yaml:"origin_x" json:"origin_x"`
	OriginY  float64 `yaml:"origin_y" json:"origin_y"`
	Bays     int     `yaml:"bays" json:"bays"`
	Rows     int     `yaml:"rows" json:"rows"`
	BayWidth float64 `yaml:"bay_width" json:"bay_width"`
	RowWidth float64 `yaml:"row_width" json:"row_width"`
	MaxTiers int     `yaml:"max_tiers" json:"max_tiers"`
}

// Stack is one bay/row position: a fixed footprint and a tier limit.
type Stack struct {
	ID       string    `json:"id"`
	Block    string    `json:"block"`
	Rect     geom.Rect `json:"rect"`
	MaxTiers int       `json:"max_tiers"`
}

// Yard is the static collection of stacks.
type Yard struct {
	Stacks map[string]*Stack
}

// StackID builds the identifier of the stack at (bay, row), 1-based.
func StackID(block string, bay, row int) string {
	return fmt.Sprintf("%s-B%02d-R%02d", block, bay, row)
}

// New validates the block configurations and generates all stacks. Every
// violation is reported with its field path; on any error the yard is nil
// and the caller is expected to refuse to start.
func New(blocks []BlockCfg) (*Yard, ferr.List) {
	var errs ferr.List
	var all []*Stack
	seenBlocks := map[string]bool{}

	for bi, b := range blocks {
		prefix := fmt.Sprintf("blocks[%d]", bi)
		bad := false
		if b.ID == "" {
			errs = ferr.Appendf(errs, prefix+".id", "must not be empty")
			bad = true
		} else if seenBlocks[b.ID] {
			errs = ferr.Appendf(errs, prefix+".id", "duplicate block id %q", b.ID)
			bad = true
		}
		seenBlocks[b.ID] = true
		if b.Bays < 1 {
			errs = ferr.Appendf(errs, prefix+".bays", "must be >= 1, got %d", b.Bays)
			bad = true
		}
		if b.Rows < 1 {
			errs = ferr.Appendf(errs, prefix+".rows", "must be >= 1, got %d", b.Rows)
			bad = true
		}
		if b.BayWidth <= 0 {
			errs = ferr.Appendf(errs, prefix+".bay_width", "must be positive, got %v", b.BayWidth)
			bad = true
		}
		if b.RowWidth <= 0 {
			errs = ferr.Appendf(errs, prefix+".row_width", "must be positive, got %v", b.RowWidth)
			bad = true
		}
		if b.MaxTiers < 1 {
			errs = ferr.Appendf(errs, prefix+".max_tiers", "must be >= 1, got %d", b.MaxTiers)
			bad = true
		}
		if bad {
			continue
		}
		for bay := 1; bay <= b.Bays; bay++ {
			for row := 1; row <= b.Rows; row++ {
				r := geom.Rect{
					MinX: b.OriginX + float64(bay-1)*b.BayWidth,
					MinY: b.OriginY + float64(row-1)*b.RowWidth,
					MaxX: b.OriginX + float64(bay)*b.BayWidth,
					MaxY: b.OriginY + float64(row)*b.RowWidth,
				}
				all = append(all, &Stack{
					ID:       StackID(b.ID, bay, row),
					Block:    b.ID,
					Rect:     r,
					MaxTiers: b.MaxTiers,
				})
			}
		}
	}

	// Stacks must not overlap, including across block boundaries.
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[i].Rect.Overlaps(all[j].Rect) {
				errs = ferr.Appendf(errs,
					fmt.Sprintf("stacks[%s,%s]", all[i].ID, all[j].ID),
					"stacks overlap: %v and %v", all[i].Rect, all[j].Rect)
			}
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}

	y := &Yard{Stacks: make(map[string]*Stack, len(all))}
	for _, s := range all {
		y.Stacks[s.ID] = s
	}
	return y, nil
}

// Get returns the stack with the given id, or nil.
func (y *Yard) Get(id string) *Stack { return y.Stacks[id] }
