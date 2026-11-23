#!/usr/bin/env bash
# 构建独立镜像 homing-mihomo（mihomo 内核 + 自研管理台）
#
# 在仓库根目录执行；构建上下文是仓库根，Dockerfile 在 deploy/ 下。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${HOMING_MIHOMO_VERSION:-local}"
exec docker build \
  -f deploy/Dockerfile \
  --build-arg "VERSION=${VERSION}" \
  -t "homing-mihomo:${VERSION}" \
  .
