#!/usr/bin/env python3
"""Render only verified internal image digests and attach the pull identity."""
import json
import os
import pathlib
import sys
import yaml

lock = json.load(open(os.environ['OPS_IMAGE_LOCK'], encoding='utf-8'))
mapping = {x['source']: x['destination'] for x in lock}

def walk(value):
    if isinstance(value, dict):
        for key, item in value.items():
            if key == 'image' and isinstance(item, str):
                if item not in mapping:
                    raise RuntimeError('Image missing from verified offline closure: ' + item)
                value[key] = mapping[item]
            else:
                walk(item)
        if 'containers' in value:
            value['imagePullSecrets'] = [{'name': 'ops-harbor-pull'}]
            for container in value.get('containers', []) + value.get('initContainers', []):
                resources = container.setdefault('resources', {})
                resources.setdefault('requests', {'cpu': '25m', 'memory': '64Mi'})
                resources.setdefault('limits', {'cpu': '1', 'memory': '512Mi'})
    elif isinstance(value, list):
        for item in value:
            walk(item)

COLLECTOR_LAYOUTS = {
    'ops-collector': ('Deployment', 'ops-collector', 'opentelemetry-collector-platform-gateway'),
    'ops-collector-a': ('Deployment', 'ops-collector-a', 'opentelemetry-collector-test-tenant-a'),
    'ops-collector-b': ('Deployment', 'ops-collector-b', 'opentelemetry-collector-test-tenant-b'),
    'ops-infra-logs': ('DaemonSet', 'ops-infra-logs-agent', 'opentelemetry-collector-node-logs-agent'),
}


def retain_collector_services(documents, release):
    kind, previous, replacement = COLLECTOR_LAYOUTS[release]
    workload, = [d for d in documents if d.get('kind') == kind and d['metadata']['name'] == previous]
    service, = [d for d in documents if d.get('kind') == 'Service' and d['metadata']['name'] == release]
    labels = workload['spec']['template']['metadata']['labels']
    selector = workload['spec']['selector']['matchLabels']
    expected = 'agent-collector' if kind == 'DaemonSet' else 'standalone-collector'
    if selector.get('component') != expected or service['spec']['selector'] != selector:
        raise ValueError('Unexpected upstream Collector selector; review the migration for this Chart.')
    if any(labels.get(key) != value for key, value in selector.items()):
        raise ValueError('Collector template labels do not match ownership selector.')
    workload['metadata']['name'] = replacement
    selector['component'] = release + '-canonical'
    labels['component'] = selector['component']
    labels['ops.platform/route'] = release
    service['spec']['selector'] = {
        'app.kubernetes.io/instance': release,
        'ops.platform/route': release,
    }


documents = [document for document in yaml.safe_load_all(sys.stdin) if document is not None]
release = os.getenv('OPS_COLLECTOR_WORKLOAD_NAMES')
if release:
    retain_collector_services(documents, release)
for document in documents:
    walk(document)
    sys.stdout.write('---\n' + yaml.safe_dump(document, allow_unicode=True))
