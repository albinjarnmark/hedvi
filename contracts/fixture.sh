#!/usr/bin/env bash
# Prints a fresh contract-test tenant as JSON. Requires `make up` and a
# migrated local database; the API must run separately (`make dev`).
set -euo pipefail
cd "$(dirname "$0")/../apps/api"
go run ./cmd/hedvi admin contract-fixture 2>/dev/null | grep '"test_key"'
