// Package store defines the persistence interface for events and snapshots
// and provides the in-memory implementation used in tests. The PostgreSQL
// implementation lives in postgres.go.
package store

import (
	"sort"
	"sync"

	"yardstress/internal/events"
)

// Snapshot captures the full engine state after a given event sequence
// number: the checkpoint grid values and the per-stack bookkeeping.
// Snapshots make historical queries and crash recovery cheap; they are a
// performance device only, never a source of truth — the event log is.
type Snapshot struct {
	Seq    int64
	Values []float64
	Stacks map[string]events.StackState
}

// Store persists events and snapshots.
type Store interface {
	// AppendEvent persists an event. The event's Seq is assigned by the
	// engine before the call; AppendEvent must be atomic with respect to
	// other appends.
	AppendEvent(e events.Event) error
	// List returns the persisted events with from <= seq <= to, ordered by
	// sequence number.
	List(from, to int64) ([]events.Event, error)
	// LatestSnapshot returns the snapshot with the highest seq.
	LatestSnapshot() (Snapshot, bool, error)
	// LatestSnapshotBefore returns the snapshot with the highest seq <= maxSeq.
	LatestSnapshotBefore(maxSeq int64) (Snapshot, bool, error)
	// SaveSnapshot persists a snapshot.
	SaveSnapshot(s Snapshot) error
	// Close releases resources.
	Close() error
}

// Memory is an in-memory Store for tests and single-process runs.
type Memory struct {
	mu     sync.Mutex
	events []events.Event
	snaps  []Snapshot // kept sorted by Seq
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{} }

// AppendEvent implements Store.
func (m *Memory) AppendEvent(e events.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

// List implements Store.
func (m *Memory) List(from, to int64) ([]events.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []events.Event
	for _, e := range m.events {
		if e.Seq >= from && e.Seq <= to {
			out = append(out, e)
		}
	}
	return out, nil
}

// LatestSnapshot implements Store.
func (m *Memory) LatestSnapshot() (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.snaps) == 0 {
		return Snapshot{}, false, nil
	}
	return m.snaps[len(m.snaps)-1], true, nil
}

// LatestSnapshotBefore implements Store.
func (m *Memory) LatestSnapshotBefore(maxSeq int64) (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := sort.Search(len(m.snaps), func(i int) bool { return m.snaps[i].Seq > maxSeq })
	if i == 0 {
		return Snapshot{}, false, nil
	}
	return m.snaps[i-1], true, nil
}

// SaveSnapshot implements Store.
func (m *Memory) SaveSnapshot(s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := sort.Search(len(m.snaps), func(i int) bool { return m.snaps[i].Seq >= s.Seq })
	if i < len(m.snaps) && m.snaps[i].Seq == s.Seq {
		m.snaps[i] = s
		return nil
	}
	m.snaps = append(m.snaps, Snapshot{})
	copy(m.snaps[i+1:], m.snaps[i:])
	m.snaps[i] = s
	return nil
}

// Close implements Store.
func (m *Memory) Close() error { return nil }
