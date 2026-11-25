#!/usr/bin/env python3
"""Generate the Grafana Helm overlay for the platform resource views."""

import argparse
import json
import re
import sys


OWNER = 'ops_tenant="platform"'
MANAGED = 'namespace=~"ops-observability|kube-system|cert-manager"'
FILESYSTEM = 'fstype!~"tmpfs|overlay|squashfs|nsfs|proc|sysfs|cgroup.*",mountpoint!~"^/(proc|sys|run|dev)($|/).*"'
INTERFACE = 'device!="lo",node=~"${node:regex}"'
UID_PREFIX = "ops-resources-"
FOLDER = "platform-resources"
DIRECTORY = f"/var/lib/grafana/dashboards/{FOLDER}"
VERSION = 3
CONFIG_MAP = f"ops-platform-resource-dashboards-v{VERSION}"


def metric(name, selectors=""):
    extra = "," + selectors if selectors else ""
    return name + "{" + OWNER + extra + "}"


def panel(kind, title, expression, description, unit="short", legend="", thresholds=None):
    return {
        "type": kind,
        "title": title,
        "description": description,
        "expression": expression,
        "unit": unit,
        "legend": legend,
        "thresholds": thresholds,
    }


def text_panel(title, content):
    return {"type": "text", "title": title, "content": content}


def request_total(resource):
    requests = metric("kube_pod_container_resource_requests", f'resource="{resource}",node!=""')
    active = metric("kube_pod_status_phase", 'phase=~"Pending|Running"')
    return f"sum by(node) ({requests} * on(namespace,pod) group_left max by(namespace,pod) ({active}))"


def table_value(labels, expression):
    return f"max by({labels}) ({expression})"


def views():
    ready = metric("kube_node_status_condition", 'condition="Ready",status="true"')
    up = metric("up")
    pvc_pending = metric("kube_persistentvolumeclaim_status_phase", 'phase="Pending",' + MANAGED)
    allocated = lambda resource: metric("kube_node_status_allocatable", f'resource="{resource}"')
    node_cpu = f'100 * (1 - avg by(node) (rate({metric("node_cpu_seconds_total", "mode=\"idle\"")}[5m])))'
    mem = metric("node_memory_MemAvailable_bytes")
    total_mem = metric("node_memory_MemTotal_bytes")
    memory_pct = f"100 * (1 - {mem} / {total_mem})"
    fs_available = metric("node_filesystem_avail_bytes", FILESYSTEM)
    fs_size = metric("node_filesystem_size_bytes", FILESYSTEM)
    kvm = metric("kube_node_status_allocatable", 'resource=~"neonvm[/_]kvm"')
    rx = metric("node_network_receive_bytes_total", INTERFACE)
    tx = metric("node_network_transmit_bytes_total", INTERFACE)
    busiest = f"topk by(node) (3, rate({rx}[15m] @ end()) + rate({tx}[15m] @ end()))"
    ranked_interfaces = f"and on(node,device) {busiest}"
    return [
        (
            "home", "公共平台资源总览", "节点与数据可用性一览",
            [
                [panel("stat", "已发现节点", "count(" + metric("kube_node_info") + ")", "来自 Kubernetes 节点对象。登记覆盖请与中文工作台核对。"),
                 panel("stat", "就绪节点", f"sum({ready})", "Ready 条件由 Kubernetes 上报。"),
                 panel("stat", "公共抓取可达", f"sum({up})", "仅公共平台数据域。", legend="可达"),
                 panel("stat", "公共抓取目标", f"count({up})", "分母随抓取发现配置变化，不能代表业务系统的完整覆盖。")],
                [panel("timeseries", "各节点 CPU 使用率", node_cpu, "5 分钟速率，适合判断持续压力。", "percent", "{{node}}"),
                 panel("timeseries", "各节点内存使用率", memory_pct, "按主机可用内存估算，与 Kubernetes 请求量各自独立。", "percent", "{{node}}")],
                [text_panel("从哪里开始", """- 调度失败：进入「调度与配额」，核对节点可调度性和运行档要求。
- PVC 等待：进入「存储与持久卷」，同时核对卷状态与节点磁盘。
- DNS 或连接异常：进入「网络与 DNS」，先核对时间窗，再缩小到具体节点。
- 数据不齐：进入「采集与可用性」，检查抓取和采集队列。

Neon 部署、卸载和更新只作为维护观察。节点、DNS、存储和采集链路仍在公共平台诊断范围。""")],
            ],
        ),
        (
            "nodes", "节点与计算资源", "评估节点容量、真实用量和压力",
            [
                [panel("timeseries", "CPU 使用率", node_cpu, "5 分钟平均负载，不把单次尖峰当作持续压力。", "percent", "{{node}}"),
                 panel("timeseries", "内存使用率", memory_pct, "按主机可用内存估算。", "percent", "{{node}}")],
                [panel("table", "分节点可分配 CPU", table_value("node", allocated("cpu")), "使用 Kubernetes 扣除系统预留后公布的可分配量评估部署请求。", "suffix: 核"),
                 panel("table", "分节点可分配内存", table_value("node", allocated("memory")), "Kubernetes 可分配内存已扣除为系统预留的部分。", "bytes")],
                [panel("table", "活动 Pod 的常规容器 CPU 请求", request_total("cpu"), "同节点 Running 或 Pending 工作负载的常规容器请求。该值未计入 Init 容器与 Pod 开销。", "suffix: 核"),
                 panel("table", "活动 Pod 的常规容器内存请求", request_total("memory"), "请求量与实时用量、实际可放置余量分别使用。", "bytes")],
                [panel("table", "节点压力条件", table_value("node,condition", metric("kube_node_status_condition", 'status="true",condition=~"MemoryPressure|DiskPressure|PIDPressure|NetworkUnavailable"')), "值为 1 表示对应条件生效。缺样本需检查来源。"),
                 panel("table", "KVM 可分配资源登记", table_value("node,resource", kvm), "读取 Kubernetes 扩展资源登记。设备可用性和来宾虚拟机运行需独立验证。")],
            ],
        ),
        (
            "storage", "存储与持久卷", "在提交部署前区分卷声明、绑定与主机空间",
            [
                [panel("stat", "平台待绑定 PVC", f"sum({pvc_pending})", "此处只统计公共平台纳管命名空间。维护工作负载的声明不会触发该项。", thresholds=(1, 3)),
                 panel("stat", "已绑定持久卷", f'sum({metric("kube_persistentvolume_status_phase","phase=\"Bound\"")})', "集群通用元数据。Retain 的 Released 卷与绑定容器分开查。"),
                 panel("stat", "PV 标称总容量", f'sum({metric("kube_persistentvolume_capacity_bytes")})', "卷对象中的容量，含非绑定卷，不是后端可用空间。", "bytes")],
                [panel("timeseries", "节点文件系统使用率", f"100 * (1 - {fs_available} / {fs_size})", "排除虚拟文件系统，观察节点与挂载点。", "percent", "{{node}} · {{mountpoint}}"),
                 panel("timeseries", "节点磁盘忙碌时间比例", f'100 * rate({metric("node_disk_io_time_seconds_total", "device!~\"^(loop|ram).*\"")}[5m])', "每设备忙碌时间比例，不等同于 IOPS 或磁盘时延。", "percent", "{{node}} · {{device}}")],
                [panel("table", "平台 PVC 申请大小", table_value("namespace,persistentvolumeclaim", metric("kube_persistentvolumeclaim_resource_requests_storage_bytes", MANAGED)), "只列公共平台纳管命名空间。申请大小与实际已用容量独立。", "bytes"),
                 panel("table", "卷生命周期", table_value("persistentvolume,phase", f'{metric("kube_persistentvolume_status_phase")} == 1'), "绑定、保留、释放均按 Kubernetes 对象状态呈现。")],
                [text_panel("还需要什么证据", "本档位提供节点文件系统与 PV/PVC 元数据。单卷实际已用字节、对象存储可用性和跨节点恢复需要独立来源。")],
            ],
        ),
        (
            "network", "网络与 DNS", "核对节点接口、解析压力与未覆盖路径",
            [
                [panel("timeseries", "节点接口接收吞吐", f"rate({rx}[5m]) {ranked_interfaces}", "按窗口末端近 15 分钟收发流量为每节点选出前三个接口，避免在集群层重复累加虚拟接口流量。", "Bps", "{{node}} · {{device}}"),
                 panel("timeseries", "节点接口发送吞吐", f"rate({tx}[5m]) {ranked_interfaces}", "与接收面板使用同一组接口，便于比较。", "Bps", "{{node}} · {{device}}")],
                [panel("timeseries", "接口接收与发送错误率", f'(rate({metric("node_network_receive_errs_total", INTERFACE)}[5m]) + rate({metric("node_network_transmit_errs_total", INTERFACE)}[5m])) > 0', "仅绘制发生错误的接口。空图时与上方吞吐及采集可用性联读，区分未发生错误和缺采样。", "suffix: 次/秒", "{{node}} · {{device}}"),
                 panel("timeseries", "CoreDNS 响应码", f'sum by(rcode) (rate({metric("coredns_dns_responses_total")}[5m]))', "检查 SERVFAIL/REFUSED 的具体窗口；缺少某类响应码可能是未发生或尚无采样。", "suffix: 次/秒", "{{rcode}}")],
                [text_panel("观察范围", "节点接口和 CoreDNS 可用于基础排障。服务间流向、策略拒绝路径与跨节点延迟需要独立探针或经批准的 Hubble 试点。")],
            ],
        ),
        (
            "schedule", "调度与配额", "区分调度条件、容量与部署维护",
            [
                [panel("stat", "不可调度节点", f'sum({metric("kube_node_spec_unschedulable")})', "包含管理员主动封锁的节点。先核对维护记录。", thresholds=(1, 3)),
                 panel("stat", "平台 Pending Pod", f'sum({metric("kube_pod_status_phase","phase=\"Pending\"," + MANAGED)})', "范围仅为公共平台、系统服务、证书组件；值包含正常启动队列。", thresholds=(1, 5)),
                 panel("stat", "平台待绑定 PVC", f"sum({pvc_pending})", "与调度观察联读，确认是卷、配额还是节点条件。", thresholds=(1, 3))],
                [panel("table", "节点封锁状态", table_value("node", metric("kube_node_spec_unschedulable")), "1 表示禁止新调度。现有 Pod 未必停止。"),
                 panel("table", "可调度节点可分配 CPU", table_value("node", f'{allocated("cpu")} and on(node) ({metric("kube_node_spec_unschedulable")} == 0)'), "候选节点的可分配量。部署前还需核对污点、亲和性、设备、存储类和系统预留。", "suffix: 核")],
                [text_panel("配额与准入", "当前命名空间配额 hard/used 在中文工作台从 Kubernetes 只读目录读取。KVM、NUMA 和虚拟化设备资格需要独立验证。业务维护过程中负载数量的变化可影响容量观察，业务工作负载本身不触发平台副本故障。")],
            ],
        ),
        (
            "pipeline", "采集与可用性", "先确认数据是否可信，再解释指标",
            [
                [panel("stat", "节点采集可达", f'sum({metric("up","job=\"nodes\"")})', "按节点 exporter 读取成功数。"),
                 panel("stat", "平台目录观察年龄", f'time() - max({metric("ops_worker_last_success_timestamp_seconds")})', "秒数超过 90 表示需要检查来源或采集，缺少样本为未知。", "s", thresholds=(60, 90)),
                 panel("stat", "日志采集器可达", f'sum({metric("up","job=\"infrastructure-logs\"")})', "仅基础设施白名单采集器。")],
                [panel("timeseries", "各类抓取目标可达比例", f"100 * avg by(job) ({up})", "目标分母基于当前发现配置。与登记覆盖分别检查。", "percent", "{{job}}"),
                 panel("timeseries", "导出队列占用率", f'100 * ({metric("otelcol_exporter_queue_size")} / ({metric("otelcol_exporter_queue_capacity")} > 0))', "按实例和导出器观察，空样本不填零。", "percent", "{{instance}} · {{exporter}}")],
                [panel("table", "抓取目标明细", table_value("job,instance", up), "1 可达，0 不可达。缺少样本时先核对发现配置。")],
            ],
        ),
    ]


def render(dashboard, datasource_uid):
    key, title, summary, rows = dashboard
    datasource = {"type": "prometheus", "uid": datasource_uid}
    dashboard_links = [
        {"title": title, "type": "link", "url": f"/d/{UID_PREFIX}{key}", "includeVars": False, "keepTime": True}
        for key, title, _, _ in views()
    ]
    panels, y = [], 0
    for row in rows:
        width = 24 // len(row)
        height = 5 if all(v["type"] == "stat" for v in row) else 9
        for column, value in enumerate(row):
            kind = value["type"]
            base = {
                "id": len(panels) + 1, "type": kind, "title": value["title"],
                "gridPos": {"x": column * width, "y": y, "w": width, "h": height},
                "transparent": False,
            }
            if kind == "text":
                base["options"] = {"mode": "markdown", "content": value["content"]}
                panels.append(base)
                continue
            base.update(description=value["description"], datasource=datasource)
            instant = kind in ("stat", "table")
            base["targets"] = [{
                "refId": "A", "expr": value["expression"], "datasource": datasource,
                "range": not instant, "instant": instant, "legendFormat": value["legend"],
                "format": "time_series",
            }]
            defaults = {"unit": value["unit"], "noValue": "未取得样本", "color": {"mode": "palette-classic"}}
            if value["thresholds"]:
                warn, bad = value["thresholds"]
                defaults["color"] = {"mode": "thresholds"}
                defaults["thresholds"] = {
                    "mode": "absolute", "steps": [
                        {"value": None, "color": "green"}, {"value": warn, "color": "orange"}, {"value": bad, "color": "red"},
                    ],
                }
            base["fieldConfig"] = {"defaults": defaults, "overrides": []}
            if kind == "stat":
                base["options"] = {
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "value" if value["thresholds"] else "none", "graphMode": "none", "textMode": "value",
                }
            elif kind == "timeseries":
                defaults["custom"] = {"lineWidth": 2, "fillOpacity": 12, "showPoints": "never", "spanNulls": False}
                base["options"] = {
                    "legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                    "tooltip": {"mode": "multi", "sort": "desc"},
                }
            elif kind == "table":
                base["options"] = {"showHeader": True}
                labels = {"node": "节点", "namespace": "命名空间", "resource": "扩展资源",
                          "persistentvolume": "持久卷", "persistentvolumeclaim": "持久卷声明",
                          "phase": "阶段", "condition": "条件", "storageclass": "存储类",
                          "job": "抓取任务", "instance": "抓取端点", "Value": "当前值", "Value #A": "当前值"}
                base["transformations"] = [
                    {"id": "labelsToFields", "options": {"mode": "columns"}},
                    {"id": "merge", "options": {}},
                    {
                        "id": "organize", "options": {
                            "excludeByName": {"Time": True, "__name__": True, "ops_tenant": True},
                            "indexByName": {"node": 0, "namespace": 0, "persistentvolume": 0,
                                            "job": 0, "persistentvolumeclaim": 1, "phase": 1,
                                            "resource": 1, "instance": 1, "Value": 2, "Value #A": 2},
                            "renameByName": labels,
                        },
                    },
                ]
            panels.append(base)
        y += height
    result = {
        "uid": UID_PREFIX + key, "title": title, "description": summary,
        "schemaVersion": 39, "version": VERSION, "editable": False, "tags": ["公共资源", "平台运维"],
        "timezone": "Asia/Shanghai", "time": {"from": "now-1h", "to": "now"},
        "refresh": "1m", "links": dashboard_links, "panels": panels,
    }
    if key == "network":
        node_query = f'label_values({metric("kube_node_info")}, node)'
        result["templating"] = {"list": [{
            "name": "node", "label": "节点", "type": "query", "datasource": datasource,
            "query": {"query": node_query, "refId": "NodeVariable"}, "definition": node_query,
            "refresh": 1, "sort": 1, "multi": True, "includeAll": True,
            "current": {"text": "全部", "value": "$__all"},
        }]}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--datasource-uid", required=True)
    parser.add_argument("--format", choices=("helm", "dashboards", "configmap"), default="helm")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9_-]{1,40}", args.datasource_uid):
        parser.error("invalid datasource UID")
    dashboards = [render(d, args.datasource_uid) for d in views()]
    if args.format == "dashboards":
        output = dashboards
    elif args.format == "configmap":
        output = {
            "apiVersion": "v1", "kind": "ConfigMap", "immutable": True,
            "metadata": {"name": CONFIG_MAP, "labels": {
                "app.kubernetes.io/part-of": "ops-observability-platform",
                "app.kubernetes.io/component": "grafana-resource-dashboards",
            }},
            "data": {d["uid"] + ".json": json.dumps(d, ensure_ascii=False) for d in dashboards},
        }
    else:
        output = {
            "grafana.ini": {
                "users": {"default_language": "zh-Hans"},
                "dashboards": {"default_home_dashboard_path": DIRECTORY + "/" + UID_PREFIX + "home.json"},
            },
            "dashboardProviders": {"dashboardproviders.yaml": {"apiVersion": 1, "providers": [{
                "name": FOLDER, "orgId": 1, "folder": "公共平台资源", "folderUid": "ops-resource-folder",
                "type": "file", "disableDeletion": False, "updateIntervalSeconds": 30, "allowUiUpdates": False,
                "options": {"path": DIRECTORY},
            }]}},
            "dashboardsConfigMaps": {FOLDER: CONFIG_MAP},
        }
    json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
