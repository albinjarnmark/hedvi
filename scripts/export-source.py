#!/usr/bin/env python3
"""Create a new, allowlisted source tree; never copy private Git history or state."""
import argparse
import hashlib
import json
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ROOT_FILES = {'.dockerignore','.editorconfig','.gitignore','.gitleaks.toml','.npmrc',
 'Makefile','README.md','LICENSE','NOTICE','SECURITY.md','CONTRIBUTING.md','TRADEMARKS.md',
 'THIRD_PARTY_NOTICES.md','compose.yaml','package.json','pnpm-lock.yaml','pnpm-workspace.yaml'}
PREFIXES = ('apps/', 'contracts/', '.github/', 'scripts/', 'third_party/',
 'docs/architecture/', 'docs/runbooks/', 'docs/open-source/',
 'infra/terraform/modules/', 'infra/terraform/bootstrap/',
 'infra/terraform/envs/staging/', 'infra/terraform/envs/prod/',
 'brand/icons/', 'brand/logo/')
DOCS = {'docs/compatibility.md','docs/status.md','docs/data-map.md','docs/self-hosting.md',
 'docs/INTEGRATION-EXAMPLES-LICENSE','infra/terraform/README.md','infra/terraform/AUTH-MAIL.md'}
BAD_PARTS = {'.git','.data','.aws','.ssh','.next','.terraform','node_modules','bin','.venv','__pycache__'}
BAD_SUFFIXES = ('.tfstate','.tfplan','.tfvars','.tfvars.json','.pem','.key','.p12','.pfx','.zip','.log','.tsbuildinfo','.pyc','.crt','.p8','.jks','.kdbx','.bak','.backup','.swp','.tar','.gz','.7z')

def allowed(name):
 p=Path(name)
 if any(part in BAD_PARTS for part in p.parts): return False
 if p.name in {'.DS_Store','.netrc','override.tf','override.tf.json'} or p.name.endswith(('_override.tf','_override.tf.json')): return False
 if p.name.startswith('.env') and p.name != '.env.example': return False
 if '.tfstate' in p.name or p.name.endswith(BAD_SUFFIXES) or p.name=='backend.hcl': return False
 return name in ROOT_FILES or name in DOCS or name.startswith(PREFIXES)

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('destination',type=Path)
 parser.add_argument('--deny-file',type=Path,help='Private newline-separated literals that must not occur in the export; keep this file outside the repository.')
 args=parser.parse_args(); destination=args.destination.resolve()
 if destination.exists(): raise SystemExit('Destination must not exist; refusing to merge or overwrite a source export.')
 if ROOT==destination or ROOT in destination.parents: raise SystemExit('Destination must be outside the development checkout.')
 candidates=subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=ROOT).decode().split('\0')
 names=sorted({n for n in candidates if n and allowed(n) and (ROOT/n).exists()})
 denied=[line.strip().lower() for line in args.deny_file.read_bytes().splitlines() if line.strip()] if args.deny_file else []
 # Check all paths/content before creating a destination. Do not reveal a
 # matched private value in logs; report only the offending source path.
 for name in names:
  path=ROOT/name
  if path.is_symlink() or not path.is_file() or ROOT not in path.resolve().parents: raise SystemExit('Unsafe source path: '+name)
  if denied and any(value in path.read_bytes().lower() for value in denied): raise SystemExit('Private content matched in: '+name)
 files=[]
 for name in names:
  path=ROOT/name
  if path.is_symlink() or not path.is_file() or ROOT not in path.resolve().parents: raise SystemExit('Unsafe source path: '+name)
  target=destination/name;target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(path,target)
  files.append({'path':name,'sha256':hashlib.sha256(target.read_bytes()).hexdigest()})
 manifest={'format':1,'license':'AGPL-3.0-only','history':'new public history; no private checkout objects','files':files}
 (destination/'source-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
 print(json.dumps({'destination':str(destination),'files':len(files),'manifest':'source-manifest.json'}))

if __name__=='__main__': main()
