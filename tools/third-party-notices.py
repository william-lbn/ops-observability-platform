#!/usr/bin/env python3
"""Collect distributed Go license texts without network or private host paths."""
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[1]
vendor = root / 'vendor'
if not vendor.is_dir():
    raise SystemExit('Run go mod vendor from the verified module closure before packaging')
goroot = pathlib.Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
texts = [('Go standard library / LICENSE', (goroot/'LICENSE').read_text())]
for path in sorted(vendor.rglob('*')):
    if path.is_file() and any(path.name.upper().startswith(prefix) for prefix in ('LICENSE','LICENCE','COPYING','NOTICE')):
        texts.append((path.relative_to(root).as_posix(), path.read_text(encoding='utf-8', errors='replace')))
output = root / 'dist/THIRD_PARTY_NOTICES.txt'
output.parent.mkdir(exist_ok=True)
output.write_text('Third-party notices for the distributed Go application dependency closure.\n\n' + '\n\n'.join('===== '+name+' =====\n'+text for name,text in texts), encoding='utf-8')
print('Distributed Go license documents:', len(texts))
