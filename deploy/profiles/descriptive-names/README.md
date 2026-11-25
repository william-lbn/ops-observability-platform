# 可识别名称候选

`core.yaml` 可作为自研平台和参考服务 Chart 的后置覆盖值。它只调整 Deployment 名称，保留入口 Service、RBAC、数据卷、采集身份与配置连接。Helm 直接替换 Deployment 不等同于滚动更新，应先按迁移方案验证切换顺序。

四份 Collector 覆盖值用于组合渲染或新环境。同一个 `fullnameOverride` 也可能影响 Service 和服务账号。存量环境必须额外核对旧客户端 DNS、证书 SAN、NetworkPolicy、Prometheus 抓取和已有接收器，准备可回退切换后再应用。

具体范围见[组件命名与部署精简](../../../docs/组件命名与部署精简.md)。此目录不包含现场凭据和运行状态。
