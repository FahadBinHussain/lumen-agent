package persist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store snapshots a directory (typically the session dir) into a Neon
// Postgres table so the bot can restore its state on a fresh container
// (Render recycles the filesystem on every deploy/spin-down). It is a
// write-only-ish backup layer: the local dir stays authoritative while
// running, Restore only fills a missing/empty dir, and Sync only touches
// rows whose content actually changed.
type Store struct {
	pool    *pgxpool.Pool
	dir     string
	exclude []string
	touchCh chan struct{}
	minSync time.Duration
}

const minSyncInterval = 2 * time.Second

func Open(ctx context.Context, dsn string, dir string, exclude []string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("persist connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("persist ping: %w", err)
	}
	s := &Store{
		pool:    pool,
		dir:     dir,
		exclude: exclude,
		touchCh: make(chan struct{}, 1),
		minSync: minSyncInterval,
	}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("persist migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS lumen_snapshots (
			path       TEXT PRIMARY KEY,
			data       BYTEA NOT NULL,
			sha256     TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

// Restore fills the local dir from the snapshot table, but only when the dir
// is missing or contains no real files (i.e. a fresh container). Subdirectories
// created at boot (memory/, logs/) do not count as existing state, so restore
// still runs. Existing local files always win, and excluded paths are never
// written.
func (s *Store) Restore(ctx context.Context) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("persist restore read dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return nil
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT path, data FROM lumen_snapshots ORDER BY path`)
	if err != nil {
		return fmt.Errorf("persist restore query: %w", err)
	}
	defer rows.Close()
	restored := 0
	for rows.Next() {
		var rel string
		var data []byte
		if err := rows.Scan(&rel, &data); err != nil {
			return fmt.Errorf("persist restore scan: %w", err)
		}
		abs, ok := s.safePath(rel)
		if !ok {
			log.Printf("persist: skipping restore of out-of-root path %q", rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("persist restore mkdir %s: %w", rel, err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return fmt.Errorf("persist restore write %s: %w", rel, err)
		}
		restored++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("persist restore rows: %w", err)
	}
	if restored > 0 {
		log.Printf("persist: restored %d file(s) from snapshot", restored)
	}
	return nil
}

// Sync walks the local dir and upserts files whose content differs from the
// snapshot table, and deletes rows for files that no longer exist locally.
func (s *Store) Sync(ctx context.Context) error {
	live := map[string]string{} // rel path -> sha256
	if err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != s.dir && s.isExcluded(relOf(s.dir, path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel := relOf(s.dir, path)
		if s.isExcluded(rel) {
			return nil
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		live[rel] = sum
		return nil
	}); err != nil {
		return fmt.Errorf("persist sync walk: %w", err)
	}

	stored := map[string]string{}
	rows, err := s.pool.Query(ctx, `SELECT path, sha256 FROM lumen_snapshots`)
	if err != nil {
		return fmt.Errorf("persist sync query: %w", err)
	}
	for rows.Next() {
		var rel string
		var sum string
		if err := rows.Scan(&rel, &sum); err != nil {
			rows.Close()
			return fmt.Errorf("persist sync scan: %w", err)
		}
		stored[rel] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("persist sync rows: %w", err)
	}

	changed := 0
	for rel, sum := range live {
		if stored[rel] == sum {
			continue
		}
		abs, ok := s.safePath(rel)
		if !ok {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			log.Printf("persist: sync read %s: %v", rel, err)
			continue
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO lumen_snapshots (path, data, sha256, updated_at)
			VALUES ($1, $2, $3, NOW())
			ON CONFLICT (path) DO UPDATE SET data = $2, sha256 = $3, updated_at = NOW()
		`, rel, data, sum); err != nil {
			return fmt.Errorf("persist sync upsert %s: %w", rel, err)
		}
		changed++
	}

	stale := make([]string, 0)
	for rel := range stored {
		if _, ok := live[rel]; !ok {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		if _, err := s.pool.Exec(ctx, `DELETE FROM lumen_snapshots WHERE path = ANY($1)`, stale); err != nil {
			return fmt.Errorf("persist sync delete stale: %w", err)
		}
	}
	if changed > 0 || len(stale) > 0 {
		log.Printf("persist: synced %d changed, %d stale removed", changed, len(stale))
	}
	return nil
}

// Touch requests an immediate background sync (coalesced, non-blocking). Call
// it right after important local writes so a sudden container loss costs at
// most one write instead of a full interval.
func (s *Store) Touch() {
	if s == nil {
		return
	}
	select {
	case s.touchCh <- struct{}{}:
	default:
	}
}

// Run drives background syncs: a periodic catch-all plus immediate coalesced
// syncs on Touch. Blocks until ctx is done.
func (s *Store) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last time.Time
	sync := func() {
		if time.Since(last) < s.minSync {
			return
		}
		if err := s.Sync(ctx); err != nil {
			log.Printf("persist: sync: %v", err)
		}
		last = time.Now()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.touchCh:
			sync()
		case <-ticker.C:
			sync()
		}
	}
}

// SyncNow performs a best-effort synchronous sync (used at shutdown).
func (s *Store) SyncNow(ctx context.Context) {
	if s == nil {
		return
	}
	if err := s.Sync(ctx); err != nil {
		log.Printf("persist: shutdown sync: %v", err)
	}
}

func (s *Store) isExcluded(rel string) bool {
	for _, ex := range s.exclude {
		if strings.TrimSpace(ex) == "" {
			continue
		}
		rel = filepath.ToSlash(rel)
		ex = strings.TrimSuffix(strings.TrimSpace(filepath.ToSlash(ex)), "/")
		if rel == ex || strings.HasPrefix(rel, ex+"/") {
			return true
		}
	}
	return false
}

// safePath resolves a stored relative path under the store dir and rejects
// anything that escapes it.
func (s *Store) safePath(rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	abs := filepath.Join(s.dir, rel)
	if !strings.HasPrefix(abs, s.dir+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

func relOf(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
