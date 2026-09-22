#!/bin/bash

# =====================================================
# GPU Descheduler Webhook Image Build Script
# Usage:
#   ./build-image.sh [options]
#
# Options:
#   -t, --tag <tag>          Image tag (default: v2.5.1)
#   -r, --registry <url>     Image registry URL (optional)
#   -p, --platform <arch>    Target platform (linux/amd64, linux/arm64, or all)
#   -v, --version <ver>      Version number (for ldflags)
#   --push                   Push image after build
#   --no-cache               Build without cache
#   -h, --help               Show help info
#
# Examples:
#   # Build amd64 image locally
#   ./build-image.sh -t v2.5.1
#
#   # Build and push to GHCR
#   ./build-image.sh -t v2.5.1 -r ghcr.io/project-hami --push
#
#   # Build multi-arch image
#   ./build-image.sh -t latest -p all -r ghcr.io/project-hami --push
# =====================================================

set -e

# Defaults
TAG="v0.0.2"
REGISTRY=""
PLATFORM="linux/amd64"
VERSION="${TAG}"
PUSH=false
NO_CACHE=""
DOCKERFILE="Dockerfile"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

show_help() {
    cat << EOF
Usage: $0 [options]

Build HAMi Intra-Defrag Webhook Docker image

Options:
  -t, --tag <tag>          Image tag (default: ${TAG})
  -r, --registry <url>     Image registry URL prefix (e.g., ghcr.io/project-hami)
  -p, --platform <arch>    Target platform:
                            linux/amd64  (default, x86_64)
                            linux/arm64  (ARM64, e.g., Apple Silicon)
                            all           (multi-arch)
  -v, --version <ver>      Version number injected into binary (default: same as tag)
  --push                   Push image to registry after build
  --no-cache               Build without Docker cache
  --use-buildx             Use docker buildx (recommended for multi-arch builds)
  -h, --help               Show this help message

Examples:
  # Basic usage - build locally
  $0 -t v2.5.1

  # Push to GitHub Container Registry
  $0 -t v2.5.1 -r ghcr.io/project-hami --push

  # Build multi-arch image and push
  $0 -t latest -p all -r ghcr.io/project-hami --push --use-buildx

  # Dev mode - no cache
  $0 -t dev --no-cache
EOF
    exit 0
}

# Parse command line args
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -r|--registry)
            REGISTRY="$2/"
            shift 2
            ;;
        -p|--platform)
            PLATFORM="$2"
            if [ "$PLATFORM" = "arm64" ]; then
                PLATFORM="linux/arm64"
                DOCKERFILE="Dockerfile.arm64"
            elif [ "$PLATFORM" = "amd64" ]; then
                PLATFORM="linux/amd64"
                DOCKERFILE="Dockerfile"
            elif [ "$PLATFORM" != "all" ] && [ "$PLATFORM" != "linux/amd64" ] && [ "$PLATFORM" != "linux/arm64" ]; then
                log_error "Unsupported platform: $PLATFORM"
                log_error "Supported values: amd64, arm64, all, linux/amd64, linux/arm64"
                exit 1
            fi
            shift 2
            ;;
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        --push)
            PUSH=true
            shift
            ;;
        --no-cache)
            NO_CACHE="--no-cache"
            shift
            ;;
        --use-buildx)
            USE_BUILDX=true
            shift
            ;;
        -h|--help)
            show_help
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            ;;
    esac
done

# Get Git info
if command -v git &> /dev/null && [ -d "${PROJECT_ROOT}/.git" ]; then
    REVISION=$(git -C "${PROJECT_ROOT}" rev-parse --short HEAD 2>/dev/null || echo "unknown")
else
    REVISION="unknown"
fi

BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build image name
IMAGE_NAME="${REGISTRY}projecthami/gpu-descheduler-webhook:${TAG}"

log_info "=========================================="
log_info "Building GPU Descheduler Webhook Image"
log_info "=========================================="
echo ""
log_info "Image Name:    ${IMAGE_NAME}"
log_info "Platform:      ${PLATFORM}"
log_info "Version:       ${VERSION}"
log_info "Git Revision:  ${REVISION}"
log_info "Build Date:    ${BUILD_DATE}"
log_info "Dockerfile:    ${DOCKERFILE}"
if [ "$PUSH" = true ]; then
    log_info "Push:          Yes"
fi
if [ -n "$NO_CACHE" ]; then
    log_info "Cache:         No"
fi
echo ""

# Check Docker availability
if ! command -v docker &> /dev/null; then
    log_error "Docker not installed or not in PATH"
    exit 1
fi

# For multi-arch build without buildx, suggest enabling it
if [ "$PLATFORM" = "all" ] && [ "$USE_BUILDX" != "true" ]; then
    log_warn "Multi-arch build recommends --use-buildx option"
    log_info "Enabling buildx..."
    USE_BUILDX=true
fi

cd "${PROJECT_DIR:-${SCRIPT_DIR}}"

# Build image
if [ "$USE_BUILDX" = "true" ] || [ "$PLATFORM" = "all" ]; then
    log_info "Using Docker BuildX..."

    # Ensure buildx is available
    if ! docker buildx version &> /dev/null; then
        log_error "docker buildx not available"
        log_error "Enable it first: docker buildx create --use"
        exit 1
    fi

    BUILD_CMD="docker buildx build ${NO_CACHE} \
        --platform ${PLATFORM} \
        --build-arg VERSION=${VERSION} \
        --build-arg REVISION=${REVISION} \
        --build-arg BUILD_DATE=\"${BUILD_DATE}\" \
        -t ${IMAGE_NAME} \
        -f ${SCRIPT_DIR}/${DOCKERFILE} \
        ${PROJECT_ROOT}"

    if [ "$PUSH" = true ]; then
        BUILD_CMD="${BUILD_CMD} --push"
    else
        # Load to local when not pushing
        BUILD_CMD="${BUILD_CMD} --load"
        # Note: multi-arch without push cannot load, only single-arch
        if [ "$PLATFORM" = "all" ]; then
            log_error "Error: multi-arch build requires --push"
            exit 1
        fi
    fi

    eval ${BUILD_CMD}
else
    log_info "Using traditional Docker build..."

    docker build ${NO_CACHE} \
        --platform ${PLATFORM} \
        --build-arg VERSION=${VERSION} \
        --build-arg REVISION=${REVISION} \
        --build-arg BUILD_DATE="${BUILD_DATE}" \
        -t ${IMAGE_NAME} \
        -f ${SCRIPT_DIR}/${DOCKERFILE} \
        ${PROJECT_ROOT}
fi

# Check build result
if [ $? -eq 0 ]; then
    echo ""
    log_info "Image build succeeded!"
    log_info "Image info:"
    echo ""

    if [ "$USE_BUILDX" != "true" ] || [ "$PLATFORM" != "all" ]; then
        docker images | grep "gpu-descheduler-webhook" | grep "${TAG}" || true
    fi

    echo ""
    log_info "Image tag: ${IMAGE_NAME}"

    if [ "$PUSH" = false ]; then
        echo ""
        log_info "To push to registry, run:"
        echo "  docker push ${IMAGE_NAME}"
        echo ""
        log_info "Or rebuild and push with this script:"
        echo "  $0 -t ${TAG} -r ${REGISTRY%/} --push"
    else
        echo ""
        log_info "Image pushed to registry!"
    fi

    echo ""
    log_info "=========================================="
    log_info "Update Helm Chart image config:"
    log_info "=========================================="
    echo ""
    echo "In chart/values.yaml, modify:"
    echo ""
    echo "image:"
    echo "  repository: projecthami/gpu-descheduler-webhook"
    echo "  tag: \"${TAG}\""
    echo ""
else
    log_error "Image build failed!"
    exit 1
fi