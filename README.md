# 统一运维观测平台

面向 Kubernetes、服务与异步工作流的独立公共运维平台。使用中文工作台组织指标、日志、调用链、资源目录、事件证据、告警与平台审计。公共核心不依赖 Neon；Neon 适配器默认关闭。

当前工程采用 Go 观测平台接口服务／后台采集服务、TypeScript Web、PostgreSQL 元数据与标准开源遥测后端。它们是本仓自己的组件，详见[组件与 Neon 隔离说明](docs/组件职责与Neon隔离说明.md)。平台保存与解释观察，不接管被观测系统的执行权威。

## 阅读入口

| 需要了解 | 文档 |
|---|---|
| 架构分层、数据和模块边界 | [架构与功能](docs/架构分层与功能说明.md) |
| 开发任务、依赖与完成口径 | [开发任务](docs/开发任务与验收.md) |
| Chart、镜像、Harbor 与升级 | [发行与升级](docs/制品分发与升级管理.md) |
| 数据保护、故障与恢复 | [运维与恢复](docs/运维与恢复.md) |
| 数据真实性与当前资格 | [支持范围](docs/支持范围与资格.md) |
| 多租户、平台数据与诊断边界 | [多租户与平台级设计](docs/多租户与平台级观测设计.md) |
| 具体症状、核验顺序与恢复 | [排障手册](docs/可执行排障手册.md) |
| 工作台、Grafana、原生诊断与统一升级 | [端到端方案](docs/公共观测平台端到端方案.md) |

现场机器、内网地址、凭据和原始运行证据保存在仓外受控工作区。公开源码与现场 overlay 分离；本仓示例不包含实际登录凭据。

## 主要入口

- 运行概览：请求结果、资源效果、服务体验、证据覆盖分别解释。
- 服务目录：读取明确授权命名空间内的平台资源，保留原始 UID 和观测时刻。
- 指标与链路：按登记来源调用受限查询模板，后端不可用时返回中文错误。
- 事件证据：原字节保存、摘要、重复确认、冲突拒绝、授权下载和附件隔离。
- 告警处理：规则症状、人工确认责任和当前规则恢复分别记录。
- 授权与审计：平台管理请求与 outbox 在同一 PostgreSQL 事务中提交。
- 接入与能力：明确登记、启用、观察和运行资格，Neon 显示未接入。

## 构建与验证

已固定的构建档位：Go 1.27.4、Node 24.21.0、TypeScript 5.9.3、Helm 4.3.2；Python 3 用于离线打包和验证，镜像重写另需 PyYAML。现场联合验收与 GitHub CI 的状态分别记录，CI 通过不自动授予集群运行资格。

```sh
cd web
npm ci
npm run build
cd ..
go test ./...
mkdir -p dist
BUILD_VERSION="$(python3 tools/source-fingerprint.py --version)"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w -X main.Version=${BUILD_VERSION}" -o dist/platform ./cmd/platform
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o dist/reference ./cmd/reference
go mod vendor
python3 tools/build-images.py
```

真实 PostgreSQL 用例需要 `OPS_TEST_DATABASE_URL` 指向独立验收数据库。未设置时该项明确跳过；不把普通单元测试成功等同于数据库验收。GitHub CI 使用真实 PostgreSQL service 并执行此项，以及构建后二进制的 HTTPS/权限/原件端到端测试。

离线环境先在获准的出口主机取得固定版本与依赖校验，再使用 vendor 和已下载的前端依赖构建；安装阶段不依赖公网。

## 部署

`charts/ops-platform` 负责公共 API、Worker、参考服务、平台 PostgreSQL 和最小命名空间 RBAC。`deploy/upstream` 是标准后端配置；上游 Chart 不放入核心依赖，也不复制修改其源码。

安装必须提供完整镜像摘要、独立 Secret、证书与存储配置。Chart 拒绝启用 Neon 和使用非 SHA256 镜像身份。`tools/harbor-post-renderer.py` 将上游镜像转换为实际已验证的 Harbor 摘要，并拒绝不在离线清单中的镜像。

Helm 4 使用 `tools/helm4-postrenderer` 的 `postrenderer/v1` 插件，需设置 `HELM_PLUGINS`、`OPS_POST_RENDERER_SCRIPT` 与 `OPS_IMAGE_LOCK`，再指定 `--post-renderer ops-harbor-renderer`。不能沿用 Helm 3 的外部脚本参数方式。完整档位须显式启用网络隔离和平台只读采集，并预置采集入口的独立认证／双向 TLS Secret；不要跳过这些配置直接推断多租户已安全。

本项目暂未指定对外开源许可证。第三方组件遵循各自的许可证、NOTICE 与来源要求，发行清单应随实际选定制品一起维护。
