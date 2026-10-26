#!/usr/bin/env bash
# Update the existing trial installation; run from its Git checkout.
set -Eeuo pipefail
umask 077
cd "${1:-/root/keepet}"
repo=$(pwd -P)
for command in git docker curl; do command -v "$command" >/dev/null; done
git fetch origin main
target=$(git rev-parse origin/main)
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
docker image tag "$old_image" "keepet-backup:$stamp"
printf '%s\n' "keepet-backup:$stamp" > "$backup/previous-image.txt"
stopped=false
started=false
on_error() {
  code=$?
  trap - ERR
  # If backup failed before the new app started, resume the unchanged old app.
  if $stopped && ! $started; then docker start "$app_id" >/dev/null || true; fi
  printf '\nUpdate stopped. Backup: %s\nDo not delete volumes or restore a database blindly.\n' "$backup" >&2
  exit "$code"
}
trap on_error ERR
git restore --source=HEAD --staged --worktree -- Dockerfile compose.yaml
git merge --ff-only "$target"
# Preserve existing public-IP access while Caddy continues using loopback.
if [[ "$old_port" == 0.0.0.0:* ]]; then
  sed -i 's/127\.0\.0\.1:8080:8080/0.0.0.0:8080:8080/' compose.yaml
fi
COMPOSE_BAKE=false docker compose --progress plain build app
# Keep the old app available throughout the build; pause writes for the backup.
docker stop "$app_id" >/dev/null
stopped=true
docker compose exec -T db pg_dump -U keepet -d keepet -Fc > "$backup/database.dump"
test -s "$backup/database.dump"
docker compose exec -T db pg_restore -l < "$backup/database.dump" > "$backup/database-contents.txt"
docker cp "$app_id:/var/lib/keepet/settings.key" "$backup/settings.key"
test -s "$backup/settings.key"
chmod 600 "$backup/settings.key"
docker compose up -d --no-build
started=true
healthy=false
for attempt in $(seq 1 30); do
  if curl -fsS --connect-timeout 2 --max-time 3 http://127.0.0.1:8080/health > "$backup/health.txt"; then
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
docker compose ps
docker compose exec -T db psql -U keepet -d keepet -c \
  'SELECT enabled,provider_enabled,invitee_bonus_enabled,invitee_bonus,vet_bps,grooming_bps,sampling_bps,basis FROM referral_settings;'
echo "Update healthy. Commit: $target"
echo "Backup: $backup"
echo 'Open https://keepet.ir and https://keepet.ir/management/referrals'
