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
		CREATE TABLE IF NOT EXISTS whatsapp_sessions (
			id           TEXT PRIMARY KEY DEFAULT 'default',
			session_data BYTEA NOT NULL,
			wacli_data   BYTEA,
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

type WhatsAppSession struct {
	SessionData []byte
	WacliData   []byte
	UpdatedAt   string
}

func (db *DB) SaveWhatsAppSession(ctx context.Context, sessionData, wacliData []byte) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO whatsapp_sessions (id, session_data, wacli_data, updated_at)
		VALUES ('default', $1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET session_data = $1, wacli_data = $2, updated_at = NOW()
	`, sessionData, wacliData)
	return err
}

func (db *DB) LoadWhatsAppSession(ctx context.Context) (*WhatsAppSession, error) {
	var s WhatsAppSession
	err := db.pool.QueryRow(ctx,
		`SELECT session_data, wacli_data, updated_at::text FROM whatsapp_sessions WHERE id = 'default'`,
	).Scan(&s.SessionData, &s.WacliData, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}