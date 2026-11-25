#!/usr/bin/env python3
"""Render only verified internal image digests and attach the pull identity."""
import json
import os
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

for document in yaml.safe_load_all(sys.stdin):
    if document is not None:
        walk(document)
        sys.stdout.write('---\n' + yaml.safe_dump(document, allow_unicode=True))
