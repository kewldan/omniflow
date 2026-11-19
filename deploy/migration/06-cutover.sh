#!/bin/sh
# 06 — Cutover: move production from Bedolaga to Omniflow.
#
# A checklist that runs the steps it can and stops for the ones a person must
# do in a provider's dashboard. Every step that changes something prints what
# it will do and waits for a typed confirmation. Run it in a terminal you keep
# open; it records the cutover time for 07-rollback.sh.
#
# Order matters: Bedolaga stops taking orders before the final import, and
# Omniflow's worker starts last, after verification, because it is the first
# Omniflow process that writes to Remnawave.
#
# Environment (values are checked, never printed):
#   everything 03-plan.sh needs, with APP_BEDOLAGA_DATABASE_URL pointing at the
#     production bot database — ideally a read-only role
#   APP_TELEGRAM_TOKEN         the bot token Bedolaga used (BOT_TOKEN); the same bot moves
#   APP_PUBLIC_URL             Omniflow's public origin, for payment webhook URLs
#   BEDOLAGA_BOT_CONTAINER     default remnawave_bot
#   BEDOLAGA_EXTRA_CONTAINERS  other Bedolaga containers to stop (space-separated), e.g. the cabinet
#   OMNIFLOW_BOT_CONTAINER     optional; started by this script when set
#   OMNIFLOW_WORKER_CONTAINER  optional; started by this script when set
#   OMNIFLOW_TELEGRAM_MODE     webhook (default) or polling
#   PAYMENT_PROVIDERS          provider slugs that need a webhook, default "yookassa"
#   SAMPLE_SUBSCRIPTION_URL    optional; an existing customer's subscription link, checked before and after
set -eu
. "$(dirname "$0")/lib.sh"

here="$(dirname "$0")"
require_tool docker
require_tool curl
require_importer_env
require_env APP_TELEGRAM_TOKEN APP_PUBLIC_URL
prepare_dir
bedolaga_bot="${BEDOLAGA_BOT_CONTAINER:-remnawave_bot}"
mode="${OMNIFLOW_TELEGRAM_MODE:-webhook}"
case "$mode" in webhook | polling) ;; *) die "OMNIFLOW_TELEGRAM_MODE must be webhook or polling" ;; esac
public_url=$(printf '%s' "$APP_PUBLIC_URL" | sed 's:/*$::')

# telegram METHOD calls the Bot API. The token travels on curl's stdin, not on
# its command line, so it never shows up in the process list.
telegram() {
  printf 'url = "https://api.telegram.org/bot%s/%s"\n' "$APP_TELEGRAM_TOKEN" "$1" |
    curl -sS --max-time 20 -K -
  printf '\n'
}

check_subscription() {
  [ -n "${SAMPLE_SUBSCRIPTION_URL:-}" ] || return 0
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 "$SAMPLE_SUBSCRIPTION_URL" || true)
  say "sample subscription link answered HTTP $code"
  [ "$code" = "200" ] || warn "the sample subscription link did not answer 200"
}

step "1. Preconditions"
ask "A rehearsal on a restored snapshot (02–05) passed verification with this mapping file"
ask "Omniflow production is deployed, its migrations are applied, and the API answers on $public_url"
ask "Omniflow's bot and worker are NOT running yet (the worker is the first Omniflow process that writes to Remnawave)"
ask "Customers were told about the maintenance window"
check_subscription

step "2. Put Bedolaga into maintenance"
say "will run: docker stop $bedolaga_bot ${BEDOLAGA_EXTRA_CONTAINERS:-}"
say "Bedolaga stops answering and stops taking payments. Its database keeps running for the final import."
say "Reversible with: docker start $bedolaga_bot ${BEDOLAGA_EXTRA_CONTAINERS:-}"
confirm "stop bedolaga"
# shellcheck disable=SC2086 # the extra containers are a word list on purpose
docker stop "$bedolaga_bot" ${BEDOLAGA_EXTRA_CONTAINERS:-} >/dev/null
date -u +%Y-%m-%dT%H:%M:%SZ >"$MIGRATION_DIR/cutover-at"
say "Bedolaga stopped at $(cat "$MIGRATION_DIR/cutover-at")"
say "If a payment provider still delivers a webhook to Bedolaga now, it is retried later; nothing is lost yet."

step "3. Final snapshot"
"$here/01-snapshot-bedolaga.sh"

step "4. Final import and verification"
ask "APP_BEDOLAGA_DATABASE_URL points at the production bot database (not the staging copy)"
"$here/04-apply.sh" || die "the final import did not finish cleanly; fix and re-run 04-apply.sh, or roll back with 07-rollback.sh"
"$here/05-verify.sh" || die "verification failed; do not switch traffic. Investigate, or roll back with 07-rollback.sh"

step "5. Move the Telegram bot"
say "Current webhook registration:"
telegram getWebhookInfo
if [ "$mode" = "polling" ]; then
  say "Omniflow will use long polling, which Telegram refuses while a webhook is registered."
  say "will call: deleteWebhook (pending updates are kept)"
  confirm "delete webhook"
  telegram deleteWebhook
else
  say "Omniflow's bot registers its own webhook (APP_TELEGRAM_WEBHOOK_URL) when it starts."
fi
if [ -n "${OMNIFLOW_BOT_CONTAINER:-}" ]; then
  say "will run: docker start $OMNIFLOW_BOT_CONTAINER"
  confirm "start omniflow bot"
  docker start "$OMNIFLOW_BOT_CONTAINER" >/dev/null
else
  ask "Start Omniflow's bot with APP_TELEGRAM_TOKEN set to the same token Bedolaga used"
fi
sleep 5
say "Webhook registration now:"
telegram getWebhookInfo
ask "Send /start to the bot from a Telegram account that was in Bedolaga and see your subscription and balance"

step "6. Point payment providers at Omniflow"
for slug in ${PAYMENT_PROVIDERS:-yookassa}; do
  say "  $slug: $public_url/v1/payments/webhooks/$slug"
done
say "Telegram Stars needs no URL: payments arrive through the bot's own updates."
ask "Each provider's notification URL above is set in that provider's dashboard"

step "7. Subscription links stay where they are"
say "Remnawave owns subscription links. Do not change the panel's subscription domain or path."
check_subscription
ask "The sample customer's VPN still connects with the link they already had"

step "8. Start Omniflow's worker"
say "From now on the worker reconciles imported entitlements with Remnawave every 15 minutes."
if [ -n "${OMNIFLOW_WORKER_CONTAINER:-}" ]; then
  say "will run: docker start $OMNIFLOW_WORKER_CONTAINER"
  confirm "start omniflow worker"
  docker start "$OMNIFLOW_WORKER_CONTAINER" >/dev/null
else
  ask "Start Omniflow's worker"
fi
ask "After 15 minutes, /admin → System → Drift shows no unexpected drift for imported subscriptions"

step "Cutover complete"
say "Cutover time: $(cat "$MIGRATION_DIR/cutover-at") (kept in $MIGRATION_DIR/cutover-at for 07-rollback.sh)"
say "Keep the Bedolaga containers stopped, not removed, until you are sure you will not roll back."
