package messenger

// Session keepalive (2026-11-14): on 2026-11-13 at 23:29 Meta killed the
// Messenger session mid-run (DGW 4003 Unauthorized on a reconnect) after it
// had lived 6 days on the cookie set uploaded 2026-11-07. Two structural
// gaps made that fatal instead of routine:
//
//  1. Nothing ever refreshed the session. messagix rotates cookies in
//     memory from Set-Cookie responses, but the live client makes almost no
//     HTTP requests (traffic rides the DGW socket), so the session went
//     hours without any refresh traffic.
//  2. Refreshed cookies were never persisted. GetAll() had zero callers:
//     the cookies file (and its Neon snapshot) always held the stale
//     upload-time set, so every restart reloaded 6-day-old cookies.
//
// The keeper closes both gaps: once an hour it performs a real GET of the
// messages page — the same request a browser tab makes — so Meta's
// Set-Cookie responses rotate the session in memory, then writes the
// refreshed cookies back to the cookies file when they changed. The 1m
// persist loop snapshots the file to Neon, so a restart reloads the
// refreshed set.
//
// Deliberately NOT LoadMessagesPage: that rebuilds the messagix sync
// manager, which is unsafe to call on a live connected socket. A plain
// authenticated GET refreshes cookies with no sync-manager side effects.
//
// The keeper never fights a dead session: while auth-halted it skips
// silently, and a terminal auth error from the refresh halts like every
// other terminal error in this package.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"element-orion/internal/cookies"
)

const (
	// defaultSessionKeepaliveInterval is the refresh cadence when the config
	// key is empty. One hour is frequent enough to keep the session warm
	// and quiet enough to look like an idle open tab, not a bot.
	defaultSessionKeepaliveInterval = time.Hour
	// minSessionKeepaliveInterval is the floor when the keeper is enabled:
	// anything faster looks automated to Meta and buys nothing.
	minSessionKeepaliveInterval = 5 * time.Minute
	// sessionKeepaliveTimeout bounds one refresh request; the proxy path
	// can stall for minutes during an exit-node outage.
	sessionKeepaliveTimeout = 30 * time.Second
)

// SetSessionKeepaliveInterval configures the session keeper: how often the
// live session is refreshed and the refreshed cookies persisted. 0 disables
// the keeper. Must be called before Start; the bridge wires it from config
// in New.
func (c *Client) SetSessionKeepaliveInterval(d time.Duration) {
	c.keeperMu.Lock()
	defer c.keeperMu.Unlock()
	c.keeperInterval = d
}

// startSessionKeeper launches the keeper loop once. Safe to call from every
// Start (ReloadCookies re-enters Start); the guard keeps exactly one loop.
func (c *Client) startSessionKeeper() {
	c.keeperMu.Lock()
	if c.keeperRunning || c.keeperInterval <= 0 {
		c.keeperMu.Unlock()
		return
	}
	c.keeperRunning = true
	stop := make(chan struct{})
	c.keeperStop = stop
	interval := c.keeperInterval
	c.keeperMu.Unlock()
	log.Printf("messenger: session keepalive started (every %s)", interval)
	go c.sessionKeeperLoop(interval, stop)
}

// stopSessionKeeper stops the keeper loop. Called from Disconnect.
func (c *Client) stopSessionKeeper() {
	c.keeperMu.Lock()
	if !c.keeperRunning {
		c.keeperMu.Unlock()
		return
	}
	c.keeperRunning = false
	stop := c.keeperStop
	c.keeperStop = nil
	c.keeperMu.Unlock()
	close(stop)
}

func (c *Client) sessionKeeperLoop(interval time.Duration, stop <-chan struct{}) {
	// First tick lands one interval after start: Login already refreshed
	// the session moments ago, so there is nothing to do immediately.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.refreshSessionOnce()
		}
	}
}

// refreshSessionOnce performs one keepalive cycle. It skips silently while
// unauthenticated or auth-halted: dead cookies must never be hammered.
func (c *Client) refreshSessionOnce() {
	mc := c.currentClient()
	if mc == nil {
		return
	}
	if c.IsAuthHalted() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionKeepaliveTimeout)
	defer cancel()
	httpClient := mc.GetHTTP()
	headers := httpClient.BuildHeaders(true, true)
	resp, _, err := httpClient.MakeRequestOnce(ctx, mc.GetEndpoint("messages"), "GET", headers, nil, types.NONE)
	if err != nil {
		if isTerminalAuthError(err) {
			c.haltAuth(err.Error())
			log.Printf("messenger: AUTH HALTED during session keepalive: %v — auto-reconnect stopped; upload fresh cookies after resolving account state", err)
			return
		}
		log.Printf("messenger: session keepalive refresh failed: %v (retrying next cycle)", err)
		return
	}
	// MakeRequestOnce does not apply Set-Cookie itself (only the unexported
	// fetchPageData does), so apply the rotation explicitly.
	if resp != nil {
		if cc := mc.GetCookies(); cc != nil {
			cc.UpdateFromResponse(resp)
		}
	}
	changed, total, err := c.persistSessionCookies(mc)
	if err != nil {
		log.Printf("messenger: session keepalive persist failed: %v", err)
		return
	}
	log.Printf("messenger: session keepalive: refreshed, %d cookies (%d changed)", total, changed)
}

// persistSessionCookies exports the live messagix cookies and writes them to
// the cookies file when they differ from what's on disk. Returns the number
// of changed cookies and the total. The 1m persist loop snapshots the file
// to Neon, so a restart reloads the refreshed set instead of the stale
// upload-time one.
func (c *Client) persistSessionCookies(mc *messagix.Client) (changed, total int, err error) {
	cc := mc.GetCookies()
	if cc == nil {
		return 0, 0, fmt.Errorf("messagix client has no cookie jar")
	}
	live := make(cookies.CookieMap, len(cc.GetAll()))
	for k, v := range cc.GetAll() {
		live[string(k)] = v
	}
	total = len(live)
	onDisk, err := cookies.LoadFromFile(c.cookiesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, total, fmt.Errorf("read cookies file: %w", err)
		}
		onDisk = cookies.CookieMap{}
	}
	changed = countCookieChanges(onDisk, live)
	if changed == 0 {
		return 0, total, nil
	}
	if err := writeCookiesFile(c.cookiesPath, live); err != nil {
		return 0, total, err
	}
	return changed, total, nil
}

// countCookieChanges counts added, removed, and changed-value cookies
// between the on-disk set and the live set.
func countCookieChanges(old, new cookies.CookieMap) int {
	changed := 0
	for k, v := range new {
		if ov, ok := old[k]; !ok || ov != v {
			changed++
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			changed++
		}
	}
	return changed
}

// writeCookiesFile writes the cookie map atomically (temp file + rename) so
// a crash mid-write never leaves a truncated cookies file behind. Mode
// 0600: these are live session credentials.
func writeCookiesFile(path string, m cookies.CookieMap) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cookies directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".cookies-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp cookies file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp cookies file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp cookies file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp cookies file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace cookies file: %w", err)
	}
	return nil
}
