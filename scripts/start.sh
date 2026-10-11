#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
scripts/db.sh
go build -o bin/keepet ./cmd/server
exec ./bin/keepet
