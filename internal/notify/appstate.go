package notify

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// appStateDB is lumen's own Neon key/value table. the supabase watcher stores
// its per-account refresh tokens here (NOT in vaultwarden - vaultwarden items
// are encrypted and can't be decrypted by a machine without the master
// password; lumen owns its tokens in its own DB instead). rows are keyed
// `supabase.<ref>.refresh_token`, `supabase.<ref>.issuer`, etc. created on
// demand so the table appears on whichever Neon DB the config points at.
type appStateDB struct {
	pool *pgxpool.Pool
	table string
}

func newAppStateDB(ctx context.Context, dsn, table string) (*appStateDB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	db := &appStateDB{pool: pool, table: table}
	stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`, quoteIdent(table))
	if _, err := pool.Exec(ctx, stmt); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

func (d *appStateDB) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
	}
}

// Get returns the value for a key ("" when absent).
func (d *appStateDB) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := d.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT value FROM %s WHERE key = $1`, quoteIdent(d.table)),
		key).Scan(&v)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// Set upserts a key/value pair.
func (d *appStateDB) Set(ctx context.Context, key, value string) error {
	_, err := d.pool.Exec(ctx,
		fmt.Sprintf(`INSERT INTO %s (key, value, updated_at) VALUES ($1, $2, NOW())
			ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()`, quoteIdent(d.table)),
		key, value)
	return err
}

// Keys returns all keys matching a LIKE pattern (e.g. "supabase.%").
func (d *appStateDB) Keys(ctx context.Context, like string) ([]string, error) {
	rows, err := d.pool.Query(ctx,
		fmt.Sprintf(`SELECT key FROM %s WHERE key LIKE $1`, quoteIdent(d.table)),
		like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	var k string
	for rows.Next() {
		if err := rows.Scan(&k); err == nil {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

func quoteIdent(s string) string {
	return `"` + s + `"`
}
