#!/usr/bin/env python3
"""Read Helm and Kubernetes state for the public observability components."""

import argparse
from collections import defaultdict
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import subprocess
import sys


OWNER = "ops-observability-platform"
PINNED = re.compile(r"@sha256:[a-f0-9]{64}$")
CONTROLLERS = {"Deployment", "StatefulSet", "DaemonSet"}

# Component service names describe the public platform profile. The release
# label or annotation is still required before a workload is attributed.
COMPONENTS = {
    "ops-platform": ("中文工作台、采集与元数据", ["ops-api", "ops-postgres"]),
    "ops-prometheus": ("指标、规则与抓取目标", ["ops-prometheus", "ops-alertmanager", "ops-node-exporter", "ops-kube-state-metrics"]),
    "ops-loki": ("日志存储与查询", ["ops-loki"]),
    "ops-tempo": ("调用链存储与查询", ["ops-tempo"]),
    "ops-grafana": ("专家仪表板与 Explore", ["ops-grafana"]),
    "ops-dex": ("OIDC 身份", ["ops-dex"]),
    "ops-collector": ("平台 OTLP 接入", ["ops-collector"]),
    "ops-collector-a": ("测试租户 A OTLP 接入", ["ops-collector-a"]),
    "ops-collector-b": ("测试租户 B OTLP 接入", ["ops-collector-b"]),
    "ops-infra-logs": ("白名单基础设施日志", ["ops-infra-logs"]),
}


def read_json(*args):
    process = subprocess.run(args, check=True, capture_output=True, text=True, encoding="utf-8", timeout=45)
    return json.loads(process.stdout)


def owned_release(resource):
    metadata = resource["metadata"]
    annotations = metadata.get("annotations", {})
    labels = metadata.get("labels", {})
    return annotations.get("meta.helm.sh/release-name") or labels.get("app.kubernetes.io/instance")


def containers(spec):
    return [*spec.get("initContainers", []), *spec.get("containers", [])]


def inspect_workload(resource, pods, replicas, claims):
    metadata = resource["metadata"]
    spec = resource["spec"]
    status = resource.get("status", {})
    template = spec["template"]["spec"]
    kind = resource["kind"]
    controller_uids = {metadata["uid"], *replicas.get(metadata["uid"], [])}
    related = [
        pod for pod in pods
        if not pod["metadata"].get("deletionTimestamp")
        and any(ref["uid"] in controller_uids for ref in pod["metadata"].get("ownerReferences", []))
    ]
    configured = {c["name"]: c["image"] for c in containers(template)}
    runtime = defaultdict(set)
    for pod in related:
        pod_status = pod.get("status", {})
        for container in pod_status.get("containerStatuses", []) + pod_status.get("initContainerStatuses", []):
            identity = container.get("imageID")
            if identity:
                runtime[container["name"]].add(identity)
    volume_names = {
        v["persistentVolumeClaim"]["claimName"]
        for p in [template, *(pod["spec"] for pod in related)]
        for v in p.get("volumes", [])
        if "persistentVolumeClaim" in v
    }
    policy = spec.get("persistentVolumeClaimRetentionPolicy", {})
    retention = None
    if spec.get("volumeClaimTemplates"):
        retention = {key: policy.get(key, "Retain") for key in ("whenScaled", "whenDeleted")}
    desired = status.get("desiredNumberScheduled") if kind == "DaemonSet" else spec.get("replicas", 1)
    ready = status.get("numberReady", 0) if kind == "DaemonSet" else status.get("readyReplicas", 0)
    return {
        "kind": kind,
        "name": metadata["name"],
        "uid": metadata["uid"],
        "desired": desired,
        "ready": ready,
        "configuredImages": configured,
        "observedImageIDs": {name: sorted(ids) for name, ids in sorted(runtime.items())},
        "storageRetention": retention,
        "claims": {name: claims.get(name, {"phase": "Unknown"}) for name in sorted(volume_names)},
    }


def known_identity(report):
    return {
        name: {
            "chart": entry["helm"].get("chart"),
            "workloads": {
                workload["name"]: {
                    "images": workload["configuredImages"],
                    "retention": workload["storageRetention"],
                }
                for workload in entry["workloads"]
            },
        }
        for name, entry in report["components"].items()
    }


def collect(args):
    namespace = read_json(args.kubectl, "get", "namespace", args.namespace, "-o", "json")
    if namespace["metadata"].get("labels", {}).get("app.kubernetes.io/part-of") != OWNER:
        raise ValueError("命名空间归属校验失败")
    releases = {
        release["name"]: {key: release.get(key) for key in ("chart", "revision", "status", "app_version", "updated")}
        for release in read_json(args.helm, "list", "-n", args.namespace, "-o", "json")
        if release["name"] in COMPONENTS
    }
    objects = read_json(
        args.kubectl, "get", "deploy,sts,ds,rs,pods,svc,pvc",
        "-n", args.namespace, "-o", "json",
    )["items"]
    by_kind = defaultdict(list)
    for resource in objects:
        by_kind[resource["kind"]].append(resource)
    replicas = defaultdict(list)
    for replica in by_kind["ReplicaSet"]:
        for ref in replica["metadata"].get("ownerReferences", []):
            replicas[ref["uid"]].append(replica["metadata"]["uid"])
    claims = {
        p["metadata"]["name"]: {
            "phase": p.get("status", {}).get("phase"),
            "class": p["spec"].get("storageClassName"),
            "size": p["spec"].get("resources", {}).get("requests", {}).get("storage"),
        }
        for p in by_kind["PersistentVolumeClaim"]
    }
    services = {item["metadata"]["name"]: item for item in by_kind["Service"]}
    report = {
        "schemaVersion": 1,
        "observedAt": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "namespace": args.namespace,
        "namespaceUID": namespace["metadata"]["uid"],
        "components": {},
        "findings": [],
    }
    for name, (purpose, service_names) in COMPONENTS.items():
        workloads = sorted(
            (inspect_workload(resource, by_kind["Pod"], replicas, claims)
             for kind in CONTROLLERS for resource in by_kind[kind]
             if owned_release(resource) == name),
            key=lambda workload: workload["name"],
        )
        helm = releases.get(name, {})
        report["components"][name] = {
            "purpose": purpose, "helm": helm, "workloads": workloads,
            "services": [
                {"name": s, "type": services[s]["spec"]["type"],
                 "ports": [p["port"] for p in services[s]["spec"]["ports"]]}
                for s in service_names if s in services and owned_release(services[s]) == name
            ],
        }
        findings = report["findings"]
        if helm.get("status") != "deployed" or not workloads:
            findings.append(f"{name} 缺少已部署发行或可归属工作负载")
        for workload in workloads:
            label = f"{name}/{workload['name']}"
            if not workload["desired"] or workload["ready"] != workload["desired"]:
                findings.append(f"{label} 未达到预期就绪数量")
            if any(not PINNED.search(image) for image in workload["configuredImages"].values()):
                findings.append(f"{label} 存在未锁定摘要的容器镜像")
            if workload["storageRetention"] and set(workload["storageRetention"].values()) != {"Retain"}:
                findings.append(f"{label} 存在会释放 PVC 的保留策略")
            if any(claim["phase"] != "Bound" for claim in workload["claims"].values()):
                findings.append(f"{label} 有数据卷未绑定")
    if args.baseline:
        baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
        if baseline["schemaVersion"] != report["schemaVersion"] or baseline["namespaceUID"] != report["namespaceUID"]:
            raise ValueError("基线与当前命名空间身份不一致")
        before, after = known_identity(baseline), known_identity(report)
        for name in COMPONENTS:
            if before.get(name) != after[name]:
                report["findings"].append(f"{name} 的 Chart、镜像、工作负载或存储策略与基线不同")
    return report


def markdown(report):
    lines = [
        "# 公共观测组件运行清单",
        "",
        f"观察时刻 {report['observedAt']}，命名空间 `{report['namespace']}`。",
        "Chart 应用版本来自包元数据，运行身份请查看 JSON 中的镜像摘要。",
        "",
        "| 发行 | 用途 | Chart | Chart 应用版本 | 修订 | 工作负载就绪数 |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for name, data in report["components"].items():
        helm = data["helm"]
        ready = "、".join(f"{w['name']} {w['ready']}/{w['desired']}" for w in data["workloads"])
        lines.append(f"| {name} | {data['purpose']} | {helm.get('chart', '未知')} | {helm.get('app_version', '未知')} | {helm.get('revision', '未知')} | {ready or '未观察到'} |")
    lines += ["", "## 需要核查", ""]
    lines += [f"- {finding}" for finding in report["findings"]] or ["当前清单未发现上述声明偏差。实际信号可读性需单独验证。"]
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", default="ops-observability")
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--format", choices=("json", "markdown"), default="json")
    args = parser.parse_args()
    try:
        report = collect(args)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        print(f"组件清单读取失败（{type(exc).__name__}）。请核验命名空间归属、客户端权限和集群连接。", file=sys.stderr)
        return 2
    print(markdown(report) if args.format == "markdown" else json.dumps(report, ensure_ascii=False, indent=2))
    return 1 if report["findings"] else 0


if __name__ == "__main__":
    sys.exit(main())
