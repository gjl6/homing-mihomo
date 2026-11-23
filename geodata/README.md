# 内置 geodata

这两个文件用于让 mihomo 在没有外网/GitHub 访问时也能直接使用 `GEOIP` / `GEOSITE` 规则：

- `geoip.metadb`
- `geosite.dat`

来源：MetaCubeX meta-rules-dat（<https://github.com/MetaCubeX/meta-rules-dat>）。
镜像启动时，如果 `/data/mihomo/` 下缺少文件（或文件明显不完整），会复制到这里的一份；
已有完整数据不会被覆盖，mihomo 的自动更新逻辑仍然有效。

如需更新，替换这两个文件后重新构建镜像即可。
