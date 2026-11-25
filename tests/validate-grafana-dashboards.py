#!/usr/bin/env python3
"""Check that the Helm overlay and Grafana boards agree on offline provisioning."""

import json
import pathlib
import re
import subprocess
import sys


ROOT = pathlib.Path(__file__).resolve().parent.parent
UID = "test-platform-prometheus"


def generate(output):
    return json.loads(subprocess.check_output([
        sys.executable, str(ROOT / "tools/platform-dashboards.py"), "--datasource-uid", UID, "--format", output,
    ]))


def main():
    values, configmap = generate("helm"), generate("configmap")
    dashboards = {name.removesuffix(".json"): json.loads(data) for name, data in configmap["data"].items()}
    expected = {"ops-resources-" + name for name in ("home", "nodes", "storage", "network", "schedule", "pipeline")}
    assert set(dashboards) == expected
    assert values["dashboardsConfigMaps"] == {"platform-resources": configmap["metadata"]["name"]}
    assert configmap["immutable"] is True and "dashboards" not in values
    assert values["grafana.ini"]["users"]["default_language"] == "zh-Hans"
    provider, = values["dashboardProviders"]["dashboardproviders.yaml"]["providers"]
    assert values["grafana.ini"]["dashboards"]["default_home_dashboard_path"] == (
        provider["options"]["path"] + "/ops-resources-home.json"
    )
    lock = json.loads((ROOT / "deploy/public-artifacts.lock.json").read_text())["visualization"]
    assert configmap["metadata"]["name"] == lock["grafanaDashboardConfigMap"]
    assert lock["grafanaDashboardCount"] == len(dashboards)
    assert provider["allowUiUpdates"] is False

    for uid, board in dashboards.items():
        assert board["uid"] == uid and not board["editable"]
        assert configmap["metadata"]["name"].endswith(f"-v{board['version']}")
        assert board["timezone"] == "Asia/Shanghai"
        assert {link["url"] for link in board["links"]} == {"/d/" + name for name in expected}
        for panel in board["panels"]:
            if panel["type"] == "table":
                transforms = panel["transformations"]
                assert [transform["id"] for transform in transforms] == ["labelsToFields", "organize"]
                assert transforms[1]["options"]["renameByName"]["Value"] == "当前值"
            for target in panel.get("targets", []):
                assert target["datasource"]["uid"] == UID
                query = target["expr"].replace("${node:regex}", "")
                metric_selectors = re.findall(r"\b[a-zA-Z_:][\w:]*\{([^{}]*)\}", query)
                assert metric_selectors and all('ops_tenant="platform"' in labels for labels in metric_selectors)
    network = dashboards["ops-resources-network"]
    node, = network["templating"]["list"]
    assert node["name"] == "node" and node["datasource"]["uid"] == UID
    assert 'ops_tenant="platform"' in node["query"]["query"]
    print("Grafana provisioning, scope and table fields verified")


if __name__ == "__main__":
    main()
