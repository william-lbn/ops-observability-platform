# 身份页面上游声明

`templates/` 保留自 [Dex v2.46.0 web/templates](https://github.com/dexidp/dex/tree/v2.46.0/web/templates) 的原始模板，用于本地离线运行。上游采用 Apache License 2.0，全文保留于 `LICENSE.dex`。中文覆盖、平台样式与装配配置位于本目录其他文件，不能删除上游版权及许可信息。

Dex、Grafana、Prometheus、Loki、Tempo、OpenTelemetry、PostgreSQL 和浏览器验收运行时保留各自上游镜像及许可。应用镜像额外包含 `/usr/share/licenses/ops-platform/THIRD_PARTY_NOTICES.txt`，由固定 Go vendor 闭包生成。本工程尚未另外指定自身源码的对外开源许可证。
