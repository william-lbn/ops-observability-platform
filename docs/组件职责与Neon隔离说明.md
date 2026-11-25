# 公共观测平台组件与 Neon 隔离说明

本仓建设公共运维可观测性系统。文中的 API、Worker、数据库和页面均属于本平台，不是 Neon 的同名组件。

| 工程组件 | 部署名称 | 中文职责 | 当前边界 |
|---|---|---|---|
| `cmd/platform` 的 api 模式 | `ops-observability-api` | 观测平台接口服务：提供中文页面、当前身份授权、受限查询、证据下载、告警处理和审计 | 仅访问本平台数据库和已登记后端，入口 Service 为 `ops-api` |
| `cmd/platform` 的 worker 模式 | `ops-observability-worker` | 观测平台后台采集服务：只读目录观察、身份与新鲜度检查、告警投影、站内通知、outbox 处理 | Kubernetes 只读，不具备 Secret、exec、删除或更新权限 |
| `cmd/reference` | `ops-observability-test-reference` | 隔离验收参考应用，产生真实 HTTP 请求、指标、日志和 Trace | 测试来源，不代表实际业务系统已接入 |
| PostgreSQL | `ops-postgres` | 保存本平台原件、附件、目录、审计、告警与身份状态 | 独立实例，不使用 Neon 数据库 |
| Prometheus / Loki / Tempo | 对应 `ops-` release | 指标、日志与链路存储查询 | 使用独立发行和本平台访问边界 |
| OpenTelemetry Collector | `opentelemetry-collector-*` | 经登记的遥测接入、固定租户路由、隐私字段处理、基础设施白名单日志采集 | 存量 Service 保持原 DNS，区分工作负载名称与接入地址 |
| Grafana / Dex | 对应 `ops-` release | 平台管理分析工具与统一身份源 | 独立配置和存储；Grafana 管理入口不作为租户授权代理 |

## 哪些更新属于本任务

“观测平台接口服务／后台采集服务滚动更新”指替换本仓构建的公共平台镜像。它们与 Prometheus、Loki、Tempo、Grafana、Collector、Dex 一起构成公共观测平台。现场发行清单应记录命名空间、release、资源 UID、镜像摘要和验证结果，避免仅凭简称判断归属。

## 哪些事情仍然暂停

- Neon provider、领域接口、Neon 部署统一化和 Neon 发行资格均不在当前执行范围。
- 不部署、升级、重启、删除或接管 Neon 工作负载、数据卷和控制器。
- 不重置共享节点，不更换或启用共享 CNI/Hubble 配置，不更改共享 DNS、存储类和全局信任设置。
- `neonEnabled=true` 会被应用配置和 Chart 拒绝；CI 检查公共 Core 的依赖不导入 provider。

## 平台只读观察与业务接入的区别

集群节点资源、控制器条件、命名空间健康计数和存储绑定状态属于公共基础设施观察。平台可以只读观察这些信息，从而发现节点压力、配额不足或调度异常。即便计数中包含其他团队管理的命名空间，也不意味着接入其业务数据或获得操作权。

本档位不采集、持久化或展示 Secret 值、工作负载环境变量内容和 Neon 业务请求数据，不采集 Neon 业务日志。Kubernetes 目录仅解码和保存允许的身份、状态与健康字段，不解码 Pod Spec；Kubernetes RBAC 对 Pod 读取本身没有字段级权限，因此该采集身份属于受信任的平台采集边界。基础设施文件日志采用命名空间路径白名单。当前快照、普通遥测和历史失败数量不能证明任何 Neon 原业务操作成功或失败。

测试租户与同名参考服务仅用于证明公共平台隔离边界，不是模拟已完成的 Neon 接入。
