# 归途 Mihomo：mihomo 内核 + 自研管理台，单镜像单容器。
#
# 构建（在仓库根目录）：
#   docker build -t homing-mihomo:1.0.0 .
#
# 多架构：
#   docker buildx build --platform linux/amd64,linux/arm64 -t yourname/homing-mihomo:1.0.0 .
#
# 可固定内核版本：
#   --build-arg MIHOMO_IMAGE=metacubex/mihomo:v1.19.34

ARG MIHOMO_IMAGE=metacubex/mihomo:v1.19.34

# ---------- mihomo 内核 ----------
FROM ${MIHOMO_IMAGE} AS mihomo

# ---------- 管理台 / BFF ----------
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY *.go ui.html ./
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=vendor -trimpath -ldflags "-s -w" -o /out/homing-mihomo .

# ---------- 运行 ----------
FROM alpine:3.20
ARG VERSION=dev
LABEL org.opencontainers.image.title="Homing Mihomo" \
      org.opencontainers.image.description="Mihomo kernel with a self-hosted management console" \
      org.opencontainers.image.source="https://github.com/gjl6/homing-mihomo" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="GPL-2.0-only"

RUN apk add --no-cache ca-certificates tzdata iproute2
COPY --from=build /out/homing-mihomo /usr/local/bin/homing-mihomo
COPY --from=mihomo /mihomo /usr/local/bin/mihomo
COPY geodata /opt/mihomo-geodata

ENV DATA_DIR=/data \
    ADMIN_LISTEN=:8088 \
    MIHOMO_BIN=/usr/local/bin/mihomo \
    GEODATA_DIR=/opt/mihomo-geodata

VOLUME ["/data"]
EXPOSE 8088 7890
HEALTHCHECK --interval=20s --timeout=3s --retries=3 --start-period=15s \
  CMD wget -q -O- http://127.0.0.1:8088/healthz || exit 1
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/homing-mihomo"]
