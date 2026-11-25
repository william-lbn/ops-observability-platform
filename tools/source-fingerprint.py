#!/usr/bin/env python3
"""Stable source identity independent of checkout path and Git metadata."""
import argparse
import hashlib
import json
import pathlib

root = pathlib.Path(__file__).resolve().parents[1]
parser=argparse.ArgumentParser();parser.add_argument('--version',action='store_true');args=parser.parse_args()
paths=[root/'go.mod',root/'go.sum']
for folder in ('cmd','internal','contracts','web/src'):
    paths.extend(p for p in root.joinpath(folder).rglob('*') if p.is_file())
paths.extend(root/'web'/name for name in ('package.json','package-lock.json','tsconfig.json'))
files={p.relative_to(root).as_posix():hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(set(paths))}
digest=hashlib.sha256()
for name,value in sorted(files.items()):digest.update(name.encode()+b'\0'+bytes.fromhex(value))
identity=digest.hexdigest();version='0.1.0+'+identity[:12]
if args.version:print(version)
else:print(json.dumps({'applicationSourceSHA256':identity,'version':version,'files':files},sort_keys=True,indent=2))
