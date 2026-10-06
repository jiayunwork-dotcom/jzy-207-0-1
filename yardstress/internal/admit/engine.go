// Package admit implements job admission, concurrency control, event
// commitment and the incremental maintenance of the checkpoint grid.
//
// # Concurrency model
//
// The stress field is linear, so the grid is just the sum of the committed
// events' contributions. A job is admitted under locks that cover exactly
// what it can touch:
//
//  1. the locks of the stacks it reads/writes (tier and weight bookkeeping),
//     acquired in ascending stack-id order;
//  2. the locks of the checkpoint tiles its load can influence (every point
//     where the contribution exceeds the discretization cutoff), acquired in
//     ascending tile-id order.
//
// The fixed global lock order (stacks before tiles, ascending ids inside
// each domain) makes deadlock impossible. Jobs whose influence zones are
// disjoint share no tile and proceed fully in parallel; jobs from adjacent
// blocks whose influence zones overlap serialise on the shared tiles, and
// the later one re-validates against the grid state that already includes
// the earlier one — so of two concurrently submitted jobs that are only
// acceptable in isolation, exactly one takes effect.
//
// Event numbers are assigned inside the tile critical section (via a short
// commit mutex), therefore the per-checkpoint order of incremental updates
// equals the event order, and the live grid agrees bit-for-bit with a
// replay of the committed events in sequence order.
package admit

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"yardstress/internal/discretize"
	"yardstress/internal/events"
	"yardstress/internal/ferr"
	"yardstress/internal/grid"
	"yardstress/internal/store"
	"yardstress/internal/stress"
	"yardstress/internal/yard"
)

// Params tunes the engine.
type Params struct {
	// TopK is the number of worst exceedances reported on rejection.
	TopK int
	// SnapshotInterval is the event cadence at which snapshots are
	// persisted (0 disables snapshots).
	SnapshotInterval int64
}

// JobRequest is one yard operation submitted for admission.
type JobRequest struct {
	Type    events.Type `json:"type"`
	StackID string      `json:"stack_id,omitempty"` // PLACE / PICK
	FromID  string      `json:"from_id,omitempty"`  // RESTOW
	ToID    string      `json:"to_id,omitempty"`    // RESTOW
	Weight  float64     `json:"weight"`             // kN
	Tiers   int         `json:"tiers"`              // tier count change
}

// Exceedance describes one checkpoint that would exceed its allowable
// stress after the job.
type Exceedance struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Stress    float64 `json:"stress"`
	Allowable float64 `json:"allowable"`
	Excess    float64 `json:"excess"`
}

// Rejection is returned when a job is refused for exceeding the allowable
// stress; the grid is left untouched.
type Rejection struct {
	Reason      string       `json:"reason"`
	Count       int          `json:"count"` // total number of exceeding checkpoints
	Exceedances []Exceedance `json:"exceedances"`
}

// Outcome is the result of submitting a job: exactly one of the fields is
// meaningful — Seq on success, Rejected on geotechnical refusal,
// FieldErrors on validation failure.
type Outcome struct {
	Seq         int64
	Rejected    *Rejection
	FieldErrors ferr.List
}

// CorrectionResult is the outcome of a weight correction.
type CorrectionResult struct {
	Seq         int64
	FieldErrors ferr.List
	// Impacted lists the effective jobs after the corrected one that would
	// have been rejected under the corrected data.
	Impacted []ImpactedEvent
	// CurrentExceedances are the worst exceedances of the live grid right
	// after applying the correction (informational; corrections themselves
	// are never refused because they describe physical reality).
	CurrentExceedances []Exceedance
}

// ImpactedEvent marks one historically effective job that would have been
// rejected under corrected data.
type ImpactedEvent struct {
	Seq         int64        `json:"seq"`
	Count       int          `json:"count"`
	Exceedances []Exceedance `json:"exceedances"`
}

// State is a consistent view of the checkpoint grid at a given event
// sequence number.
type State struct {
	Seq    int64
	Values []float64
}

// stackState is the dynamic per-stack bookkeeping.
type stackState struct {
	mu     sync.Mutex
	Tiers  int
	Weight float64
}

// Engine is the admission and grid-maintenance service.
type Engine struct {
	yard  *yard.Yard
	g     *grid.Grid
	disc  discretize.Params
	st    store.Store
	topK  int
	snapE int64

	states map[string]*stackState // dynamic stack state, per-stack locked

	commitMu sync.Mutex   // serialises seq assignment + persistence
	seq      atomic.Int64 // last committed event sequence number

	corrMu sync.Mutex // serialises corrections (rare, and they read the log)

	// HookPreCommit, when set, runs inside the admission critical section
	// just before a job is applied. Test-only instrumentation.
	HookPreCommit func()
}

// New builds an engine and recovers its state from the store: the latest
// snapshot plus a replay of the events that follow it.
func New(y *yard.Yard, g *grid.Grid, disc discretize.Params, st store.Store, p Params) (*Engine, error) {
	if p.TopK < 1 {
		p.TopK = 5
	}
	e := &Engine{
		yard: y, g: g, disc: disc, st: st,
		topK: p.TopK, snapE: p.SnapshotInterval,
		states: make(map[string]*stackState, len(y.Stacks)),
	}
	for id := range y.Stacks {
		e.states[id] = &stackState{}
	}

	snap, ok, err := st.LatestSnapshot()
	if err != nil {
		return nil, err
	}
	from := int64(0)
	if ok {
		if len(snap.Values) != g.Len() {
			return nil, fmt.Errorf("snapshot at seq %d has %d values, grid has %d checkpoints", snap.Seq, len(snap.Values), g.Len())
		}
		copy(g.Values, snap.Values)
		for id, ss := range snap.Stacks {
			if st, ok := e.states[id]; ok {
				st.Tiers, st.Weight = ss.Tiers, ss.Weight
			}
		}
		from = snap.Seq
		e.seq.Store(snap.Seq)
	}
	evts, err := st.List(from+1, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	for _, ev := range evts {
		acc, _ := e.deltaMap(ev)
		applyMap(g.Values, acc)
		e.applyStacks(ev)
		e.seq.Store(ev.Seq)
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func (e *Engine) validateStatic(req JobRequest) ferr.List {
	var errs ferr.List
	switch req.Type {
	case events.Place, events.Pick:
		if req.StackID == "" {
			errs = ferr.Appendf(errs, "stack_id", "is required")
		} else if e.yard.Get(req.StackID) == nil {
			errs = ferr.Appendf(errs, "stack_id", "unknown stack %q", req.StackID)
		}
	case events.Restow:
		if req.FromID == "" {
			errs = ferr.Appendf(errs, "from_id", "is required")
		} else if e.yard.Get(req.FromID) == nil {
			errs = ferr.Appendf(errs, "from_id", "unknown stack %q", req.FromID)
		}
		if req.ToID == "" {
			errs = ferr.Appendf(errs, "to_id", "is required")
		} else if e.yard.Get(req.ToID) == nil {
			errs = ferr.Appendf(errs, "to_id", "unknown stack %q", req.ToID)
		}
	default:
		errs = ferr.Appendf(errs, "type", "must be one of PLACE, PICK, RESTOW, got %q", req.Type)
	}
	if !(req.Weight > 0) {
		errs = ferr.Appendf(errs, "weight", "must be positive, got %v", req.Weight)
	}
	if req.Tiers < 1 {
		errs = ferr.Appendf(errs, "tiers", "must be >= 1, got %d", req.Tiers)
	}
	return errs
}

// stackIDs returns the stacks whose state the request reads or writes.
func (r JobRequest) stackIDs() []string {
	switch r.Type {
	case events.Place, events.Pick:
		return []string{r.StackID}
	case events.Restow:
		return []string{r.FromID, r.ToID}
	}
	return nil
}

// validateTiers checks tier feasibility against live stack state. Callers
// must hold the involved stack locks.
func (e *Engine) validateTiers(req JobRequest) ferr.List {
	var errs ferr.List
	switch req.Type {
	case events.Place:
		st := e.yard.Get(req.StackID)
		cur := e.states[req.StackID].Tiers
		if cur+req.Tiers > st.MaxTiers {
			errs = ferr.Appendf(errs, "tiers",
				"stack %s holds %d tiers, placing %d exceeds max %d", req.StackID, cur, req.Tiers, st.MaxTiers)
		}
	case events.Pick:
		cur := e.states[req.StackID].Tiers
		if cur < req.Tiers {
			errs = ferr.Appendf(errs, "tiers",
				"stack %s holds %d tiers, cannot pick %d", req.StackID, cur, req.Tiers)
		}
	case events.Restow:
		to := e.yard.Get(req.ToID)
		if cur := e.states[req.FromID].Tiers; cur < req.Tiers {
			errs = ferr.Appendf(errs, "tiers",
				"stack %s holds %d tiers, cannot restow %d", req.FromID, cur, req.Tiers)
		}
		if cur := e.states[req.ToID].Tiers; cur+req.Tiers > to.MaxTiers {
			errs = ferr.Appendf(errs, "tiers",
				"stack %s holds %d tiers, restowing %d exceeds max %d", req.ToID, cur, req.Tiers, to.MaxTiers)
		}
	}
	return errs
}

// ---------------------------------------------------------------------------
// load -> checkpoint mapping
// ---------------------------------------------------------------------------

// deltaMap computes the per-checkpoint stress change of an event and the
// set of tiles it touches. It is a pure function of the event and the
// static configuration, which is what makes replay bit-for-bit reproducible.
func (e *Engine) deltaMap(ev events.Event) (map[int]float64, []int) {
	acc := make(map[int]float64, 512)
	tileSet := make(map[int]struct{}, 32)
	z := e.g.Depth
	for _, d := range ev.Deltas() {
		st := e.yard.Get(d.StackID)
		if st == nil {
			continue
		}
		for _, pl := range discretize.Patch(st.Rect, d.Weight, e.disc) {
			r := discretize.InfluenceRadius(pl.P, z, e.disc.Cutoff)
			i0, i1, j0, j1, ok := e.g.Range(pl.X-r, pl.Y-r, pl.X+r, pl.Y+r)
			if !ok {
				continue
			}
			for _, t := range e.g.TilesForRange(i0, i1, j0, j1) {
				tileSet[t] = struct{}{}
			}
			for j := j0; j <= j1; j++ {
				for i := i0; i <= i1; i++ {
					idx := e.g.Idx(i, j)
					x, y := e.g.XY(idx)
					v := stress.PointLoadAt(pl.P, pl.X, pl.Y, x, y, z)
					if math.Abs(v) >= e.disc.Cutoff {
						acc[idx] += v
					}
				}
			}
		}
	}
	tiles := make([]int, 0, len(tileSet))
	for t := range tileSet {
		tiles = append(tiles, t)
	}
	sort.Ints(tiles)
	return acc, tiles
}

// checkAdmission compares candidate stress (current + delta) against the
// allowable at every affected checkpoint and returns the worst violations.
func (e *Engine) checkAdmission(acc map[int]float64) *Rejection {
	var bad []Exceedance
	for idx, d := range acc {
		cand := e.g.Values[idx] + d
		if allow := e.g.Allow[idx]; cand > allow {
			x, y := e.g.XY(idx)
			bad = append(bad, Exceedance{X: x, Y: y, Stress: cand, Allowable: allow, Excess: cand - allow})
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Slice(bad, func(a, b int) bool { return bad[a].Excess > bad[b].Excess })
	count := len(bad)
	if len(bad) > e.topK {
		bad = bad[:e.topK]
	}
	return &Rejection{Reason: "allowable_exceeded", Count: count, Exceedances: bad}
}

func applyMap(values []float64, acc map[int]float64) {
	for idx, d := range acc {
		values[idx] += d
	}
}

func rollbackMap(values []float64, acc map[int]float64) {
	for idx, d := range acc {
		values[idx] -= d
	}
}

// ---------------------------------------------------------------------------
// stack bookkeeping
// ---------------------------------------------------------------------------

func (e *Engine) lockStacks(ids ...string) func() {
	ids = uniqueSorted(ids)
	for _, id := range ids {
		e.states[id].mu.Lock()
	}
	return func() {
		for k := len(ids) - 1; k >= 0; k-- {
			e.states[ids[k]].mu.Unlock()
		}
	}
}

func uniqueSorted(ids []string) []string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	out := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			out = append(out, id)
		}
	}
	return out
}

// applyStacks folds the event's tier/weight change into the live state.
// Callers must hold the involved stack locks.
func (e *Engine) applyStacks(ev events.Event) {
	switch ev.Type {
	case events.Place:
		st := e.states[ev.StackID]
		st.Tiers += ev.Tiers
		st.Weight += ev.Weight
	case events.Pick:
		st := e.states[ev.StackID]
		st.Tiers -= ev.Tiers
		st.Weight -= ev.Weight
	case events.Restow:
		f := e.states[ev.FromID]
		t := e.states[ev.ToID]
		f.Tiers -= ev.Tiers
		f.Weight -= ev.Weight
		t.Tiers += ev.Tiers
		t.Weight += ev.Weight
	case events.Correction:
		for _, d := range ev.DeltaList {
			st := e.states[d.StackID]
			st.Weight += d.Weight
		}
	}
}

func (e *Engine) rollbackStacks(ev events.Event) {
	inv := ev
	switch ev.Type {
	case events.Place:
		inv.Type = events.Pick
	case events.Pick:
		inv.Type = events.Place
	case events.Restow:
		inv.FromID, inv.ToID = ev.ToID, ev.FromID
	case events.Correction:
		for i := range inv.DeltaList {
			inv.DeltaList[i].Weight = -inv.DeltaList[i].Weight
		}
	}
	e.applyStacks(inv)
}

// ---------------------------------------------------------------------------
// commit
// ---------------------------------------------------------------------------

// commit assigns the next sequence number, persists the event and, when
// due, persists a snapshot. It runs inside the caller's tile critical
// section; the commit mutex only serialises numbering + persistence.
func (e *Engine) commit(ev events.Event) (int64, error) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	ev.Seq = e.seq.Load() + 1
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	if err := e.st.AppendEvent(ev); err != nil {
		return 0, fmt.Errorf("persist event: %w", err)
	}
	e.seq.Store(ev.Seq)
	if e.snapE > 0 && ev.Seq%e.snapE == 0 {
		if err := e.saveSnapshot(ev.Seq); err != nil {
			// Snapshots are a performance device only; a failure here does
			// not compromise correctness, so it is logged, not fatal.
			fmt.Printf("snapshot at seq %d failed: %v\n", ev.Seq, err)
		}
	}
	return ev.Seq, nil
}

// saveSnapshot materialises the state at seq purely from the event log
// (latest earlier snapshot + replay), so a snapshot always equals a
// sequential replay regardless of concurrent in-flight jobs.
func (e *Engine) saveSnapshot(seq int64) error {
	prev, ok, err := e.st.LatestSnapshotBefore(seq)
	if err != nil {
		return err
	}
	values := make([]float64, e.g.Len())
	stacks := make(map[string]events.StackState, len(e.states))
	from := int64(0)
	if ok {
		copy(values, prev.Values)
		for id, ss := range prev.Stacks {
			stacks[id] = ss
		}
		from = prev.Seq
	}
	evts, err := e.st.List(from+1, seq)
	if err != nil {
		return err
	}
	e.replayOnto(values, stacks, evts)
	return e.st.SaveSnapshot(store.Snapshot{Seq: seq, Values: values, Stacks: stacks})
}

// replayOnto folds events into values/stacks. Pure: no locks, no live state.
func (e *Engine) replayOnto(values []float64, stacks map[string]events.StackState, evts []events.Event) {
	for _, ev := range evts {
		acc, _ := e.deltaMap(ev)
		applyMap(values, acc)
		events.ApplyToStacks(stacks, ev)
	}
}

// ---------------------------------------------------------------------------
// public operations
// ---------------------------------------------------------------------------

// Submit validates, adjudicates and (unless rejected) commits one job.
func (e *Engine) Submit(req JobRequest) (Outcome, error) {
	var out Outcome
	if errs := e.validateStatic(req); len(errs) > 0 {
		out.FieldErrors = errs
		return out, nil
	}
	ev := events.Event{
		Type: req.Type, StackID: req.StackID, FromID: req.FromID, ToID: req.ToID,
		Weight: req.Weight, Tiers: req.Tiers,
	}

	unlockStacks := e.lockStacks(req.stackIDs()...)
	defer unlockStacks()
	if errs := e.validateTiers(req); len(errs) > 0 {
		out.FieldErrors = errs
		return out, nil
	}

	acc, tiles := e.deltaMap(ev)
	unlockTiles := e.g.LockTiles(tiles)
	defer unlockTiles()

	if rej := e.checkAdmission(acc); rej != nil {
		out.Rejected = rej
		return out, nil
	}
	if e.HookPreCommit != nil {
		e.HookPreCommit()
	}
	applyMap(e.g.Values, acc)
	e.applyStacks(ev)
	seq, err := e.commit(ev)
	if err != nil {
		rollbackMap(e.g.Values, acc)
		e.rollbackStacks(ev)
		return out, err
	}
	out.Seq = seq
	return out, nil
}

// Correct records a weight correction for an already effective job. The
// correction is itself committed as a new event; history is never rewritten.
// The returned impact list names the later effective jobs that would have
// been rejected under the corrected data.
func (e *Engine) Correct(targetSeq int64, newWeight float64) (CorrectionResult, error) {
	var res CorrectionResult
	if !(newWeight > 0) {
		res.FieldErrors = ferr.Appendf(res.FieldErrors, "new_weight", "must be positive, got %v", newWeight)
		return res, nil
	}
	e.corrMu.Lock()
	defer e.corrMu.Unlock()

	all, err := e.st.List(0, math.MaxInt64)
	if err != nil {
		return res, err
	}
	var target *events.Event
	for i := range all {
		if all[i].Seq == targetSeq && all[i].Type != events.Correction {
			target = &all[i]
			break
		}
	}
	if target == nil {
		res.FieldErrors = ferr.Appendf(res.FieldErrors, "event_id", "no job event with seq %d", targetSeq)
		return res, nil
	}
	effW, _ := events.EffectiveWeight(all, targetSeq)
	deltaW := newWeight - effW

	ev := events.Event{Type: events.Correction, TargetSeq: targetSeq, NewWeight: newWeight}
	switch target.Type {
	case events.Place:
		ev.DeltaList = []events.Delta{{StackID: target.StackID, Weight: deltaW}}
	case events.Pick:
		ev.DeltaList = []events.Delta{{StackID: target.StackID, Weight: -deltaW}}
	case events.Restow:
		ev.DeltaList = []events.Delta{
			{StackID: target.FromID, Weight: -deltaW},
			{StackID: target.ToID, Weight: deltaW},
		}
	}

	ids := ev.StackIDs()
	unlockStacks := e.lockStacks(ids...)
	acc, tiles := e.deltaMap(ev)
	unlockTiles := e.g.LockTiles(tiles)

	applyMap(e.g.Values, acc)
	e.applyStacks(ev)
	seq, err := e.commit(ev)
	if err != nil {
		rollbackMap(e.g.Values, acc)
		e.rollbackStacks(ev)
		unlockTiles()
		unlockStacks()
		return res, err
	}
	unlockTiles()
	unlockStacks()
	res.Seq = seq

	// Grid-wide consistent read of the post-correction state.
	cur := e.Current()
	res.CurrentExceedances = e.worstExceedances(cur.Values)

	// Impact analysis: replay the log with the correction folded in and
	// re-adjudicate every later effective job.
	full, err := e.st.List(0, math.MaxInt64)
	if err != nil {
		return res, err
	}
	res.Impacted = e.impactedJobs(full, targetSeq)
	return res, nil
}

// worstExceedances reports the TopK worst exceedances of the given grid
// values against the allowables.
func (e *Engine) worstExceedances(values []float64) []Exceedance {
	var bad []Exceedance
	for idx, v := range values {
		if allow := e.g.Allow[idx]; v > allow {
			x, y := e.g.XY(idx)
			bad = append(bad, Exceedance{X: x, Y: y, Stress: v, Allowable: allow, Excess: v - allow})
		}
	}
	sort.Slice(bad, func(a, b int) bool { return bad[a].Excess > bad[b].Excess })
	if len(bad) > e.topK {
		bad = bad[:e.topK]
	}
	return bad
}

// impactedJobs replays the log with all corrections folded into their
// targets and reports every effective job after targetSeq whose admission
// check would have failed under the corrected data. The replay applies
// every job that actually happened (whether or not it would now be
// rejected), because the ground did feel those loads; the question answered
// is "would this job have passed the gate at its submission time".
func (e *Engine) impactedJobs(all []events.Event, targetSeq int64) []ImpactedEvent {
	eff := events.EffectiveJobs(all)
	sim := make([]float64, e.g.Len())
	var out []ImpactedEvent
	for _, ev := range eff {
		acc, _ := e.deltaMap(ev)
		if ev.Seq > targetSeq {
			var bad []Exceedance
			for idx, d := range acc {
				cand := sim[idx] + d
				if allow := e.g.Allow[idx]; cand > allow {
					x, y := e.g.XY(idx)
					bad = append(bad, Exceedance{X: x, Y: y, Stress: cand, Allowable: allow, Excess: cand - allow})
				}
			}
			if len(bad) > 0 {
				sort.Slice(bad, func(a, b int) bool { return bad[a].Excess > bad[b].Excess })
				count := len(bad)
				if len(bad) > e.topK {
					bad = bad[:e.topK]
				}
				out = append(out, ImpactedEvent{Seq: ev.Seq, Count: count, Exceedances: bad})
			}
		}
		applyMap(sim, acc)
	}
	return out
}

// ---------------------------------------------------------------------------
// queries
// ---------------------------------------------------------------------------

// ErrSeqOutOfRange is returned for grid queries beyond the committed log.
var ErrSeqOutOfRange = errors.New("event sequence out of range")

// Current returns a consistent copy of the live grid. It locks every tile,
// so it reflects a state between two committed events.
func (e *Engine) Current() State {
	unlock := e.g.LockAll()
	defer unlock()
	values := append([]float64(nil), e.g.Values...)
	return State{Seq: e.seq.Load(), Values: values}
}

// GridAt reconstructs the grid state exactly after event seq: latest
// snapshot at or before seq, then a replay of the remaining events.
func (e *Engine) GridAt(seq int64) (State, error) {
	cur := e.seq.Load()
	if seq < 0 || seq > cur {
		return State{}, ErrSeqOutOfRange
	}
	snap, ok, err := e.st.LatestSnapshotBefore(seq)
	if err != nil {
		return State{}, err
	}
	values := make([]float64, e.g.Len())
	stacks := make(map[string]events.StackState, len(e.states))
	from := int64(0)
	if ok {
		copy(values, snap.Values)
		for id, ss := range snap.Stacks {
			stacks[id] = ss
		}
		from = snap.Seq
	}
	evts, err := e.st.List(from+1, seq)
	if err != nil {
		return State{}, err
	}
	e.replayOnto(values, stacks, evts)
	return State{Seq: seq, Values: values}, nil
}

// Events returns the committed events in [from, to].
func (e *Engine) Events(from, to int64) ([]events.Event, error) {
	return e.st.List(from, to)
}

// Seq returns the last committed event sequence number.
func (e *Engine) Seq() int64 { return e.seq.Load() }

// Grid exposes the checkpoint grid (geometry and allowables are immutable;
// values must not be mutated by callers).
func (e *Engine) Grid() *grid.Grid { return e.g }

// StackView is a consistent snapshot of one stack's static and dynamic data.
type StackView struct {
	ID       string     `json:"id"`
	Block    string     `json:"block"`
	Rect     [4]float64 `json:"rect"`
	MaxTiers int        `json:"max_tiers"`
	Tiers    int        `json:"tiers"`
	Weight   float64    `json:"weight"`
}

// Stacks returns a consistent snapshot of every stack, sorted by id.
func (e *Engine) Stacks() []StackView {
	ids := make([]string, 0, len(e.states))
	for id := range e.states {
		ids = append(ids, id)
	}
	unlock := e.lockStacks(ids...)
	defer unlock()
	sort.Strings(ids) // lockStacks sorts a copy; sort here for output order
	out := make([]StackView, 0, len(ids))
	for _, id := range ids {
		st := e.states[id]
		geo := e.yard.Get(id)
		out = append(out, StackView{
			ID: id, Block: geo.Block,
			Rect:     [4]float64{geo.Rect.MinX, geo.Rect.MinY, geo.Rect.MaxX, geo.Rect.MaxY},
			MaxTiers: geo.MaxTiers,
			Tiers:    st.Tiers, Weight: st.Weight,
		})
	}
	return out
}
