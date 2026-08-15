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

			err = uconn.Handshake()
			if err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("utls handshake: %w", err)
			}

			return uconn.NetConn(), nil
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