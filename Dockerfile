FROM golang:1.25 AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o element-orion ./cmd/element-orion

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg wget && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /build/element-orion .
COPY config/production.yaml /app/config/production.yaml
ENV PORT=7860
ENV ELEMENT_ORION_BRIDGE_NOTIFICATIONS_SECRET=
EXPOSE 7860
CMD ["/bin/sh", "-c", "exec ./element-orion serve -config /app/config/production.yaml"]
