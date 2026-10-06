package admission_test

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"yardgate/internal/admission"

	"yardgate/internal/events"
	"yardgate/internal/grid"
	"yardgate/internal/stress"
	"yardgate/internal/yard"
)

// testEnv builds a small yard: two 1x1 m slots per block, blocks as given.
type testEnv struct {
	eng   *admission.Engine
	store *events.MemStore
	yard  *yard.Yard
	grid  *grid.Grid
}

func newEnv(t *testing.T, blocks []yard.Block, g *grid.Grid) *testEnv {
	t.Helper()
	y, err := yard.Build(blocks)
	if err != nil {
		t.Fatal(err)
	}
	store := events.NewMemStore()
	disc := stress.Discretizer{MaxCellSize: 0.5, FarFactor: 4}
	eng := admission.NewEngine(y, g, disc, 1e-6, store, 0, 5)
	t.Cleanup(eng.Close)
	return &testEnv{eng: eng, store: store, yard: y, grid: g}
}

func mustGrid(t *testing.T, minx, miny, dx, dy float64, nx, ny int, z float64, zones []grid.Zone, def float64) *grid.Grid {
	t.Helper()
	g, err := grid.New(minx, miny, dx, dy, nx, ny, z, zones, def)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// twoBlocks: block A with slots centered at (-0.5,0.5) and block B at
// (0.5,0.5); the checkpoint (0,0.5)... we use grid origin -4,-2 spacing 1.
func twoBlocks() []yard.Block {
	return []yard.Block{
		{ID: "A", OriginX: -1, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
		{ID: "B", OriginX: 0, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
	}
}

func TestRejectInvalidJobs(t *testing.T) {
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 100)
	env := newEnv(t, twoBlocks(), g)

	cases := []admission.Job{
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 0, Tiers: 1},    // non-positive weight
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: -5, Tiers: 1},   // negative weight
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 0},  // zero tiers
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 99}, // exceeds max tiers
		{Type: admission.Remove, From: admission.SlotRef{Block: "A"}, Tiers: 1},                // nothing to remove
		{Type: admission.Place, From: admission.SlotRef{Block: "ZZ"}, WeightKN: 100, Tiers: 1}, // unknown slot
		{Type: "lift", From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 1},           // unknown type
		{Type: admission.Reshuffle, From: admission.SlotRef{Block: "A"}, Tiers: 1},             // missing destination
	}
	for i, j := range cases {
		if _, err := env.eng.Submit(j); err == nil {
			t.Errorf("case %d: expected rejection, got accept", i)
		} else if _, ok := err.(*admission.ValidationError); !ok {
			t.Errorf("case %d: expected admission.ValidationError, got %T (%v)", i, err, err)
		}
	}
	if seq := env.eng.Seq(); seq != 0 {
		t.Fatalf("rejected jobs must not produce events, seq = %d", seq)
	}
}

func TestOverLimitRejectLeavesGridUnchanged(t *testing.T) {
	// Allowance 12 kPa; a single 100 kN box on slot A gives ~8.5 kPa at the
	// nearest checkpoints — OK. A second box on B pushes it over 12.
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 12)
	env := newEnv(t, twoBlocks(), g)

	r1, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 1})
	if err != nil || !r1.Accepted {
		t.Fatalf("first job should be accepted: %+v %v", r1, err)
	}
	before := append([]float64(nil), env.eng.State().Grid.Values...)

	r2, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "B"}, WeightKN: 100, Tiers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Accepted {
		t.Fatal("second job should exceed the allowance and be rejected")
	}
	if len(r2.Violations) == 0 {
		t.Fatal("rejection must report violating checkpoints")
	}
	for _, v := range r2.Violations {
		if v.ExcessKPa <= 0 || v.StressKPa <= v.AllowanceKPa {
			t.Errorf("bad violation record: %+v", v)
		}
	}
	// Grid must be untouched and no event recorded.
	after := env.eng.State().Grid.Values
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("grid changed at %d after rejection", i)
		}
	}
	if env.eng.Seq() != 1 {
		t.Fatalf("rejected job must not get a sequence number, seq = %d", env.eng.Seq())
	}
	if evs := env.eng.Events(0, 0); len(evs) != 1 {
		t.Fatalf("event log length = %d, want 1", len(evs))
	}
}

func TestRemoveAndReshuffle(t *testing.T) {
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 1000)
	env := newEnv(t, twoBlocks(), g)

	if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 2}); err != nil {
		t.Fatal(err)
	}
	// Move one tier from A to B.
	r, err := env.eng.Submit(admission.Job{Type: admission.Reshuffle, From: admission.SlotRef{Block: "A"}, To: &admission.SlotRef{Block: "B"}, Tiers: 1})
	if err != nil || !r.Accepted {
		t.Fatalf("reshuffle: %+v %v", r, err)
	}
	loads := env.eng.SlotLoads()
	if loads["A/0/0"] != 100 || loads["B/0/0"] != 100 {
		t.Fatalf("slot loads after reshuffle: %v", loads)
	}
	// admission.Remove the remaining tier from A.
	if _, err := env.eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: "A"}, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	if loads := env.eng.SlotLoads(); loads["A/0/0"] != 0 {
		t.Fatalf("slot A should be empty: %v", loads)
	}
	// Removing more than present must be rejected.
	if _, err := env.eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: "A"}, Tiers: 1}); err == nil {
		t.Fatal("remove from empty slot should be rejected")
	}
}

// Two jobs on adjacent blocks whose influence zones overlap: submitted
// concurrently, each individually within limits, together over the limit —
// exactly one may take effect.
func TestConcurrentAdjacentBlocksSingleWinner(t *testing.T) {
	for round := 0; round < 30; round++ {
		g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 12)
		env := newEnv(t, twoBlocks(), g)

		var wg sync.WaitGroup
		results := make([]admission.Result, 2)
		start := make(chan struct{})
		for i, block := range []string{"A", "B"} {
			wg.Add(1)
			go func(i int, block string) {
				defer wg.Done()
				<-start
				r, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: block}, WeightKN: 100, Tiers: 1})
				if err != nil {
					t.Errorf("submit: %v", err)
					return
				}
				results[i] = r
			}(i, block)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, r := range results {
			if r.Accepted {
				wins++
			} else if len(r.Violations) == 0 {
				t.Error("rejected job must report violations")
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d jobs accepted, want exactly 1", round, wins)
		}
		if env.eng.Seq() != 1 {
			t.Fatalf("round %d: seq = %d, want 1", round, env.eng.Seq())
		}
	}
}

// Jobs on far-apart blocks with disjoint influence zones must not wait for
// each other.
func TestConcurrentDisjointBlocksNoBlocking(t *testing.T) {
	blocks := []yard.Block{
		{ID: "W", OriginX: 0, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
		{ID: "E", OriginX: 200, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
	}
	g := mustGrid(t, -60, -5, 2, 2, 170, 6, 2, nil, 1000)
	env := newEnv(t, blocks, g)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once int32
	env.eng.SetTestHook(func() {
		if atomic.CompareAndSwapInt32(&once, 0, 1) {
			close(entered)
			<-release // first job parks inside its critical section
		}
	})

	doneA := make(chan error, 1)
	go func() {
		_, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "W"}, WeightKN: 100, Tiers: 1})
		doneA <- err
	}()
	<-entered

	// The second job must complete even though the first one is parked
	// inside the admission critical section.
	doneB := make(chan error, 1)
	go func() {
		_, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "E"}, WeightKN: 100, Tiers: 1})
		doneB <- err
	}()
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatalf("job B: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disjoint job blocked behind an unrelated critical section")
	}
	close(release)
	if err := <-doneA; err != nil {
		t.Fatalf("job A: %v", err)
	}
	if env.eng.Seq() != 2 {
		t.Fatalf("seq = %d, want 2", env.eng.Seq())
	}
}

// The grid state at any historical event number must be queryable.
func TestGridAtHistoricalSeq(t *testing.T) {
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 1000)
	env := newEnv(t, twoBlocks(), g)

	jobs := []admission.Job{
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 100, Tiers: 2},
		{Type: admission.Place, From: admission.SlotRef{Block: "B"}, WeightKN: 80, Tiers: 1},
		{Type: admission.Reshuffle, From: admission.SlotRef{Block: "A"}, To: &admission.SlotRef{Block: "B"}, Tiers: 1},
		{Type: admission.Remove, From: admission.SlotRef{Block: "B"}, Tiers: 1},
		{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 120, Tiers: 1},
	}
	for _, j := range jobs {
		if _, err := env.eng.Submit(j); err != nil {
			t.Fatal(err)
		}
	}
	// Take a snapshot mid-history to exercise the snapshot+tail path.
	if err := env.eng.SnapshotNow(); err != nil {
		t.Fatal(err)
	}
	more := []admission.Job{
		{Type: admission.Remove, From: admission.SlotRef{Block: "B"}, Tiers: 1},
		{Type: admission.Place, From: admission.SlotRef{Block: "B"}, WeightKN: 60, Tiers: 1},
	}
	for _, j := range more {
		if _, err := env.eng.Submit(j); err != nil {
			t.Fatal(err)
		}
	}

	// Reference: replay the first k events from empty for every k.
	all := env.eng.Events(0, 0)
	if len(all) != 7 {
		t.Fatalf("events = %d, want 7", len(all))
	}
	for k := int64(0); k <= 7; k++ {
		ref := admission.NewState(env.yard, mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 1000), env.eng.State().Disc, env.eng.State().Cutoff)
		var prefix []admission.Event
		for _, ev := range all {
			if ev.Seq <= k {
				prefix = append(prefix, ev)
			}
		}
		if err := admission.ReplayEvents(ref, prefix); err != nil {
			t.Fatal(err)
		}
		got, _, err := env.eng.GridAt(k)
		if err != nil {
			t.Fatal(err)
		}
		for i := range got.Values {
			if math.Abs(got.Values[i]-ref.Grid.Values[i]) > 1e-6 {
				t.Fatalf("grid at seq %d, checkpoint %d: got %v, want %v", k, i, got.Values[i], ref.Grid.Values[i])
			}
		}
	}
	if _, _, err := env.eng.GridAt(8); err == nil {
		t.Fatal("querying beyond the current seq must fail")
	}
}

// Weight correction: history is not rewritten, the correction is a new
// event, and the impact list names the later jobs the corrected data would
// have rejected.
func TestCorrectionImpactList(t *testing.T) {
	// Allowance 15 kPa. One 50 kN box on A gives ~4.3 kPa at the nearest
	// checkpoints; 100 kN on B gives ~8.5. Both fit (12.8 < 15). Correcting
	// A's box to 100 kN makes the pair exceed (17 > 15).
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 15)
	env := newEnv(t, twoBlocks(), g)

	if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 50, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "B"}, WeightKN: 100, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	// The A box leaves the yard; the correction then changes no current
	// load and must be accepted, but the impact list still flags job 2.
	if _, err := env.eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: "A"}, Tiers: 1}); err != nil {
		t.Fatal(err)
	}

	res, err := env.eng.Correct(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("correction should be accepted: %+v", res)
	}
	if res.Seq != 4 {
		t.Fatalf("correction seq = %d, want 4", res.Seq)
	}
	if len(res.AffectedSeqs) != 1 || res.AffectedSeqs[0] != 2 {
		t.Fatalf("affected = %v, want [2]", res.AffectedSeqs)
	}
	// History is intact: event 1 still records the original weight.
	ev1 := env.eng.Events(0, 1)[0]
	if ev1.Job.WeightKN != 50 {
		t.Fatalf("history rewritten! event 1 weight = %v", ev1.Job.WeightKN)
	}
	// The correction event is stored as a new event.
	ev4 := env.eng.Events(3, 4)[0]
	if ev4.Type != admission.EventCorrection || ev4.Correction.TargetSeq != 1 || ev4.Correction.NewWeightKN != 100 {
		t.Fatalf("bad correction event: %+v", ev4)
	}
}

// A correction that would push the current grid over the limit is rejected.
func TestCorrectionRejectedWhenOverLimit(t *testing.T) {
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 15)
	env := newEnv(t, twoBlocks(), g)

	if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "A"}, WeightKN: 50, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: "B"}, WeightKN: 100, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	// Boxes are still on site: correcting 50 -> 100 lifts the nearest
	// checkpoints to ~17 kPa > 15 kPa.
	res, err := env.eng.Correct(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Fatal("correction exceeding the allowance must be rejected")
	}
	if len(res.Violations) == 0 {
		t.Fatal("rejected correction must report violations")
	}
	if env.eng.Seq() != 2 {
		t.Fatalf("rejected correction must not produce an event, seq = %d", env.eng.Seq())
	}
	// Validation errors.
	if _, err := env.eng.Correct(1, 0); err == nil {
		t.Error("non-positive new weight should be rejected")
	}
	if _, err := env.eng.Correct(99, 100); err == nil {
		t.Error("unknown target should be rejected")
	}
	// A remove event carries no box weight to correct.
	if _, err := env.eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: "B"}, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.eng.Correct(3, 100); err == nil {
		t.Error("correcting a non-place event should be rejected")
	}
}

// Incremental maintenance vs full replay: after 100k effective events the
// live grid must match a from-scratch replay within 1e-6 kPa (it is in fact
// bit-identical, since both apply the same operations in the same order).
func TestIncrementalMatchesReplay100K(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100k-event test in short mode")
	}
	blocks := []yard.Block{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 3, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "B", OriginX: 24, OriginY: 0, Bays: 3, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
	}
	y, err := yard.Build(blocks)
	if err != nil {
		t.Fatal(err)
	}
	g := mustGrid(t, -10, -5, 2, 2, 31, 8, 2, nil, 1e9) // huge allowance: nothing rejected
	store := events.NewMemStore()
	disc := stress.Discretizer{MaxCellSize: 1.5, FarFactor: 4}
	eng := admission.NewEngine(y, g, disc, 1e-6, store, 1000, 5)
	defer eng.Close()

	const total = 100_000
	rng := uint64(42)
	next := func() uint64 { rng ^= rng << 13; rng ^= rng >> 7; rng ^= rng << 17; return rng }
	accepted := 0
	for accepted < total {
		block := []string{"A", "B"}[next()%2]
		bay, row := int(next()%3), int(next()%2)
		switch next() % 3 {
		case 0, 1: // place
			w := 50 + float64(next()%250)
			if r, err := eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: block, Bay: bay, Row: row}, WeightKN: w, Tiers: 1}); err == nil && r.Accepted {
				accepted++
			}
		case 2: // remove or reshuffle
			if next()%2 == 0 {
				if r, err := eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: block, Bay: bay, Row: row}, Tiers: 1}); err == nil && r.Accepted {
					accepted++
				}
			} else {
				to := admission.SlotRef{Block: []string{"A", "B"}[next()%2], Bay: int(next() % 3), Row: int(next() % 2)}
				if r, err := eng.Submit(admission.Job{Type: admission.Reshuffle, From: admission.SlotRef{Block: block, Bay: bay, Row: row}, To: &to, Tiers: 1}); err == nil && r.Accepted {
					accepted++
				}
			}
		}
	}
	if err := eng.SnapshotNow(); err != nil {
		t.Fatal(err)
	}

	// Full replay from the empty yard.
	all, err := store.EventsInRange(0, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("stored events = %d, want %d", len(all), total)
	}
	g2 := mustGrid(t, -10, -5, 2, 2, 31, 8, 2, nil, 1e9)
	ref := admission.NewState(y, g2, disc, 1e-6)
	if err := admission.ReplayEvents(ref, all); err != nil {
		t.Fatal(err)
	}
	maxDiff := 0.0
	for i := range g.Values {
		if d := math.Abs(g.Values[i] - g2.Values[i]); d > maxDiff {
			maxDiff = d
		}
	}
	t.Logf("max |incremental - replay| after %d events: %g kPa", total, maxDiff)
	if maxDiff > 1e-6 {
		t.Fatalf("incremental state diverged from replay: %g kPa", maxDiff)
	}

	// Recovery through snapshot + tail replay must give the same state.
	eng2, err := admission.Bootstrap(y, mustGrid(t, -10, -5, 2, 2, 31, 8, 2, nil, 1e9), disc, 1e-6, store, 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()
	for i := range g.Values {
		if d := math.Abs(g.Values[i] - eng2.State().Grid.Values[i]); d > 1e-6 {
			t.Fatalf("snapshot recovery diverged at %d: %g kPa", i, d)
		}
	}
	if eng2.Seq() != total {
		t.Fatalf("recovered seq = %d, want %d", eng2.Seq(), total)
	}
}

// Corrections must work correctly in the presence of snapshots: a snapshot
// that does not include the correction's effect must not be used as the
// counterfactual base; one that does may be used.
func TestCorrectionWithSnapshots(t *testing.T) {
	g := mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 15)
	env := newEnv(t, twoBlocks(), g)

	place := func(block string, w float64) {
		t.Helper()
		if _, err := env.eng.Submit(admission.Job{Type: admission.Place, From: admission.SlotRef{Block: block}, WeightKN: w, Tiers: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Each kN on either slot contributes ~0.085 kPa at the nearest
	// checkpoints; the allowance is 15 kPa.
	place("A", 50)                                // seq 1 -> 4.26
	place("B", 100)                               // seq 2 -> 12.78
	if err := env.eng.SnapshotNow(); err != nil { // snapshot at seq 2
		t.Fatal(err)
	}
	// Remove A's box (seq 3), then correct event 1 to 100 kN (seq 4).
	if _, err := env.eng.Submit(admission.Job{Type: admission.Remove, From: admission.SlotRef{Block: "A"}, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	res, err := env.eng.Correct(1, 100)
	if err != nil || !res.Accepted {
		t.Fatalf("correction: %+v %v", res, err)
	}
	if len(res.AffectedSeqs) != 1 || res.AffectedSeqs[0] != 2 {
		t.Fatalf("affected = %v, want [2]", res.AffectedSeqs)
	}
	// A snapshot taken at or after the correction must not truncate the
	// counterfactual analysis (regression: the base search is bounded by
	// the corrected event, not by the newest snapshot).
	if err := env.eng.SnapshotNow(); err != nil { // snapshot at seq 4
		t.Fatal(err)
	}
	if affected, err := env.eng.ImpactList(1, 4); err != nil || len(affected) != 1 || affected[0] != 2 {
		t.Fatalf("impact list with later snapshot = %v, %v; want [2]", affected, err)
	}

	// More history on top, then a snapshot that includes the correction.
	place("A", 40)                                // seq 5 -> 11.93
	if err := env.eng.SnapshotNow(); err != nil { // snapshot at seq 5
		t.Fatal(err)
	}
	place("B", 20) // seq 6 -> 13.63
	// Correct event 5 (40 -> 50, seq 7): the snapshot at 5 predates this
	// correction, so the analysis must fall back to an earlier base.
	res, err = env.eng.Correct(5, 50)
	if err != nil || !res.Accepted {
		t.Fatalf("correction 2: %+v %v", res, err)
	}
	if len(res.AffectedSeqs) != 0 {
		t.Fatalf("affected = %v, want []", res.AffectedSeqs)
	}
	if err := env.eng.SnapshotNow(); err != nil { // snapshot at seq 7, includes all corrections
		t.Fatal(err)
	}
	// Correct event 6 (20 -> 25, seq 8): the snapshot at 7 already includes
	// every correction with a target at or before it, so it is a valid
	// counterfactual base; the answer must still be exact.
	res, err = env.eng.Correct(6, 25)
	if err != nil || !res.Accepted {
		t.Fatalf("correction 3: %+v %v", res, err)
	}
	if len(res.AffectedSeqs) != 0 {
		t.Fatalf("affected = %v, want []", res.AffectedSeqs)
	}
	// Live grid must still match a faithful replay of everything.
	all := env.eng.Events(0, 0)
	ref := admission.NewState(env.yard, mustGrid(t, -4, -2, 1, 1, 9, 5, 2, nil, 15), env.eng.State().Disc, env.eng.State().Cutoff)
	if err := admission.ReplayEvents(ref, all); err != nil {
		t.Fatal(err)
	}
	for i := range env.grid.Values {
		if env.grid.Values[i] != ref.Grid.Values[i] {
			t.Fatalf("live grid diverged from replay at %d: %v vs %v", i, env.grid.Values[i], ref.Grid.Values[i])
		}
	}
}

// Hammer the engine with concurrent jobs over overlapping footprints; the
// final live grid must equal a sequential replay of the event log.
func TestConcurrentStressMatchesReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent stress test in short mode")
	}
	blocks := []yard.Block{
		{ID: "A", OriginX: 0, OriginY: 0, Bays: 2, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "B", OriginX: 12, OriginY: 0, Bays: 2, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
		{ID: "C", OriginX: 24, OriginY: 0, Bays: 2, Rows: 2, BayWidth: 6, RowWidth: 2.5, MaxTiers: 5},
	}
	y, err := yard.Build(blocks)
	if err != nil {
		t.Fatal(err)
	}
	mkGrid := func() *grid.Grid {
		g, err := grid.New(-10, -5, 1, 1, 44, 12, 2, nil, 1e9)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	store := events.NewMemStore()
	disc := stress.Discretizer{MaxCellSize: 1.0, FarFactor: 4}
	eng := admission.NewEngine(y, mkGrid(), disc, 1e-6, store, 0, 5)
	defer eng.Close()

	const workers = 16
	const perWorker = 500
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := uint64(1234 + w)
			next := func() uint64 { rng ^= rng << 13; rng ^= rng >> 7; rng ^= rng << 17; return rng }
			for i := 0; i < perWorker; i++ {
				ref := admission.SlotRef{Block: []string{"A", "B", "C"}[next()%3], Bay: int(next() % 2), Row: int(next() % 2)}
				var j admission.Job
				switch next() % 3 {
				case 0, 1:
					j = admission.Job{Type: admission.Place, From: ref, WeightKN: 50 + float64(next()%200), Tiers: 1}
				case 2:
					if next()%2 == 0 {
						j = admission.Job{Type: admission.Remove, From: ref, Tiers: 1}
					} else {
						to := admission.SlotRef{Block: []string{"A", "B", "C"}[next()%3], Bay: int(next() % 2), Row: int(next() % 2)}
						j = admission.Job{Type: admission.Reshuffle, From: ref, To: &to, Tiers: 1}
					}
				}
				if r, err := eng.Submit(j); err == nil && r.Accepted {
					accepted.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	n := accepted.Load()
	t.Logf("accepted %d events", n)
	if n == 0 {
		t.Fatal("no events accepted")
	}

	all, err := store.EventsInRange(0, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(all)) != n {
		t.Fatalf("stored %d events, accepted %d", len(all), n)
	}
	for i, ev := range all {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event numbering not gapless at %d: seq=%d", i, ev.Seq)
		}
	}
	ref := admission.NewState(y, mkGrid(), disc, 1e-6)
	if err := admission.ReplayEvents(ref, all); err != nil {
		t.Fatal(err)
	}
	live, _ := eng.Current()
	maxDiff := 0.0
	for i := range live {
		if d := math.Abs(live[i] - ref.Grid.Values[i]); d > maxDiff {
			maxDiff = d
		}
	}
	t.Logf("max |live - replay| = %g kPa", maxDiff)
	if maxDiff > 1e-6 {
		t.Fatalf("live grid diverged from replay: %g kPa", maxDiff)
	}
}
