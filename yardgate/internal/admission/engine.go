package admission

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"yardgate/internal/grid"
	"yardgate/internal/stress"
	"yardgate/internal/yard"
)

// EventStore persists events and snapshots. Implemented by the events
// package (PostgreSQL in production, in-memory for tests).
type EventStore interface {
	SaveEvent(ev Event) error
	SaveSnapshot(snap Snapshot) error
	LatestSnapshot() (Snapshot, bool, error)
	SnapshotAtOrBefore(seq int64) (Snapshot, bool, error)
	EventsInRange(fromExclusive, toInclusive int64) ([]Event, error)
	MaxSeq() (int64, error)
}

// Engine serializes admission decisions and maintains the live grid.
//
// Concurrency model:
//   - mu: jobs take RLock (many jobs may evaluate/apply concurrently);
//     corrections and snapshot captures take Lock (exclusive).
//   - tiles: a job locks exactly the checkpoint tiles its stress footprint
//     touches; jobs with disjoint footprints never wait for each other,
//     jobs with overlapping footprints serialize and therefore see each
//     other's effects.
//   - commitMu: makes event numbering gapless and orders the store writes.
type Engine struct {
	mu    sync.RWMutex
	state *State
	store EventStore
	tiles *tileTable

	commitMu sync.Mutex
	seq      atomic.Int64

	logMu sync.RWMutex
	log   []Event // all effective events, indexed by seq-1

	snapEvery int64
	topK      int
	snapCh    chan struct{}
	done      chan struct{}

	hook func() // test hook, invoked inside the tile critical section
}

// NewEngine creates an engine over a fresh (empty) state.
func NewEngine(y *yard.Yard, g *grid.Grid, disc stress.Discretizer, cutoff float64, store EventStore, snapEvery int64, topK int) *Engine {
	return NewEngineWithState(NewState(y, g, disc, cutoff), store, snapEvery, topK)
}

// NewEngineWithState creates an engine over an existing state.
func NewEngineWithState(st *State, store EventStore, snapEvery int64, topK int) *Engine {
	if topK <= 0 {
		topK = 5
	}
	e := &Engine{
		state:     st,
		store:     store,
		tiles:     newTileTable(st.Grid, 16),
		snapEvery: snapEvery,
		topK:      topK,
		snapCh:    make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	go e.snapshotLoop()
	return e
}

// Bootstrap recovers the engine from the store: latest snapshot plus a
// faithful replay of the events after it.
func Bootstrap(y *yard.Yard, g *grid.Grid, disc stress.Discretizer, cutoff float64, store EventStore, snapEvery int64, topK int) (*Engine, error) {
	st := NewState(y, g, disc, cutoff)
	var seq int64
	if snap, ok, err := store.LatestSnapshot(); err != nil {
		return nil, err
	} else if ok {
		if len(snap.Values) != g.Len() {
			return nil, fmt.Errorf("snapshot at seq %d has %d values, grid has %d", snap.Seq, len(snap.Values), g.Len())
		}
		copy(st.Grid.Values, snap.Values)
		st.RestoreSlots(snap.Slots)
		seq = snap.Seq
	}
	tail, err := store.EventsInRange(seq, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if err := ReplayEvents(st, tail); err != nil {
		return nil, fmt.Errorf("recovery replay: %w", err)
	}
	all, err := store.EventsInRange(0, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	for i, ev := range all {
		if ev.Seq != int64(i+1) {
			return nil, fmt.Errorf("event log not contiguous at position %d: seq=%d", i, ev.Seq)
		}
	}
	e := NewEngineWithState(st, store, snapEvery, topK)
	e.log = all
	if n := len(all); n > 0 {
		e.seq.Store(all[n-1].Seq)
	} else {
		e.seq.Store(seq)
	}
	return e, nil
}

// SetTestHook installs a hook invoked inside the tile critical section
// before planning. For tests only.
func (e *Engine) SetTestHook(h func()) { e.hook = h }

// State exposes the live state for read-only use by trusted callers that
// hold appropriate locks (replay helpers in this package).
func (e *Engine) State() *State { return e.state }

// footprintRadius computes the influence radius needed for realized loads.
func (e *Engine) footprintRadius(loads []LoadDelta) float64 {
	r := 0.0
	for _, ld := range loads {
		if rr := stress.InfluenceRadius(ld.DeltaKN, e.state.Grid.Z, e.state.Cutoff); rr > r {
			r = rr
		}
	}
	return r
}

// estimateRadius gives a first-guess footprint radius for a job before its
// loads are planned; the retry loop in Submit corrects under-estimates.
// It must not read mutable yard state (that would race with concurrent
// commits), so remove/reshuffle start at zero and let the retry widen.
func (e *Engine) estimateRadius(j Job) float64 {
	if j.Type == Place {
		return stress.InfluenceRadius(j.WeightKN*float64(j.Tiers), e.state.Grid.Z, e.state.Cutoff)
	}
	return 0
}

// footprintTiles computes the tile ids covering the job's influence zones
// at the given radius.
func (e *Engine) footprintTiles(j Job, radius float64) []int {
	var lists [][]int
	for _, ref := range j.Slots() {
		sl, ok := e.state.Yard.Slot(ref.Block, ref.Bay, ref.Row)
		if !ok {
			continue // Plan will report the unknown slot
		}
		r := radius + math.Hypot(sl.A, sl.B)/2
		lists = append(lists, e.tiles.rect(e.state.Grid, sl.CX-r, sl.CY-r, sl.CX+r, sl.CY+r))
	}
	return mergeTileIDs(lists...)
}

// Submit validates, adjudicates and (if within limits) applies a job.
func (e *Engine) Submit(j Job) (Result, error) {
	if err := j.ValidateSyntax(); err != nil {
		return Result{}, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()

	radius := e.estimateRadius(j)
	for attempt := 0; ; attempt++ {
		ids := e.footprintTiles(j, radius)
		e.tiles.lockAll(ids)
		if e.hook != nil {
			e.hook()
		}
		loads, err := e.state.Plan(j)
		if err != nil {
			e.tiles.unlockAll(ids)
			return Result{}, err
		}
		need := e.footprintRadius(loads)
		if need > radius {
			// Under-locked: widen the footprint and retry. After several
			// retries (state keeps changing under us) fall back to locking
			// the whole grid, which always succeeds.
			e.tiles.unlockAll(ids)
			if attempt >= 7 {
				radius = 1e15
			} else {
				radius = need
			}
			continue
		}
		pd := e.state.GridDeltas(loads)
		if viol := e.state.Grid.Evaluate(pd, e.topK); len(viol) > 0 {
			e.tiles.unlockAll(ids)
			return Result{Accepted: false, Violations: viol}, nil
		}
		seq, err := e.commit(Event{Type: EventType(j.Type), Job: &j, Loads: loads})
		if err != nil {
			e.tiles.unlockAll(ids)
			return Result{}, err
		}
		e.state.CommitJob(j, seq)
		e.state.Grid.Apply(pd)
		e.tiles.unlockAll(ids)
		e.maybeSnapshot(seq)
		return Result{Accepted: true, Seq: seq}, nil
	}
}

// commit assigns the next sequence number and persists the event.
// Gapless: the sequence is only advanced after the store write succeeds.
func (e *Engine) commit(ev Event) (int64, error) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	ev.Seq = e.seq.Load() + 1
	ev.CreatedAt = time.Now()
	if err := e.store.SaveEvent(ev); err != nil {
		return 0, fmt.Errorf("persist event: %w", err)
	}
	e.seq.Store(ev.Seq)
	e.logMu.Lock()
	e.log = append(e.log, ev)
	e.logMu.Unlock()
	return ev.Seq, nil
}

// Correct replaces the box weight of a previous place event and reports
// which later effective jobs the corrected data would have rejected.
func (e *Engine) Correct(targetSeq int64, newWeight float64) (CorrectionResult, error) {
	if newWeight <= 0 {
		return CorrectionResult{}, &ValidationError{Field: "new_weight_kn", Message: "box weight must be positive"}
	}
	e.mu.Lock()

	e.logMu.RLock()
	var target *Event
	if targetSeq >= 1 && targetSeq <= int64(len(e.log)) && e.log[targetSeq-1].Seq == targetSeq {
		target = &e.log[targetSeq-1]
	}
	e.logMu.RUnlock()
	if target == nil {
		e.mu.Unlock()
		return CorrectionResult{}, &ValidationError{Field: "target_seq", Message: fmt.Sprintf("no event with seq %d", targetSeq)}
	}
	if target.Type != EventPlace || target.Job == nil {
		e.mu.Unlock()
		return CorrectionResult{}, &ValidationError{Field: "target_seq", Message: "only place events carry a box weight to correct"}
	}

	loads, oldWeight, onSite := e.state.CorrectionLoads(targetSeq, newWeight)
	if !onSite {
		oldWeight = e.effectiveWeight(targetSeq)
	}
	pd := e.state.GridDeltas(loads)
	if viol := e.state.Grid.Evaluate(pd, e.topK); len(viol) > 0 {
		e.mu.Unlock()
		return CorrectionResult{Accepted: false, Violations: viol}, nil
	}
	seq, err := e.commit(Event{
		Type:       EventCorrection,
		Loads:      loads,
		Correction: &Correction{TargetSeq: targetSeq, OldWeightKN: oldWeight, NewWeightKN: newWeight},
	})
	if err != nil {
		e.mu.Unlock()
		return CorrectionResult{}, err
	}
	e.state.CommitCorrection(targetSeq, newWeight)
	e.state.Grid.Apply(pd)
	e.mu.Unlock()
	e.maybeSnapshot(seq)

	affected, err := e.ImpactList(targetSeq, seq)
	if err != nil {
		return CorrectionResult{}, err
	}
	return CorrectionResult{Accepted: true, Seq: seq, AffectedSeqs: affected}, nil
}

// effectiveWeight returns the box weight of a place event after applying
// all correction events up to now.
func (e *Engine) effectiveWeight(placeSeq int64) float64 {
	e.logMu.RLock()
	defer e.logMu.RUnlock()
	w := 0.0
	if placeSeq >= 1 && placeSeq <= int64(len(e.log)) && e.log[placeSeq-1].Job != nil {
		w = e.log[placeSeq-1].Job.WeightKN
	}
	for _, ev := range e.log {
		if ev.Type == EventCorrection && ev.Correction != nil && ev.Correction.TargetSeq == placeSeq {
			w = ev.Correction.NewWeightKN
		}
	}
	return w
}

// ImpactList replays history counterfactually (with every correction up to
// upTo applied at its target) and lists the effective jobs after targetSeq
// that the corrected data would have rejected.
func (e *Engine) ImpactList(targetSeq, upTo int64) ([]int64, error) {
	e.logMu.RLock()
	if upTo > int64(len(e.log)) {
		upTo = int64(len(e.log))
	}
	events := make([]Event, 0, upTo)
	events = append(events, e.log[:upTo]...)
	e.logMu.RUnlock()

	overrides := map[int64]float64{}
	correctedAt := map[int64]int64{} // target seq -> seq of its latest correction
	for _, ev := range events {
		if ev.Type == EventCorrection && ev.Correction != nil {
			overrides[ev.Correction.TargetSeq] = ev.Correction.NewWeightKN
			correctedAt[ev.Correction.TargetSeq] = ev.Seq
		}
	}

	// Base state: newest usable snapshot at or before the corrected event
	// (jobs after targetSeq must be replayed to be admission-checked), and
	// such that every correction's effect is either included in it or
	// applied as an override during the tail replay.
	base, err := e.counterfactualBase(correctedAt, targetSeq)
	if err != nil {
		return nil, err
	}
	var tail []Event
	for _, ev := range events {
		if ev.Seq > base.seq {
			tail = append(tail, ev)
		}
	}
	rejected, err := ReplayCounterfactual(base.state, tail, overrides, e.topK)
	if err != nil {
		return nil, err
	}
	// Only jobs after the corrected event belong to the impact list.
	out := make([]int64, 0, len(rejected))
	for _, seq := range rejected {
		if seq > targetSeq {
			out = append(out, seq)
		}
	}
	return out, nil
}

type cfBase struct {
	seq   int64
	state *State
}

// counterfactualBase picks the newest usable snapshot with seq <= maxBaseSeq
// (or the empty state) and returns a private state copy to replay on. A
// snapshot is usable when every correction either targets an event after the
// snapshot (the override is applied during the tail replay) or happened at
// or before the snapshot (the snapshot already includes its effect; by
// linearity the real and counterfactual states then coincide at the
// snapshot).
func (e *Engine) counterfactualBase(correctedAt map[int64]int64, maxBaseSeq int64) (cfBase, error) {
	s := maxBaseSeq
	for s >= 0 {
		snap, ok, err := e.store.SnapshotAtOrBefore(s)
		if err != nil {
			return cfBase{}, err
		}
		if !ok {
			break
		}
		usable := true
		for target, corrSeq := range correctedAt {
			if target <= snap.Seq && corrSeq > snap.Seq {
				usable = false
				break
			}
		}
		if usable {
			st := e.freshState()
			copy(st.Grid.Values, snap.Values)
			st.RestoreSlots(snap.Slots)
			return cfBase{seq: snap.Seq, state: st}, nil
		}
		if snap.Seq == 0 {
			break
		}
		s = snap.Seq - 1
	}
	return cfBase{seq: 0, state: e.freshState()}, nil
}

func (e *Engine) freshState() *State {
	return NewState(e.state.Yard, e.state.Grid.EmptyClone(), e.state.Disc, e.state.Cutoff)
}

// GridAt reconstructs the checkpoint grid as it was right after event seq.
// It uses the newest snapshot at or before seq and applies the stored load
// deltas of the remaining events; slot state is not needed for this.
func (e *Engine) GridAt(seq int64) (*grid.Grid, int64, error) {
	maxSeq := e.seq.Load()
	if seq < 0 || seq > maxSeq {
		return nil, 0, &ValidationError{Field: "seq", Message: fmt.Sprintf("seq must be in [0, %d]", maxSeq)}
	}
	g := e.state.Grid.EmptyClone()
	base := int64(0)
	if snap, ok, err := e.store.SnapshotAtOrBefore(seq); err != nil {
		return nil, 0, err
	} else if ok && snap.Seq <= seq {
		if len(snap.Values) != g.Len() {
			return nil, 0, fmt.Errorf("snapshot at seq %d has wrong size", snap.Seq)
		}
		copy(g.Values, snap.Values)
		base = snap.Seq
	}
	evs, err := e.store.EventsInRange(base, seq)
	if err != nil {
		return nil, 0, err
	}
	for _, ev := range evs {
		pd := g.ComputePointDeltas(e.rectsOf(ev.Loads), e.state.Disc, e.state.Cutoff)
		g.Apply(pd)
	}
	return g, seq, nil
}

func (e *Engine) rectsOf(loads []LoadDelta) []stress.RectLoad {
	rects := make([]stress.RectLoad, 0, len(loads))
	for _, ld := range loads {
		if sl, ok := e.state.Yard.Slot(ld.Slot.Block, ld.Slot.Bay, ld.Slot.Row); ok {
			rects = append(rects, stress.RectLoad{CX: sl.CX, CY: sl.CY, A: sl.A, B: sl.B, Q: ld.DeltaKN})
		}
	}
	return rects
}

// Current returns the live grid values and the current sequence number.
func (e *Engine) Current() (values []float64, seq int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state.Grid.CloneValues(), e.seq.Load()
}

// Seq returns the current event sequence number.
func (e *Engine) Seq() int64 { return e.seq.Load() }

// GridMeta returns the immutable grid geometry.
func (e *Engine) GridMeta() *grid.Grid { return e.state.Grid }

// Events returns the stored events with from < seq <= to (to <= 0 means
// "up to the latest").
func (e *Engine) Events(from, to int64) []Event {
	e.logMu.RLock()
	defer e.logMu.RUnlock()
	var out []Event
	for _, ev := range e.log {
		if ev.Seq > from && (to <= 0 || ev.Seq <= to) {
			out = append(out, ev)
		}
	}
	return out
}

// maybeSnapshot triggers an asynchronous snapshot every snapEvery events.
func (e *Engine) maybeSnapshot(seq int64) {
	if e.snapEvery <= 0 || seq%e.snapEvery != 0 {
		return
	}
	select {
	case e.snapCh <- struct{}{}:
	default:
	}
}

// SnapshotNow captures and stores a snapshot synchronously.
func (e *Engine) SnapshotNow() error {
	e.mu.Lock()
	snap := Snapshot{
		Seq:    e.seq.Load(),
		Values: e.state.Grid.CloneValues(),
		Slots:  e.state.CopySlots(),
	}
	e.mu.Unlock()
	return e.store.SaveSnapshot(snap)
}

// Close stops the background snapshot loop.
func (e *Engine) Close() { close(e.done) }

// snapshotLoop serializes snapshot captures. The capture happens under the
// exclusive lock so the stored state is exactly the state after some event.
func (e *Engine) snapshotLoop() {
	for {
		select {
		case <-e.snapCh:
			e.mu.Lock()
			snap := Snapshot{
				Seq:    e.seq.Load(),
				Values: e.state.Grid.CloneValues(),
				Slots:  e.state.CopySlots(),
			}
			e.mu.Unlock()
			_ = e.store.SaveSnapshot(snap) // best effort: events remain the source of truth
		case <-e.done:
			return
		}
	}
}

// SlotLoads returns the current per-slot total loads (for API introspection).
// It takes the exclusive lock because slot stacks are mutated by concurrent
// jobs under their tile locks.
func (e *Engine) SlotLoads() map[string]float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]float64{}
	for id, st := range e.state.Slots {
		w := 0.0
		for _, l := range st.Layers {
			w += l.WeightKN
		}
		if w != 0 {
			out[id] = w
		}
	}
	return out
}
