package zengate

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// decodeSessionHex decodes a 12-hex session id part in the descending client
// form: value = ~(ms*0x1000 + counter), so current = ^value within 48 bits.
func decodeSessionHex(t *testing.T, hexPart string) (ms int64, counter uint64) {
	t.Helper()
	if len(hexPart) != 12 {
		t.Fatalf("hex part = %q, want 12 chars", hexPart)
	}
	value, err := strconv.ParseUint(hexPart, 16, 64)
	if err != nil {
		t.Fatalf("parsing %q: %v", hexPart, err)
	}
	current := (^value) & 0xFFFFFFFFFFFF
	return int64(current >> 12), current & 0xfff
}

// TestSessionIDMatchesCapturedClientID pins the scheme against a real id:
// ses_ec709aa0effe3GvtgLxbBwocdO was captured from the opencode v2.0.23
// client on 2026-10-14 (ms 1791957030385). Its 12 hex chars must decode to
// that ms modulo 2^36 with counter 1.
func TestSessionIDMatchesCapturedClientID(t *testing.T) {
	ms, counter := decodeSessionHex(t, "ec709aa0effe")
	if want := int64(1791957030385) % (int64(1) << 36); ms != want || counter != 1 {
		t.Fatalf("decode = ms %d counter %d, want %d/1", ms, counter, want)
	}
}

// TestSessionIDIsFreshDescendingAndWellFormed round-trips generated ids:
// the decoded timestamp must sit within seconds of now (an ascending or
// random id would not) and the counter must be in range.
func TestSessionIDIsFreshDescendingAndWellFormed(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	const window = int64(1) << 36
	now := time.Now().UnixMilli()
	for i := 0; i < 3; i++ {
		id, err := SessionID()
		if err != nil {
			t.Fatalf("SessionID: %v", err)
		}
		if len(id) != 26 || !pattern.MatchString(id) {
			t.Fatalf("id = %q, want 26 chars: 12 hex + 14 base62", id)
		}
		ms, counter := decodeSessionHex(t, id[:12])
		if counter < 1 || counter > 4096 {
			t.Fatalf("id %q counter = %d, want 1..4096", id, counter)
		}
		base := now / window * window
		best := int64(1) << 62
		for _, candidate := range []int64{base + ms, base + ms - window, base + ms + window} {
			diff := candidate - now
			if diff < 0 {
				diff = -diff
			}
			if diff < best {
				best = diff
			}
		}
		if best > 5000 {
			t.Fatalf("id %q decodes to a timestamp %dms from now, want within 5s (descending form drift?)", id, best)
		}
	}
}

func TestToolsIsTheCapturedMainAgentArray(t *testing.T) {
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(Tools(), &tools); err != nil {
		t.Fatalf("embedded tools JSON does not parse: %v", err)
	}
	if len(tools) != 12 {
		t.Fatalf("tools = %d entries, want the captured 12", len(tools))
	}
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			t.Fatalf("malformed tool entry: %+v", tool)
		}
	}
}
