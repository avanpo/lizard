# Multi-stage build for Lizard Server Monitor
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Copy manifests and source
COPY go.mod ./
COPY *.go ./
COPY web/ web/

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/lizard .

# Production runtime image
FROM alpine:3.20

# Install ca-certificates, tzdata, and docker-cli
RUN apk --no-cache add ca-certificates tzdata docker-cli

# Copy compiled binary from builder
COPY --from=builder /build/lizard /usr/local/bin/lizard

# Default container environment configuration
ENV LIZARD_PORT=8080 \
    LIZARD_HOST=0.0.0.0 \
    LIZARD_DOCKER_SOCKET=/var/run/docker.sock \
    LIZARD_HOST_ROOT=/host

# Healthcheck (short interval/start-period so container transitions to healthy quickly)
HEALTHCHECK --interval=5s --timeout=2s --start-period=1s --retries=3 \
  CMD wget -qO- http://127.0.0.1:${LIZARD_PORT:-8080}/healthz || exit 1

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/lizard"]
