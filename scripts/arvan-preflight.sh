#!/usr/bin/env bash
# Read-only inspection for the existing development/trial Compose installation.
set -Eeuo pipefail
cd "${1:-/root/keepet}"
for tool in git docker curl df awk python3; do command -v "$tool" >/dev/null; done
fail() { printf 'Preflight blocked: %s\n' "$1" >&2; exit 1; }
[[ $(git rev-parse --is-inside-work-tree) == true ]] || fail 'not a Git checkout'
[[ -f compose.yaml && -f Dockerfile ]] || fail 'Compose configuration is missing'
app_id=$(docker compose ps -q app)
db_id=$(docker compose ps -q db)
[[ -n "$app_id" && -n "$db_id" ]] || fail 'app and database must already be running'
docker exec "$app_id" sh -c 'test "$APP_MODE" = development' || fail 'only the existing trial installation is supported'
# The updater can back up this file, not an arbitrary external key provider.
docker exec "$app_id" sh -c 'test -z "${ADMIN_SETTINGS_KEY:-}" && test "$SETTINGS_KEY_FILE" = /var/lib/keepet/settings.key' || fail 'external/nonstandard encryption key requires a separate backup procedure'
sql() { docker compose exec -T db psql -X -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 -c "$1"; }
state=$(sql "SELECT version::text||':'||dirty::text FROM schema_migrations;")
[[ "$state" =~ ^([0-9]+):false$ ]] || fail 'migration state is missing, ambiguous or dirty'
version=${BASH_REMATCH[1]}
(( version >= 11 && version <= 21 )) || fail 'this updater supports schema versions 11 through 21 only'
encrypted=$(sql 'SELECT count(*) FROM platform_integrations WHERE secret IS NOT NULL;')
if [[ $(sql "SELECT to_regclass('public.arrival_challenges') IS NOT NULL;") == t ]]; then
  arrival=$(sql 'SELECT count(*) FROM arrival_challenges;')
  encrypted=$((encrypted + arrival))
fi
if (( encrypted > 0 )); then
  docker exec "$app_id" sh -c 'test -s /var/lib/keepet/settings.key && test "$(wc -c < /var/lib/keepet/settings.key)" -eq 32' || fail 'matching 32-byte encryption key is missing'
fi
bytes=$(sql "SELECT pg_database_size(current_database());")
free_kb=$(df -Pk .. | awk 'END {print $4}')
(( free_kb * 1024 > bytes * 3 + 536870912 )) || fail 'insufficient free space for database backup and build'
health_url=${KEEPET_HEALTH_URL:-http://127.0.0.1:8080/health}
[[ "$health_url" =~ ^http://127\.0\.0\.1:[0-9]+/health$ ]] || fail 'health URL must be local'
body=$(curl -fsS --connect-timeout 2 --max-time 5 "$health_url") || fail 'current app health failed'
[[ "$body" == ok ]] || fail 'unexpected current health response'
printf 'Trial preflight passed. Commit: %s\nSchema: %s (clean)\nEncrypted records requiring the matching key: %s\nFree space: %s KiB\n' "$(git rev-parse HEAD)" "$version" "$encrypted" "$free_kb"
printf 'No DNS, CDN, firewall, application data or configuration changed. Real SMS/payment remain unavailable in this release.\n'
