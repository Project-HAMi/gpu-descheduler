# GPU Descheduler Webhook Dockerfile
# Multi-stage build: compile stage + runtime stage

# ============================================
# Stage 1: build the Go binary
# ============================================
FROM golang:1.27.4-bookworm@sha256:b754301872c53d3dff666ec39e3fae971dc6f5ecd5a9b01fdefabbebb0f59901 AS builder

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

# Build a static binary; nothing here needs cgo
RUN GO_VERSION=$(go version | awk '{print $3}') && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
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
# distroless static: no shell or package manager, runs as uid 65532
FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5

COPY --from=builder /build/webhook-gpu-descheduler /webhook-gpu-descheduler

USER 65532:65532

# Ports: webhook, metrics, health probes
EXPOSE 8443 8080 8081

ENTRYPOINT ["/webhook-gpu-descheduler"]

CMD ["--tls-dir=/etc/webhook/certs", "--port=8443", "--metrics-bind-address=:8080", "--scheduler-port=9395", "--sync-interval=60s", "--enable-leader-election=true"]
