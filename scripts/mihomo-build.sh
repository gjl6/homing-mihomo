#!/usr/bin/env bash
# 构建独立镜像 homing-mihomo（mihomo 内核 + 自研管理台）
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${HOMING_MIHOMO_VERSION:-local}"
exec docker build \
  --build-arg "VERSION=${VERSION}" \
  -t "homing-mihomo:${VERSION}" \
  .
