#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/env.sh
scripts/db.sh
if ! docker exec keepet-db psql -U keepet -tAc "SELECT 1 FROM pg_database WHERE datname='keepet_test'" | rg -q '^1$'; then
  docker exec keepet-db createdb -U keepet keepet_test
fi
export TEST_DATABASE_URL=postgres://keepet:keepet_local_only@127.0.0.1:5432/keepet_test?sslmode=disable
go test -race -count=1 -v ./...
go vet ./...
