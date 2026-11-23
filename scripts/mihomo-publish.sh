#!/usr/bin/env bash
# 构建并推送独立镜像 homing-mihomo（多架构）
#
# 在仓库根目录执行；先 docker login。
# 复用 deploy/.env 的 HOMING_IMAGE_PREFIX，例如 yourname/。
set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE="deploy/.env"
if [ ! -f "$ENV_FILE" ]; then
  echo "未找到 deploy/.env，先复制模板：cp deploy/.env.example deploy/.env"
  exit 1
fi

read_env() {
  grep -E "^[[:space:]]*$1=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- | tr -d '"'
}

PREFIX="$(read_env HOMING_IMAGE_PREFIX)"
VERSION="$(read_env HOMING_MIHOMO_VERSION)"
VERSION="${VERSION:-1.0.0}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
PUSH_LATEST="${PUSH_LATEST:-1}"

if [ -z "$PREFIX" ]; then
  echo "请先在 deploy/.env 设置 HOMING_IMAGE_PREFIX（例如 yourname/）"
  exit 1
fi
PREFIX="${PREFIX%/}"

TAGS=(-t "${PREFIX}/homing-mihomo:${VERSION}")
if [ "$PUSH_LATEST" = "1" ]; then
  TAGS+=(-t "${PREFIX}/homing-mihomo:latest")
fi

echo ">>> ${PREFIX}/homing-mihomo:${VERSION} (${PLATFORMS})"
docker buildx build \
  --platform "${PLATFORMS}" \
  -f deploy/Dockerfile \
  --build-arg "VERSION=${VERSION}" \
  "${TAGS[@]}" \
  --push \
  .
