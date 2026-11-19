#!/bin/sh
# 04 — Apply: write customers, balances, subscriptions, and referral pairs into
# Omniflow. Safe to re-run: finished records are never written twice, and a
# run that stops part-way continues where it stopped.
#
# It writes to APP_DATABASE_URL only. Bedolaga is read inside a read-only
# snapshot and Remnawave's user list is only read.
#
# Environment: as 03-plan.sh, plus
#   APPLY_LIMIT  stop after this many writes (a staged first pass), default none
#   APPLY_ACTOR  name recorded in audit events, default migrate-bedolaga
#
# Exit status: 0 nothing left to write, 3 failures or work remaining, 1 error.
set -eu
. "$(dirname "$0")/lib.sh"

require_importer_env
prepare_dir
base="$MIGRATION_DIR/apply-$(timestamp)"

step "Plan first, so you see what is about to be written"
"$MIGRATE_BEDOLAGA" plan -mapping "$MAPPING_FILE" -json "$base.plan.json" >"$base.plan.txt" ||
  die "plan failed; see $base.plan.txt"
cat "$base.plan.txt"

step "Apply to the Omniflow database"
say "This creates customers and writes wallet credits in the database APP_DATABASE_URL points to."
say "Created customers and ledger entries are not deleted afterwards; a re-run only adds what is missing."
confirm "apply"

set -- apply -mapping "$MAPPING_FILE" -confirm -json "$base.json" -actor "${APPLY_ACTOR:-migrate-bedolaga}"
if [ -n "${APPLY_LIMIT:-}" ]; then
  set -- "$@" -limit "$APPLY_LIMIT"
fi
status=0
"$MIGRATE_BEDOLAGA" "$@" >"$base.txt" 2>&1 || status=$?
cat "$base.txt"

say ""
say "result: $base.txt"
say "        $base.json"
case "$status" in
  0) say "Apply finished with nothing left to write. Next: 05-verify.sh" ;;
  3) warn "apply left failures or remaining work; read the result, fix, and run 04-apply.sh again" ;;
  *) die "apply did not finish (status $status); run 04-apply.sh again to continue" ;;
esac
exit "$status"
