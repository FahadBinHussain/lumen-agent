// Package zengate holds the byte-exact opencode zen free-tier gate prompt
// shared by every caller that talks to the zen lane (internal/vision for
// image descriptions, internal/llm's zen transport for chat).
//
// The zen free tier answers 403 FreeTierError ("can only be used from within
// OpenCode") unless the request passes the gate, which is ~request-body: the
// system message must START, from char 0, with a byte-exact copy of a
// registered official opencode prompt of at least ~1000 chars. Everything a
// caller adds MUST be appended after that prefix, never inside it. One
// changed byte in the file below is a hard 403.
//
// The prompt is refreshed by extracting it from the INSTALLED opencode binary
// (the gate follows the prompt the installed client registers). History:
// captured from opencode v1.18.25, refreshed 2026-10-16 from v2.0.23
// (scoop\apps\opencode2\current\opencode.exe, two byte-identical copies;
// ends at </examples>). `.gitattributes` pins the file to LF - this box runs
// core.autocrlf=true and would otherwise flip the endings around.
package zengate

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"io"
)

//go:embed zengate-title-prompt.txt
var systemPrompt string

// SystemPrompt returns the byte-exact gate prompt. Callers must treat it as
// an immutable prefix and append their own instructions only AFTER it.
func SystemPrompt() string {
	return systemPrompt
}

const (
	identityIDHex    = "0123456789abcdef"
	identityIDBase62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// IdentityID builds the opencode identity tail used in the x-opencode-session
// and x-opencode-request headers: 12 hex chars + 14 base62 chars (callers add
// the ses_/msg_ prefixes). Fresh id per attempt; the gate treats malformed ids
// as a mismatch.
func IdentityID() (string, error) {
	var buf [26]byte
	if _, err := io.ReadFull(rand.Reader, buf[:12]); err != nil {
		return "", fmt.Errorf("generating identity id: %w", err)
	}
	for i := 0; i < 12; i++ {
		buf[i] = identityIDHex[buf[i]&0xf]
	}
	if _, err := io.ReadFull(rand.Reader, buf[12:]); err != nil {
		return "", fmt.Errorf("generating identity id: %w", err)
	}
	for i := 12; i < 26; i++ {
		buf[i] = identityIDBase62[int(buf[i])%len(identityIDBase62)]
	}
	return string(buf[:]), nil
}
