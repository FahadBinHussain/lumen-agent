FROM golang:1.25 AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o element-orion ./cmd/element-orion
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o sockschain ./cmd/sockschain

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg wget postgresql-client git openssl && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /build/element-orion .
COPY --from=builder /build/sockschain .
COPY config/production.yaml /app/config/production.yaml
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
RUN wget -qO- https://pkgs.tailscale.com/stable/tailscale_1.102.2_amd64.tgz | tar -xz --strip-components=1 -C /tmp && \
    cp /tmp/tailscale /tmp/tailscaled /app/ && chmod +x /app/tailscale /app/tailscaled && rm -rf /tmp/tailscale*
ENV PORT=7860
ENV ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET=
EXPOSE 7860
CMD ["/bin/sh", "/app/entrypoint.sh"]
