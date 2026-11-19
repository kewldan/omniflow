#!/bin/sh
# 07 — Roll back a cutover: put Bedolaga back in front of customers.
#
# The importer never writes to Bedolaga, so Bedolaga's database is exactly as
# it was when it was stopped; rolling back is restarting it and pointing
# Telegram and the payment providers back at it. Nothing here deletes data in
# either system. What customers did in Omniflow after the cutover — payments,
# top-ups — does not flow back and is listed for manual handling.
#
# Environment:
#   APP_TELEGRAM_TOKEN         the bot token both systems use
#   BEDOLAGA_BOT_CONTAINER     default remnawave_bot
#   BEDOLAGA_EXTRA_CONTAINERS  other Bedolaga containers stopped at cutover
#   OMNIFLOW_BOT_CONTAINER     optional; stopped by this script when set
#   OMNIFLOW_WORKER_CONTAINER  optional; stopped by this script when set
#   OMNIFLOW_DB_CONTAINER      optional; used to count what happened in Omniflow since the cutover
#   OMNIFLOW_DB_USER, OMNIFLOW_DB_NAME  default omniflow / omniflow
set -eu
. "$(dirname "$0")/lib.sh"

require_tool docker
require_tool curl
require_env APP_TELEGRAM_TOKEN
prepare_dir
bedolaga_bot="${BEDOLAGA_BOT_CONTAINER:-remnawave_bot}"
cutover_at=""
[ -r "$MIGRATION_DIR/cutover-at" ] && cutover_at=$(cat "$MIGRATION_DIR/cutover-at")

telegram() {
  printf 'url = "https://api.telegram.org/bot%s/%s"\n' "$APP_TELEGRAM_TOKEN" "$1" |
    curl -sS --max-time 20 -K -
  printf '\n'
}

step "1. Stop Omniflow's bot and worker"
say "The worker is stopped first: it is the Omniflow process that writes to Remnawave."
if [ -n "${OMNIFLOW_WORKER_CONTAINER:-}" ] || [ -n "${OMNIFLOW_BOT_CONTAINER:-}" ]; then
  say "will run: docker stop ${OMNIFLOW_WORKER_CONTAINER:-} ${OMNIFLOW_BOT_CONTAINER:-}"
  confirm "stop omniflow"
  # shellcheck disable=SC2086 # either may be empty
  docker stop ${OMNIFLOW_WORKER_CONTAINER:-} ${OMNIFLOW_BOT_CONTAINER:-} >/dev/null
else
  ask "Omniflow's worker and bot are stopped (keep the API up until providers are re-pointed)"
fi

step "2. What happened in Omniflow since the cutover"
sql="SELECT 'paid orders', count(*) FROM orders
  WHERE paid_at >= '${cutover_at:-infinity}'::timestamptz AND idempotency_key NOT LIKE 'bedolaga:%'
UNION ALL SELECT 'new customers', count(*) FROM users u
  WHERE u.created_at >= '${cutover_at:-infinity}'::timestamptz
    AND NOT EXISTS (SELECT 1 FROM bedolaga_import_items i WHERE i.user_id = u.id)
UNION ALL SELECT 'wallet movements', count(*) FROM ledger_transactions
  WHERE created_at >= '${cutover_at:-infinity}'::timestamptz AND reference_id NOT LIKE 'bedolaga-user:%';"
if [ -z "$cutover_at" ]; then
  warn "no cutover time recorded in $MIGRATION_DIR/cutover-at; counts are skipped"
elif [ -n "${OMNIFLOW_DB_CONTAINER:-}" ]; then
  printf '%s\n' "$sql" | docker exec -i "$OMNIFLOW_DB_CONTAINER" psql -U "${OMNIFLOW_DB_USER:-omniflow}" \
    -d "${OMNIFLOW_DB_NAME:-omniflow}" -At -F ': '
else
  say "Run this against the Omniflow database to see what needs manual handling:"
  printf '%s\n' "$sql"
fi
say "Customers who paid Omniflow after $cutover_at must be credited in Bedolaga by hand; Omniflow keeps its records."

step "3. Hand Telegram back to Bedolaga"
say "will run: docker start $bedolaga_bot ${BEDOLAGA_EXTRA_CONTAINERS:-}"
say "Bedolaga registers its own webhook when it starts in webhook mode."
confirm "start bedolaga"
# shellcheck disable=SC2086 # the extra containers are a word list on purpose
docker start "$bedolaga_bot" ${BEDOLAGA_EXTRA_CONTAINERS:-} >/dev/null
sleep 10
say "Webhook registration now:"
telegram getWebhookInfo
ask "The URL above is Bedolaga's webhook (or empty, if Bedolaga runs in polling mode)"

step "4. Point payment providers back at Bedolaga"
ask "Each provider's notification URL is Bedolaga's again (as recorded before the cutover)"

step "5. Remnawave"
say "While Omniflow's worker ran it may have re-pushed expiries, limits, and squads for imported users."
say "Those values came from the panel itself, so a short run normally changes nothing a customer notices."
ask "Bedolaga's panel sync shows no unexpected changes for a sample of customers"

step "Rollback complete"
say "Omniflow's imported data is left in place. To try again later, run 04-apply.sh: it updates what changed."
