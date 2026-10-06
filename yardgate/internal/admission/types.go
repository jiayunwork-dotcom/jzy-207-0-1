// Package admission implements job admission and concurrency control: it
// validates yard jobs (place / remove / reshuffle), checks them against the
// allowable-stress constraint on the checkpoint grid, applies accepted jobs
// incrementally, numbers the resulting events in effect order, and handles
// weight corrections with their impact analysis.
package admission

import (
	"fmt"
	"time"

	"yardgate/internal/grid"
)

// Violation is re-exported from the grid package for API convenience.
type Violation = grid.Violation

// JobType is the kind of a yard job.
type JobType string

const (
	Place     JobType = "place"     // drop containers onto a slot
	Remove    JobType = "remove"    // lift containers off a slot
	Reshuffle JobType = "reshuffle" // move containers between slots
)

// SlotRef identifies one stacking position.
type SlotRef struct {
	Block string `json:"block"`
	Bay   int    `json:"bay"`
	Row   int    `json:"row"`
}

// Job is a single yard operation request.
type Job struct {
	Type JobType  `json:"type"`
	From SlotRef  `json:"from"`         // the slot (place/remove) or source slot (reshuffle)
	To   *SlotRef `json:"to,omitempty"` // destination slot (reshuffle only)
	// WeightKN is the per-box weight in kN; required for place, corrected
	// by correction events. Remove/reshuffle take weights from yard state.
	WeightKN float64 `json:"weight_kn,omitempty"`
	Tiers    int     `json:"tiers"` // number of tiers placed / removed / moved
}

// ValidateSyntax checks request fields that do not depend on yard state.
func (j Job) ValidateSyntax() error {
	switch j.Type {
	case Place, Remove, Reshuffle:
	default:
		return &ValidationError{Field: "type", Message: fmt.Sprintf("unknown job type %q", j.Type)}
	}
	if j.From.Block == "" {
		return &ValidationError{Field: "from.block", Message: "block id is required"}
	}
	if j.From.Bay < 0 || j.From.Row < 0 {
		return &ValidationError{Field: "from", Message: "bay and row must be non-negative"}
	}
	if j.Type == Place && j.WeightKN <= 0 {
		return &ValidationError{Field: "weight_kn", Message: "box weight must be positive"}
	}
	if j.Tiers < 1 {
		return &ValidationError{Field: "tiers", Message: "tiers must be a positive integer"}
	}
	if j.Type == Reshuffle {
		if j.To == nil {
			return &ValidationError{Field: "to", Message: "destination slot is required for reshuffle"}
		}
		if j.To.Block == "" || j.To.Bay < 0 || j.To.Row < 0 {
			return &ValidationError{Field: "to", Message: "invalid destination slot"}
		}
		if *j.To == j.From {
			return &ValidationError{Field: "to", Message: "destination must differ from source"}
		}
	}
	return nil
}

// Slots returns the slot references involved in the job.
func (j Job) Slots() []SlotRef {
	if j.Type == Reshuffle && j.To != nil {
		return []SlotRef{j.From, *j.To}
	}
	return []SlotRef{j.From}
}

// LoadDelta is a realized load change (kN) on one slot.
type LoadDelta struct {
	Slot    SlotRef `json:"slot"`
	DeltaKN float64 `json:"delta_kn"`
}

// EventType classifies stored events.
type EventType string

const (
	EventPlace      EventType = "place"
	EventRemove     EventType = "remove"
	EventReshuffle  EventType = "reshuffle"
	EventCorrection EventType = "correction"
)

// Correction is the payload of a correction event: the box weight of a
// previous place event was misreported and is replaced. History is not
// rewritten; the correction is a new event.
type Correction struct {
	TargetSeq   int64   `json:"target_seq"`
	OldWeightKN float64 `json:"old_weight_kn"`
	NewWeightKN float64 `json:"new_weight_kn"`
}

// Event is one admitted, effective change, numbered in effect order.
type Event struct {
	Seq        int64       `json:"seq"`
	Type       EventType   `json:"type"`
	Job        *Job        `json:"job,omitempty"`
	Loads      []LoadDelta `json:"loads"` // realized load changes of this event
	Correction *Correction `json:"correction,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
}

// Layer is one stacked box tier: its weight and the event that placed it.
type Layer struct {
	WeightKN  float64 `json:"weight_kn"`
	OriginSeq int64   `json:"origin_seq"`
}

// Snapshot is a consistent capture of the full mutable state after a given
// event sequence number.
type Snapshot struct {
	Seq    int64              `json:"seq"`
	Values []float64          `json:"values"` // grid stress values
	Slots  map[string][]Layer `json:"slots"`  // slot id -> tier stack
}

// ValidationError reports a rejected request field.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Result is the outcome of a job submission.
type Result struct {
	Accepted   bool
	Seq        int64
	Violations []Violation
}

// CorrectionResult is the outcome of a weight correction.
type CorrectionResult struct {
	Accepted     bool
	Seq          int64
	AffectedSeqs []int64 // jobs after the corrected one that the corrected data would reject
	Violations   []Violation
}
