package whatsapp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
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
		TLSHandshakeTimeout: 15 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

func proxyDialer(proxyAddr string) proxy.ContextDialer {
	if proxyAddr == "" {
		return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	}
	addr := strings.TrimPrefix(strings.TrimPrefix(proxyAddr, "socks5://"), "socks5h://")
	d, err := proxy.SOCKS5("tcp", addr, nil, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second})
	if err != nil {
		return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd
	}
	return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
}