# Source this file from any working directory. No credentials are stored here.
export PATH="/workspace/tools/go/bin:$PATH"
export GOPATH=/workspace/.cache/go
export GOCACHE=/workspace/.cache/go-build
export GOMODCACHE=/workspace/.cache/go-mod
export APP_MODE=development
export DATABASE_URL="${DATABASE_URL:-postgres://keepet:keepet_local_only@127.0.0.1:5432/keepet?sslmode=disable}"
export PORT="${PORT:-8080}"
