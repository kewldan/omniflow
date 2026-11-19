#!/bin/sh
# Converts a Bedolaga .env into the Omniflow variables it has equivalents for.
#
#   sh deploy/migration/00-convert-env.sh /root/remnawave-bedolaga-telegram-bot/.env omniflow.env
#
# What it carries:
#   - the core settings in the runbook's table (bot token, Remnawave, operators,
#     YooKassa, CryptoBot, webhook secret, support link, public URL);
#   - every payment provider whose <PROVIDER>_ENABLED is true in Bedolaga: each of
#     its <PROVIDER>_* keys that Omniflow also reads becomes APP_<PROVIDER>_*.
#     A disabled provider's credentials are deliberately left behind.
#
# It never prints a value: the summary lists variable names only, and the output
# file is created with mode 600. It refuses to overwrite an existing file.
# Review the result, then merge it into Omniflow's .env by hand.
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
source_env="${1:-}"
target_env="${2:-}"
[ -n "$source_env" ] && [ -n "$target_env" ] || {
  echo "usage: $0 <bedolaga .env> <output file>" >&2
  exit 2
}
[ -r "$source_env" ] || { echo "error: cannot read $source_env" >&2; exit 1; }
[ ! -e "$target_env" ] || { echo "error: $target_env exists; refusing to overwrite" >&2; exit 1; }

known="$(mktemp)"
trap 'rm -f "$known"' EXIT INT TERM
# Every APP_* name Omniflow documents, commented or not.
sed -n 's/^#* *\(APP_[A-Z0-9_]*\)=.*/\1/p' "$root/.env.example" | sort -u >"$known"

umask 077
: >"$target_env"
chmod 600 "$target_env"

awk -v known="$known" -v out="$target_env" '
  function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
  function unquote(s) {
    if (s ~ /^".*"$/ || s ~ /^\x27.*\x27$/) return substr(s, 2, length(s) - 2)
    return s
  }
  BEGIN {
    while ((getline line < known) > 0) omniflow[line] = 1
    core["BOT_TOKEN"] = "APP_TELEGRAM_TOKEN"
    core["ADMIN_IDS"] = "APP_OPERATOR_TELEGRAM_IDS"
    core["ADMIN_NOTIFICATIONS_CHAT_ID"] = "APP_OPERATOR_CHAT_ID"
    core["REMNAWAVE_API_URL"] = "APP_REMNAWAVE_URL"
    core["REMNAWAVE_API_KEY"] = "APP_REMNAWAVE_TOKEN"
    core["YOOKASSA_SHOP_ID"] = "APP_YOOKASSA_SHOP_ID"
    core["YOOKASSA_SECRET_KEY"] = "APP_YOOKASSA_SECRET"
    core["CRYPTOBOT_API_TOKEN"] = "APP_CRYPTOBOT_TOKEN"
    core["CRYPTOBOT_TESTNET"] = "APP_CRYPTOBOT_TESTNET"
    core["WEBHOOK_SECRET_TOKEN"] = "APP_TELEGRAM_WEBHOOK_SECRET"
    core["CABINET_URL"] = "APP_PUBLIC_URL"
    core["SUPPORT_USERNAME"] = "APP_SUPPORT_URL"
    n = split("HELEKET MULENPAY PAL24 PLATEGA CASHERA TRIBUTE FREEKASSA KASSA_AI RIOPAY SEVERPAY ROLLYPAY AURAPAY WATA CLOUDPAYMENTS LAVA OVERPAY PARITYPAY PAYHOT CISPAY ANTILOPAY ANORE DONUT ETOPLATEZHI JUPITER TABPAY", providers, " ")
  }
  /^[ \t]*#/ || !/=/ { next }
  {
    eq = index($0, "=")
    key = trim(substr($0, 1, eq - 1))
    sub(/^export[ \t]+/, "", key)
    value = unquote(trim(substr($0, eq + 1)))
    if (key == "" || value == "") next
    env[key] = value
    order[++count] = key
  }
  END {
    for (i = 1; i <= count; i++) {
      key = order[i]; value = env[key]; target = ""
      if (key in core) {
        target = core[key]
        if (key == "SUPPORT_USERNAME" && value !~ /^https?:\/\//) { sub(/^@/, "", value); value = "https://t.me/" value }
      } else {
        for (p = 1; p <= n; p++) {
          prefix = providers[p] "_"
          if (index(key, prefix) == 1 && tolower(env[providers[p] "_ENABLED"]) == "true" && ("APP_" key) in omniflow) {
            target = "APP_" key
          }
        }
      }
      if (target == "" || (target in written)) continue
      printf "%s=%s\n", target, value >> out
      written[target] = 1
      print "  " key " -> " target
      carried++
    }
    for (p = 1; p <= n; p++) {
      if (tolower(env[providers[p] "_ENABLED"]) == "true") enabled = enabled " " tolower(providers[p])
    }
    print ""
    print carried + 0 " variables written. Enabled payment providers carried:" (enabled == "" ? " none" : enabled)
  }
' "$source_env"

echo "Output: $target_env (mode 600). Values were not printed. Review it before merging into Omniflow's .env."
echo "Not carried (no equivalent): TRIAL_*, WEB_API_*, MINIAPP_*, REMNAWAVE_WEBHOOK_*, Redis and Bedolaga's own database settings."
