#!/bin/sh
# 03 — Plan: report what apply would do. Writes nothing to any database.
#
# Saves the text and JSON report to MIGRATION_DIR/plan-<time>.{txt,json}.
#
# Environment (see docs/operations/migrate-from-bedolaga.mdx):
#   APP_DATABASE_URL, APP_DATA_ENCRYPTION_KEY, APP_REMNAWAVE_URL,
#   APP_REMNAWAVE_TOKEN, APP_BEDOLAGA_DATABASE_URL, MAPPING_FILE,
#   MIGRATE_BEDOLAGA (path to the importer, default: migrate-bedolaga on PATH)
set -eu
. "$(dirname "$0")/lib.sh"

require_importer_env
prepare_dir
base="$MIGRATION_DIR/plan-$(timestamp)"

step "Plan the import (read-only)"
status=0
"$MIGRATE_BEDOLAGA" plan -mapping "$MAPPING_FILE" -json "$base.json" >"$base.txt" 2>&1 || status=$?
cat "$base.txt"
[ "$status" -eq 0 ] || die "plan failed (status $status)"
say ""
say "report: $base.txt"
say "        $base.json"
say "Before apply: map every tariff listed as unmapped, decide blockedUsers, and read every conflict."
