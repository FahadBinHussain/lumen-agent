#!/bin/sh
set -e

# optional: join the home tailnet in userspace mode (no /dev/net/tun on
# Render free). tailscaled exposes a SOCKS5 server on 127.0.0.1:1055 that
# can reach tailnet IPs (the home laptop's socks5-proxy at 100.76.10.50:1080).
if [ -n "$TS_AUTHKEY" ]; then
  /app/tailscaled --tun=userspace-networking --socks5-server=127.0.0.1:1055 --state=mem --accept-dns=false &
  /app/tailscale up --authkey="$TS_AUTHKEY" --hostname=lumen-render --accept-dns=false --timeout=40s
  echo "entrypoint: tailscale userspace node up ($(/app/tailscale ip -4))"
else
  echo "entrypoint: TS_AUTHKEY unset, skipping tailscale mesh join"
fi

# optional: chain relay that forwards WHATSAPP_PROXY_URL's SOCKS5 connections
# through tailscale's socks server to the home laptop's socks5-proxy.
if [ -n "$SOCKS_CHAIN_UPSTREAM" ]; then
  /app/sockschain &
fi

exec ./element-orion serve -config /app/config/production.yaml
