# 归途 Mihomo（homing-mihomo）

mihomo 内核 + 自研管理台的一体化镜像，一个容器同时提供代理端口和管理页面。
用于替代「官方 mihomo 镜像 + MetaCubeXD 另一个容器」的组合。

- 默认只开 mixed 代理端口（HTTP/SOCKS5，默认 `7890`），不设置系统代理、不改主机路由；
- TUN 默认关闭，可在设置页开启；开启需要 host 网络 + `NET_ADMIN` + `/dev/net/tun`；
- 管理台自带登录、订阅管理、策略组/节点、规则、连接、日志、设置和配置覆写；
- 代理入口支持可选的用户名/密码认证，方便安全地提供给 Homing 或 LAN 使用；
- external-controller 只监听容器内 `127.0.0.1:9091`，带随机 secret，不暴露给浏览器或局域网；
- 镜像内置 `geoip.metadb` / `geosite.dat`，首次校验/启动不需要从 GitHub 下载 MMDB；
- 订阅与运行配置保存在 `/data`，配置应用前会用 `mihomo -t` 校验，失败自动回滚。

## 快速开始

部署文件都在 `deploy/` 目录，容器数据落在仓库根的 `data/`：

```bash
cd deploy
cp .env.example .env
vi .env                  # 至少修改 ADMIN_PASSWORD
docker compose up -d
```

打开 `http://<宿主机>:8088`，用 `.env` 里的 `ADMIN_USER` / `ADMIN_PASSWORD` 登录。

默认端口：

| 用途 | 端口 |
| --- | --- |
| 管理台 | `8088` |
| mixed HTTP/SOCKS5 | `7890` |
| mihomo API | 容器内 `127.0.0.1:9091`（不对外） |

首次启动会创建一个「默认直连」配置，之后可以在「订阅」页导入订阅 URL 或上传 YAML。

> 在页面修改代理端口后，需要同步修改 compose 的端口映射（或改用 host 网络），否则宿主机映射不会自动变化。

## TUN 模式

默认 compose 使用 bridge 网络，只映射代理端口，所以 TUN 不可用。
如果希望 mihomo 接管宿主机流量，编辑 `deploy/docker-compose.yml`：

```yaml
services:
  homing-mihomo:
    network_mode: host
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    # 删除 ports:
```

然后重启容器，在「设置 → TUN」里开启 TUN。注意：

- host 网络下管理台和代理端口直接监听宿主机，注意端口冲突；
- 开启 TUN 后 mihomo 会修改宿主机路由，确认这台机器上没有别的 TUN 代理；
- 不需要 TUN 时保持默认，既有 Homing 出口页仍可通过 `7890` 使用它。

## 订阅与配置

- 支持订阅 URL 和本地 YAML 上传；
- 订阅保存到 `/data/profiles/<id>.yaml`；
- 运行配置由「订阅 + 控制台设置」生成到 `/data/runtime/config.yaml`；
- 只覆盖入口、API、模式、TUN、DNS 等控制台字段，订阅里的节点、策略组、规则、provider 原样保留；
- 应用流程：生成配置 → `mihomo -t` 校验 → 原子写入 → 重启内核 → 健康检查 → 失败回滚；
- 配置备份在 `/data/backups/`。

「设置 → 配置覆写」可以填写一段 YAML，深度合并到订阅配置之上，用来补节点、策略组、
规则或 provider；列表字段会整体替换。入口端口、API、TUN、DNS 等控制台字段仍由页面强制覆盖。

## 构建

在仓库根目录构建（构建上下文是仓库根，Dockerfile 在 `deploy/` 下）：

```bash
docker build -f deploy/Dockerfile -t homing-mihomo:1.0.0 .
```

多架构：

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deploy/Dockerfile \
  --build-arg MIHOMO_IMAGE=metacubex/mihomo:v1.19.34 \
  -t yourname/homing-mihomo:1.0.0 --push .
```

也可以直接用脚本：`scripts/mihomo-build.sh`（本地构建）、
`scripts/mihomo-publish.sh`（多架构推送，读取 `deploy/.env`）。

构建时会从 `metacubex/mihomo` 镜像取 `/mihomo` 静态二进制；离线环境先 `docker pull` 或预打同名 tag。

## 目录结构

```text
.
├─ cmd/homing-mihomo/   进程入口，只做装配：env、目录、secret、设置、存储、渲染、
│                       启动内核守护与 HTTP 服务、信号处理
├─ internal/config/     控制台设置、订阅配置渲染（YAML 覆盖/TUN/DNS）、通用文件工具
├─ internal/kernel/     mihomo external-controller 客户端 + 内核进程守护与日志环
├─ internal/profiles/   订阅存储：URL 下载、YAML 校验、激活状态
├─ internal/console/    管理台 HTTP API、登录会话、配置应用/回滚
├─ web/                 ui.html 与 go:embed 封装（前端打进二进制）
├─ deploy/              Dockerfile、docker-compose.yml、.env.example
├─ scripts/             镜像本地构建与多架构发布脚本
├─ geodata/             内置 geoip.metadb / geosite.dat
└─ vendor/              锁定依赖（构建统一用 -mod=vendor）
```

## 与 Homing 的关系

Homing 的「出口」页只负责把 VPN 流量送到代理地址/端口：

- 本机 TUN：宿主机已有 mihomo TUN；
- 远程代理：填写本控制台的 `7890`（支持 SOCKS5/HTTP 和认证）；
  如果控制台设置了代理认证，在 Homing 出口页填同样的用户名/密码；
- 本控制台独立部署、独立升级，不依赖 Homing 镜像。

## 许可证

本子项目自身代码随仓库按 GPL-2.0-only 发布；镜像内置的 mihomo 内核为 GPL-3.0，
源码见 <https://github.com/MetaCubeX/mihomo>，重新分发镜像时请保留对应源码链接。

## 安全

- 不要把管理台直接暴露到公网；建议只开在 LAN 或通过反向代理加 HTTPS；
- `ADMIN_PASSWORD` 只在首次启动时使用，之后以 `/data/admin.json` 为准；
- 订阅 URL 可能含密钥，只保存在服务端 `/data`，日志不输出完整 URL；
- 浏览器只访问本控制台，不直接访问 mihomo API。
