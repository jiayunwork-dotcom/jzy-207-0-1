package events

import (
	"testing"

	"yardgate/internal/admission"
)

func TestMemStoreOrdering(t *testing.T) {
	m := NewMemStore()
	for i := int64(1); i <= 5; i++ {
		if err := m.SaveEvent(admission.Event{Seq: i, Type: admission.EventPlace}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SaveSnapshot(admission.Snapshot{Seq: 3}); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSnapshot(admission.Snapshot{Seq: 1}); err != nil {
		t.Fatal(err)
	}

	seq, err := m.MaxSeq()
	if err != nil || seq != 5 {
		t.Fatalf("MaxSeq = %d, %v", seq, err)
	}
	latest, ok, err := m.LatestSnapshot()
	if err != nil || !ok || latest.Seq != 3 {
		t.Fatalf("LatestSnapshot = %+v, %v, %v", latest, ok, err)
	}
	at, ok, err := m.SnapshotAtOrBefore(2)
	if err != nil || !ok || at.Seq != 1 {
		t.Fatalf("SnapshotAtOrBefore(2) = %+v, %v", at, ok)
	}
	if _, ok, _ := m.SnapshotAtOrBefore(0); ok {
		t.Fatal("no snapshot expected at or before 0")
	}
	evs, err := m.EventsInRange(2, 4)
	if err != nil || len(evs) != 2 || evs[0].Seq != 3 || evs[1].Seq != 4 {
		t.Fatalf("EventsInRange = %+v, %v", evs, err)
	}
}
