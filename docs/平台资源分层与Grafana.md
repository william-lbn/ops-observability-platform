# 平台资源分层与 Grafana

公共资源运行视图以工作台为日常入口，以 Grafana 作为工程师的趋势分析入口。工作台展示登记覆盖、观察时刻、节点与配额、需核查事项。Grafana 将时间窗、真实样本、节点和数据源组织在六张仪表板中。两处都保留空数据与正常零值的区别。

## 资源层和部署依赖

| 层 | 运行责任 | 当前可验证数据 | 仍需单独核实 |
| --- | --- | --- | --- |
| 节点与计算 | Linux/amd64 节点、CPU、内存、系统预留、设备登记 | 登记节点、Ready、压力、可分配量、主机实时用量、常规容器请求、KVM 扩展资源登记 | KVM、NUMA 和虚拟机运行资格，全量调度开销与可放置预算 |
| 调度与配额 | 节点可调度性、命名空间配额、卷绑定 | 节点封锁、纳管命名空间 Pending Pod 和 PVC、实际配额使用 | Neon 亲和性、污点、设备结算与业务请求放置由各权威系统验证 |
| 存储 | 节点文件系统、PVC、PV、存储类和回收策略 | 标称容量、申请量、绑定阶段、节点文件系统与磁盘忙碌时间 | 单卷实际使用字节、对象存储可用性与跨节点数据恢复 |
| 网络和 DNS | 节点网卡、CoreDNS、现有集群网络 | 选取节点后对比近 15 分钟收发流量最高的三个接口、各接口发生的错误、CoreDNS 响应码 | 服务之间的路径与拒绝原因，按场景审核 Hubble。共享 CNI 交由原负责人 |
| 公共依赖 | API Server 可用性、内网制品仓、证书生命周期 | 现场只读预检与保管证据；页面展示连续采集的目标和投递队列 | 尚未登记的依赖应呈现未观测。平台公共探针与业务实例维护状态分别登记 |
| 采集可用性 | Prometheus、目录采集、OTLP 导出和节点日志 | 有效抓取样本、最近采集年龄、按节点和出口的队列与失败 | 预期覆盖由工作台登记独立核对，目标发现数量不是完整性证明 |

已上报的 KVM 资源可用于部署前筛选候选节点。实际放置还需要 Kubernetes 调度条件、可消耗的资源数量与虚拟机运行验证。使用 `local-path` 时，节点与数据绑定，需要按存储恢复方案验证故障后的跨节点恢复能力。

## 告警与维护边界

副本失败、启动循环和卷等待诊断只覆盖公共平台、Kubernetes 系统组件和证书组件。普通工作负载元数据进入「维护及其他仅观察的命名空间」。Neon 的部署、卸载和升级期间可看到只读观察，但不因业务副本或声明数量改变就报公共平台故障。旧告警单保留历史，规则范围调整不改写旧证据。节点健康、存储和数据新鲜度继续独立核验。

受控维护验收在临时测试命名空间创建独立待绑定声明，验证工作台能看见观察值，且该命名空间不产生故障诊断。Prometheus 规则另用同样的副本失败样本分别检验纳管和维护命名空间。

## 部署 Grafana 仪表板

输入为现有、已认证并限定工程师访问的 Grafana Prometheus 数据源 UID。安装前在现场用只读 API 核对它。执行环境使用 Python 3.12 或更高版本。

```sh
python3 tools/platform-dashboards.py --datasource-uid "$PLATFORM_PROM_UID" \
  --format configmap > dashboards.configmap.json
python3 tools/platform-dashboards.py --datasource-uid "$PLATFORM_PROM_UID" \
  --format helm > dashboards.values.json
```

先在目标发行命名空间预检并创建版本化的不可变 ConfigMap，再将 Helm 覆盖值合入原有 Grafana 发行。服务地址、镜像摘要、持久卷绑定与原配置按实况比较，模板中使用 Kubernetes 查询的位置使用服务端 dry-run 复核。Chart 的 `dashboardsConfigMaps` 直接挂载本地 JSON，避免额外下载容器和外网资产。更改内容时使用新的 ConfigMap 名称，完成回退窗口后再归档未使用版本。

工作台的租户账号按当前授权访问受限查询。Grafana 工程入口走受控接入和独立身份，供有权操作相关数据源的人员使用。Grafana 站点默认使用 `zh-Hans`。现有用户可以保留自己的语言偏好。验收同时检查原生菜单、六张仪表板的内容、单位、链接、实际样本和存储表格。

在具备 Kubernetes 授权的运维终端可转发 Grafana 服务到回环地址。

```sh
kubectl -n ops-observability port-forward --address 127.0.0.1 svc/ops-grafana 13000:80
```

真实联合验收应对照：Prometheus 查询结果、Grafana 登录与图表、浏览器实际渲染、平台只读和租户负向、维护观察、镜像及卷身份。升级后另核对 Grafana 首页、面板样本、新旧视觉截图和运行告警。

Grafana 13.3.2 的瞬时向量可以返回多个数据帧。资源表依次展开标签、合并数据帧和整理字段，使不同节点或卷在同一张表中逐行显示。浏览器验收要求多条真实记录同时可见。[Grafana 13.3.2 标签转换实现](https://github.com/grafana/grafana/blob/v13.3.2/packages/grafana-data/src/transformations/transformers/labelsToFields.ts)

`deploy/public-artifacts.lock.json` 记录当前应用制品和 Grafana 配置版本。`application` 对应当前应用源码和镜像，`offlineBuildTools.bundledApplicationSourceSHA256` 标识工具载体内附源码的历史版本。离线构建当前版本时使用本版本源码和已有锁定工具，并校验 `go.mod`、`go.sum`、源码指纹及容器归档摘要。
