#!/bin/sh
# 02 — Restore a Bedolaga snapshot into a scratch PostgreSQL for a rehearsal.
#
# Starts a throwaway PostgreSQL container bound to 127.0.0.1, checks the
# snapshot's checksum, restores it, and writes the connection string to
# MIGRATION_DIR/staging.env (mode 600) as APP_BEDOLAGA_DATABASE_URL. The
# password is generated here and exists only in that file.
#
# Usage: 02-restore-staging.sh path/to/bedolaga-<time>.dump
#
# Environment:
#   STAGING_CONTAINER  default bedolaga-staging
#   STAGING_PORT       default 55432
#   STAGING_IMAGE      default postgres:18.7-alpine (match Bedolaga's major version)
#   STAGING_REPLACE    set to 1 to remove an existing scratch container first
set -eu
. "$(dirname "$0")/lib.sh"

dump="${1:-}"
[ -n "$dump" ] || die "usage: $0 path/to/bedolaga-….dump"
[ -r "$dump" ] || die "$dump is not readable"
[ -r "$dump.sha256" ] || die "$dump.sha256 is missing; take snapshots with 01-snapshot-bedolaga.sh"

container="${STAGING_CONTAINER:-bedolaga-staging}"
port="${STAGING_PORT:-55432}"
image="${STAGING_IMAGE:-postgres:18.7-alpine}"

require_tool docker
prepare_dir

step "Check the snapshot checksum"
expected=$(cut -d' ' -f1 "$dump.sha256")
actual=$(sha256_file "$dump" | cut -d' ' -f1)
[ "$expected" = "$actual" ] || die "checksum mismatch: the snapshot changed or is incomplete"
say "checksum ok"

if docker inspect "$container" >/dev/null 2>&1; then
  [ "${STAGING_REPLACE:-}" = "1" ] || die "container $container already exists; set STAGING_REPLACE=1 to replace it"
  step "Remove the existing scratch container $container and its data"
  say "This deletes the previous rehearsal copy only. Production is not touched."
  confirm "replace $container"
  docker rm -f "$container" >/dev/null
fi

password=$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')
step "Start scratch PostgreSQL $container on 127.0.0.1:$port"
docker run -d --name "$container" -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB=bedolaga \
  -p "127.0.0.1:$port:5432" "$image" >/dev/null

tries=0
until docker exec "$container" pg_isready -U postgres -d bedolaga >/dev/null 2>&1; do
  tries=$((tries + 1))
  [ "$tries" -gt 60 ] && die "PostgreSQL did not start"
  sleep 1
done
sleep 2

step "Restore $dump"
docker exec -i "$container" pg_restore -U postgres -d bedolaga --no-owner --no-privileges --exit-on-error <"$dump"

env_file="$MIGRATION_DIR/staging.env"
umask 077
cat >"$env_file" <<EOF
APP_BEDOLAGA_DATABASE_URL=postgres://postgres:$password@127.0.0.1:$port/bedolaga?sslmode=disable
EOF
chmod 600 "$env_file"
users=$(docker exec "$container" psql -U postgres -d bedolaga -Atc 'SELECT count(*) FROM users')
say "restored: $users Bedolaga users"
say "connection: $env_file (load it with: set -a; . $env_file; set +a)"
say "Remove the copy when the rehearsal is over: docker rm -f $container"
