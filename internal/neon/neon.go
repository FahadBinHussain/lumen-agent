package neon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) Close() {
	if db == nil || db.pool == nil {
		return
	}
	db.pool.Close()
}

func (db *DB) migrate(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.whatsapp_sessions (
			id           TEXT PRIMARY KEY DEFAULT 'default',
			session_data BYTEA NOT NULL,
			wacli_data   BYTEA,
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.pending_notifications (
			id           BIGSERIAL PRIMARY KEY,
			platform     TEXT NOT NULL,
			thread_id    TEXT NOT NULL,
			route        TEXT NOT NULL DEFAULT '',
			title        TEXT NOT NULL DEFAULT '',
			message      TEXT NOT NULL,
			dedupe_key   TEXT NOT NULL DEFAULT '',
			source       TEXT NOT NULL DEFAULT '',
			url          TEXT NOT NULL DEFAULT '',
			attempts     INT NOT NULL DEFAULT 0,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_attempt TIMESTAMPTZ
		)
	`)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS pending_notifications_dedupe_idx ON public.pending_notifications(dedupe_key) WHERE dedupe_key <> ''`)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS pending_notifications_platform_idx ON public.pending_notifications(platform, thread_id)`)
	return err
}

type WhatsAppSession struct {
	SessionData []byte
	WacliData   []byte
	UpdatedAt   string
}

func (db *DB) SaveWhatsAppSession(ctx context.Context, sessionData, wacliData []byte) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO public.whatsapp_sessions (id, session_data, wacli_data, updated_at)
		VALUES ('default', $1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET session_data = $1, wacli_data = $2, updated_at = NOW()
	`, sessionData, wacliData)
	return err
}

func (db *DB) LoadWhatsAppSession(ctx context.Context) (*WhatsAppSession, error) {
	var s WhatsAppSession
	err := db.pool.QueryRow(ctx,
		`SELECT session_data, wacli_data, updated_at::text FROM public.whatsapp_sessions WHERE id = 'default'`,
	).Scan(&s.SessionData, &s.WacliData, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

type PendingNotification struct {
	ID        int64  `json:"id"`
	Platform  string `json:"platform"`
	ThreadID  string `json:"threadId"`
	Route     string `json:"route"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	DedupeKey string `json:"dedupeKey"`
	Source    string `json:"source"`
	URL       string `json:"url"`
	Attempts  int    `json:"attempts"`
}

func (db *DB) SavePending(ctx context.Context, p PendingNotification) (int64, error) {
	if p.DedupeKey != "" {
		var existing int64
		err := db.pool.QueryRow(ctx, `SELECT id FROM public.pending_notifications WHERE dedupe_key = $1 LIMIT 1`, p.DedupeKey).Scan(&existing)
		if err == nil {
			return existing, nil
		}
	}
	var id int64
	err := db.pool.QueryRow(ctx, `
		INSERT INTO public.pending_notifications (platform, thread_id, route, title, message, dedupe_key, source, url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id
	`, p.Platform, p.ThreadID, p.Route, p.Title, p.Message, p.DedupeKey, p.Source, p.URL).Scan(&id)
	return id, err
}

func (db *DB) ListPending(ctx context.Context) ([]PendingNotification, error) {
	rows, err := db.pool.Query(ctx, `SELECT id, platform, thread_id, route, title, message, dedupe_key, source, url, attempts FROM public.pending_notifications ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingNotification
	for rows.Next() {
		var p PendingNotification
		if err := rows.Scan(&p.ID, &p.Platform, &p.ThreadID, &p.Route, &p.Title, &p.Message, &p.DedupeKey, &p.Source, &p.URL, &p.Attempts); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) DeletePending(ctx context.Context, id int64) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM public.pending_notifications WHERE id = $1`, id)
	return err
}

func (db *DB) IncPendingAttempts(ctx context.Context, id int64) error {
	_, err := db.pool.Exec(ctx, `UPDATE public.pending_notifications SET attempts = attempts + 1, last_attempt = NOW() WHERE id = $1`, id)
	return err
}