package messenger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"element-orion/internal/cookies"
)

func TestCountCookieChanges(t *testing.T) {
	cases := []struct {
		name string
		old  cookies.CookieMap
		new  cookies.CookieMap
		want int
	}{
		{"identical", cookies.CookieMap{"a": "1", "b": "2"}, cookies.CookieMap{"a": "1", "b": "2"}, 0},
		{"one changed", cookies.CookieMap{"a": "1", "b": "2"}, cookies.CookieMap{"a": "1", "b": "3"}, 1},
		{"one added", cookies.CookieMap{"a": "1"}, cookies.CookieMap{"a": "1", "b": "2"}, 1},
		{"one removed", cookies.CookieMap{"a": "1", "b": "2"}, cookies.CookieMap{"a": "1"}, 1},
		{"empty vs full", cookies.CookieMap{}, cookies.CookieMap{"a": "1", "b": "2"}, 2},
		{"full vs empty", cookies.CookieMap{"a": "1", "b": "2"}, cookies.CookieMap{}, 2},
		{"both empty", cookies.CookieMap{}, cookies.CookieMap{}, 0},
	}
	for _, tc := range cases {
		if got := countCookieChanges(tc.old, tc.new); got != tc.want {
			t.Errorf("%s: countCookieChanges = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestWriteCookiesFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cookies.json")
	m := cookies.CookieMap{"c_user": "123", "xs": "abc", "sb": "def"}
	if err := writeCookiesFile(path, m); err != nil {
		t.Fatalf("writeCookiesFile: %v", err)
	}
	back, err := cookies.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after write: %v", err)
	}
	if countCookieChanges(m, back) != 0 {
		t.Fatalf("round trip changed cookies: wrote %v, read %v", m, back)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cookies file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cookies file mode = %o, want 0600", info.Mode().Perm())
	}
	// No temp file left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 file in dir, got %d", len(entries))
	}
}

// testMessagixClient builds a real (unconnected) messagix client holding the
// given cookies, offline-safe: NewClient performs no network I/O.
func testMessagixClient(t *testing.T, m cookies.CookieMap) *messagix.Client {
	t.Helper()
	cc := cookies.ToMessagix(m, types.Facebook)
	logger := zerolog.New(os.Stderr).With().Str("component", "messagix-test").Timestamp().Logger()
	mCfg, err := buildMessagixConfig("")
	if err != nil {
		t.Fatalf("buildMessagixConfig: %v", err)
	}
	return messagix.NewClient(cc, logger, mCfg)
}

func TestPersistSessionCookiesWritesOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messenger-cookies.json")
	live := cookies.CookieMap{"c_user": "123", "xs": "fresh-xs", "sb": "def"}
	c := &Client{cookiesPath: path}
	mc := testMessagixClient(t, live)

	changed, total, err := c.persistSessionCookies(mc)
	if err != nil {
		t.Fatalf("persist (missing file): %v", err)
	}
	if changed != 3 || total != 3 {
		t.Fatalf("persist (missing file): changed=%d total=%d, want 3/3", changed, total)
	}
	onDisk, err := cookies.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after persist: %v", err)
	}
	if countCookieChanges(live, onDisk) != 0 {
		t.Fatalf("persisted file differs from live cookies: %v vs %v", onDisk, live)
	}

	// Second persist with nothing changed must be a no-op (no rewrite).
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	changed, total, err = c.persistSessionCookies(mc)
	if err != nil {
		t.Fatalf("persist (unchanged): %v", err)
	}
	if changed != 0 || total != 3 {
		t.Fatalf("persist (unchanged): changed=%d total=%d, want 0/3", changed, total)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged persist rewrote the file")
	}

	// Rotated value must be picked up.
	mc.GetCookies().Set("xs", "rotated-xs")
	changed, total, err = c.persistSessionCookies(mc)
	if err != nil {
		t.Fatalf("persist (rotated): %v", err)
	}
	if changed != 1 || total != 3 {
		t.Fatalf("persist (rotated): changed=%d total=%d, want 1/3", changed, total)
	}
	onDisk, err = cookies.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after rotation: %v", err)
	}
	if onDisk["xs"] != "rotated-xs" {
		t.Fatalf("rotated xs not persisted: %q", onDisk["xs"])
	}
}

func TestRefreshSessionOnceSkipsWhenUnauthenticatedOrHalted(t *testing.T) {
	// No messagix client at all: must be a silent no-op, never a panic.
	c := &Client{cookiesPath: filepath.Join(t.TempDir(), "cookies.json")}
	c.refreshSessionOnce()

	// Halted client with a live messagix client: must skip before any HTTP
	// attempt (no network in this test — a request would fail loudly and
	// the sentinel file below proves nothing was written).
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.json")
	sentinel := []byte(`{"c_user": "sentinel"}` + "\n")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	mc := testMessagixClient(t, cookies.CookieMap{"c_user": "123", "xs": "abc"})
	c = newClientShell(mc, path, types.Facebook, nil)
	c.haltAuth("test halt")
	c.refreshSessionOnce()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(after) != string(sentinel) {
		t.Fatalf("halted refresh touched the cookies file")
	}
}

func TestSessionKeeperStartStopIsSingleFlight(t *testing.T) {
	// Halted so ticks never touch the network; this only exercises the
	// start/stop guard, not the refresh itself.
	c := &Client{cookiesPath: filepath.Join(t.TempDir(), "cookies.json")}
	c.haltAuth("test halt")
	c.SetSessionKeepaliveInterval(10 * time.Millisecond)
	c.startSessionKeeper()
	c.startSessionKeeper() // second start must be a no-op
	time.Sleep(50 * time.Millisecond)
	c.stopSessionKeeper()
	c.stopSessionKeeper() // second stop must be a no-op
	c.keeperMu.Lock()
	running := c.keeperRunning
	c.keeperMu.Unlock()
	if running {
		t.Fatalf("keeper still running after stop")
	}
}

func TestSessionKeeperDisabledByDefault(t *testing.T) {
	c := &Client{cookiesPath: filepath.Join(t.TempDir(), "cookies.json")}
	// No setter call: interval is 0, Start must not launch a keeper.
	c.startSessionKeeper()
	c.keeperMu.Lock()
	running := c.keeperRunning
	c.keeperMu.Unlock()
	if running {
		t.Fatalf("keeper started without an interval configured")
	}
}
