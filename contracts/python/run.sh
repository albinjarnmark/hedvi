#!/usr/bin/env bash
# Contract tests for resend-python against a local Hedvi API (`make dev`).
set -euo pipefail
cd "$(dirname "$0")"
if [ ! -d .venv ]; then python3 -m venv .venv; fi
.venv/bin/pip install -q --require-hashes -r requirements.txt
HEDVI_FIXTURE="$(../fixture.sh)" .venv/bin/python -m unittest -v test_contract.py
