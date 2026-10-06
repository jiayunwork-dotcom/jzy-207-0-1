package store

import (
	"context"
	"os"
	"testing"
	"time"

	"yardstress/internal/events"
)

// Integration test against a real PostgreSQL 16. Skipped unless DATABASE_URL
// is set (docker compose exec app go test ./... -run Postgres, or a local
// postgres).
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Clean slate for a deterministic run.
	if _, err := p.pool.Exec(ctx, "TRUNCATE events, snapshots"); err != nil {
		t.Fatal(err)
	}

	for i := int64(1); i <= 5; i++ {
		if err := p.AppendEvent(events.Event{Seq: i, Type: events.Place, StackID: "A-B01-R01", Weight: float64(i) * 100, Tiers: 1, Time: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	evts, err := p.List(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 3 || evts[0].Seq != 2 || evts[2].Weight != 400 {
		t.Fatalf("List(2,4) = %+v", evts)
	}

	if err := p.SaveSnapshot(Snapshot{Seq: 3, Values: []float64{1.5, 2.5}, Stacks: map[string]events.StackState{"A-B01-R01": {Tiers: 3, Weight: 600}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSnapshot(Snapshot{Seq: 5, Values: []float64{3.5}, Stacks: map[string]events.StackState{}}); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := p.LatestSnapshot()
	if err != nil || !ok || snap.Seq != 5 || snap.Values[0] != 3.5 {
		t.Fatalf("LatestSnapshot = %+v %v %v", snap, ok, err)
	}
	snap, ok, err = p.LatestSnapshotBefore(4)
	if err != nil || !ok || snap.Seq != 3 || snap.Values[1] != 2.5 {
		t.Fatalf("LatestSnapshotBefore(4) = %+v %v %v", snap, ok, err)
	}
	if snap.Stacks["A-B01-R01"].Weight != 600 {
		t.Fatalf("snapshot stacks = %+v", snap.Stacks)
	}
	if _, ok, _ := p.LatestSnapshotBefore(2); ok {
		t.Fatal("expected no snapshot before seq 2")
	}
}
