#!/bin/sh
# 05 — Verify: reconcile Bedolaga against Omniflow. Writes nothing.
#
# Checks that nothing is left to apply, that every importable user has a
# customer holding their Telegram ID, that carried-over balances sum to the
# Bedolaga balances, that every active subscription has an entitlement ending
# when Bedolaga says on the same Remnawave user, and that every referral pair
# points at the right referrer.
#
# Environment: as 03-plan.sh. Exit status 0 when every check passes, 4 on a
# mismatch, 1 on an error.
set -eu
. "$(dirname "$0")/lib.sh"

require_importer_env
prepare_dir
base="$MIGRATION_DIR/verify-$(timestamp)"

step "Verify the import (read-only)"
status=0
"$MIGRATE_BEDOLAGA" verify -mapping "$MAPPING_FILE" -json "$base.json" >"$base.txt" 2>&1 || status=$?
cat "$base.txt"
say ""
say "report: $base.txt"
say "        $base.json"
case "$status" in
  0) say "Verification passed." ;;
  4) warn "verification found a mismatch; do not cut over until it is explained" ;;
  *) die "verification could not run (status $status)" ;;
esac
exit "$status"
