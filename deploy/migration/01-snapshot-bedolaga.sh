#!/bin/sh
# 01 — Snapshot the Bedolaga database.
#
# Runs pg_dump inside the bot's PostgreSQL container and writes a custom-format
# dump plus its SHA-256 checksum to MIGRATION_DIR. Read-only for Bedolaga;
# it never overwrites an existing file.
#
# Environment:
#   BEDOLAGA_DB_CONTAINER  default remnawave_bot_db
#   BEDOLAGA_DB_NAME       default remnawave_bot
#   BEDOLAGA_DB_USER       default remnawave_user
#   MIGRATION_DIR          default ./bedolaga-migration
set -eu
. "$(dirname "$0")/lib.sh"

container="${BEDOLAGA_DB_CONTAINER:-remnawave_bot_db}"
database="${BEDOLAGA_DB_NAME:-remnawave_bot}"
user="${BEDOLAGA_DB_USER:-remnawave_user}"

require_tool docker
prepare_dir
docker inspect "$container" >/dev/null 2>&1 || die "container $container does not exist"

target="$MIGRATION_DIR/bedolaga-$(timestamp).dump"
step "Snapshot $database from container $container"
say "will write: $target"
say "            $target.sha256"

set -C # never clobber an earlier snapshot
: >"$target"
set +C
if ! docker exec "$container" pg_dump -U "$user" -d "$database" --format=custom --no-owner --no-privileges >"$target"; then
  rm -f "$target"
  die "pg_dump failed; no snapshot was kept"
fi
[ -s "$target" ] || die "the dump is empty"
sha256_file "$target" >"$target.sha256"
chmod 600 "$target" "$target.sha256"

size=$(wc -c <"$target" | tr -d ' ')
say "snapshot: $target ($size bytes)"
say "checksum: $(cut -d' ' -f1 "$target.sha256")"
say "Keep this file: it is the archive of everything that does not migrate (payment history, Stars orders, tickets)."
