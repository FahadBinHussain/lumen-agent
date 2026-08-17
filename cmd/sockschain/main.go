package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/armon/go-socks5"
)

// sockschain is a SOCKS5-to-SOCKS5 chain relay: it accepts SOCKS5 clients
// (no auth or any auth) on a local listener and forwards every CONNECT
// through a second SOCKS5 proxy ("via", e.g. tailscale's userspace socks
// server) to a final SOCKS5 proxy ("upstream", e.g. the home laptop's
// socks5-proxy). The original CONNECT target is re-issued by the upstream
// proxy, so the egress IP is the laptop's home IP.
//
// Env:
//   SOCKS_CHAIN_LISTEN   listen address            (default 127.0.0.1:1081)
//   SOCKS_CHAIN_VIA      first-hop socks5 proxy    (default 127.0.0.1:1055)
//   SOCKS_CHAIN_UPSTREAM final socks5 URL          (required,
//                        socks5://user:pass@host:port)
//   SOCKS_CHAIN_VIA_USER / SOCKS_CHAIN_VIA_PASS    optional auth for the
//                        via proxy (e.g. for local testing)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listenAddr := getenv("SOCKS_CHAIN_LISTEN", "127.0.0.1:1081")
	viaAddr := getenv("SOCKS_CHAIN_VIA", "127.0.0.1:1055")

	upstreamRaw := os.Getenv("SOCKS_CHAIN_UPSTREAM")
	if upstreamRaw == "" {
		log.Fatal("SOCKS_CHAIN_UPSTREAM required: socks5://user:pass@host:port")
	}
	u, err := url.Parse(upstreamRaw)
	if err != nil || (u.Scheme != "socks5" && u.Scheme != "socks5h") {
		log.Fatalf("SOCKS_CHAIN_UPSTREAM must be a socks5:// URL: %v", err)
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		log.Fatalf("SOCKS_CHAIN_UPSTREAM must include a port: %v", err)
	}
	upstreamHost := u.Host
	user, pass := "", ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	viaUser := os.Getenv("SOCKS_CHAIN_VIA_USER")
	viaPass := os.Getenv("SOCKS_CHAIN_VIA_PASS")

	log.Printf("sockschain: listen %s, via %s, upstream %s (user %q)", listenAddr, viaAddr, upstreamHost, user)

	conf := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return chainDial(ctx, viaAddr, viaUser, viaPass, upstreamHost, user, pass, addr)
		},
	}
	server, err := socks5.New(conf)
	if err != nil {
		log.Fatalf("socks5 server: %v", err)
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	log.Printf("sockschain: listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go func() {
			defer conn.Close()
			if err := server.ServeConn(conn); err != nil {
				log.Printf("serve %s: %v", conn.RemoteAddr(), err)
			}
		}()
	}
}

func chainDial(ctx context.Context, via, viaUser, viaPass, upstream, user, pass, target string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	c, err := d.DialContext(ctx, "tcp", via)
	if err != nil {
		return nil, fmt.Errorf("dial via %s: %w", via, err)
	}
	if err := socks5ClientHandshake(c, viaUser, viaPass, upstream); err != nil {
		c.Close()
		return nil, fmt.Errorf("via handshake to %s: %w", upstream, err)
	}
	if err := socks5ClientHandshake(c, user, pass, target); err != nil {
		c.Close()
		return nil, fmt.Errorf("upstream handshake: %w", err)
	}
	return c, nil
}

// socks5ClientHandshake performs a client-side SOCKS5 handshake on conn,
// optionally authenticating with user/pass, then issues a CONNECT to target.
func socks5ClientHandshake(conn net.Conn, user, pass, target string) error {
	methods := []byte{0x00}
	if user != "" {
		methods = append(methods, 0x02)
	}
	buf := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(buf); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("bad SOCKS version %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
	case 0x02:
		if user == "" {
			return fmt.Errorf("server requires auth")
		}
		ub := []byte(user)
		pb := []byte(pass)
		auth := append([]byte{0x01, byte(len(ub))}, ub...)
		auth = append(auth, byte(len(pb)))
		auth = append(auth, pb...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		ar := make([]byte, 2)
		if _, err := io.ReadFull(conn, ar); err != nil {
			return err
		}
		if ar[1] != 0x00 {
			return fmt.Errorf("auth failed (status %d)", ar[1])
		}
	default:
		return fmt.Errorf("unsupported auth method %d", resp[1])
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("bad target %q: %w", target, err)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return err
	}
	var addr []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			addr = append([]byte{0x01}, v4...)
		} else {
			addr = append([]byte{0x04}, ip.To16()...)
		}
	} else {
		h := []byte(host)
		if len(h) > 255 {
			return fmt.Errorf("host too long")
		}
		addr = append([]byte{0x03, byte(len(h))}, h...)
	}
	req := []byte{0x05, 0x01, 0x00}
	req = append(req, addr...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	rep := make([]byte, 4)
	if _, err := io.ReadFull(conn, rep); err != nil {
		return err
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		return fmt.Errorf("CONNECT failed (rep %d)", rep[1])
	}
	switch rep[3] {
	case 0x01:
		_, err = io.CopyN(io.Discard, conn, 4+2)
	case 0x04:
		_, err = io.CopyN(io.Discard, conn, 16+2)
	case 0x03:
		var l [1]byte
		if _, err = io.ReadFull(conn, l[:]); err != nil {
			return err
		}
		_, err = io.CopyN(io.Discard, conn, int64(l[0])+2)
	default:
		return fmt.Errorf("bad atyp %d", rep[3])
	}
	return err
}
