package admission

import (
	"fmt"
	"sort"

	"yardgate/internal/grid"
	"yardgate/internal/stress"
	"yardgate/internal/yard"
)

// SlotState is the mutable stacking state of one slot.
type SlotState struct {
	Layers []Layer // bottom to top
}

// State is the full mutable yard state: per-slot tier stacks plus the
// checkpoint grid. It is NOT concurrency-safe; the Engine serializes access.
type State struct {
	Yard   *yard.Yard
	Grid   *grid.Grid
	Disc   stress.Discretizer
	Cutoff float64 // kPa; contributions below this are neglected
	Slots  map[string]*SlotState
}

// NewState creates an empty yard state. The slot map is pre-populated with
// every yard slot so that it is never resized afterwards: concurrent jobs
// touching different slots then only ever read the map (their SlotState
// structs are distinct), which is race-free.
func NewState(y *yard.Yard, g *grid.Grid, disc stress.Discretizer, cutoff float64) *State {
	s := &State{
		Yard:   y,
		Grid:   g,
		Disc:   disc,
		Cutoff: cutoff,
		Slots:  make(map[string]*SlotState),
	}
	for _, sl := range y.Slots() {
		s.Slots[sl.ID] = &SlotState{}
	}
	return s
}

// slotState returns the stack of a slot. The map is pre-populated, so this
// never writes to the map.
func (s *State) slotState(id string) *SlotState {
	st, ok := s.Slots[id]
	if !ok {
		// Only reachable for states built before pre-population or with a
		// mismatched yard; keep the lazy behaviour for safety.
		st = &SlotState{}
		s.Slots[id] = st
	}
	return st
}

func (s *State) lookupSlot(ref SlotRef) (*yard.Slot, error) {
	sl, ok := s.Yard.Slot(ref.Block, ref.Bay, ref.Row)
	if !ok {
		return nil, &ValidationError{Field: "slot", Message: fmt.Sprintf("unknown slot %s/%d/%d", ref.Block, ref.Bay, ref.Row)}
	}
	return sl, nil
}

// Plan validates a job against the current state and computes its load
// changes, without mutating anything.
func (s *State) Plan(j Job) ([]LoadDelta, error) {
	if err := j.ValidateSyntax(); err != nil {
		return nil, err
	}
	switch j.Type {
	case Place:
		sl, err := s.lookupSlot(j.From)
		if err != nil {
			return nil, err
		}
		st := s.slotState(sl.ID)
		if len(st.Layers)+j.Tiers > sl.MaxTiers {
			return nil, &ValidationError{Field: "tiers", Message: fmt.Sprintf("placing %d tier(s) would exceed slot max_tiers=%d (current %d)", j.Tiers, sl.MaxTiers, len(st.Layers))}
		}
		return []LoadDelta{{Slot: j.From, DeltaKN: j.WeightKN * float64(j.Tiers)}}, nil

	case Remove:
		sl, err := s.lookupSlot(j.From)
		if err != nil {
			return nil, err
		}
		st := s.slotState(sl.ID)
		if len(st.Layers) < j.Tiers {
			return nil, &ValidationError{Field: "tiers", Message: fmt.Sprintf("cannot remove %d tier(s): slot holds only %d", j.Tiers, len(st.Layers))}
		}
		w := 0.0
		for _, l := range st.Layers[len(st.Layers)-j.Tiers:] {
			w += l.WeightKN
		}
		return []LoadDelta{{Slot: j.From, DeltaKN: -w}}, nil

	case Reshuffle:
		from, err := s.lookupSlot(j.From)
		if err != nil {
			return nil, err
		}
		to, err := s.lookupSlot(*j.To)
		if err != nil {
			return nil, err
		}
		stFrom := s.slotState(from.ID)
		stTo := s.slotState(to.ID)
		if len(stFrom.Layers) < j.Tiers {
			return nil, &ValidationError{Field: "tiers", Message: fmt.Sprintf("cannot move %d tier(s): source slot holds only %d", j.Tiers, len(stFrom.Layers))}
		}
		if len(stTo.Layers)+j.Tiers > to.MaxTiers {
			return nil, &ValidationError{Field: "tiers", Message: fmt.Sprintf("moving %d tier(s) would exceed destination max_tiers=%d (current %d)", j.Tiers, to.MaxTiers, len(stTo.Layers))}
		}
		w := 0.0
		for _, l := range stFrom.Layers[len(stFrom.Layers)-j.Tiers:] {
			w += l.WeightKN
		}
		return []LoadDelta{{Slot: j.From, DeltaKN: -w}, {Slot: *j.To, DeltaKN: w}}, nil
	}
	return nil, &ValidationError{Field: "type", Message: "unsupported job type"}
}

// CommitJob mutates the slot stacks for a job that Plan has accepted.
// seq is the event sequence number assigned to the job.
func (s *State) CommitJob(j Job, seq int64) {
	switch j.Type {
	case Place:
		sl, _ := s.Yard.Slot(j.From.Block, j.From.Bay, j.From.Row)
		st := s.slotState(sl.ID)
		for i := 0; i < j.Tiers; i++ {
			st.Layers = append(st.Layers, Layer{WeightKN: j.WeightKN, OriginSeq: seq})
		}
	case Remove:
		sl, _ := s.Yard.Slot(j.From.Block, j.From.Bay, j.From.Row)
		st := s.slotState(sl.ID)
		st.Layers = st.Layers[:len(st.Layers)-j.Tiers]
	case Reshuffle:
		from, _ := s.Yard.Slot(j.From.Block, j.From.Bay, j.From.Row)
		to, _ := s.Yard.Slot(j.To.Block, j.To.Bay, j.To.Row)
		stFrom := s.slotState(from.ID)
		stTo := s.slotState(to.ID)
		moved := stFrom.Layers[len(stFrom.Layers)-j.Tiers:]
		stTo.Layers = append(stTo.Layers, moved...)
		stFrom.Layers = stFrom.Layers[:len(stFrom.Layers)-j.Tiers]
	}
}

// GridDeltas computes the checkpoint stress increments for realized load
// changes.
func (s *State) GridDeltas(loads []LoadDelta) []grid.PointDelta {
	rects := make([]stress.RectLoad, 0, len(loads))
	for _, ld := range loads {
		sl, ok := s.Yard.Slot(ld.Slot.Block, ld.Slot.Bay, ld.Slot.Row)
		if !ok {
			continue
		}
		rects = append(rects, stress.RectLoad{CX: sl.CX, CY: sl.CY, A: sl.A, B: sl.B, Q: ld.DeltaKN})
	}
	return s.Grid.ComputePointDeltas(rects, s.Disc, s.Cutoff)
}

// CorrectionLoads computes the load changes of correcting the box weight of
// a place event: only boxes still on site contribute, at their current slot.
// Returns the per-slot deltas, the weight currently recorded on those boxes,
// and whether any boxes of that event are still on site.
func (s *State) CorrectionLoads(targetSeq int64, newWeight float64) (loads []LoadDelta, oldWeight float64, onSite bool) {
	type agg struct {
		ref SlotRef
		n   int
	}
	bySlot := map[string]*agg{}
	order := []string{}
	for id, st := range s.Slots {
		for _, l := range st.Layers {
			if l.OriginSeq != targetSeq {
				continue
			}
			oldWeight = l.WeightKN
			onSite = true
			a, ok := bySlot[id]
			if !ok {
				sl, ok := s.Yard.SlotByID(id)
				if !ok {
					continue
				}
				a = &agg{ref: SlotRef{Block: sl.BlockID, Bay: sl.Bay, Row: sl.Row}}
				bySlot[id] = a
				order = append(order, id)
			}
			a.n++
		}
	}
	if !onSite {
		return nil, 0, false
	}
	sort.Strings(order)
	for _, id := range order {
		a := bySlot[id]
		loads = append(loads, LoadDelta{Slot: a.ref, DeltaKN: (newWeight - oldWeight) * float64(a.n)})
	}
	return loads, oldWeight, true
}

// CommitCorrection rewrites the recorded weight of all on-site boxes that
// originate from the given place event.
func (s *State) CommitCorrection(targetSeq int64, newWeight float64) {
	for _, st := range s.Slots {
		for i := range st.Layers {
			if st.Layers[i].OriginSeq == targetSeq {
				st.Layers[i].WeightKN = newWeight
			}
		}
	}
}

// CopySlots returns a deep copy of all slot stacks (for snapshots).
func (s *State) CopySlots() map[string][]Layer {
	out := make(map[string][]Layer, len(s.Slots))
	for id, st := range s.Slots {
		if len(st.Layers) == 0 {
			continue
		}
		cp := make([]Layer, len(st.Layers))
		copy(cp, st.Layers)
		out[id] = cp
	}
	return out
}

// RestoreSlots replaces the slot stacks (snapshot recovery). The map is
// re-populated with every yard slot first, keeping it read-only afterwards.
func (s *State) RestoreSlots(slots map[string][]Layer) {
	s.Slots = make(map[string]*SlotState, len(s.Slots))
	for _, sl := range s.Yard.Slots() {
		s.Slots[sl.ID] = &SlotState{}
	}
	for id, layers := range slots {
		cp := make([]Layer, len(layers))
		copy(cp, layers)
		s.Slots[id] = &SlotState{Layers: cp}
	}
}
