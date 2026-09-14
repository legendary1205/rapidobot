#!/usr/bin/env bash
#
#  RapidoBot - installer and management CLI.
#
#  Install:
#    bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapidobot/master/rapidobot.sh) install
#
#  Afterwards the command is available system-wide as `rapidobot`.
#
set -euo pipefail

VERSION="1.0.0"
APP_DIR="${RAPIDOBOT_DIR:-/opt/rapidobot}"
BIN_PATH="/usr/local/bin/rapidobot"
REPO_RAW="https://raw.githubusercontent.com/legendary1205/rapidobot/master"
IMAGE="ghcr.io/legendary1205/rapidobot:latest"

# Real escape bytes, not the text \033 - usage() prints through cat, which
# would show a backslash-033 literally instead of colouring.
if [ -t 1 ]; then
    R=$(printf '\033[0m'); B=$(printf '\033[1m'); D=$(printf '\033[2m')
    RED=$(printf '\033[0;31m'); GRN=$(printf '\033[0;32m'); YEL=$(printf '\033[0;33m')
    BLU=$(printf '\033[1;34m'); CYN=$(printf '\033[0;36m')
else
    R=''; B=''; D=''; RED=''; GRN=''; YEL=''; BLU=''; CYN=''
fi

log()  { printf "%s▶%s %s\n" "$CYN" "$R" "$*"; }
ok()   { printf "%s✔%s %s\n" "$GRN" "$R" "$*"; }
warn() { printf "%s!%s %s\n" "$YEL" "$R" "$*"; }
die()  { printf "%s✘ %s%s\n" "$RED" "$*" "$R" >&2; exit 1; }

require_root() { [ "$(id -u)" -eq 0 ] || die "Run this as root."; }
require_installed() { [ -f "$APP_DIR/docker-compose.yml" ] || die "RapidoBot is not installed. Run: rapidobot install"; }

compose() { (cd "$APP_DIR" && docker compose "$@"); }

# ── prerequisites ────────────────────────────────────────────────────────────

pkg_install() {
    if command -v apt-get >/dev/null 2>&1; then
        # One stale third-party list making `apt-get update` fail is routine
        # on a rented VPS and must not end the install.
        apt-get update -y >/dev/null 2>&1 || true
        apt-get install -y "$@" >/dev/null || die "Could not install: $*"
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y "$@" >/dev/null || die "Could not install: $*"
    elif command -v yum >/dev/null 2>&1; then
        yum install -y "$@" >/dev/null || die "Could not install: $*"
    else
        die "No supported package manager (apt-get, dnf or yum)."
    fi
}

ensure_docker() {
    command -v curl >/dev/null 2>&1 || pkg_install curl
    if ! command -v docker >/dev/null 2>&1; then
        log "Installing Docker..."
        curl -fsSL https://get.docker.com -o /tmp/get-docker.sh \
            || die "Could not download the Docker installer - check outbound network."
        sh /tmp/get-docker.sh >/dev/null || die "The Docker installer failed."
        rm -f /tmp/get-docker.sh
    fi
    docker info >/dev/null 2>&1 || systemctl start docker 2>/dev/null || true
    docker info >/dev/null 2>&1 || die "Docker is installed but not running: systemctl status docker"
    if ! docker compose version >/dev/null 2>&1; then
        log "Installing the Docker Compose plugin..."
        pkg_install docker-compose-plugin
    fi
    ok "Docker ready."
}

# ── configuration ────────────────────────────────────────────────────────────

# ask VAR "prompt" [secret] - uses the environment value when set, so the
# whole install also runs unattended; otherwise asks on the terminal.
ask() {
    local var="$1" prompt="$2" secret="${3:-}" value="${!1:-}"
    while [ -z "$value" ]; do
        # With no terminal, `read` hits EOF at once and this would loop forever.
        [ -t 0 ] || die "No terminal to ask on - set $var in the environment instead."
        if [ -n "$secret" ]; then
            read -r -s -p "  $prompt: " value || value=""
            echo
        else
            read -r -p "  $prompt: " value || value=""
        fi
    done
    printf -v "$var" '%s' "$value"
}

write_env() {
    if [ -f "$APP_DIR/.env" ]; then
        warn ".env already exists - keeping it (change it with: rapidobot edit-env)."
        return 0
    fi
    printf "\n%sBot settings%s\n" "$B" "$R"
    ask BOT_TOKEN      "Bot token from @BotFather"
    ask ADMIN_IDS      "Your numeric Telegram id (several: comma-separated)"
    ask PANEL_URL      "Rapido-Go panel URL (https://...)"
    ask PANEL_USERNAME "Panel sudo admin username"
    ask PANEL_PASSWORD "Panel sudo admin password" secret

    umask 077
    {
        echo "# Values are single-quoted on purpose: docker compose expands \$NAME inside"
        echo "# unquoted and double-quoted env_file values, which silently corrupts any"
        echo "# password containing a dollar sign. Keep the quotes if you edit these."
        echo "BOT_TOKEN=$(env_value "$BOT_TOKEN")"
        echo "ADMIN_IDS=$(env_value "$ADMIN_IDS")"
        echo "PANEL_URL=$(env_value "${PANEL_URL%/}")"
        echo "PANEL_USERNAME=$(env_value "$PANEL_USERNAME")"
        echo "PANEL_PASSWORD=$(env_value "$PANEL_PASSWORD")"
        echo "LOG_LEVEL=info"
    } > "$APP_DIR/.env"
    ok "Settings written to $APP_DIR/.env"
}

# env_value encodes a value for docker compose's env_file. Verified against
# compose itself rather than assumed: an unquoted or double-quoted `a$b` reaches
# the container as `a` (compose expands $b to nothing), while a single-quoted
# value arrives byte for byte - dollar, space, # and " included. A value that
# itself contains a single quote cannot be single-quoted, so for that one case
# each $ is doubled instead, which compose turns back into a literal $.
env_value() {
    case "$1" in
        *"'"*) printf '%s' "${1//\$/\$\$}" ;;
        *)     printf "'%s'" "$1" ;;
    esac
}

fetch_files() {
    mkdir -p "$APP_DIR"
    curl -fsSL "$REPO_RAW/docker-compose.yml" -o "$APP_DIR/docker-compose.yml" \
        || die "Could not download docker-compose.yml."
    curl -fsSL "$REPO_RAW/rapidobot.sh" -o "$APP_DIR/rapidobot.sh" || true
    if [ -s "$APP_DIR/rapidobot.sh" ]; then
        install -m 755 "$APP_DIR/rapidobot.sh" "$BIN_PATH"
    fi
}

obtain_image() {
    log "Fetching the RapidoBot image..."
    if docker pull "$IMAGE" >/dev/null 2>&1; then
        ok "Image ready."
        return 0
    fi
    die "Could not pull $IMAGE - check that this server can reach ghcr.io."
}

# check runs the bot's own end-to-end validation in a throwaway container: it
# logs in to the panel and asks Telegram about the token, so a typo is caught
# now and not at the first customer's purchase.
check_config() {
    log "Checking the panel login and the bot token..."
    local out
    if out=$(compose run --rm --no-deps bot check 2>&1); then
        ok "${out##*$'\n'}"
        return 0
    fi
    printf "%s\n" "$out" | tail -5 >&2
    die "The settings did not work - fix them with: rapidobot edit-env, then: rapidobot restart"
}

wait_running() {
    local i
    for i in $(seq 1 20); do
        if [ "$(docker inspect -f '{{.State.Running}}' rapidobot 2>/dev/null || echo false)" = "true" ]; then
            ok "RapidoBot is running."
            return 0
        fi
        sleep 1
    done
    compose logs --tail 20 bot >&2 || true
    die "The bot did not stay up - see the log above."
}

# ── commands ─────────────────────────────────────────────────────────────────

cmd_install() {
    require_root
    printf "\n%sRapidoBot %s%s\n\n" "$B" "$VERSION" "$R"
    ensure_docker
    fetch_files
    write_env
    obtain_image
    check_config
    compose up -d
    wait_running
    printf "\n%s%sDone.%s Open your bot in Telegram and send /start.\n" "$GRN" "$B" "$R"
    printf "%sAs an admin, set the card number first: ⚙️ پنل مدیریت → ⚙️ تنظیمات%s\n\n" "$D" "$R"
}

cmd_update() {
    require_root
    require_installed
    fetch_files
    obtain_image
    compose up -d
    wait_running
    ok "Updated."
}

cmd_backup() {
    require_installed
    local stamp dest name
    stamp=$(date +%Y%m%d-%H%M%S)
    name="rapidobot-$stamp.db"
    dest="${1:-$APP_DIR/backups/$name}"
    mkdir -p "$(dirname "$dest")"
    # VACUUM INTO inside the running container gives a consistent snapshot;
    # copying the live database file (with its -wal beside it) can tear.
    compose exec -T bot rapidobot backup "/data/$name" >/dev/null \
        || die "Backup failed - is the bot running? (rapidobot status)"
    compose cp "bot:/data/$name" "$dest" >/dev/null || die "Could not copy the backup out."
    compose exec -T bot rm -f "/data/$name" >/dev/null 2>&1 || true
    [ -s "$dest" ] || die "The backup came out empty."
    ok "Backup written: $dest"
}

cmd_restore() {
    require_root
    require_installed
    local src="${1:-}"
    [ -n "$src" ] && [ -f "$src" ] || die "Usage: rapidobot restore <file.db>"
    warn "This REPLACES the bot's database with $src."
    printf "Type 'yes' to continue: "
    local answer; read -r answer || answer=""
    [ "$answer" = "yes" ] || die "Aborted."
    cmd_backup >/dev/null || warn "Could not take a safety backup first - continuing."
    compose stop bot >/dev/null
    compose cp "$src" bot:/data/rapidobot.db >/dev/null || die "Could not copy the file in."
    # docker cp writes the file as root, but the bot runs as uid 10001: hand
    # it back, or the restored database opens read-only and every sale fails.
    # The old -wal/-shm must go too, or SQLite replays them over the restore.
    compose run --rm --no-deps --user root --entrypoint sh bot \
        -c 'chown 10001 /data/rapidobot.db && rm -f /data/rapidobot.db-wal /data/rapidobot.db-shm' >/dev/null \
        || die "Could not fix the restored file's ownership."
    compose start bot >/dev/null
    wait_running
    ok "Restored."
}

cmd_uninstall() {
    require_root
    require_installed
    warn "This removes RapidoBot's container, its database and $APP_DIR."
    printf "Type 'yes' to continue: "
    local answer; read -r answer || answer=""
    [ "$answer" = "yes" ] || die "Aborted."
    cmd_backup "/root/rapidobot-final-$(date +%Y%m%d-%H%M%S).db" || warn "No final backup was taken."
    compose down --volumes --remove-orphans || true
    rm -rf "$APP_DIR"
    rm -f "$BIN_PATH"
    ok "RapidoBot removed. A final backup was left in /root."
}

usage() {
    cat <<EOF

${B}RapidoBot${R} ${D}v${VERSION}${R} - a Telegram shop for Rapido-Go

  ${BLU}install${R}            ${YEL}Install Docker if needed, configure and start${R}
  ${BLU}update${R}             ${YEL}Pull the latest version and restart${R}
  ${BLU}status${R}             ${YEL}Show whether the bot is running${R}
  ${BLU}logs${R}               ${YEL}Follow the bot's log${R}
  ${BLU}restart${R}            ${YEL}Restart the bot${R}
  ${BLU}up | down${R}          ${YEL}Start or stop the bot${R}
  ${BLU}backup [file]${R}      ${YEL}Save a consistent copy of the database${R}
  ${BLU}restore <file>${R}     ${YEL}Replace the database from a backup (asks first)${R}
  ${BLU}edit-env${R}           ${YEL}Edit the settings, then restart${R}
  ${BLU}uninstall${R}          ${YEL}Remove everything (keeps a final backup)${R}

EOF
}

main() {
    local cmd="${1:-}"
    [ $# -gt 0 ] && shift || true
    case "$cmd" in
        install)   cmd_install ;;
        update)    cmd_update ;;
        status)    require_installed; compose ps ;;
        logs)      require_installed; compose logs -f --tail 100 bot ;;
        restart)   require_installed; compose restart bot; wait_running ;;
        up)        require_installed; compose up -d; wait_running ;;
        down)      require_installed; compose down ;;
        backup)    cmd_backup "${1:-}" ;;
        restore)   cmd_restore "${1:-}" ;;
        edit-env)  require_installed; "${EDITOR:-nano}" "$APP_DIR/.env" ;;
        uninstall) cmd_uninstall ;;
        version|-v|--version) echo "rapidobot $VERSION" ;;
        ""|help|-h|--help) usage ;;
        *) usage; die "Unknown command: $cmd" ;;
    esac
}

main "$@"
