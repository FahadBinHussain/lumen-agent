package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dedupe DB — faithful port of the Vercel pollers' Neon tables
// (murmur/vercel/db/schema.ts): steam_seen(gid) + game_seen(guid), created on
// demand so the tables appear on whichever Neon project DATABASE_URL points at
// (same behavior as drizzle's migrate on first deploy).
type dedupeDB struct {
	pool *pgxpool.Pool
}

func newDedupeDB(ctx context.Context, dsn string) (*dedupeDB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	db := &dedupeDB{pool: pool}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS steam_seen (
			gid      TEXT PRIMARY KEY,
			game_name TEXT NOT NULL,
			title    TEXT NOT NULL,
			seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS game_seen (
			guid     TEXT PRIMARY KEY,
			title    TEXT NOT NULL,
			seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS crack_seen (
			guid     TEXT PRIMARY KEY,
			title    TEXT NOT NULL,
			seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return db, nil
}

func (d *dedupeDB) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
	}
}

type dbResult struct {
	seen map[string]bool
}

// dbQuery returns the set of already-seen ids for the table's id column
// (inArray semantics of the TS poller).
func (s *Service) dbQuery(ctx context.Context, table, idCol string, idsFn func() []string) (*dbResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("no dedupe db")
	}
	ids := idsFn()
	if len(ids) == 0 {
		return &dbResult{seen: map[string]bool{}}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
		idCol, table, idCol, strings.Join(placeholders, ","))
	rows, err := s.db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var v string
	for rows.Next() {
		if err := rows.Scan(&v); err == nil {
			seen[v] = true
		}
	}
	return &dbResult{seen: seen}, rows.Err()
}

// dbInsertSeen mirrors the TS onConflictDoNothing insert.
func (s *Service) dbInsertSeen(ctx context.Context, table, idCol string, cols map[string]string) error {
	if s.db == nil {
		return fmt.Errorf("no dedupe db")
	}
	names := make([]string, 0, len(cols))
	vals := make([]any, 0, len(cols))
	placeholders := make([]string, 0, len(cols))
	i := 1
	for k, v := range cols {
		names = append(names, k)
		vals = append(vals, v)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		i++
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING",
		table, strings.Join(names, ","), strings.Join(placeholders, ","), idCol)
	_, err := s.db.pool.Exec(ctx, query, vals...)
	return err
}