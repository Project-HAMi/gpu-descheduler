# GPU Descheduler Webhook Dockerfile
# Multi-stage build: compile stage + runtime stage

# ============================================
# Stage 1: build the Go binary
# ============================================
FROM golang:1.26.11-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

# Go module proxy (goproxy.cn helps users in China)
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# Working directory
WORKDIR /build

# Copy go.mod and go.sum first to use the Docker layer cache
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy sources
COPY . .

# Build arguments
ARG VERSION=dev
ARG REVISION=none
ARG BUILD_DATE=

# Build the binary
# CGO_ENABLED=1: HAMi/NVML requires CGO
# TARGETOS/TARGETARCH are injected by buildx, each platform builds with native gcc
RUN GO_VERSION=$(go version | awk '{print $3}') && \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w \
    -X main.Version=${VERSION} \
    -X main.BuildTime=${BUILD_DATE} \
    -X main.GitCommit=${REVISION} \
    -X main.GoVersion=${GO_VERSION}" \
    -o webhook-gpu-descheduler \
    .

# ============================================
# Stage 2: runtime image
# ============================================
FROM debian:bookworm-slim

# Avoid interactive prompts
ENV DEBIAN_FRONTEND=noninteractive

# Base packages (ca-certificates for HTTPS, curl for the health check)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/* && \
    apt-get clean && \
    rm -rf /tmp/* /var/tmp/*
# Run as a non-root user
RUN groupadd -r hami && useradd -r -g hami -s /sbin/nologin -M hami

# Certificate directory
RUN mkdir -p /etc/webhook/certs && \
    chown -R hami:hami /etc/webhook/certs

# Copy the binary from the build stage
COPY --from=builder /build/webhook-gpu-descheduler /webhook-gpu-descheduler

# Permissions
RUN chmod +x /webhook-gpu-descheduler && \
    chown hami:hami /webhook-gpu-descheduler

# Switch to the non-root user
USER hami

# Ports: webhook, metrics, health probes
EXPOSE 8443 8080 8081

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8081/readyz || exit 1

# Environment
ENV TLS_DIR=/etc/webhook/certs
ENV PORT=8443
ENV METRICS_BIND_ADDRESS=:8080
ENV HEALTH_PROBE_BIND_ADDRESS=:8081

# Entrypoint
ENTRYPOINT ["/webhook-gpu-descheduler"]

# Default arguments
CMD ["--tls-dir=/etc/webhook/certs", "--port=8443", "--metrics-bind-address=:8080", "--scheduler-port=9395", "--sync-interval=60s", "--enable-leader-election=true"]