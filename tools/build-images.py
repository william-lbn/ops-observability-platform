#!/usr/bin/env python3
"""Create deterministic Docker archives from offline-built static executables."""
import hashlib
import io
import json
import pathlib
import subprocess
import sys
import tarfile

root = pathlib.Path(__file__).resolve().parents[1]
dist = root / 'dist'
dist.mkdir(exist_ok=True)
subprocess.run([sys.executable,str(root/'tools/third-party-notices.py')],check=True)
notices=dist.joinpath('THIRD_PARTY_NOTICES.txt').read_bytes()

def write_json(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()

def add(tf, name, data, mode=0o644):
    entry = tarfile.TarInfo(name)
    entry.size = len(data)
    entry.mode = mode
    entry.mtime = 0
    entry.uid = entry.gid = 65532
    tf.addfile(entry, io.BytesIO(data))

for name in ('platform', 'reference'):
    layer = io.BytesIO()
    binary = (dist / name).read_bytes()
    with tarfile.open(fileobj=layer, mode='w') as tf:
        add(tf, name, binary, 0o755)
        add(tf, 'usr/share/licenses/ops-platform/THIRD_PARTY_NOTICES.txt', notices, 0o444)
    raw = layer.getvalue()
    config = write_json({
        'architecture': 'amd64', 'os': 'linux',
        'created': '1970-01-01T00:00:00Z',
        'config': {'Entrypoint': ['/' + name], 'User': '65532:65532'},
        'rootfs': {'type': 'layers', 'diff_ids': ['sha256:' + hashlib.sha256(raw).hexdigest()]},
        'history': [{'created': '1970-01-01T00:00:00Z', 'created_by': 'offline static Go build'}],
    })
    config_name = hashlib.sha256(config).hexdigest() + '.json'
    manifest = write_json([{'Config': config_name, 'RepoTags': ['ops-local/' + name + ':0.1.0'], 'Layers': ['layer.tar']}])
    archive = dist / (name + '.docker.tar')
    with tarfile.open(archive, 'w') as tf:
        add(tf, 'manifest.json', manifest)
        add(tf, config_name, config)
        add(tf, 'layer.tar', raw)
    print(json.dumps({'image': name, 'binarySHA256': hashlib.sha256(binary).hexdigest(), 'archiveSHA256': hashlib.sha256(archive.read_bytes()).hexdigest()}))
