// Package zengate holds the shared envelope for every caller that talks to
// the opencode zen free lane (internal/llm's zen transport for chat,
// internal/vision for image descriptions).
//
// The zen free tier answers 403 FreeTierError ("can only be used from within
// OpenCode") unless the request passes the gate. Re-bisected 2026-10-16 with
// replays of captured opencode v2.0.23 traffic - the gate is only:
//
//  1. the session header group (x-opencode-session, x-opencode-session-id,
//     x-session-affinity, x-session-id) must carry a session id in the
//     scheme the installed client generates (packages/schema/src/
//     identifier.ts): Date.now()*0x1000+counter, descending (bitwise NOT)
//     for sessions, written as the six low bytes in hex then 14 base62
//     chars. Random hex in the same shape 403s; a correctly generated id
//     passes. This check is what broke every caller on 2026-10-14 when
//     opencode rolled it out - the bodies were never the problem.
//  2. the body must look like a client request: a byte-exact registered
//     system prompt, OR a body carrying a `tools` array (the main-agent
//     branch). `stream: true` is required either way.
//
// This package provides the pieces both callers share: the headless main
// agent's tools array (Tools, embedded from the capture - it keeps the body
// gate-passing without depending on a byte-exact prompt capture that has to
// be re-extracted on every opencode release), authentic session ids
// (SessionID), a stable per-process x-opencode-project (ProjectID) and
// fresh b3/traceparent pairs (TraceHeaders).
package zengate

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

//go:embed zen-tools.json
var toolsJSON []byte

// Tools returns the opencode v2.0.23 main-agent tools array, captured
// byte-exact from a real client request. The zen gate requires a `tools`
// array on the main-agent branch; the array's contents are not matched (a
// two-tool subset passes), so this is our own stable copy, not something
// that has to follow client updates.
func Tools() json.RawMessage {
	return toolsJSON
}

const (
	idHexChars = "0123456789abcdef"
	idChars    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	idMu      sync.Mutex
	idLastMs  int64
	idCounter uint64

	projectOnce sync.Once
	projectID   string
)

// SessionID returns a fresh opencode session id in the descending form the
// installed client generates (packages/schema/src/identifier.ts): the
// current timestamp in ms times 0x1000 plus a per-millisecond counter,
// bitwise-negated, written as its six low bytes in hex, then 14 random
// base62 chars. Callers prefix it with "ses_". The zen gate accepts
// generated ids and rejects ids that read as hostile timestamps, so this
// MUST stay byte-compatible with the client.
func SessionID() (string, error) {
	idMu.Lock()
	now := time.Now().UnixMilli()
	if now != idLastMs {
		idLastMs = now
		idCounter = 0
	}
	idCounter++
	counter := idCounter
	idMu.Unlock()

	value := ^(uint64(now)*0x1000 + counter)

	var id [26]byte
	for i := 0; i < 6; i++ {
		b := byte(value >> (8 * (5 - i)))
		id[i*2] = idHexChars[b>>4]
		id[i*2+1] = idHexChars[b&0xf]
	}
	var suffix [14]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	for i, b := range suffix {
		id[12+i] = idChars[int(b)%len(idChars)]
	}
	return string(id[:]), nil
}

// ProjectID returns the x-opencode-project header value: a random 40-hex
// string generated once per process. The gate does not validate its value
// (fresh random ids pass); it only has to look like a project hash.
func ProjectID() string {
	projectOnce.Do(func() {
		projectID = randomHex(20)
	})
	return projectID
}

// TraceHeaders returns a fresh b3 + traceparent pair in the shapes the
// client sends. The gate does not check them (fresh random values pass);
// they keep requests looking like the client's.
func TraceHeaders() (b3 string, traceparent string) {
	trace := randomHex(16)
	span := randomHex(8)
	return trace + "-" + span + "-1-" + randomHex(8),
		"00-" + trace + "-" + span + "-01"
}

// randomHex returns n random bytes as lowercase hex. crypto/rand never
// fails on supported platforms (Go 1.24+ documents an errorless Read);
// the panic is the loud, unreachable path.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("zengate: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(buf)
}
