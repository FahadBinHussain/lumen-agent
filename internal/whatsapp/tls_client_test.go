package whatsapp

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Regression test for the direct-dial (no proxy) uTLS path: the transport must
// get a real HTTP 200 through the uTLS conn.
//
// Two past bugs are pinned here:
//   - returning uconn.NetConn() handed the transport the raw TCP under a live
//     TLS session, so it wrote plaintext HTTP into it and the server answered
//     with a TLS record ("malformed HTTP response \x17\x03\x03...").
//   - the Chrome hello spec hardcodes ALPN ["h2","http/1.1"] and wins over
//     config.NextProtos, so the server negotiated h2 while the transport (which
//     can't read ALPN from a non-*tls.Conn) spoke HTTP/1.1 and died parsing h2
//     frames as a status line.
//
// Network probe: run with LUMEN_NET_PROBE=1 (skipped otherwise so
// `go test ./...` stays offline-green).
func TestChromeHTTPClientDirectDial(t *testing.T) {
	if os.Getenv("LUMEN_NET_PROBE") == "" {
		t.Skip("set LUMEN_NET_PROBE=1 to run the network probe")
	}
	client := NewChromeHTTPClient("")
	client.Timeout = 20 * time.Second

	req, err := http.NewRequest(http.MethodGet, "https://web.whatsapp.com/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through NewChromeHTTPClient: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		t.Fatalf("expected 200, got %d body=%q", resp.StatusCode, body)
	}
}
