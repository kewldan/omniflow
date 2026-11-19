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
#   - the Telegram Stars shop (Fragment cookies, TON wallet, TON API) when
#     STARS_SHOP_ENABLED is true.
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
    core["REMNAWAVE_AUTH_TYPE"] = "APP_REMNAWAVE_AUTH_TYPE"
    core["REMNAWAVE_USERNAME"] = "APP_REMNAWAVE_USERNAME"
    core["REMNAWAVE_PASSWORD"] = "APP_REMNAWAVE_PASSWORD"
    core["REMNAWAVE_CADDY_TOKEN"] = "APP_REMNAWAVE_CADDY_TOKEN"
    core["REMNAWAVE_SECRET_KEY"] = "APP_REMNAWAVE_SECRET_KEY"
    core["REMNAWAVE_API_TOTAL_TIMEOUT"] = "APP_REMNAWAVE_TIMEOUT_SECONDS"
    core["REMNAWAVE_API_REQUESTS_PER_MINUTE"] = "APP_REMNAWAVE_REQUESTS_PER_MINUTE"
    core["REMNAWAVE_USER_USERNAME_TEMPLATE"] = "APP_REMNAWAVE_USERNAME_TEMPLATE"
    core["REMNAWAVE_USER_DESCRIPTION_TEMPLATE"] = "APP_REMNAWAVE_DESCRIPTION_TEMPLATE"
    core["YOOKASSA_SHOP_ID"] = "APP_YOOKASSA_SHOP_ID"
    core["YOOKASSA_SECRET_KEY"] = "APP_YOOKASSA_SECRET"
    core["YOOKASSA_DEFAULT_RECEIPT_EMAIL"] = "APP_YOOKASSA_RECEIPT_EMAIL"
    core["YOOKASSA_VAT_CODE"] = "APP_YOOKASSA_VAT_CODE"
    core["YOOKASSA_PAYMENT_SUBJECT"] = "APP_YOOKASSA_PAYMENT_SUBJECT"
    core["YOOKASSA_PAYMENT_MODE"] = "APP_YOOKASSA_PAYMENT_MODE"
    core["NOTIFICATION_EXPIRED_WAVE2_DISCOUNT_PERCENT"] = "APP_WINBACK_WAVE2_PERCENT"
    core["NOTIFICATION_EXPIRED_WAVE3_DISCOUNT_PERCENT"] = "APP_WINBACK_WAVE3_PERCENT"
    core["NOTIFICATION_EXPIRED_WAVE3_TRIGGER_DAYS"] = "APP_WINBACK_WAVE3_AFTER_DAYS"
    core["NOTIFICATION_EXPIRED_WAVE2_VALID_HOURS"] = "APP_WINBACK_VALID_HOURS"
    core["CRYPTOBOT_API_TOKEN"] = "APP_CRYPTOBOT_TOKEN"
    core["CRYPTOBOT_TESTNET"] = "APP_CRYPTOBOT_TESTNET"
    core["WEBHOOK_SECRET_TOKEN"] = "APP_TELEGRAM_WEBHOOK_SECRET"
    core["CABINET_URL"] = "APP_PUBLIC_URL"
    core["SUPPORT_USERNAME"] = "APP_SUPPORT_URL"
    # Telegram Stars shop of the Omniflow fork: carried only when STARS_SHOP_ENABLED=true.
    stars["FRAGMENT_COOKIES"] = "APP_FRAGMENT_COOKIES"
    stars["FRAGMENT_WALLET_MNEMONIC"] = "APP_FRAGMENT_WALLET_MNEMONIC"
    stars["FRAGMENT_WALLET_VERSION"] = "APP_FRAGMENT_WALLET_VERSION"
    stars["FRAGMENT_TON_API_KEY"] = "APP_FRAGMENT_TON_API_KEY"
    stars["FRAGMENT_TON_API_PROVIDER"] = "APP_FRAGMENT_TON_API_PROVIDER"
    stars["FRAGMENT_TON_API_RPS"] = "APP_FRAGMENT_TON_API_RPS"
    stars["FRAGMENT_PROXY"] = "APP_FRAGMENT_PROXY_URL"
    stars["STARS_SHOP_SHOW_SENDER"] = "APP_FRAGMENT_SHOW_SENDER"
    stars["STARS_SHOP_DRY_RUN"] = "APP_FRAGMENT_DRY_RUN"
    stars["STARS_SHOP_TON_RATE_KOPEKS"] = "APP_FRAGMENT_TON_RATE_MINOR"
    stars["STARS_SHOP_WALLET_LOW_STARS"] = "APP_FRAGMENT_WALLET_LOW_STARS"
    stars["STARS_SHOP_VPN_OFFER_ENABLED"] = "APP_GOODS_VPN_OFFER_ENABLED"
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
      if ((key in stars) && tolower(env["STARS_SHOP_ENABLED"]) == "true") {
        target = stars[key]
      } else if (key in core) {
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
      # Single-quote anything a shell or Compose would split or expand
      # (mnemonics, cookie strings); both read single quotes literally.
      if (value ~ /[ \t;#$"`\\]/ && value !~ /\x27/) value = "\x27" value "\x27"
      printf "%s=%s\n", target, value >> out
      written[target] = 1
      print "  " key " -> " target
      carried++
    }
    # Bedolaga sends cabinet mail over SMTP; with the Resend SMTP relay the
    # password is the Resend API key, which is what the Omniflow transport takes.
    if (tolower(env["SMTP_HOST"]) ~ /resend/ && env["SMTP_PASSWORD"] != "" && env["SMTP_FROM_EMAIL"] != "") {
      from = env["SMTP_FROM_EMAIL"]
      if (env["SMTP_FROM_NAME"] != "") from = env["SMTP_FROM_NAME"] " <" from ">"
      printf "APP_RESEND_API_KEY=%s\nAPP_EMAIL_FROM=\x27%s\x27\n", env["SMTP_PASSWORD"], from >> out
      print "  SMTP_PASSWORD -> APP_RESEND_API_KEY, SMTP_FROM_* -> APP_EMAIL_FROM (e-mail sign-in)"
      carried += 2
    } else if (env["SMTP_HOST"] != "") {
      print "WARNING: Bedolaga sends mail through " env["SMTP_HOST"] ", which is not Resend. Omniflow sends sign-in"
      print "         mail through Resend only: set APP_RESEND_API_KEY and APP_EMAIL_FROM, or e-mail customers cannot sign in."
    }
    for (p = 1; p <= n; p++) {
      if (tolower(env[providers[p] "_ENABLED"]) == "true") enabled = enabled " " tolower(providers[p])
    }
    print ""
    print carried + 0 " variables written. Enabled payment providers carried:" (enabled == "" ? " none" : enabled)
    if (env["YOOKASSA_SHOP_ID"] != "" && !("APP_YOOKASSA_RECEIPT_EMAIL" in written)) {
      print "WARNING: Bedolaga always sends YooKassa receipts. Set APP_YOOKASSA_RECEIPT_EMAIL, or every payment"
      print "         fails if the shop uses \"Чеки от ЮKassa\" or an online cash register."
    }
    if (tolower(env["STARS_SHOP_ENABLED"]) == "true") {
      print "Stars shop carried. Set the price per Star, limits and presets in /admin/shop -> Stars:"
      print "  STARS_SHOP_PRICE_PER_STAR_KOPEKS, _MIN_QUANTITY, _MAX_QUANTITY and _PRESETS are panel settings in Omniflow."
    }
  }
' "$source_env"

echo "Output: $target_env (mode 600). Values were not printed. Review it before merging into Omniflow's .env."
echo "Not carried (no equivalent): TRIAL_*, WEB_API_*, MINIAPP_*, REMNAWAVE_WEBHOOK_*, Redis and Bedolaga's own database settings."
