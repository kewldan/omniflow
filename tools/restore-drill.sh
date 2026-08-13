#!/usr/bin/env sh
# The disaster-recovery drill from docs/operations/backup-restore.mdx, as a
# script rather than as a list somebody follows by hand.
#
# It restores the most recent completed backup into a throwaway PostgreSQL
# instance, compares row counts with the live database, and proves that
# APP_DATA_ENCRYPTION_KEY still opens a sealed value in the restored copy — the
# step the runbook calls the one most often skipped and the one that most often
# fails, because a database restored without its data-encryption key loads fine
# and has unreadable secrets.
#
# It never writes to the live database. The only thing it creates is a container
# it removes again.
#
#   sh tools/restore-drill.sh
#   COMPOSE="docker compose -f compose.yaml -f compose.prod.yaml" sh tools/restore-drill.sh
#
# Exits non-zero when the drill fails, so it can be run from a scheduler and
# have its result mean something.
set -u

COMPOSE="${COMPOSE:-docker compose}"
THROWAWAY="${THROWAWAY:-omniflow-restore-drill}"
PG_IMAGE="${PG_IMAGE:-postgres:18.4-alpine}"
ENV_FILE="${ENV_FILE:-.env}"
WORK="$(mktemp -d)"
RESULT=0

# The tables the runbook names, plus the operator and audit tables, so the
# comparison still means something on an installation that has not sold
# anything yet.
TABLES="${TABLES:-orders ledger_entries entitlements subscriptions admin_users audit_events}"

cleanup() {
  docker rm -f "$THROWAWAY" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

say() { printf '%s\n' "$*"; }
fail() { say "FAIL: $*"; RESULT=1; }

# The throwaway instance joins the stack's network so the worker can be handed
# the dump over a pipe and the API's own database credentials keep working.
NETWORK="$($COMPOSE ps --format '{{.Name}}' postgres 2>/dev/null | head -1 |
  xargs -r docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null)"
if [ -z "${NETWORK:-}" ]; then
  say "could not find the stack's network; is it running?"
  exit 1
fi

say "== the most recent completed backup =="
BACKUP=$($COMPOSE exec -T postgres psql -U omniflow -d omniflow -t -A -F'|' \
  -c "select file_name, size_bytes from backups where status='completed' order by started_at desc limit 1;" |
  tr -d '\r')
FILE=$(printf '%s' "$BACKUP" | cut -d'|' -f1)
if [ -z "$FILE" ]; then
  say "no completed backup exists. Set APP_BACKUP_ENABLED=true and let the worker take one."
  exit 1
fi
say "$BACKUP"
say "(a backup reaches 'completed' only after being read back and digest-checked)"

say ""
say "== live row counts =="
QUERY=""
for TABLE in $TABLES; do
  [ -n "$QUERY" ] && QUERY="$QUERY union all "
  QUERY="$QUERY select '$TABLE' as t, count(*) from $TABLE"
done
QUERY="$QUERY order by 1"
$COMPOSE exec -T postgres psql -U omniflow -d omniflow -t -A -F',' -c "$QUERY" |
  tr -d '\r' > "$WORK/live.txt"
cat "$WORK/live.txt"

say ""
say "== throwaway instance =="
docker rm -f "$THROWAWAY" >/dev/null 2>&1
docker run -d --name "$THROWAWAY" --network "$NETWORK" \
  -e POSTGRES_DB=omniflow -e POSTGRES_USER=omniflow -e POSTGRES_PASSWORD=omniflow \
  "$PG_IMAGE" >/dev/null
i=0
while [ "$i" -lt 60 ]; do
  docker exec "$THROWAWAY" pg_isready -U omniflow -d omniflow >/dev/null 2>&1 && break
  i=$((i + 1))
  sleep 1
done
say "ready"

say ""
say "== decrypt and restore =="
$COMPOSE exec -T worker sh -c "cat /var/lib/omniflow/backups/$FILE" > "$WORK/backup.enc" 2>/dev/null
say "encrypted: $(wc -c < "$WORK/backup.enc") bytes"
# --decrypt-backup must be the first argument, and reads the key from the
# environment before any configuration is loaded.
#
# --no-deps matters more than it looks: without it `compose run` starts this
# service's dependencies, and if the invocation does not name every override
# file the running stack uses, compose sees a different configuration and
# recreates the database underneath a drill whose whole purpose is to not
# disturb it.
$COMPOSE run --rm --no-deps -T worker --decrypt-backup \
  < "$WORK/backup.enc" > "$WORK/backup.dump" 2>"$WORK/decrypt.err"
DUMP_BYTES=$(wc -c < "$WORK/backup.dump")
say "decrypted: $DUMP_BYTES bytes"
if [ "$DUMP_BYTES" -lt 1000 ]; then
  cat "$WORK/decrypt.err"
  fail "decryption produced nothing usable"
  exit 1
fi

docker exec -i "$THROWAWAY" pg_restore \
  --clean --if-exists --no-owner --no-privileges --single-transaction \
  --dbname "postgres://omniflow:omniflow@localhost:5432/omniflow" \
  < "$WORK/backup.dump" > "$WORK/restore.log" 2>&1 ||
  say "pg_restore reported: $(tail -2 "$WORK/restore.log")"

say ""
say "== row counts in the restored copy =="
docker exec -i "$THROWAWAY" psql -U omniflow -d omniflow -t -A -F',' -c "$QUERY" |
  tr -d '\r' > "$WORK/restored.txt"
# What a difference means depends on its direction. The live database keeps
# running while the drill reads a backup taken earlier, so live > restored is
# ordinary drift and says nothing about the restore. The two that do mean
# something are a table missing from the copy — the schema did not come across —
# and restored > live, which cannot happen by drift and means the copy is not of
# this database.
printf '%-24s %10s %10s  %s\n' "TABLE" "LIVE" "RESTORED" "VERDICT"
while IFS=',' read -r TABLE LIVECOUNT; do
  [ -z "$TABLE" ] && continue
  RESTORED=$(grep "^$TABLE," "$WORK/restored.txt" | cut -d',' -f2)
  if [ -z "$RESTORED" ]; then
    VERDICT="MISSING"
    RESULT=1
  elif [ "$LIVECOUNT" = "$RESTORED" ]; then
    VERDICT="same"
  elif [ "$RESTORED" -gt "$LIVECOUNT" ]; then
    VERDICT="MORE THAN LIVE"
    RESULT=1
  else
    VERDICT="live moved on"
  fi
  printf '%-24s %10s %10s  %s\n' "$TABLE" "$LIVECOUNT" "${RESTORED:-none}" "$VERDICT"
done < "$WORK/live.txt"
say ""
say "'live moved on' is expected: the database kept working after the backup was"
say "taken. For an exact comparison, take a backup and run this immediately after."

say ""
say "== the data-encryption key still opens a sealed value =="
CIPHER=$(docker exec -i "$THROWAWAY" psql -U omniflow -d omniflow -t -A \
  -c "select encode(client_secret_ciphertext,'hex') from customer_oidc_providers
      where client_secret_ciphertext is not null limit 1;" | tr -d ' \r')
if [ -z "$CIPHER" ]; then
  say "no sealed value in this installation to check — configure a customer OIDC"
  say "provider, or check a TOTP secret or provider credential by hand."
else
  KEY=$(grep '^APP_DATA_ENCRYPTION_KEY=' "$ENV_FILE" 2>/dev/null | cut -d= -f2-)
  if [ -z "$KEY" ]; then
    fail "APP_DATA_ENCRYPTION_KEY not found in $ENV_FILE"
  else
    # Opened in a container rather than on the host, so the drill needs nothing
    # installed to run: the scheme is AES-256-GCM with the nonce prefixed and
    # the field's purpose as associated data, matching internal/customerauthpg.
    # Both values arrive in the environment. A heredoc would take the
    # interpreter's stdin, so anything piped in would never be read.
    OPENED=$(docker run --rm -e KEY="$KEY" -e CIPHER="$CIPHER" \
      python:3.13-alpine sh -c '
        pip install --quiet --disable-pip-version-check cryptography >/dev/null 2>&1 || exit 3
        python -c "
import base64, binascii, os, sys
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
key = base64.b64decode(os.environ[\"KEY\"])
raw = os.environ[\"CIPHER\"].strip()
raw = raw[2:] if raw.startswith(chr(92) + chr(120)) else raw
sealed = binascii.unhexlify(raw)
try:
    AESGCM(key).decrypt(sealed[:12], sealed[12:], b\"customer.oidc.client_secret\")
except Exception as error:
    sys.exit(\"OPEN FAILED: %s\" % error)
print(\"opened\")
"' 2>&1 | tail -1)
    case "$OPENED" in
      opened)
        say "the sealed value opened under APP_DATA_ENCRYPTION_KEY"
        say "(a restore that carries the bytes across but not the key loads fine"
        say " and has unreadable secrets, which is why this step exists)"
        ;;
      *) fail "$OPENED" ;;
    esac
  fi
fi

say ""
if [ "$RESULT" -eq 0 ]; then
  say "DRILL: PASS"
else
  say "DRILL: FAIL"
fi
exit "$RESULT"
