package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yardstress/internal/events"
)

// Postgres is the production Store backed by PostgreSQL 16.
//
// Schema (created by Migrate):
//
//	events(seq BIGINT PRIMARY KEY, payload JSONB, created_at TIMESTAMPTZ)
//	snapshots(seq BIGINT PRIMARY KEY, values BYTEA, stacks JSONB, created_at TIMESTAMPTZ)
//
// Events are append-only: nothing ever updates or deletes a row, which is
// what "corrections do not rewrite history" means at the storage level.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to the database at dsn and verifies the connection.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Migrate creates the schema if it does not exist yet.
func (p *Postgres) Migrate(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS events (
    seq        BIGINT PRIMARY KEY,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS snapshots (
    seq        BIGINT PRIMARY KEY,
    values     BYTEA NOT NULL,
    stacks     JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	return err
}

// AppendEvent implements Store.
func (p *Postgres) AppendEvent(e events.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(),
		`INSERT INTO events (seq, payload) VALUES ($1, $2)`, e.Seq, payload)
	return err
}

// List implements Store.
func (p *Postgres) List(from, to int64) ([]events.Event, error) {
	rows, err := p.pool.Query(context.Background(),
		`SELECT payload FROM events WHERE seq >= $1 AND seq <= $2 ORDER BY seq`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var e events.Event
		if err := json.Unmarshal(payload, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestSnapshot implements Store.
func (p *Postgres) LatestSnapshot() (Snapshot, bool, error) {
	return p.snapshotWhere(`ORDER BY seq DESC LIMIT 1`)
}

// LatestSnapshotBefore implements Store.
func (p *Postgres) LatestSnapshotBefore(maxSeq int64) (Snapshot, bool, error) {
	return p.snapshotWhere(`WHERE seq <= $1 ORDER BY seq DESC LIMIT 1`, maxSeq)
}

func (p *Postgres) snapshotWhere(clause string, args ...any) (Snapshot, bool, error) {
	row := p.pool.QueryRow(context.Background(),
		`SELECT seq, values, stacks FROM snapshots `+clause, args...)
	var (
		s        Snapshot
		rawVals  []byte
		rawStack []byte
	)
	if err := row.Scan(&s.Seq, &rawVals, &rawStack); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, err
	}
	s.Values = decodeFloat64s(rawVals)
	if err := json.Unmarshal(rawStack, &s.Stacks); err != nil {
		return Snapshot{}, false, err
	}
	return s, true, nil
}

// SaveSnapshot implements Store.
func (p *Postgres) SaveSnapshot(s Snapshot) error {
	stacks, err := json.Marshal(s.Stacks)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(),
		`INSERT INTO snapshots (seq, values, stacks) VALUES ($1, $2, $3)
		 ON CONFLICT (seq) DO NOTHING`,
		s.Seq, encodeFloat64s(s.Values), stacks)
	return err
}

// Close implements Store.
func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

func encodeFloat64s(v []float64) []byte {
	buf := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint64(buf[8*i:], math.Float64bits(x))
	}
	return buf
}

func decodeFloat64s(buf []byte) []float64 {
	out := make([]float64, len(buf)/8)
	for i := range out {
		out[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf[8*i:]))
	}
	return out
}
