#!/usr/bin/env python3
"""Run Helm validation and reject invalid images and forbidden integration."""
import json
import pathlib
import subprocess
import tempfile
root = pathlib.Path(__file__).resolve().parents[1]
chart = root/'charts/ops-platform'
with tempfile.TemporaryDirectory() as directory:
    values = pathlib.Path(directory)/'values.json'
    values.write_text(json.dumps({name:{'repository':'registry.example.invalid/ops/'+name,'digest':'sha256:'+'a'*64} for name in ('image','referenceImage','postgresImage')}))
    subprocess.run(['helm','lint','--strict',str(chart),'-f',str(values)],check=True)
    subprocess.run(['helm','template','ops-platform',str(chart),'-f',str(values),'--kube-version','1.36.6'],check=True,capture_output=True)
    for override in ('neonEnabled=true','image.digest=latest','postgresImage.repository='):
        p=subprocess.run(['helm','template','ops-platform',str(chart),'-f',str(values),'--set',override],capture_output=True,text=True)
        assert p.returncode != 0, 'Unsafe values accepted: '+override
print('Helm lint, Kubernetes rendering, digest validation and disabled-provider guards passed.')
