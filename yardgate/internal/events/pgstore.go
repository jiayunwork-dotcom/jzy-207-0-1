package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yardgate/internal/admission"
)

// PGStore is a PostgreSQL-backed admission.EventStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// schema is created idempotently at startup.
const schema = `
CREATE TABLE IF NOT EXISTS events (
    seq        BIGINT PRIMARY KEY,
    type       TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS snapshots (
    seq        BIGINT PRIMARY KEY,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// NewPGStore connects to PostgreSQL and ensures the schema exists.
func NewPGStore(ctx context.Context, url string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

// Close releases the connection pool.
func (s *PGStore) Close() { s.pool.Close() }

// SaveEvent inserts an event row.
func (s *PGStore) SaveEvent(ev admission.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO events (seq, type, payload, created_at) VALUES ($1, $2, $3, $4)`,
		ev.Seq, string(ev.Type), payload, ev.CreatedAt)
	return err
}

// SaveSnapshot upserts a snapshot row.
func (s *PGStore) SaveSnapshot(snap admission.Snapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO snapshots (seq, payload) VALUES ($1, $2)
		 ON CONFLICT (seq) DO UPDATE SET payload = EXCLUDED.payload`,
		snap.Seq, payload)
	return err
}

// LatestSnapshot returns the snapshot with the highest seq.
func (s *PGStore) LatestSnapshot() (admission.Snapshot, bool, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT payload FROM snapshots ORDER BY seq DESC LIMIT 1`)
	return scanSnapshot(row)
}

// SnapshotAtOrBefore returns the newest snapshot with seq <= the given one.
func (s *PGStore) SnapshotAtOrBefore(seq int64) (admission.Snapshot, bool, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT payload FROM snapshots WHERE seq <= $1 ORDER BY seq DESC LIMIT 1`, seq)
	return scanSnapshot(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(row rowScanner) (admission.Snapshot, bool, error) {
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return admission.Snapshot{}, false, nil
		}
		return admission.Snapshot{}, false, err
	}
	var snap admission.Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return admission.Snapshot{}, false, err
	}
	return snap, true, nil
}

// EventsInRange returns events with fromExclusive < seq <= toInclusive in
// sequence order.
func (s *PGStore) EventsInRange(fromExclusive, toInclusive int64) ([]admission.Event, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT payload FROM events WHERE seq > $1 AND seq <= $2 ORDER BY seq`, fromExclusive, toInclusive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []admission.Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var ev admission.Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// MaxSeq returns the highest stored event sequence (0 when empty).
func (s *PGStore) MaxSeq() (int64, error) {
	var seq *int64
	err := s.pool.QueryRow(context.Background(), `SELECT max(seq) FROM events`).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if seq == nil {
		return 0, nil
	}
	return *seq, nil
}

// Ping checks database liveness.
func (s *PGStore) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(ctx)
}
