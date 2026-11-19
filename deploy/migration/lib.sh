# Shared helpers for the Bedolaga → Omniflow migration scripts.
# Sourced, never executed. POSIX sh; no secrets are read or printed here.
#
# Configuration comes from the environment only. Put it in a file outside the
# repository (chmod 600) and source it before running a script:
#
#   set -a; . /root/omniflow-migration.env; set +a
#
# Confirmation: every step that changes something asks you to type a phrase.
# For an unattended rehearsal you may set MIGRATION_CONFIRM to that exact
# phrase; it is never accepted for a step whose phrase differs.

MIGRATION_DIR="${MIGRATION_DIR:-./bedolaga-migration}"
MIGRATE_BEDOLAGA="${MIGRATE_BEDOLAGA:-migrate-bedolaga}"

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

# require_env NAME... fails unless every variable is set and non-empty. It
# never prints a value.
require_env() {
  for require_env_name in "$@"; do
    eval "require_env_value=\${$require_env_name:-}"
    [ -n "$require_env_value" ] || die "$require_env_name is not set (see docs/operations/migrate-from-bedolaga.mdx)"
  done
  unset require_env_name require_env_value
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required but was not found on PATH"
}

# confirm PHRASE asks the operator to type PHRASE exactly. Anything else stops
# the script before the step runs.
confirm() {
  confirm_phrase="$1"
  if [ "${MIGRATION_CONFIRM:-}" = "$confirm_phrase" ]; then
    say "confirmed by MIGRATION_CONFIRM"
    return 0
  fi
  if [ ! -t 0 ]; then
    die "this step needs confirmation; run interactively or set MIGRATION_CONFIRM='$confirm_phrase'"
  fi
  printf 'Type "%s" to continue, anything else to stop: ' "$confirm_phrase"
  read -r confirm_answer || confirm_answer=""
  [ "$confirm_answer" = "$confirm_phrase" ] || die "not confirmed; nothing was changed by this step"
  unset confirm_phrase confirm_answer
}

# ask QUESTION waits for the operator to acknowledge a manual checklist item.
ask() {
  if [ "${MIGRATION_CONFIRM:-}" = "checklist" ]; then
    say "[x] $1"
    return 0
  fi
  [ -t 0 ] || die "checklist item needs an operator: $1"
  printf '[ ] %s\n    Done? (yes/no): ' "$1"
  read -r ask_answer || ask_answer=""
  [ "$ask_answer" = "yes" ] || die "stopped at: $1"
  unset ask_answer
}

# timestamp names output files; the process ID keeps two runs in one second apart.
timestamp() { printf "%s-%s" "$(date -u +%Y%m%dT%H%M%SZ)" "$$"; }

prepare_dir() {
  umask 077
  mkdir -p "$MIGRATION_DIR"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

# The importer needs these to reach the three systems. They are checked, not
# printed.
require_importer_env() {
  require_env APP_DATABASE_URL APP_DATA_ENCRYPTION_KEY APP_REMNAWAVE_URL APP_REMNAWAVE_TOKEN \
    APP_BEDOLAGA_DATABASE_URL MAPPING_FILE
  [ -r "$MAPPING_FILE" ] || die "MAPPING_FILE is not readable"
  command -v "$MIGRATE_BEDOLAGA" >/dev/null 2>&1 ||
    die "the importer binary was not found; build it with: go build -o migrate-bedolaga ./cmd/migrate-bedolaga, then set MIGRATE_BEDOLAGA"
}
