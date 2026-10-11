#!/usr/bin/env bash
set -euo pipefail
if docker container inspect keepet-db >/dev/null 2>&1; then
  docker start keepet-db >/dev/null
else
  docker run -d --name keepet-db \
    -e POSTGRES_USER=keepet -e POSTGRES_PASSWORD=keepet_local_only -e POSTGRES_DB=keepet \
    -p 127.0.0.1:5432:5432 -v keepet-postgres:/var/lib/postgresql/data \
    postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24 >/dev/null
fi
for attempt in $(seq 1 30); do
  if docker exec keepet-db pg_isready -U keepet -d keepet >/dev/null; then exit 0; fi
  sleep 1
done
printf '%s\n' 'PostgreSQL did not become ready within 30 seconds.' >&2
exit 1
