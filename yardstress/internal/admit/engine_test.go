package admit

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yardstress/internal/discretize"
	"yardstress/internal/events"
	"yardstress/internal/grid"
	"yardstress/internal/store"
	"yardstress/internal/yard"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustYard(t *testing.T, blocks []yard.BlockCfg) *yard.Yard {
	t.Helper()
	y, errs := yard.New(blocks)
	if len(errs) > 0 {
		t.Fatalf("yard: %v", errs)
	}
	return y
}

func newEngine(t *testing.T, y *yard.Yard, g *grid.Grid, disc discretize.Params, st store.Store, p Params) *Engine {
	t.Helper()
	e, err := New(y, g, disc, st, p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func uniformGrid(nx, ny int, spacing, depth, allowable float64, tileSize int) *grid.Grid {
	g := grid.New(0, 0, spacing, spacing, nx, ny, depth, nil, tileSize)
	allow := make([]float64, g.Len())
	for i := range allow {
		allow[i] = allowable
	}
	g.Allow = allow
	return g
}

func maxAbs(values []float64) float64 {
	m := 0.0
	for _, v := range values {
		if math.Abs(v) > m {
			m = math.Abs(v)
		}
	}
	return m
}

func maxDiff(a, b []float64) float64 {
	m := 0.0
	for i := range a {
		if d := math.Abs(a[i] - b[i]); d > m {
			m = d
		}
	}
	return m
}

// replayAll recomputes the grid from the empty state by replaying every
// committed event in sequence order.
func replayAll(e *Engine) []float64 {
	evts, err := e.st.List(0, math.MaxInt64)
	if err != nil {
		panic(err)
	}
	values := make([]float64, e.g.Len())
	stacks := make(map[string]events.StackState, len(e.states))
	e.replayOnto(values, stacks, evts)
	return values
}

// twoAdjacentStacks: block A footprint x[10,22.5], block B x[22.5,35],
// both y[8,11]; shared edge at x=22.5. Depth 12.
func adjacentSetup(allowable float64) (*yard.Yard, *grid.Grid) {
	y, _ := yard.New([]yard.BlockCfg{
		{ID: "A", OriginX: 10, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 10},
		{ID: "B", OriginX: 22.5, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 10},
	})
	g := uniformGrid(30, 12, 2, 12, allowable, 4)
	return y, g
}

var testDisc = discretize.Params{MaxCell: 3, Cutoff: 1e-6}

// ---------------------------------------------------------------------------
// basic job lifecycle
// ---------------------------------------------------------------------------

func TestPlacePickRestowLifecycle(t *testing.T) {
	y, g := adjacentSetup(1e9)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})

	out, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 1500, Tiers: 2})
	if err != nil || out.Rejected != nil || len(out.FieldErrors) > 0 {
		t.Fatalf("place failed: %+v err=%v", out, err)
	}
	if out.Seq != 1 {
		t.Fatalf("first event seq = %d, want 1", out.Seq)
	}
	cur := eng.Current()
	if maxAbs(cur.Values) == 0 {
		t.Fatal("grid should be non-zero after placing 1500 kN")
	}
	peak := maxAbs(cur.Values)

	// Picking the same weight returns the grid exactly to zero.
	out, err = eng.Submit(JobRequest{Type: events.Pick, StackID: "A-B01-R01", Weight: 1500, Tiers: 2})
	if err != nil || out.Rejected != nil {
		t.Fatalf("pick failed: %+v err=%v", out, err)
	}
	if m := maxAbs(eng.Current().Values); m != 0 {
		t.Fatalf("grid not zero after exact pick: max %g", m)
	}

	// Restow transfers the load between stacks.
	if _, err = eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 1200, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	before := eng.Current()
	out, err = eng.Submit(JobRequest{Type: events.Restow, FromID: "A-B01-R01", ToID: "B-B01-R01", Weight: 700, Tiers: 1})
	if err != nil || out.Rejected != nil {
		t.Fatalf("restow failed: %+v err=%v", out, err)
	}
	after := eng.Current()
	// Load moved: total absolute stress must change, stack states updated.
	stacks := eng.Stacks()
	var a, b StackView
	for _, s := range stacks {
		if s.ID == "A-B01-R01" {
			a = s
		}
		if s.ID == "B-B01-R01" {
			b = s
		}
	}
	if a.Weight != 500 || a.Tiers != 0 || b.Weight != 700 || b.Tiers != 1 {
		t.Fatalf("stack states wrong: A=%+v B=%+v", a, b)
	}
	if maxDiff(before.Values, after.Values) == 0 {
		t.Fatal("restow did not change the grid")
	}
	_ = peak
}

func TestValidationErrors(t *testing.T) {
	y, g := adjacentSetup(1e9)
	y.Stacks["A-B01-R01"].MaxTiers = 2
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})

	// One tier present for the pick/restow cases.
	if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 100, Tiers: 1}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		req   JobRequest
		field string
	}{
		{"zero weight", JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 0, Tiers: 1}, "weight"},
		{"negative weight", JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: -5, Tiers: 1}, "weight"},
		{"zero tiers", JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 10, Tiers: 0}, "tiers"},
		{"unknown stack", JobRequest{Type: events.Place, StackID: "ZZ", Weight: 10, Tiers: 1}, "stack_id"},
		{"bad type", JobRequest{Type: "DROP", StackID: "A-B01-R01", Weight: 10, Tiers: 1}, "type"},
		{"exceeds max tiers", JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 10, Tiers: 2}, "tiers"},
		{"pick too many tiers", JobRequest{Type: events.Pick, StackID: "A-B01-R01", Weight: 10, Tiers: 2}, "tiers"},
		{"restow from empty", JobRequest{Type: events.Restow, FromID: "B-B01-R01", ToID: "A-B01-R01", Weight: 10, Tiers: 1}, "tiers"},
		{"restow unknown from", JobRequest{Type: events.Restow, FromID: "ZZ", ToID: "A-B01-R01", Weight: 10, Tiers: 1}, "from_id"},
		{"restow unknown to", JobRequest{Type: events.Restow, FromID: "A-B01-R01", ToID: "ZZ", Weight: 10, Tiers: 1}, "to_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := eng.Submit(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.FieldErrors) == 0 {
				t.Fatalf("expected field error %q, got success seq=%d", tc.field, out.Seq)
			}
			if !out.FieldErrors.Has(tc.field) {
				t.Fatalf("expected field %q in %v", tc.field, out.FieldErrors)
			}
		})
	}
}

// A rejected job must leave the grid, the sequence counter and the event
// log untouched.
func TestRejectLeavesGridUnchanged(t *testing.T) {
	y, g := adjacentSetup(1e9)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})

	if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 1000, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	before := eng.Current()

	// Tighten the allowable exactly where the next job would add stress:
	// halfway between the current value and the candidate value.
	ev := events.Event{Type: events.Place, StackID: "B-B01-R01", Weight: 900, Tiers: 1}
	acc, _ := eng.deltaMap(ev)
	idx, dmax := -1, 0.0
	for i, d := range acc {
		if d > dmax {
			dmax, idx = d, i
		}
	}
	if idx < 0 {
		t.Fatal("empty delta map")
	}
	eng.g.Allow[idx] = eng.g.Values[idx] + dmax/2

	out, err := eng.Submit(JobRequest{Type: events.Place, StackID: "B-B01-R01", Weight: 900, Tiers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rejected == nil {
		t.Fatal("expected rejection")
	}
	if out.Rejected.Count == 0 || len(out.Rejected.Exceedances) == 0 {
		t.Fatal("rejection must report exceedances")
	}
	for _, ex := range out.Rejected.Exceedances {
		if ex.Excess <= 0 || ex.Stress <= ex.Allowable {
			t.Errorf("bad exceedance entry: %+v", ex)
		}
	}
	// Exceedances sorted by excess, descending.
	ex := out.Rejected.Exceedances
	for i := 1; i < len(ex); i++ {
		if ex[i].Excess > ex[i-1].Excess {
			t.Error("exceedances not sorted by excess")
		}
	}

	after := eng.Current()
	if maxDiff(before.Values, after.Values) != 0 {
		t.Fatal("grid changed after rejected job")
	}
	if eng.Seq() != before.Seq {
		t.Fatal("sequence advanced after rejected job")
	}
	evts, _ := eng.Events(0, math.MaxInt64)
	if len(evts) != 1 {
		t.Fatalf("event log grew after rejection: %d events", len(evts))
	}
}

// Superposition at engine level: applying jobs A and B together yields the
// sum of applying each to a fresh yard.
func TestEngineSuperposition(t *testing.T) {
	mk := func() *Engine {
		y, g := adjacentSetup(1e9)
		return newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
	}
	jobA := JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 800, Tiers: 1}
	jobB := JobRequest{Type: events.Place, StackID: "B-B01-R01", Weight: 1300, Tiers: 2}

	both := mk()
	if _, err := both.Submit(jobA); err != nil {
		t.Fatal(err)
	}
	if _, err := both.Submit(jobB); err != nil {
		t.Fatal(err)
	}
	onlyA := mk()
	if _, err := onlyA.Submit(jobA); err != nil {
		t.Fatal(err)
	}
	onlyB := mk()
	if _, err := onlyB.Submit(jobB); err != nil {
		t.Fatal(err)
	}
	vBoth := both.Current().Values
	vA := onlyA.Current().Values
	vB := onlyB.Current().Values
	for i := range vBoth {
		if vBoth[i] != vA[i]+vB[i] {
			t.Fatalf("superposition violated at checkpoint %d: %v != %v + %v", i, vBoth[i], vA[i], vB[i])
		}
	}
}

// ---------------------------------------------------------------------------
// concurrency
// ---------------------------------------------------------------------------

// Two jobs on adjacent blocks whose influence zones overlap: each alone is
// admissible, together they exceed. Submitted concurrently, exactly one may
// take effect.
func TestConcurrentAdjacentBlocksSingleEffect(t *testing.T) {
	const W = 4000.0

	// Calibrate the allowable: above each single job, below the pair.
	calib := func(jobs ...JobRequest) float64 {
		y, g := adjacentSetup(1e18)
		eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
		for _, j := range jobs {
			if _, err := eng.Submit(j); err != nil {
				t.Fatal(err)
			}
		}
		return maxAbs(eng.Current().Values)
	}
	placeA := JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: W, Tiers: 1}
	placeB := JobRequest{Type: events.Place, StackID: "B-B01-R01", Weight: W, Tiers: 1}
	single := calib(placeA)
	double := calib(placeA, placeB)
	if !(double > single) {
		t.Fatalf("calibration failed: single %v double %v", single, double)
	}
	allowable := (single + double) / 2

	for trial := 0; trial < 20; trial++ {
		y, g := adjacentSetup(allowable)
		eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})

		var wg sync.WaitGroup
		start := make(chan struct{})
		outcomes := make([]Outcome, 2)
		for k, job := range []JobRequest{placeA, placeB} {
			wg.Add(1)
			go func(k int, job JobRequest) {
				defer wg.Done()
				<-start
				out, err := eng.Submit(job)
				if err != nil {
					t.Error(err)
				}
				outcomes[k] = out
			}(k, job)
		}
		close(start)
		wg.Wait()

		accepted := 0
		for _, o := range outcomes {
			if o.Rejected == nil && len(o.FieldErrors) == 0 {
				accepted++
			}
		}
		if accepted != 1 {
			t.Fatalf("trial %d: %d jobs accepted, want exactly 1", trial, accepted)
		}
		// And the committed grid must match a replay of the committed log.
		if d := maxDiff(eng.Current().Values, replayAll(eng)); d != 0 {
			t.Fatalf("trial %d: live grid vs replay diff %g", trial, d)
		}
	}
}

// Jobs on non-adjacent blocks with disjoint influence zones must not wait
// on each other: while one job is held inside its critical section, the
// other still completes.
func TestConcurrentNonAdjacentNoBlock(t *testing.T) {
	y, _ := yard.New([]yard.BlockCfg{
		{ID: "A", OriginX: 10, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 10},
		{ID: "C", OriginX: 120, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 10},
	})
	g := uniformGrid(80, 12, 2, 12, 1e18, 4)
	// Large cutoff -> small influence radius (~36 m for 1000 kN) so the two
	// footprints' influence zones are disjoint.
	disc := discretize.Params{MaxCell: 3, Cutoff: 1e-2}
	eng := newEngine(t, y, g, disc, store.NewMemory(), Params{TopK: 5})

	var hooked atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	eng.HookPreCommit = func() {
		if hooked.CompareAndSwap(0, 1) {
			close(entered)
			<-release
		}
	}

	firstDone := make(chan Outcome, 1)
	go func() {
		out, _ := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 1000, Tiers: 1})
		firstDone <- out
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first job never reached the critical section")
	}

	secondDone := make(chan Outcome, 1)
	go func() {
		out, _ := eng.Submit(JobRequest{Type: events.Place, StackID: "C-B01-R01", Weight: 1000, Tiers: 1})
		secondDone <- out
	}()
	select {
	case out := <-secondDone:
		if out.Rejected != nil || len(out.FieldErrors) > 0 {
			t.Fatalf("second job not accepted: %+v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second job blocked behind the first: non-adjacent blocks must not wait")
	}
	close(release)
	out := <-firstDone
	if out.Rejected != nil || len(out.FieldErrors) > 0 {
		t.Fatalf("first job not accepted: %+v", out)
	}
	if eng.Seq() != 2 {
		t.Fatalf("seq = %d, want 2", eng.Seq())
	}
}

// Heavy concurrent load on overlapping influence zones: the live grid must
// still equal the replay of the committed log, bit for bit.
func TestConcurrentReplayConsistency(t *testing.T) {
	y, _ := yard.New([]yard.BlockCfg{
		{ID: "A", OriginX: 10, OriginY: 8, Bays: 2, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 1 << 28},
		{ID: "B", OriginX: 35, OriginY: 8, Bays: 2, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 1 << 28},
	})
	g := uniformGrid(40, 12, 2, 12, 1e18, 4)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})

	ids := []string{"A-B01-R01", "A-B02-R01", "B-B01-R01", "B-B02-R01"}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 50; i++ {
				job := JobRequest{
					Type:    events.Place,
					StackID: ids[rng.Intn(len(ids))],
					Weight:  100 + rng.Float64()*400,
					Tiers:   1,
				}
				if _, err := eng.Submit(job); err != nil {
					t.Error(err)
				}
			}
		}(int64(worker) + 1)
	}
	wg.Wait()

	live := eng.Current().Values
	replayed := replayAll(eng)
	if d := maxDiff(live, replayed); d != 0 {
		t.Fatalf("live vs replay max diff = %g, want 0", d)
	}
}

// ---------------------------------------------------------------------------
// incremental vs full replay, 100k jobs
// ---------------------------------------------------------------------------

// The grid is maintained incrementally. This test measures the deviation
// between the incrementally maintained grid and a full replay after 100 000
// jobs, and additionally the deviation of a replay in shuffled order (an
// upper bound for summation-order sensitivity).
func TestIncrementalVsReplay100k(t *testing.T) {
	y, _ := yard.New([]yard.BlockCfg{
		{ID: "A", OriginX: 2, OriginY: 2, Bays: 4, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 1 << 28},
		{ID: "B", OriginX: 2, OriginY: 12, Bays: 4, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 1 << 28},
	})
	g := uniformGrid(15, 9, 4, 12, 1e18, 4)
	disc := discretize.Params{MaxCell: 3, Cutoff: 1e-3}
	st := store.NewMemory()
	eng := newEngine(t, y, g, disc, st, Params{TopK: 5, SnapshotInterval: 1000})

	ids := make([]string, 0, 8)
	for _, b := range []string{"A", "B"} {
		for bay := 1; bay <= 4; bay++ {
			ids = append(ids, yard.StackID(b, bay, 1))
		}
	}
	const N = 100_000
	rng := rand.New(rand.NewSource(42))
	placed := make(map[string][]float64) // per-stack placed weights available to pick
	for i := 0; i < N; i++ {
		id := ids[rng.Intn(len(ids))]
		if rng.Float64() < 0.55 || len(placed[id]) == 0 {
			w := 50 + rng.Float64()*350
			out, err := eng.Submit(JobRequest{Type: events.Place, StackID: id, Weight: w, Tiers: 1})
			if err != nil || out.Rejected != nil {
				t.Fatalf("job %d failed: %+v %v", i, out, err)
			}
			placed[id] = append(placed[id], w)
		} else {
			pile := placed[id]
			w := pile[len(pile)-1]
			placed[id] = pile[:len(pile)-1]
			out, err := eng.Submit(JobRequest{Type: events.Pick, StackID: id, Weight: w, Tiers: 1})
			if err != nil || out.Rejected != nil {
				t.Fatalf("job %d failed: %+v %v", i, out, err)
			}
		}
	}
	if eng.Seq() != N {
		t.Fatalf("seq = %d, want %d", eng.Seq(), N)
	}

	live := eng.Current().Values
	replayed := replayAll(eng)
	d := maxDiff(live, replayed)
	t.Logf("incremental vs replay after %d jobs: max diff = %g kPa", N, d)
	if d > 1e-6 {
		t.Fatalf("incremental grid deviates from replay by %g kPa (> 1e-6)", d)
	}

	// Shuffled replay: same events, random order — an upper bound on
	// summation-order sensitivity of the float64 accumulation.
	evts, _ := st.List(0, math.MaxInt64)
	shuffled := append([]events.Event(nil), evts...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	values := make([]float64, g.Len())
	stacks := make(map[string]events.StackState, len(ids))
	eng.replayOnto(values, stacks, shuffled)
	dShuf := maxDiff(live, values)
	t.Logf("shuffled replay after %d jobs: max diff = %g kPa", N, dShuf)
	if dShuf > 1e-6 {
		t.Fatalf("shuffled replay deviates by %g kPa (> 1e-6)", dShuf)
	}

	// Snapshots taken along the way must equal the replay at their seq.
	snap, ok, err := st.LatestSnapshot()
	if err != nil || !ok {
		t.Fatalf("no snapshot stored: %v", err)
	}
	values = make([]float64, g.Len())
	stacks = make(map[string]events.StackState, len(ids))
	upto, _ := st.List(0, snap.Seq)
	eng.replayOnto(values, stacks, upto)
	if d := maxDiff(snap.Values, values); d != 0 {
		t.Fatalf("snapshot at seq %d deviates from replay by %g", snap.Seq, d)
	}
}

// ---------------------------------------------------------------------------
// historical queries
// ---------------------------------------------------------------------------

func TestGridAtEventSeq(t *testing.T) {
	y, g := adjacentSetup(1e18)
	st := store.NewMemory()
	eng := newEngine(t, y, g, testDisc, st, Params{TopK: 5, SnapshotInterval: 3})

	weights := []float64{100, 250, 80, 400, 150, 320, 90, 210, 500, 60}
	for i, w := range weights {
		id := "A-B01-R01"
		if i%2 == 1 {
			id = "B-B01-R01"
		}
		if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: id, Weight: w, Tiers: 1}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := st.List(0, math.MaxInt64)
	for k := int64(0); k <= int64(len(weights)); k++ {
		got, err := eng.GridAt(k)
		if err != nil {
			t.Fatalf("GridAt(%d): %v", k, err)
		}
		// Expected: replay of the first k events from the empty state.
		want := make([]float64, g.Len())
		stacks := make(map[string]events.StackState)
		eng.replayOnto(want, stacks, all[:k])
		if d := maxDiff(got.Values, want); d != 0 {
			t.Fatalf("GridAt(%d) deviates from replay: %g", k, d)
		}
	}
	if _, err := eng.GridAt(int64(len(weights)) + 1); err != ErrSeqOutOfRange {
		t.Fatalf("expected ErrSeqOutOfRange, got %v", err)
	}
	// Current must equal GridAt at the latest seq.
	cur := eng.Current()
	atCur, _ := eng.GridAt(cur.Seq)
	if d := maxDiff(cur.Values, atCur.Values); d != 0 {
		t.Fatalf("current vs GridAt(%d): %g", cur.Seq, d)
	}
}

// Recovery: a fresh engine over the same store must rebuild the identical
// state from snapshot + tail replay.
func TestRecoveryFromStore(t *testing.T) {
	y, g := adjacentSetup(1e18)
	st := store.NewMemory()
	eng := newEngine(t, y, g, testDisc, st, Params{TopK: 5, SnapshotInterval: 2})
	for i := 0; i < 7; i++ {
		if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 100 + float64(i)*37, Tiers: 1}); err != nil {
			t.Fatal(err)
		}
	}
	live := eng.Current()

	// New engine over the same store (simulating a restart).
	y2, g2 := adjacentSetup(1e18)
	eng2 := newEngine(t, y2, g2, testDisc, st, Params{TopK: 5, SnapshotInterval: 2})
	rec := eng2.Current()
	if rec.Seq != live.Seq {
		t.Fatalf("recovered seq %d, want %d", rec.Seq, live.Seq)
	}
	if d := maxDiff(rec.Values, live.Values); d != 0 {
		t.Fatalf("recovered grid deviates: %g", d)
	}
	stacks := eng2.Stacks()
	var a StackView
	for _, s := range stacks {
		if s.ID == "A-B01-R01" {
			a = s
		}
	}
	if a.Tiers != 7 {
		t.Fatalf("recovered stack tiers %d, want 7", a.Tiers)
	}
}

// ---------------------------------------------------------------------------
// weight corrections
// ---------------------------------------------------------------------------

func TestCorrectionImpactList(t *testing.T) {
	const W1, W2, W1c = 3000.0, 2500.0, 4500.0

	// Calibrate allowable: both original jobs pass; with W1 corrected to
	// W1c, the second job would have failed.
	calib := func(w1, w2 float64) float64 {
		y, g := adjacentSetup(1e18)
		eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
		if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: w1, Tiers: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "B-B01-R01", Weight: w2, Tiers: 1}); err != nil {
			t.Fatal(err)
		}
		return maxAbs(eng.Current().Values)
	}
	orig := calib(W1, W2)
	corrected := calib(W1c, W2)
	if !(corrected > orig) {
		t.Fatalf("calibration failed: orig %v corrected %v", orig, corrected)
	}
	allowable := (orig + corrected) / 2

	y, g := adjacentSetup(allowable)
	st := store.NewMemory()
	eng := newEngine(t, y, g, testDisc, st, Params{TopK: 5, SnapshotInterval: 2})

	out1, _ := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: W1, Tiers: 1})
	out2, _ := eng.Submit(JobRequest{Type: events.Place, StackID: "B-B01-R01", Weight: W2, Tiers: 1})
	if out1.Rejected != nil || out2.Rejected != nil {
		t.Fatalf("setup jobs rejected: %+v %+v", out1.Rejected, out2.Rejected)
	}
	gridBefore := eng.Current()

	res, err := eng.Correct(out1.Seq, W1c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.FieldErrors) > 0 {
		t.Fatalf("correction rejected: %v", res.FieldErrors)
	}
	if res.Seq != 3 {
		t.Fatalf("correction seq = %d, want 3", res.Seq)
	}

	// The impact list must name job 2.
	if len(res.Impacted) != 1 || res.Impacted[0].Seq != out2.Seq {
		t.Fatalf("impacted = %+v, want exactly job %d", res.Impacted, out2.Seq)
	}
	if res.Impacted[0].Count == 0 || len(res.Impacted[0].Exceedances) == 0 {
		t.Fatal("impacted job must carry exceedance detail")
	}
	// After the correction the live grid actually exceeds the allowable.
	if len(res.CurrentExceedances) == 0 {
		t.Fatal("expected current exceedances after correction")
	}

	// History is not rewritten: the log still shows the original weight,
	// and the historical grid at seq 2 is unchanged.
	evts, _ := st.List(0, math.MaxInt64)
	if len(evts) != 3 || evts[0].Weight != W1 {
		t.Fatalf("history rewritten: %+v", evts)
	}
	if evts[2].Type != events.Correction || evts[2].TargetSeq != out1.Seq || evts[2].NewWeight != W1c {
		t.Fatalf("correction event malformed: %+v", evts[2])
	}
	at2, err := eng.GridAt(out2.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if d := maxDiff(at2.Values, gridBefore.Values); d != 0 {
		t.Fatalf("historical grid at seq %d changed by %g", out2.Seq, d)
	}
	// The live grid equals the replay including the correction event.
	if d := maxDiff(eng.Current().Values, replayAll(eng)); d != 0 {
		t.Fatalf("live vs replay after correction: %g", d)
	}
}

func TestCorrectionValidation(t *testing.T) {
	y, g := adjacentSetup(1e18)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
	out, _ := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 500, Tiers: 1})

	if res, _ := eng.Correct(out.Seq, 0); !res.FieldErrors.Has("new_weight") {
		t.Fatalf("expected new_weight error, got %v", res.FieldErrors)
	}
	if res, _ := eng.Correct(999, 100); !res.FieldErrors.Has("event_id") {
		t.Fatalf("expected event_id error, got %v", res.FieldErrors)
	}
	// Correcting a correction event is not allowed.
	res, err := eng.Correct(out.Seq, 700)
	if err != nil || len(res.FieldErrors) > 0 {
		t.Fatalf("correction failed: %v %v", res.FieldErrors, err)
	}
	if res2, _ := eng.Correct(res.Seq, 800); !res2.FieldErrors.Has("event_id") {
		t.Fatalf("expected event_id error for correcting a correction, got %v", res2.FieldErrors)
	}
	// Two sequential corrections fold: effective weight is the latest.
	if _, err := eng.Correct(out.Seq, 900); err != nil {
		t.Fatal(err)
	}
	all, _ := eng.Events(0, math.MaxInt64)
	if w, _ := events.EffectiveWeight(all, out.Seq); w != 900 {
		t.Fatalf("effective weight = %v, want 900", w)
	}
	if d := maxDiff(eng.Current().Values, replayAll(eng)); d != 0 {
		t.Fatalf("live vs replay after corrections: %g", d)
	}
}

// A correction on a RESTOW moves the weight delta on both stacks.
func TestCorrectionOnRestow(t *testing.T) {
	y, g := adjacentSetup(1e18)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
	if _, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 1000, Tiers: 1}); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Submit(JobRequest{Type: events.Restow, FromID: "A-B01-R01", ToID: "B-B01-R01", Weight: 600, Tiers: 1})
	if err != nil || out.Rejected != nil {
		t.Fatalf("restow failed: %+v %v", out, err)
	}
	res, err := eng.Correct(out.Seq, 900) // restowed 900, not 600
	if err != nil || len(res.FieldErrors) > 0 {
		t.Fatalf("correction failed: %v %v", res.FieldErrors, err)
	}
	stacks := eng.Stacks()
	var a, b StackView
	for _, s := range stacks {
		if s.ID == "A-B01-R01" {
			a = s
		}
		if s.ID == "B-B01-R01" {
			b = s
		}
	}
	if a.Weight != 100 || b.Weight != 900 {
		t.Fatalf("stack weights after correction: A=%v B=%v", a.Weight, b.Weight)
	}
	if d := maxDiff(eng.Current().Values, replayAll(eng)); d != 0 {
		t.Fatalf("live vs replay: %g", d)
	}
}

// ---------------------------------------------------------------------------
// event log
// ---------------------------------------------------------------------------

func TestEventNumberingAndListing(t *testing.T) {
	y, g := adjacentSetup(1e18)
	eng := newEngine(t, y, g, testDisc, store.NewMemory(), Params{TopK: 5})
	for i := 0; i < 5; i++ {
		out, err := eng.Submit(JobRequest{Type: events.Place, StackID: "A-B01-R01", Weight: 50, Tiers: 1})
		if err != nil || out.Rejected != nil {
			t.Fatalf("job %d failed", i)
		}
		if out.Seq != int64(i+1) {
			t.Fatalf("job %d got seq %d", i, out.Seq)
		}
	}
	evts, err := eng.Events(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 3 || evts[0].Seq != 2 || evts[2].Seq != 4 {
		t.Fatalf("Events(2,4) = %+v", evts)
	}
	if !sort.SliceIsSorted(evts, func(i, j int) bool { return evts[i].Seq < evts[j].Seq }) {
		t.Fatal("events not ordered by seq")
	}
}
