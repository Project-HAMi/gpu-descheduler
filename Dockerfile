# HAMi Intra-Defrag Webhook Dockerfile
# 多阶段构建：编译阶段 + 运行阶段

# ============================================
# 阶段 1: 编译 Go 二进制文件
# ============================================
FROM golang:1.26.6-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# 设置 Go 模块代理（国内用户可使用 goproxy.cn）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# 设置工作目录
WORKDIR /build

# 先复制 go.mod 和 go.sum 以利用 Docker 缓存
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建参数
ARG VERSION=dev
ARG REVISION=none
ARG BUILD_DATE=

# 构建二进制文件
# CGO_ENABLED=1: HAMi/NVML 强依赖 CGO，不可关闭
# TARGETOS/TARGETARCH 由 buildx 自动注入，每个平台用原生 gcc 编译
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
# 阶段 2: 运行时镜像（轻量级）
# ============================================
FROM debian:bookworm-slim

# 避免交互式提示
ENV DEBIAN_FRONTEND=noninteractive

# 安装基础依赖（ca-certificates 用于 HTTPS 请求）
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/* && \
    apt-get clean && \
    rm -rf /tmp/* /var/tmp/*
# 创建非 root 用户（安全最佳实践）
RUN groupadd -r hami && useradd -r -g hami -s /sbin/nologin -M hami

# 创建必要的目录
RUN mkdir -p /etc/webhook/certs && \
    chown -R hami:hami /etc/webhook/certs

# 从构建阶段复制二进制文件
COPY --from=builder /build/webhook-gpu-descheduler /webhook-gpu-descheduler

# 设置权限
RUN chmod +x /webhook-gpu-descheduler && \
    chown hami:hami /webhook-gpu-descheduler

# 切换到非 root 用户
USER hami

# 暴露端口
EXPOSE 8443 8080 8081

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8081/readyz || exit 1

# 设置环境变量
ENV TLS_DIR=/etc/webhook/certs
ENV PORT=8443
ENV METRICS_BIND_ADDRESS=:8080
ENV HEALTH_PROBE_BIND_ADDRESS=:8081

# 入口点
ENTRYPOINT ["/webhook-gpu-descheduler"]

# 默认参数
CMD ["--tls-dir=/etc/webhook/certs", "--port=8443", "--metrics-bind-address=:8080", "--scheduler-port=9395", "--sync-interval=10s", "--enable-leader-election=true"]