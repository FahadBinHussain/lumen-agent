#!/bin/sh
set -e

# optional: join the home tailnet in userspace mode (no /dev/net/tun on
# Render free). tailscaled exposes a SOCKS5 server on 127.0.0.1:1055 that
# can reach tailnet IPs (the home laptop's socks5-proxy at 100.76.10.50:1080).
# Log-noise filter (identified 2026-08-17): tailscaled's netstack runs a port
# discovery/HTTP-probe loop that dials the container's own listeners with
# HTTP-ish first bytes (0x48 'H'). Every probe that lands on a SOCKS listener
# logs "Unsupported SOCKS version: [72]" from BOTH listeners (tailscaled's
# 1055 and sockschain's 1081 via armon/go-socks5) at ~1-2/s, which drowns the
# Render logs. Filter the noise out of both processes' stderr (cosmetic fix;
# probes are harmless rejects).
LOG_FILTER='Unsupported SOCKS version|incompatible SOCKS version|socks5: client connection failed|peerapi: unknown peer|RATELIMIT'
if [ -n "$TS_AUTHKEY" ]; then
  /app/tailscaled --tun=userspace-networking --socks5-server=127.0.0.1:1055 --state=mem 2>&1 | grep --line-buffered -vE "$LOG_FILTER" &
  /app/tailscale up --authkey="$TS_AUTHKEY" --hostname=lumen-render --accept-dns=false --timeout=40s
  echo "entrypoint: tailscale userspace node up ($(/app/tailscale ip -4))"
else
  echo "entrypoint: TS_AUTHKEY unset, skipping tailscale mesh join"
fi

# optional: chain relay that forwards WHATSAPP_PROXY_URL's SOCKS5 connections
# through tailscale's socks server to the home laptop's socks5-proxy.
if [ -n "$SOCKS_CHAIN_UPSTREAM" ]; then
  /app/sockschain 2>&1 | grep --line-buffered -vE "$LOG_FILTER" &
fi

exec ./element-orion serve -config /app/config/production.yaml
