#!/usr/bin/env python3
"""Run Helm validation and reject invalid images and forbidden integration."""
import json
import pathlib
import re
import subprocess
import tempfile
root = pathlib.Path(__file__).resolve().parents[1]
chart = root/'charts/ops-platform'
with tempfile.TemporaryDirectory() as directory:
    values = pathlib.Path(directory)/'values.json'
    values.write_text(json.dumps({name:{'repository':'registry.example.invalid/ops/'+name,'digest':'sha256:'+'a'*64} for name in ('image','referenceImage','postgresImage')}))
    subprocess.run(['helm','lint','--strict',str(chart),'-f',str(values)],check=True)
    subprocess.run(['helm','template','ops-platform',str(chart),'-f',str(values),'--kube-version','1.36.6'],check=True,capture_output=True)
    naming = root/'deploy/profiles/descriptive-names/core.yaml'
    args = ['helm', 'template', 'ops-platform', str(chart), '-f', str(values)]
    legacy = subprocess.check_output(args, text=True)
    candidate = subprocess.check_output([*args, '-f', str(naming)], text=True)
    for expected in ('ops-observability-api', 'ops-observability-worker', 'ops-observability-test-reference'):
        assert f'kind: Deployment\nmetadata:\n  name: {expected}\n' in candidate, expected
    # Keep Service routing and StatefulSet storage identity stable for this profile.
    def stable_objects(manifest):
        return [doc for doc in manifest.split('\n---\n') if re.search(r'^kind: (Service|StatefulSet)$', doc, re.M)]
    assert stable_objects(legacy) == stable_objects(candidate)
    reference = root/'charts/ops-reference'
    reference_values = pathlib.Path(directory)/'reference.json'
    reference_values.write_text(json.dumps({
        'tenant': 'test-a',
        'image': {'repository':'registry.example.invalid/ops/reference', 'digest':'sha256:'+'a'*64},
        'collectorEndpoint': 'https://otel.example.invalid:4318',
    }))
    tenant = subprocess.check_output(['helm','template','ops-reference',str(reference),'-f',str(reference_values),'-f',str(naming)],text=True)
    assert 'kind: Deployment\nmetadata:\n  name: ops-observability-test-reference\n' in tenant
    for override in ('neonEnabled=true','image.digest=latest','postgresImage.repository='):
        p=subprocess.run(['helm','template','ops-platform',str(chart),'-f',str(values),'--set',override],capture_output=True,text=True)
        assert p.returncode != 0, 'Unsafe values accepted: '+override
print('Helm lint, Kubernetes rendering, digest validation and disabled-provider guards passed.')
