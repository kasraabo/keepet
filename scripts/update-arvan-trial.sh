#!/usr/bin/env bash
# Update the existing trial installation; run from its Git checkout.
set -Eeuo pipefail
umask 077
cd "${1:-/root/keepet}"
repo=$(pwd -P)
[[ ! -e .keepet-recovery ]] || { echo "A recovery stack is active; review .keepet-recovery before updating." >&2; exit 1; }
for command in git docker curl flock sha256sum python3 find sort xargs; do command -v "$command" >/dev/null; done
# Serialize updates on this checkout, including builds and backup creation.
exec 9>"$(git rev-parse --git-path keepet-update.lock)"
flock -n 9 || { echo 'Another update is already running.' >&2; exit 1; }
git fetch origin main
requested=${2:-origin/main}
target=$(git rev-parse --verify "$requested^{commit}")
git merge-base --is-ancestor "$target" origin/main || {
  echo 'Requested release is not part of origin/main.' >&2
  exit 1
}
# Inspect through the target's preflight so this also works from an old checkout.
preflight=$(mktemp)
git show "$target:scripts/arvan-preflight.sh" > "$preflight"
if ! bash "$preflight" "$repo"; then rm -f "$preflight"; exit 1; fi
rm -f "$preflight"
git merge-base --is-ancestor HEAD "$target" || {
  echo 'Local commits differ from main; no files changed. Review before updating.' >&2
  exit 1
}
# Preserve the two configuration files previously edited during server setup.
while IFS= read -r file; do
  case "$file" in Dockerfile|compose.yaml|'') ;; *)
    echo "Local changes in $file; no files changed. Review before updating." >&2
    exit 1 ;;
  esac
done < <(git diff --name-only HEAD)
app_id=$(docker compose ps -q app)
db_id=$(docker compose ps -q db)
test -n "$app_id" && test -n "$db_id"
docker compose config --format json > /dev/null
old_image=$(docker inspect --format '{{.Image}}' "$app_id")
old_port=$(docker compose port app 8080)
stamp=$(date +%Y%m%d-%H%M%S)-$$
backup="$(dirname "$repo")/keepet-backups/$stamp"
mkdir -p "$backup"
chmod 700 "$backup"
git rev-parse HEAD > "$backup/previous-commit.txt"
printf '%s\n' "$target" > "$backup/new-commit.txt"
git diff --binary HEAD > "$backup/local-changes.patch"
cp compose.yaml Dockerfile "$backup/"
docker compose config --format json > "$backup/compose.resolved.json"
docker inspect --format '{{.Image}}' "$db_id" > "$backup/previous-db-image.txt"
docker compose exec -T db psql -X -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 -c "SELECT version::text||':'||dirty::text FROM schema_migrations;" > "$backup/previous-schema.txt"
if [[ -f .env ]]; then cp -p .env "$backup/environment.env"; chmod 600 "$backup/environment.env"; fi
docker image tag "$old_image" "keepet-backup:$stamp"
printf '%s\n' "keepet-backup:$stamp" > "$backup/previous-image.txt"
stopped=false
migration_attempted=false
on_error() {
  local code=$1
  trap - ERR EXIT INT TERM
  # If backup failed before the new app started, resume the unchanged old app.
  if $stopped && ! $migration_attempted; then docker start "$app_id" >/dev/null || true; fi
  if $migration_attempted; then
    # A restart policy must not keep retrying a failed/partially migrated release.
    docker compose stop app >/dev/null 2>&1 || true
    echo 'The new app was stopped; do not start the old image against a migrated/dirty schema.' >&2
  fi
  printf '\nUpdate stopped. Backup: %s\nDo not delete volumes or restore a database blindly.\n' "$backup" >&2
  exit "$code"
}
trap 'on_error $?' ERR
trap 'exit 130' INT
trap 'exit 143' TERM
# EXIT also handles interrupted backups. Successful completion removes the trap.
trap 'status=$?; if (( status != 0 )); then on_error "$status"; fi' EXIT
git restore --source=HEAD --staged --worktree -- Dockerfile compose.yaml
git merge --ff-only "$target"
# Preserve existing public-IP access while Caddy continues using loopback.
if [[ "$old_port" == 0.0.0.0:* ]]; then
  sed -i 's/127\.0\.0\.1:8080:8080/0.0.0.0:8080:8080/' compose.yaml
fi
cp scripts/rollback-arvan-trial.sh scripts/database-fingerprint.sql "$backup/"
COMPOSE_BAKE=false docker compose --progress plain build app
# Keep the old app available throughout the build; pause writes for the backup.
stopped=true
docker stop "$app_id" >/dev/null
docker compose exec -T db pg_dump -U keepet -d keepet -Fc > "$backup/database.dump"
test -s "$backup/database.dump"
docker compose exec -T db pg_restore -l < "$backup/database.dump" > "$backup/database-contents.txt"
docker compose exec -T db psql -X -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 < "$backup/database-fingerprint.sql" > "$backup/database-fingerprint.json"
mkdir -p "$backup/app-secrets"
docker cp "$app_id:/var/lib/keepet/." "$backup/app-secrets/"
encrypted_count=$(docker compose exec -T db psql -U keepet -d keepet -Atqc \
  'SELECT count(*) FROM platform_integrations WHERE secret IS NOT NULL;')
# This table is absent on releases before service attendance was added.
arrival_table=$(docker compose exec -T db psql -U keepet -d keepet -Atqc "SELECT to_regclass('public.arrival_challenges') IS NOT NULL;")
if [ "$arrival_table" = t ]; then
  arrival_count=$(docker compose exec -T db psql -U keepet -d keepet -Atqc 'SELECT count(*) FROM arrival_challenges;')
  encrypted_count=$((encrypted_count + arrival_count))
fi
if [ "$encrypted_count" -gt 0 ]; then
  test -s "$backup/app-secrets/settings.key"
fi
# The key is created lazily for encrypted credentials or arrival codes.
if [ -f "$backup/app-secrets/settings.key" ]; then
  chmod 600 "$backup/app-secrets/settings.key"
fi
# From this point a migration may have run, even if Compose itself fails.
(cd "$backup" && find . -type f ! -name SHA256SUMS ! -name .SHA256SUMS.tmp ! -name backup.ready -print0 | sort -z | xargs -0 sha256sum > .SHA256SUMS.tmp && mv .SHA256SUMS.tmp SHA256SUMS)
# Only a fully written and atomically installed inventory marks the backup ready.
printf 'complete\n' > "$backup/backup.ready"
# Record the marker before invoking Compose; its return code cannot prove that no migration ran.
migration_attempted=true
docker compose up -d --no-build
health_url=${KEEPET_HEALTH_URL:-http://127.0.0.1:8080/health}
[[ "$health_url" =~ ^http://127\.0\.0\.1:[0-9]+/health$ ]]
healthy=false
for attempt in $(seq 1 30); do
  if curl -fsS --connect-timeout 2 --max-time 3 "$health_url" > "$backup/health.txt"; then
    healthy=true
    break
  fi
  sleep 1
done
if ! $healthy; then
  docker compose logs --tail=60 app
  echo 'The new app did not become healthy.' >&2
  false
fi
# Health alone does not establish that every migration completed.
expected_schema=$(git ls-tree -r --name-only "$target" migrations | sed -n 's|migrations/0*\([0-9][0-9]*\)_[^/]*\.up\.sql$|\1|p' | sort -n | tail -1)
actual_schema=$(docker compose exec -T db psql -X -U keepet -d keepet -Atq -v ON_ERROR_STOP=1 -c "SELECT version::text||':'||dirty::text FROM schema_migrations;")
[[ "$actual_schema" == "$expected_schema:false" ]]
docker compose ps
docker compose exec -T db psql -U keepet -d keepet -c \
  'SELECT enabled,provider_enabled,invitee_bonus_enabled,invitee_bonus,vet_bps,grooming_bps,sampling_bps,basis FROM referral_settings;'
trap - ERR EXIT INT TERM
echo "Trial update healthy. Commit: $target"
echo "Backup: $backup"
printf 'Rollback check: bash %q/rollback-arvan-trial.sh %q %q --check\n' "$backup" "$repo" "$backup"
printf 'Rollback activation: bash %q/rollback-arvan-trial.sh %q %q --restore\n' "$backup" "$repo" "$backup"
echo 'Open https://keepet.ir and review the release acceptance checks in docs/release-readiness.md'
echo 'This remains a development/trial release: displayed OTP and simulated payments are not suitable for real use.'
