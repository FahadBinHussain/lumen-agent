package whatsapp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
	utls "github.com/refraction-networking/utls"
)

func NewChromeHTTPClient(proxyAddr string) *http.Client {
	dialer := proxyDialer(proxyAddr)

	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}

			rawConn, err := dialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, fmt.Errorf("tcp dial: %w", err)
			}

			config := &utls.Config{
				InsecureSkipVerify: false,
				ServerName:         host,
			}
			uconn := utls.UClient(rawConn, config, utls.HelloChrome_120)

			uconn.SetSNI(host)

			// The Chrome hello spec hardcodes ALPN ["h2","http/1.1"] and the
			// spec wins over config.NextProtos. Go's http.Transport cannot read
			// the negotiated ALPN from a non-*tls.Conn (tlsState stays nil), so
			// it always speaks HTTP/1.1 on the conn we return: if the server
			// picks h2 it answers with HTTP/2 frames the transport parses as a
			// garbage status line ("malformed HTTP status code \x00\x00...").
			// Extensions are built lazily at handshake time, so materialize the
			// hello first, then force http/1.1-only ALPN; the handshake
			// re-applies Extensions before marshaling the ClientHello.
			if err := uconn.BuildHandshakeState(); err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("utls build hello: %w", err)
			}
			for _, ext := range uconn.Extensions {
				if alpn, ok := ext.(*utls.ALPNExtension); ok {
					alpn.AlpnProtocols = []string{"http/1.1"}
				}
			}

			err = uconn.Handshake()
			if err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("utls handshake: %w", err)
			}

			// Hand back the TLS conn itself, not uconn.NetConn(): the transport
			// writes plaintext HTTP into whatever we return, and NetConn() is
			// the raw TCP underneath the established session — plaintext there
			// corrupts the session and the server answers with a TLS record the
			// transport reads as a broken HTTP response
			// ("malformed HTTP response \x17\x03\x03...").
			return uconn, nil
		},
		DialContext:         dialer.DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 30 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}
}

func proxyDialer(proxyAddr string) proxy.ContextDialer {
	if proxyAddr == "" {
		return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	u, err := url.Parse(proxyAddr)
	if err != nil || u.Host == "" {
		return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	addr := u.Host
	var auth *proxy.Auth
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: pass}
	}
	d, err := proxy.SOCKS5("tcp", addr, auth, &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
	if err != nil {
		return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd
	}
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}
