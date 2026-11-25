# 可识别名称配置

`core.yaml` 可作为自研平台和参考服务 Chart 的后置覆盖值。0.1.2 起采用独立的 Deployment selector 和稳定的 Service 路由标签。入口 Service 地址、端口、RBAC、数据卷、采集身份与配置连接保持稳定。存量迁移先给现有 Pod 补齐路由标签，预创建带 Helm 归属的新 Deployment，等目标副本就绪后再切换。后台采集服务按单实例顺序交接。

四份 Collector 覆盖值用于组合渲染或新环境。同一个 `fullnameOverride` 也会影响 Service 和服务账号。存量环境可通过已有 Harbor 渲染器，仅为本发行设置 `OPS_COLLECTOR_WORKLOAD_NAMES`，保留现有 Service 和账号，变更工作负载名称、控制器选择条件及稳定路由。渲染器校验选定上游 Chart 的模板约定，Chart 更新后需要重新检验。节点日志 DaemonSet 切换应先停止旧读取器，记录有限的采集缺口。

具体范围见[组件命名与部署精简](../../../docs/组件命名与部署精简.md)。此目录不包含现场凭据和运行状态。
