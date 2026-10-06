// Package events persists the admitted event log and state snapshots
// (PostgreSQL in production, in-memory for tests) and wires stored events
// into the admission package's replay machinery.
package events

import (
	"sort"
	"sync"

	"yardgate/internal/admission"
)

// MemStore is an in-memory admission.EventStore for tests and embedded use.
type MemStore struct {
	mu     sync.RWMutex
	events []admission.Event
	snaps  []admission.Snapshot
}

// NewMemStore creates an empty store.
func NewMemStore() *MemStore { return &MemStore{} }

// SaveEvent appends an event.
func (m *MemStore) SaveEvent(ev admission.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}

// SaveSnapshot records a snapshot (replacing one with the same seq).
func (m *MemStore) SaveSnapshot(s admission.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s2 := range m.snaps {
		if s2.Seq == s.Seq {
			m.snaps[i] = s
			return nil
		}
	}
	m.snaps = append(m.snaps, s)
	sort.Slice(m.snaps, func(i, j int) bool { return m.snaps[i].Seq < m.snaps[j].Seq })
	return nil
}

// LatestSnapshot returns the newest snapshot.
func (m *MemStore) LatestSnapshot() (admission.Snapshot, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.snaps) == 0 {
		return admission.Snapshot{}, false, nil
	}
	return m.snaps[len(m.snaps)-1], true, nil
}

// SnapshotAtOrBefore returns the newest snapshot with seq <= the given one.
func (m *MemStore) SnapshotAtOrBefore(seq int64) (admission.Snapshot, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	best := -1
	for i, s := range m.snaps {
		if s.Seq <= seq {
			best = i
		}
	}
	if best < 0 {
		return admission.Snapshot{}, false, nil
	}
	return m.snaps[best], true, nil
}

// EventsInRange returns events with fromExclusive < seq <= toInclusive.
func (m *MemStore) EventsInRange(fromExclusive, toInclusive int64) ([]admission.Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []admission.Event
	for _, ev := range m.events {
		if ev.Seq > fromExclusive && ev.Seq <= toInclusive {
			out = append(out, ev)
		}
	}
	return out, nil
}

// MaxSeq returns the highest stored event sequence (0 when empty).
func (m *MemStore) MaxSeq() (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.events) == 0 {
		return 0, nil
	}
	return m.events[len(m.events)-1].Seq, nil
}
