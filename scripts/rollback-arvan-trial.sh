#!/usr/bin/env bash
# Check by default. --restore activates an isolated recovery stack with old data/code.
set -Eeuo pipefail
umask 077
repo=$(cd "${1:?Usage: rollback-arvan-trial.sh REPO BACKUP [--restore]}" && pwd -P)
backup=$(cd "${2:?Backup directory required}" && pwd -P)
mode=${3:---check}
[[ "$mode" == --check || "$mode" == --restore ]] || { echo 'Use --check or --restore.' >&2; exit 1; }
cd "$repo"
for tool in docker curl python3 sha256sum flock; do command -v "$tool" >/dev/null; done
exec 9>"$(git rev-parse --git-path keepet-update.lock)"
flock -n 9 || { echo 'An update or rollback is already running.' >&2; exit 1; }
[[ ! -e .keepet-recovery ]] || { echo 'A recovery stack is already active; use its recorded Compose file.' >&2; exit 1; }
for file in backup.ready SHA256SUMS database.dump database-fingerprint.json database-fingerprint.sql previous-image.txt previous-db-image.txt previous-schema.txt compose.resolved.json previous-commit.txt; do
  [[ -s "$backup/$file" ]] || { echo "Incomplete backup: $file" >&2; exit 1; }
done
(cd "$backup" && sha256sum --status -c SHA256SUMS)
old_image=$(cat "$backup/previous-image.txt")
old_db_image=$(cat "$backup/previous-db-image.txt")
docker image inspect "$old_image" >/dev/null
docker image inspect "$old_db_image" >/dev/null
docker compose exec -T db pg_restore -l < "$backup/database.dump" >/dev/null
printf 'Backup verified. Previous commit: %s\n' "$(cat "$backup/previous-commit.txt")"
if [[ "$mode" == --check ]]; then
  echo 'Check only: no application, database, DNS or firewall changed.'
  echo 'Restore returns the visible site to this snapshot; newer data must be reconciled from the preserved current database.'
  exit 0
fi
health_url=${KEEPET_HEALTH_URL:-http://127.0.0.1:8080/health}
[[ "$health_url" =~ ^http://127\.0\.0\.1:[0-9]+/health$ ]]
current_app=$(docker compose ps -a -q app)
[[ -n "$current_app" && -n $(docker compose ps -q db) ]]
stamp=$(date +%Y%m%d-%H%M%S)-$$
recovery="$(dirname "$repo")/keepet-recoveries/$stamp"
mkdir -p "$recovery/app-secrets"
project="keepet_recovery_$(date +%s)_$$"
printf '%s\n' "$project" > "$recovery/project.txt"
printf '%s\n' "$backup" > "$recovery/source-backup.txt"
cp "$backup/database-fingerprint.sql" "$recovery/"
python3 - "$backup" "$recovery" "$old_image" "$old_db_image" <<'PY'
import json, pathlib, sys
backup,out=map(pathlib.Path,sys.argv[1:3])
config=json.loads((backup/'compose.resolved.json').read_text())
app=config['services']['app']; db=config['services']['db']
# Keep the known application environment and listening ports; never reuse live data volumes.
app={k:v for k,v in app.items() if k in ('environment','ports','restart')}
app.update(image=sys.argv[3],pull_policy='never',volumes=['app-secrets:/var/lib/keepet'],depends_on={'db':{'condition':'service_healthy'}})
db={k:v for k,v in db.items() if k in ('environment','healthcheck','restart')}
db.update(image=sys.argv[4],pull_policy='never',volumes=['db-data:/var/lib/postgresql/data'])
(out/'compose.json').write_text(json.dumps({'services':{'db':db,'app':app},'volumes':{'db-data':{},'app-secrets':{}}},indent=2)+'\n')
PY
compose=(docker compose -p "$project" -f "$recovery/compose.json")
cutover=false
resume_current=false
if [[ $(docker inspect --format '{{.State.Running}}' "$current_app") == true ]] && curl -fsS --connect-timeout 2 --max-time 3 "$health_url" >/dev/null; then resume_current=true; fi
on_error() {
  local code=$1
  trap - ERR EXIT INT TERM
  if [[ -f "$repo/.keepet-recovery" ]] && [[ $(cat "$repo/.keepet-recovery") == "$recovery" ]]; then rm -f "$repo/.keepet-recovery"; fi
  "${compose[@]}" stop app >/dev/null 2>&1 || true
  # Only resume the very same healthy current container, on its untouched original DB.
  if $cutover && $resume_current; then docker start "$current_app" >/dev/null || true; fi
  printf 'Rollback stopped. Recovery evidence: %s\nOriginal database/volumes were not overwritten.\n' "$recovery" >&2
  exit "$code"
}
trap 'on_error $?' ERR
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'status=$?; if (( status != 0 )); then on_error "$status"; fi' EXIT
# Prepare and validate an independent database while the current site can keep running.
"${compose[@]}" up -d --no-build --pull never db
ready=false
for attempt in $(seq 1 40); do
  # The initialization-only PostgreSQL server accepts Unix sockets before the
  # application database exists. Require a successful query over final-server TCP.
  if [[ $("${compose[@]}" exec -T db sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -h 127.0.0.1 -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 -c "SELECT 1"' 2>/dev/null) == 1 ]]; then ready=true; break; fi
  sleep 1
done
$ready
"${compose[@]}" exec -T db pg_restore -U keepet -d keepet --exit-on-error --no-owner --no-privileges < "$backup/database.dump"
"${compose[@]}" exec -T db psql -X -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 < "$recovery/database-fingerprint.sql" > "$recovery/restored-fingerprint.json"
python3 - "$backup/database-fingerprint.json" "$recovery/restored-fingerprint.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))==json.load(open(sys.argv[2])), 'Restored rows differ from backup'
PY
"${compose[@]}" create --no-build --pull never app
restored_app=$("${compose[@]}" ps -a -q app)
docker cp "$backup/app-secrets/." "$restored_app:/var/lib/keepet/"
"${compose[@]}" run --rm --no-deps --pull never --user 0 --entrypoint sh app -c 'chown -R keepet:keepet /var/lib/keepet; if [ -f /var/lib/keepet/settings.key ]; then chmod 600 /var/lib/keepet/settings.key; fi'
# Pause the current writer, preserving a second recovery point for all newer records.
cutover=true
docker stop "$current_app" >/dev/null
docker compose exec -T db pg_dump -U keepet -d keepet -Fc > "$recovery/before-rollback.dump"
test -s "$recovery/before-rollback.dump"
docker compose exec -T db pg_restore -l < "$recovery/before-rollback.dump" > "$recovery/before-rollback-index.txt"
docker cp "$current_app:/var/lib/keepet/." "$recovery/app-secrets/"
before_image=$(docker inspect --format '{{.Image}}' "$current_app")
docker image tag "$before_image" "keepet-before-rollback:$stamp"
printf '%s\n' "keepet-before-rollback:$stamp" > "$recovery/before-rollback-image.txt"
docker compose config --format json > "$recovery/before-rollback-compose.json"
git rev-parse HEAD > "$recovery/before-rollback-commit.txt"
"${compose[@]}" up -d --no-deps --no-build --pull never app
healthy=false
for attempt in $(seq 1 30); do
  if curl -fsS --connect-timeout 2 --max-time 3 "$health_url" > "$recovery/health.txt"; then healthy=true; break; fi
  sleep 1
done
$healthy
[[ $(docker inspect --format '{{.Image}}' "$("${compose[@]}" ps -q app)") == $(docker image inspect --format '{{.Id}}' "$old_image") ]]
state=$("${compose[@]}" exec -T db psql -X -U keepet -d keepet -Atq -c "SELECT version::text||':'||dirty::text FROM schema_migrations;")
[[ "$state" == "$(cat "$backup/previous-schema.txt")" ]]
# Durable pointer prevents accidentally running the normal updater on the inactive original stack.
(cd "$recovery" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
printf '%s\n' "$recovery" > "$repo/.keepet-recovery.tmp"
mv "$repo/.keepet-recovery.tmp" "$repo/.keepet-recovery"
trap - ERR EXIT INT TERM
printf 'Previous version restored and healthy. Commit: %s\nRecovery directory: %s\n' "$(cat "$backup/previous-commit.txt")" "$recovery"
printf 'Manage the active stack with: docker compose -p %s -f %q/compose.json ...\n' "$project" "$recovery"
echo 'Newer records remain in the original database and before-rollback.dump. Reconcile payments/orders before resuming real transactions.'
