#!/usr/bin/env sh
# Proves that the shipped compose stack keeps the database in the named volume
# an operator backs up, and that a byte written to it survives the container
# being recreated.
#
# This exists because that was once false and nothing noticed. `compose.yaml`
# mounted `postgres-data` at /var/lib/postgresql/data, which was PGDATA up to
# PostgreSQL 17. Version 18 moved it to /var/lib/postgresql/18/docker and
# declares its VOLUME at the parent, so the named volume an operator sees, backs
# up, and is told carries everything was empty, and the real data directory sat
# in an anonymous volume Docker created to satisfy the declaration. That
# survives a restart, which is why nothing looked wrong. It does not survive
# `docker volume prune`, and it is not what gets copied when somebody migrates
# hosts by moving named volumes.
#
# Validating that the file parses and that the images build cannot see this.
# Only writing a row, recreating the container, and reading it back can.
#
#   sh tools/volume-drill.sh
#   COMPOSE_FILE=compose.yaml PROJECT=my-drill sh tools/volume-drill.sh
#
# It runs under a compose project of its own, so it never touches the volumes of
# a stack already running on the same host, and it removes what it created.
# Exits non-zero when the drill fails.
set -u

COMPOSE_FILE="${COMPOSE_FILE:-compose.yaml}"
PROJECT="${PROJECT:-omniflow-volume-drill}"
SERVICE="${SERVICE:-postgres}"
# The volume the operator is told to back up, and the mount it is expected at.
VOLUME="${VOLUME:-postgres-data}"
READY_TIMEOUT="${READY_TIMEOUT:-90}"
RESULT=0

COMPOSE="docker compose -p $PROJECT -f $COMPOSE_FILE"

say() { printf '%s\n' "$*"; }
fail() { say "FAIL: $*"; RESULT=1; }

# A mount the image disagrees with is refused by the entrypoint before
# PostgreSQL starts, and the reason is only in the service's log. Printing it is
# the difference between "the drill failed" and knowing what to change.
died() {
  say "FAIL: $*"
  say "-- $SERVICE said --"
  $COMPOSE logs --no-log-prefix "$SERVICE" 2>&1 | tail -30
  exit 1
}

cleanup() {
  # -v is safe here and nowhere else in this script: the project name is this
  # drill's own, so the only volumes it can reach are the ones it just made.
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1
}
trap cleanup EXIT INT TERM

if [ ! -f .env ]; then
  say "compose.yaml declares env_file: [.env] and Compose refuses to load without it."
  say "Seed it the way the documentation does: cp .env.example .env"
  exit 1
fi

psql_do() {
  $COMPOSE exec -T "$SERVICE" psql -U "$DB_USER" -d "$DB_NAME" -t -A -c "$1" | tr -d '\r'
}

wait_ready() {
  waited=0
  while [ "$waited" -lt "$READY_TIMEOUT" ]; do
    if $COMPOSE exec -T "$SERVICE" pg_isready -U "$DB_USER" -d "$DB_NAME" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done
  return 1
}

say "== starting $SERVICE under project $PROJECT =="
$COMPOSE up -d "$SERVICE" >/dev/null 2>&1

# -aq rather than -q, so a container that started and exited is still resolvable
# and can be asked why.
CONTAINER="$($COMPOSE ps -aq "$SERVICE" | head -1)"
if [ -z "${CONTAINER:-}" ]; then
  say "FAIL: $COMPOSE_FILE created no $SERVICE container at all"
  exit 1
fi
if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" != "true" ]; then
  died "$SERVICE exited instead of starting, so $COMPOSE_FILE never held a database"
fi

# The credentials are read off the container the compose file actually started,
# rather than duplicated here where they would go stale, and rather than parsed
# out of the file where another service's variable of the same name could win.
container_env() {
  docker inspect "$CONTAINER" \
    --format "{{range .Config.Env}}{{if eq (index (split . \"=\") 0) \"$1\"}}{{index (split . \"=\") 1}}{{end}}{{end}}"
}
DB_USER="$(container_env POSTGRES_USER)"
DB_NAME="$(container_env POSTGRES_DB)"
if [ -z "${DB_USER:-}" ] || [ -z "${DB_NAME:-}" ]; then
  say "the $SERVICE container declares no POSTGRES_USER/POSTGRES_DB"
  exit 1
fi

if ! wait_ready; then
  died "$SERVICE did not accept connections within ${READY_TIMEOUT}s"
fi

# Every volume mount, as "<name> <destination>". A volume Docker names itself is
# 64 hex characters; anything else was asked for by the compose file.
MOUNTS="$(docker inspect "$CONTAINER" \
  --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{.Destination}}
{{end}}{{end}}')"

say "== the mount the data directory has to be under =="
DESTINATION="$(printf '%s\n' "$MOUNTS" | grep -E "^${PROJECT}_${VOLUME} " | cut -d' ' -f2)"
if [ -z "${DESTINATION:-}" ]; then
  fail "$COMPOSE_FILE does not mount the $VOLUME volume on $SERVICE at all"
else
  say "$VOLUME is mounted at $DESTINATION"
fi

say "== anonymous volumes =="
# An anonymous volume on this container means the image declared a VOLUME that
# the compose file did not cover. For PostgreSQL that is precisely the defect:
# the data directory has gone somewhere nobody backs up.
ANONYMOUS="$(printf '%s\n' "$MOUNTS" | grep -E '^[0-9a-f]{64} ' || true)"
if [ -n "${ANONYMOUS:-}" ]; then
  printf '%s\n' "$ANONYMOUS" | while read -r name destination; do
    [ -n "${name:-}" ] && say "  $destination is an anonymous volume ($name)"
  done
  fail "$SERVICE has anonymous volumes; a mount in $COMPOSE_FILE does not cover a declared VOLUME"
else
  say "none"
fi

say "== where PostgreSQL is actually writing =="
DATA_DIRECTORY="$(psql_do 'SHOW data_directory')"
say "data_directory is $DATA_DIRECTORY"
case "${DESTINATION:-/dev/null}" in
  /) PREFIX_OK=1 ;;
  *) case "$DATA_DIRECTORY" in
       "$DESTINATION"/*) PREFIX_OK=1 ;;
       *) PREFIX_OK=0 ;;
     esac ;;
esac
if [ "${PREFIX_OK:-0}" -ne 1 ]; then
  fail "the data directory is outside $DESTINATION, so the $VOLUME volume does not hold the database"
fi

say "== a row written before the container is recreated =="
MARKER="volume drill $(date -u +%Y-%m-%dT%H:%M:%SZ)"
psql_do "CREATE TABLE IF NOT EXISTS volume_drill (marker text PRIMARY KEY)" >/dev/null
psql_do "INSERT INTO volume_drill (marker) VALUES ('$MARKER')" >/dev/null
if [ "$(psql_do "SELECT count(*) FROM volume_drill WHERE marker = '$MARKER'")" != "1" ]; then
  say "the marker row could not be written; the drill cannot conclude anything"
  exit 1
fi

# down without -v is what an operator does for an upgrade or a host restart: it
# removes the containers and keeps the named volumes. Anything that was in an
# anonymous volume is now unreferenced, which is the state `docker volume prune`
# collects. Removing only the names recorded above reproduces that without
# reaching a volume belonging to anything else on the host.
say "== recreating the container =="
$COMPOSE down >/dev/null 2>&1
if [ -n "${ANONYMOUS:-}" ]; then
  printf '%s\n' "$ANONYMOUS" | cut -d' ' -f1 | while read -r name; do
    [ -n "${name:-}" ] && docker volume rm "$name" >/dev/null 2>&1
  done
fi
$COMPOSE up -d "$SERVICE" >/dev/null 2>&1
if ! wait_ready; then
  died "$SERVICE did not come back after being recreated, so the volume it left behind is unusable"
fi

say "== reading it back =="
SURVIVED="$(psql_do "SELECT count(*) FROM volume_drill WHERE marker = '$MARKER'" 2>/dev/null)"
if [ "${SURVIVED:-0}" = "1" ]; then
  say "the row is still there"
else
  fail "the row did not survive; the database is not in the $VOLUME volume"
fi

if [ "$RESULT" -eq 0 ]; then
  say "== the database is in $VOLUME and survives being recreated =="
else
  say "== the drill failed =="
fi
exit "$RESULT"
