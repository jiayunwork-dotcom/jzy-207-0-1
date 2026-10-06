// Package events defines the domain events (jobs and weight corrections),
// their translation into ground-load deltas, and the pure replay helpers.
// Events are immutable once committed; a correction never rewrites history,
// it is appended as a new event that refers to its target.
package events

import "time"

// Type enumerates the event kinds.
type Type string

const (
	Place      Type = "PLACE"      // drop containers onto a stack
	Pick       Type = "PICK"       // remove containers from a stack
	Restow     Type = "RESTOW"     // move containers between stacks (翻箱)
	Correction Type = "CORRECTION" // weight correction of an earlier event
)

// Delta is a signed ground-load change (kN) on one stack footprint.
type Delta struct {
	StackID string  `json:"stack_id"`
	Weight  float64 `json:"weight"`
}

// Event is one committed, sequence-numbered record.
//
// For PLACE/PICK: StackID, Weight (kN, as weighed), Tiers (count change).
// For RESTOW: FromID, ToID, Weight, Tiers.
// For CORRECTION: TargetSeq and NewWeight carry the operator-facing data;
// DeltaList is the denormalised signed load change per stack, resolved at
// creation time against the then-current effective weight, so replay never
// has to chase references.
type Event struct {
	Seq       int64     `json:"seq"`
	Type      Type      `json:"type"`
	StackID   string    `json:"stack_id,omitempty"`
	FromID    string    `json:"from_id,omitempty"`
	ToID      string    `json:"to_id,omitempty"`
	Weight    float64   `json:"weight,omitempty"`
	Tiers     int       `json:"tiers,omitempty"`
	TargetSeq int64     `json:"target_seq,omitempty"`
	NewWeight float64   `json:"new_weight,omitempty"`
	DeltaList []Delta   `json:"delta_list,omitempty"`
	Time      time.Time `json:"time"`
}

// Deltas returns the signed ground-load changes of the event.
func (e Event) Deltas() []Delta {
	switch e.Type {
	case Place:
		return []Delta{{StackID: e.StackID, Weight: e.Weight}}
	case Pick:
		return []Delta{{StackID: e.StackID, Weight: -e.Weight}}
	case Restow:
		if e.FromID == e.ToID {
			return nil
		}
		return []Delta{
			{StackID: e.FromID, Weight: -e.Weight},
			{StackID: e.ToID, Weight: e.Weight},
		}
	case Correction:
		return e.DeltaList
	}
	return nil
}

// StackIDs returns the ids of the stacks whose ground load the event
// changes (used for lock selection).
func (e Event) StackIDs() []string {
	ds := e.Deltas()
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.StackID)
	}
	return ids
}

// StackState is the dynamic per-stack bookkeeping: how many tiers and how
// much weight currently rest on the footprint.
type StackState struct {
	Tiers  int     `json:"tiers"`
	Weight float64 `json:"weight"`
}

// ApplyToStacks folds the event's bookkeeping change into states.
func ApplyToStacks(states map[string]StackState, e Event) {
	add := func(id string, tiers int, weight float64) {
		if id == "" {
			return
		}
		s := states[id]
		s.Tiers += tiers
		s.Weight += weight
		states[id] = s
	}
	switch e.Type {
	case Place:
		add(e.StackID, e.Tiers, e.Weight)
	case Pick:
		add(e.StackID, -e.Tiers, -e.Weight)
	case Restow:
		add(e.FromID, -e.Tiers, -e.Weight)
		add(e.ToID, e.Tiers, e.Weight)
	case Correction:
		for _, d := range e.DeltaList {
			add(d.StackID, 0, d.Weight)
		}
	}
}

// EffectiveJobs folds every correction into its target and returns the job
// events (PLACE/PICK/RESTOW) in sequence order with corrected weights.
// This is the "as the data says now" view used by the correction impact
// analysis; the raw log remains untouched.
func EffectiveJobs(evts []Event) []Event {
	out := make([]Event, 0, len(evts))
	idxBySeq := make(map[int64]int, len(evts))
	for _, e := range evts {
		if e.Type == Correction {
			if i, ok := idxBySeq[e.TargetSeq]; ok {
				out[i].Weight = e.NewWeight
			}
			continue
		}
		idxBySeq[e.Seq] = len(out)
		out = append(out, e)
	}
	return out
}

// EffectiveWeight returns the weight of the job event targetSeq after
// folding all corrections that refer to it.
func EffectiveWeight(evts []Event, targetSeq int64) (float64, bool) {
	var w float64
	found := false
	for _, e := range evts {
		if e.Seq == targetSeq && e.Type != Correction {
			w = e.Weight
			found = true
		}
		if e.Type == Correction && e.TargetSeq == targetSeq {
			w = e.NewWeight
		}
	}
	return w, found
}
