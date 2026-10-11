#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p /workspace/tools /workspace/.cache/go /workspace/.cache/go-build /workspace/.cache/go-mod
if ! /workspace/tools/go/bin/go version 2>/dev/null | head -1 | rg -q 'go1\.26\.9'; then
  curl -fsSL https://dl.google.com/go/go1.26.9.linux-amd64.tar.gz -o /tmp/keepet-go.tar.gz
  printf '%s  %s\n' '42d158b4d8f7b61ac0a830567c940a86098fb7aac52e467a5ebec03ef5cc2f8d' '/tmp/keepet-go.tar.gz' | sha256sum -c -
  tar -xzf /tmp/keepet-go.tar.gz -C /workspace/tools
fi
source scripts/env.sh
go mod download
go mod verify
go build -o bin/keepet ./cmd/server
scripts/db.sh
