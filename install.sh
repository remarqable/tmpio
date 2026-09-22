#!/usr/bin/env bash
#
# tmp installer — https://github.com/remarqable/tmpio
#
#   curl -fsSL https://raw.githubusercontent.com/remarqable/tmpio/main/install.sh \
#     | sudo bash -s -- --domain notes.example.com
#
# Afterwards the same script lives at /opt/tmp/install.sh. Run it with no
# arguments to see what is installed and what you can do; `install.sh update`
# pulls the current image. It keeps itself current, so there is no second
# thing to fetch.
#
# Read this before running it. It is short on purpose: it installs Docker and
# Caddy, writes three files, and hands off to `docker compose`. It does not
# supervise anything, phone home, or manage the lifecycle of your data. Updates
# are `docker compose pull && docker compose up -d`, which this script does not
# need to be involved in.
#
# Re-running is safe. Existing secrets and data are kept.

set -euo pipefail

VERSION="1.1.0"

# Where this script updates itself from. Override for a fork or a branch.
RAW_URL=${TMP_RAW_URL:-https://raw.githubusercontent.com/remarqable/tmpio/main/install.sh}

# What the self-update check concluded. The relaunched process learns it had
# just updated from the environment, since it cannot see what happened before
# it started.
if [ -n "${TMP_UPDATED_FROM:-}" ]; then SELF_UPDATE_STATE="updated"; else SELF_UPDATE_STATE="unchecked"; fi

# TMP_DIR lets the verbs find an install that was made with --dir, since the
# flags are parsed after the verb has already had to locate it.
DIR=${TMP_DIR:-/opt/tmp}
TAG="1"
PORT="8000"
DOMAIN=""
OWNER="admin"
PASSWORD=""
WITH_CADDY=1
WITH_FIREWALL=1
DRY_RUN=0

# Colour only when stdout is a terminal that wants it. Piped into a file, a
# pager or `head`, these would otherwise arrive as literal [1m noise.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'; CYAN=$'\033[0;36m'; DIM=$'\033[2m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; CYAN=""; DIM=""; NC=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '\n%s==>%s %s\n' "$GREEN" "$NC" "$*"; }
note() { printf '%s    %s%s\n' "$DIM" "$*" "$NC"; }
die()  { printf '\n%serror:%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
tmp installer ${VERSION}

Usage:
  install.sh --domain <host> [options]   install, or reconfigure an install
  install.sh                             what is installed here, and the verbs
  install.sh update                      pull the current image and restart
  install.sh status                      running, healthy, which version
  install.sh logs                        follow the server log
  install.sh restart                     restart without changing version
  install.sh backup                      dump the database to a file
  install.sh restore <file>              replace the database with a backup
  install.sh reset                       empty the database, keep everything else
  install.sh uninstall [--purge]         remove tmp; --purge deletes its data

Options:
  --domain <host>     the public name this instance answers on (required)
  --user <name>       the account to create (default: ${OWNER}); an email
                      address works too if you prefer one
  --password <pass>   the password for it. Omitted, you are asked for one, or
                      one is generated and printed when nobody can be asked
  --dir <path>        install directory (default: ${DIR})
  --tag <tag>         image tag to run (default: ${TAG})
  --port <port>       loopback port to publish on (default: ${PORT}); change it
                      when something else already uses that port
  --no-caddy          do not install or configure Caddy; bring your own proxy
  --no-firewall       do not touch ufw
  --print-compose     print the compose file this script would write, and exit
  --yes, -y           do not ask to confirm a destructive command
  --purge             with uninstall: delete the data volume too
  --no-self-update    do not fetch a newer copy of this script first
  --dry-run           say what would happen, change nothing
  -h, --help          this

The install directory ends up holding docker-compose.yml and .env. Your data
lives in the Docker volume tmp_pgdata, not in that directory.
EOF
}

# --- keeping this script current -------------------------------------------

# Fetch the published script and, if it differs, replace this file and start
# again. Piped from curl there is nothing on disk to update and nothing to
# gain; in a git checkout this would overwrite someone's working copy, so it
# leaves both alone.
self_update() {
  local dir tmpf newv
  # The relaunched process carries TMP_NO_SELF_UPDATE so it cannot loop, but
  # it has just updated - saying "check skipped" there would be a lie about
  # the one run where something actually happened.
  if [ "${TMP_NO_SELF_UPDATE:-0}" = "1" ]; then
    [ "$SELF_UPDATE_STATE" = "updated" ] || SELF_UPDATE_STATE="off"
    return 0
  fi
  [ -f "$0" ] || { SELF_UPDATE_STATE="piped"; return 0; }
  dir=$(cd "$(dirname "$0")" && pwd)
  [ -d "${dir}/.git" ] && { SELF_UPDATE_STATE="checkout"; return 0; }
  [ -w "$0" ] || { SELF_UPDATE_STATE="read-only"; return 0; }
  command -v curl >/dev/null 2>&1 || { SELF_UPDATE_STATE="no curl"; return 0; }

  tmpf="${dir}/.install.sh.new"
  # raw.githubusercontent sets max-age=300 and serves from regional edges, so
  # for up to five minutes after a push two hosts can be told different
  # things. Neither a no-cache header nor a cache-busting query parameter
  # gets past it - Fastly normalises the query away and ignores the header -
  # so this simply reads what it is given. The window closes by itself; the
  # only cost is that an update run inside it reports "already current".
  if ! curl -fsSL --max-time 20 "$RAW_URL" -o "$tmpf" 2>/dev/null; then
    rm -f "$tmpf"; SELF_UPDATE_STATE="offline"; return 0
  fi
  # Never replace this script with something that is not a working script.
  if [ ! -s "$tmpf" ] \
     || ! head -1 "$tmpf" | grep -q '^#!/usr/bin/env bash' \
     || ! grep -q '^VERSION=' "$tmpf" \
     || ! bash -n "$tmpf" 2>/dev/null; then
    rm -f "$tmpf"; SELF_UPDATE_STATE="bad download"; return 0
  fi
  if cmp -s "$tmpf" "$0"; then
    rm -f "$tmpf"; SELF_UPDATE_STATE="current"; return 0
  fi

  newv=$(grep -m1 '^VERSION=' "$tmpf" | cut -d'"' -f2)
  chmod 0755 "$tmpf"
  # A rename, not a copy: the running shell keeps reading the old inode, so
  # overwriting the file it is executing cannot corrupt this run.
  mv -f "$tmpf" "$0" || { rm -f "$tmpf"; return 0; }
  TMP_NO_SELF_UPDATE=1 TMP_UPDATED_FROM="$VERSION" exec "$0" "$@"
}

# --- managing an install that already exists -------------------------------

running_version() {
  local id ref v
  id=$(docker compose ps -q app 2>/dev/null || true)
  [ -n "$id" ] || { echo "not running"; return; }
  ref=$(docker inspect "$id" --format '{{.Image}}' 2>/dev/null || true)
  v=$(docker image inspect "$ref" --format '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null || true)
  echo "${v:-unknown}"
}

# Compose returns as soon as Docker accepts the request, which is before the
# server has finished migrating, so wait on the container's own health check.
wait_healthy() {
  local id deadline=$((SECONDS + 120)) state
  id=$(docker compose ps -q app 2>/dev/null || true)
  [ -n "$id" ] || return 1
  while [ $SECONDS -lt $deadline ]; do
    state=$(docker inspect "$id" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo unknown)
    case "$state" in
      healthy|running) return 0 ;;
      exited|dead)     return 1 ;;
    esac
    sleep 2
  done
  return 1
}

# --- backup, reset, uninstall ----------------------------------------------

DB_USER=app_owner
DB_NAME=tmp

# Destructive verbs ask for the word back. A y/N is too easy to answer by
# reflex, and a script that finds no terminal must not guess on your behalf.
# The first argument that is not a flag. Without this, `restore --yes file`
# takes --yes as the filename.
first_arg() {
  local a
  for a in "$@"; do
    case "$a" in -*) ;; *) printf '%s' "$a"; return ;; esac
  done
}

# Shown on every run a person looks at, so the version in play is never a
# guess and the self-update check never happens invisibly.
installer_line() {
  local note
  case "$SELF_UPDATE_STATE" in
    updated)  note="${GREEN}self-updated from ${TMP_UPDATED_FROM}${NC}" ;;
    current)  note="${DIM}up to date${NC}" ;;
    offline)  note="${YELLOW}could not reach github${NC}" ;;
    off)      note="${DIM}check skipped (--no-self-update)${NC}" ;;
    piped)    note="${DIM}not on disk, nothing to update${NC}" ;;
    checkout) note="${DIM}git checkout, left alone${NC}" ;;
    *)        note="${DIM}${SELF_UPDATE_STATE}${NC}" ;;
  esac
  say "  ${DIM}installer${NC} ${VERSION}   ${DIM}·${NC}   ${note}"
}

confirm() { # confirm <sentence> <word>
  local answer
  [ "${ASSUME_YES:-0}" -eq 1 ] && return 0
  if [ ! -t 0 ]; then
    die "$1 Re-run from a terminal, or pass --yes if you mean it in a script."
  fi
  say ""
  say "  ${RED}$1${NC}"
  printf '  type %s to continue: ' "$2" > /dev/tty
  read -r answer < /dev/tty || true
  [ "$answer" = "$2" ] || die "cancelled; nothing was changed"
}

manage_backup() { # manage_backup [quiet]
  local ts out
  ts=$(date -u +%Y%m%d-%H%M%S)
  mkdir -p "${DIR}/backups"
  out="${DIR}/backups/tmp-${ts}.sql.gz"
  [ "${1:-}" = "quiet" ] || say ""
  say "  ${DIM}dumping the database…${NC}"
  if ! docker compose exec -T db pg_dump -U "$DB_USER" -d "$DB_NAME" 2>/dev/null | gzip > "$out"; then
    rm -f "$out"
    die "the dump failed; is the database running? ($0 status)"
  fi
  # pg_dump can exit 0 having written nothing if the container is not ready.
  if [ ! -s "$out" ]; then
    rm -f "$out"
    die "the dump came out empty; nothing was saved"
  fi
  say "  ${GREEN}✓${NC} ${out} ${DIM}($(du -h "$out" | cut -f1))${NC}"
  [ "${1:-}" = "quiet" ] || say ""
  BACKUP_PATH="$out"
}

manage_restore() { # manage_restore <file>
  local f="${1:-}"
  [ -n "$f" ] || die "which backup? $0 restore <file>   (see ${DIR}/backups)"
  [ -f "$f" ] || die "no such file: $f"
  confirm "This replaces everything in the database with ${f}." "restore"
  manage_backup quiet   # the state being replaced is worth keeping too
  say "  ${DIM}restoring…${NC}"
  docker compose exec -T db psql -qU "$DB_USER" -d "$DB_NAME" \
    -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;' >/dev/null
  case "$f" in
    *.gz) gzip -dc "$f" ;;
    *)    cat "$f" ;;
  esac | docker compose exec -T db psql -qU "$DB_USER" -d "$DB_NAME" >/dev/null
  docker compose restart app >/dev/null 2>&1
  wait_healthy || die "restored, but the app did not come back healthy ($0 logs)"
  say "  ${GREEN}✓${NC} restored from ${f}"
  say ""
}

# Keeps the install, the domain and the certificate; empties the database.
# The app recreates its schema and its owner account from .env at startup.
manage_reset() {
  local origin
  origin=$(grep -m1 '^APP_ORIGIN=' "${DIR}/.env" 2>/dev/null | cut -d= -f2- || true)
  confirm "This deletes every page, revision and sharing link in ${origin:-this instance}." "reset"
  manage_backup quiet
  say "  ${DIM}emptying the database…${NC}"
  docker compose down -v >/dev/null 2>&1
  docker compose up -d >/dev/null 2>&1
  wait_healthy || die "the app did not come back healthy ($0 logs)"
  say "  ${GREEN}✓${NC} reset. Sign in with the account in ${DIR}/.env"
  say "  ${DIM}the previous contents are in ${BACKUP_PATH}${NC}"
  say ""
}

manage_uninstall() {
  local origin domain caddyfile backup_keep
  origin=$(grep -m1 '^APP_ORIGIN=' "${DIR}/.env" 2>/dev/null | cut -d= -f2- || true)
  domain=${origin#https://}; domain=${domain#http://}; domain=${domain%%/*}

  if [ "${PURGE:-0}" -eq 1 ]; then
    confirm "This removes tmp AND deletes its data. The pages are not recoverable." "${domain:-uninstall}"
    manage_backup quiet || true
  else
    confirm "This removes tmp from this machine. Its data volume is kept." "${domain:-uninstall}"
  fi

  # Backups live inside the install directory, which is about to go.
  if [ -d "${DIR}/backups" ] && [ -n "$(ls -A "${DIR}/backups" 2>/dev/null)" ]; then
    backup_keep="/root/tmp-backups-$(date -u +%Y%m%d-%H%M%S)"
    mkdir -p "$backup_keep" && cp -a "${DIR}/backups/." "$backup_keep/" \
      && say "  ${DIM}kept your backups in ${backup_keep}${NC}"
  fi

  say "  ${DIM}stopping containers…${NC}"
  if [ "${PURGE:-0}" -eq 1 ]; then
    docker compose down -v >/dev/null 2>&1 || true
  else
    docker compose down >/dev/null 2>&1 || true
  fi

  caddyfile=/etc/caddy/Caddyfile
  if [ -f "$caddyfile" ] && grep -q '# >>> tmp >>>' "$caddyfile"; then
    cp "$caddyfile" "${caddyfile}.bak.$(date +%s)"
    awk '/# >>> tmp >>>/ { skip = 1; next } /# <<< tmp <<</ { skip = 0; next } !skip' \
      "$caddyfile" > "${caddyfile}.new" && mv "${caddyfile}.new" "$caddyfile"
    if caddy validate --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1; then
      systemctl reload caddy >/dev/null 2>&1 || systemctl restart caddy >/dev/null 2>&1 || true
      say "  ${DIM}removed the tmp block from ${caddyfile}${NC}"
    else
      # Leaving a Caddyfile that does not parse would take down every other
      # site on this host at the next reload.
      mv "${caddyfile}.bak."* "$caddyfile" 2>/dev/null || true
      say "  ${RED}left ${caddyfile} alone; it did not validate without the tmp block${NC}"
    fi
  fi

  cd /
  rm -rf "$DIR"
  say ""
  if [ "${PURGE:-0}" -eq 1 ]; then
    say "  ${GREEN}✓${NC} tmp removed, data deleted"
  else
    say "  ${GREEN}✓${NC} tmp removed. Its data volume is still here:"
    say "  ${DIM}docker volume ls | grep pgdata      to see it${NC}"
    say "  ${DIM}docker volume rm tmp_pgdata         to delete it${NC}"
  fi
  say ""
}

manage_update() {
  local before after
  before=$(running_version)
  say ""
  say "  ${CYAN}tmp${NC} ${before}"
  rule
  say "  ${DIM}pulling the current image…${NC}"
  # Compose narrates every layer; the useful news is the version, below.
  docker compose pull -q >/dev/null 2>&1 || docker compose pull >/dev/null 2>&1 || {
    say "  ${RED}could not pull the image${NC}"
    docker compose pull
    exit 1
  }

  # `up -d` leaves the container alone when the image has not changed, so it
  # is safe either way and the version afterwards tells the truth.
  docker compose up -d >/dev/null 2>&1
  wait_healthy || {
    say "  ${RED}the app did not come up healthy — the last 40 lines:${NC}"
    rule
    docker compose logs --tail 40 app >&2
    exit 1
  }

  after=$(running_version)
  rule
  if [ "$before" = "$after" ]; then
    say "  ${GREEN}✓${NC} already on ${after} ${DIM}— nothing to do${NC}"
  else
    say "  ${GREEN}✓${NC} now on ${after} ${DIM}(was ${before})${NC}"
  fi
  rule
  installer_line
  say ""
}

# Everything here lays out inside 80 columns. `docker compose ps` is about 120
# wide and wraps into a mess on a normal terminal, so the same facts are
# printed in a table this script controls the width of.
WIDTH=74

fit() { # fit <text> <width>
  local t="$1" w="$2"
  if [ "${#t}" -le "$w" ]; then printf '%s' "$t"; else printf '%s…' "${t:0:$((w - 1))}"; fi
}

rule() {
  local line
  line=$(printf '%*s' "$WIDTH" '' | tr ' ' '-')
  # A box-drawing rule where the terminal can show one; the script already
  # prints · and … so UTF-8 is assumed, but fall back rather than gamble.
  case "${LANG:-}${LC_ALL:-}" in
    *UTF-8*|*utf8*) line=$(printf '%*s' "$WIDTH" '' | sed 's/ /\xe2\x94\x80/g') ;;
  esac
  printf '  %s%s%s\n' "$DIM" "$line" "$NC"
}

# Green when it is fine, red when it is not, yellow while it is deciding.
health_colour() {
  case "$1" in
    healthy|running)             printf '%s' "$GREEN" ;;
    starting|created|restarting) printf '%s' "$YELLOW" ;;
    *)                           printf '%s' "$RED" ;;
  esac
}

service_rows() {
  local svc id state uptime image
  local services
  services=$(docker compose config --services 2>/dev/null || true)
  # app first: it is the one being asked about, and compose lists db first.
  for svc in $(printf '%s\n' $services | grep -x app || true) \
             $(printf '%s\n' $services | grep -vx app || true); do
    id=$(docker compose ps -q "$svc" 2>/dev/null || true)
    if [ -z "$id" ]; then
      printf '  %-7s %s%-9s%s %s%s%s\n' "$svc" "$RED" "stopped" "$NC" "$DIM" "not running" "$NC"
      continue
    fi
    state=$(docker inspect "$id" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo unknown)
    # "Up 36 minutes (healthy)" -> "up 36 minutes"
    uptime=$(docker ps -a --filter "id=$id" --format '{{.Status}}' 2>/dev/null | sed 's/ *(.*)//' | tr 'A-Z' 'a-z')
    image=$(docker inspect "$id" --format '{{.Config.Image}}' 2>/dev/null || echo '-')
    printf '  %-7s %s%-9s%s %-16s %s%s%s\n' \
      "$svc" "$(health_colour "$state")" "$state" "$NC" \
      "$(fit "$uptime" 16)" "$DIM" "$(fit "$image" 30)" "$NC"
  done
}

manage_status() {
  local origin
  origin=$(grep -m1 '^APP_ORIGIN=' "${DIR}/.env" 2>/dev/null | cut -d= -f2- || true)
  say ""
  say "  ${CYAN}tmp${NC} $(running_version)${origin:+   ${GREEN}${origin}${NC}}"
  rule
  service_rows
  if [ "${1:-}" != "embedded" ]; then
    rule
    installer_line
    say ""
  fi
}

# What a bare run prints when tmp is already installed here.
menu() {
  cd "$DIR"
  manage_status embedded
  rule
  say "  update    ${DIM}pull the current image and restart onto it${NC}"
  say "  status    ${DIM}running, healthy, which version${NC}"
  say "  logs      ${DIM}follow the server log${NC}"
  say "  restart   ${DIM}restart without changing version${NC}"
  say "  backup    ${DIM}dump the database to ${DIR}/backups${NC}"
  say "  reset     ${DIM}empty the database, keep this install${NC}"
  say "  uninstall ${DIM}remove tmp from this machine${NC}"
  rule
  say "  ${DIM}run${NC} $0 <command>${DIM}   ·   --help to reconfigure${NC}"
  installer_line
  say ""
}

compose_file() {
  cat <<'COMPOSE'
# tmp, self-hosted. Two containers: the server and its database.
#
#   curl -O https://raw.githubusercontent.com/remarqable/tmpio/main/docker-compose.yml
#   curl -o .env https://raw.githubusercontent.com/remarqable/tmpio/main/config/docker.env.example
#   $EDITOR .env          # at minimum: APP_ORIGIN, SESSION_SECRET, OWNER_PASSWORD
#   docker compose up -d
#
# Updating is `docker compose pull && docker compose up -d`. The server applies
# its own migrations at startup. Back up first: docs/DOCKER.md.
services:
  app:
    # Pin the major version: patches and features arrive, breaking changes do not.
    image: ghcr.io/remarqable/tmpio:1
    restart: unless-stopped
    env_file: .env
    environment:
      # The database is reachable only on this private network, so the
      # connection is plaintext and the server allows it for private addresses.
      # The app creates this NOBYPASSRLS role on first boot and migrates itself.
      DATABASE_URL: postgres://app_user:${APP_USER_PASSWORD:?set APP_USER_PASSWORD in .env}@db:5432/tmp?sslmode=disable
      DATABASE_OWNER_URL: postgres://app_owner:${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}@db:5432/tmp?sslmode=disable
      AUTO_MIGRATE: "1"
      PORT: "8000"
      # Caddy runs on the host and reaches the container through the bridge, so
      # the proxy arrives as the bridge gateway rather than 127.0.0.1. Without
      # this every caller would look like the gateway and per-IP rate limits
      # would apply to everyone at once.
      TRUSTED_PROXIES: "172.16.0.0/12"
      # Your own front page, if you publish one. Files dropped in ./web are
      # served to visitors who are not signed in; an empty directory means the
      # built-in sign-in page answers instead. See docs/DOCKER.md.
      PUBLIC_SITE_DIR: /web
    volumes:
      - ./web:/web:ro
    ports:
      # Bound to loopback: put a reverse proxy in front for TLS. Drop the
      # 127.0.0.1 prefix only if something else already terminates TLS for you.
      - "127.0.0.1:8000:8000"
    depends_on:
      db:
        condition: service_healthy

  db:
    image: postgres:16
    restart: unless-stopped
    environment:
      POSTGRES_USER: app_owner
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}
      POSTGRES_DB: tmp
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app_owner -d tmp"]
      interval: 5s
      timeout: 5s
      retries: 20

volumes:
  pgdata:
COMPOSE
}

# Subcommands come first: on a machine that already has tmp, the common verbs
# should not require remembering install flags, and a bare run should not
# reinstall anything by surprise.
CONFIG_ARGS=0
for a in "$@"; do
  case "$a" in
    --no-self-update) TMP_NO_SELF_UPDATE=1 ;;
    *) CONFIG_ARGS=$((CONFIG_ARGS + 1)) ;;
  esac
done
self_update "$@"

BARE_RUN=0
[ "$CONFIG_ARGS" -eq 0 ] && BARE_RUN=1

SUBCOMMAND=""
case "${1:-}" in
  update|status|version|logs|restart|backup|restore|reset|uninstall) SUBCOMMAND="$1"; shift ;;
esac

if [ -n "$SUBCOMMAND" ]; then
  ASSUME_YES=0; PURGE=0
  for a in "$@"; do
    case "$a" in
      --yes|-y) ASSUME_YES=1 ;;
      --purge)  PURGE=1 ;;
    esac
  done
  [ "$(id -u)" -eq 0 ] || die "run this as root (prefix it with sudo)"
  [ -f "${DIR}/docker-compose.yml" ] || die "no install in ${DIR} — run this with --domain <host> first"
  cd "$DIR"
  case "$SUBCOMMAND" in
    update)  manage_update ;;
    status)  manage_status ;;
    version) running_version ;;
    logs)    docker compose logs -f --tail "$(first_arg "$@" | grep -E '^[0-9]+$' || echo 100)" app ;;
    restart) docker compose restart app && wait_healthy && say "restarted $(running_version)" ;;
    backup)  manage_backup ;;
    restore) manage_restore "$(first_arg "$@")" ;;
    reset)     manage_reset ;;
    uninstall) manage_uninstall ;;
  esac
  exit 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    --domain)        DOMAIN="${2:-}"; shift 2 ;;
    --user)          OWNER="${2:-}"; shift 2 ;;
    --email)         OWNER="${2:-}"; shift 2 ;;   # what --user was called before
    --password)      PASSWORD="${2:-}"; shift 2 ;;
    --dir)           DIR="${2:-}"; shift 2 ;;
    --tag)           TAG="${2:-}"; shift 2 ;;
    --port)          PORT="${2:-}"; shift 2 ;;
    --no-caddy)      WITH_CADDY=0; shift ;;
    --no-firewall)   WITH_FIREWALL=0; shift ;;
    --print-compose) compose_file; exit 0 ;;
    --no-self-update) shift ;;   # handled before anything else
    --dry-run)       DRY_RUN=1; shift ;;
    -h|--help)       usage; installer_line; exit 0 ;;
    *)               die "unknown option: $1 (try --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run this as root (prefix it with sudo)"
command -v apt-get >/dev/null || die "this installer targets Debian and Ubuntu; on anything else follow docs/DOCKER.md"

ENV_FILE="${DIR}/.env"
[ -f "$ENV_FILE" ] && EXISTING=1 || EXISTING=0

# An existing install already knows its domain and owner; only a new one has to ask.
if [ "$EXISTING" -eq 0 ]; then
  [ -n "$DOMAIN" ] || die "--domain is required (the public name this instance answers on)"
  [ -n "$OWNER" ]  || die "--user cannot be empty"
  case "$DOMAIN" in *.*) ;; *) die "--domain should be a hostname such as tmp.example.com" ;; esac
fi

# A bare re-run used to perform a full reinstall: apt, the Caddyfile, the
# firewall, all of it, for someone who probably just wanted to know what was
# going on. With no arguments and an install already here, say what is here
# and what the verbs are. Reconfiguring is still one flag away.
if [ "$EXISTING" -eq 1 ] && [ "$BARE_RUN" -eq 1 ]; then
  menu
  exit 0
fi

if [ "$DRY_RUN" -eq 1 ]; then
  say "would install into ${DIR}"
  say "would install: docker$([ $WITH_CADDY -eq 1 ] && echo ", caddy")"
  say "would write:   ${DIR}/docker-compose.yml, ${ENV_FILE}$([ $WITH_CADDY -eq 1 ] && echo ", /etc/caddy/Caddyfile")"
  say "would run:     docker compose up -d   (image ghcr.io/remarqable/tmpio:${TAG}, on 127.0.0.1:${PORT})"
  [ "$EXISTING" -eq 1 ] && say "note: ${ENV_FILE} exists; its secrets would be kept"
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive

step "Docker"
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  note "already installed: $(docker --version)"
else
  curl -fsSL https://get.docker.com | sh >/dev/null
  note "installed $(docker --version)"
fi
systemctl enable --now docker >/dev/null 2>&1 || true

if [ "$WITH_CADDY" -eq 1 ]; then
  step "Caddy"
  if command -v caddy >/dev/null; then
    note "already installed: $(caddy version | head -1)"
  else
    apt-get update -qq
    apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg >/dev/null
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
      > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq && apt-get install -y -qq caddy >/dev/null
    note "installed $(caddy version | head -1)"
  fi
fi

step "Configuration"
mkdir -p "$DIR"
compose_file \
  | sed "s|ghcr.io/remarqable/tmpio:1|ghcr.io/remarqable/tmpio:${TAG}|" \
  | sed "s|127.0.0.1:8000:8000|127.0.0.1:${PORT}:${PORT}|; s|PORT: \"8000\"|PORT: \"${PORT}\"|" \
  > "${DIR}/docker-compose.yml"
note "wrote ${DIR}/docker-compose.yml"
mkdir -p "${DIR}/web"
note "created ${DIR}/web for your own front page; empty means the built-in sign-in page"

if [ "$EXISTING" -eq 1 ]; then
  note "kept ${ENV_FILE} and the secrets in it"
  DOMAIN="${DOMAIN:-$(sed -n 's|^APP_ORIGIN=https\{0,1\}://||p' "$ENV_FILE" | head -1)}"
  OWNER="$(sed -n 's|^OWNER_USER=||p;s|^OWNER_EMAIL=||p' "$ENV_FILE" | head -1)"
  PASSWORD=""
else
  # Ask for the password if there is a terminal to ask at. Piping this script
  # to bash leaves stdin holding the script, so the question goes to /dev/tty.
  if [ -z "$PASSWORD" ] && [ -r /dev/tty ]; then
    while : ; do
      printf '\n%sSet the password for %s%s\n' "" "$OWNER" "" > /dev/tty
      stty -echo < /dev/tty 2>/dev/null || true
      printf '  password (at least 12 characters, or blank to generate one): ' > /dev/tty
      read -r PASSWORD < /dev/tty || true
      printf '\n' > /dev/tty
      if [ -z "$PASSWORD" ]; then stty echo < /dev/tty 2>/dev/null || true; break; fi
      printf '  again: ' > /dev/tty
      read -r CONFIRM < /dev/tty || true
      stty echo < /dev/tty 2>/dev/null || true
      printf '\n' > /dev/tty
      if [ "$PASSWORD" != "$CONFIRM" ]; then
        printf '  %sthey do not match%s\n' "$RED" "$NC" > /dev/tty; PASSWORD=""; continue
      fi
      if [ "${#PASSWORD}" -lt 12 ]; then
        printf '  %sat least 12 characters%s\n' "$RED" "$NC" > /dev/tty; PASSWORD=""; continue
      fi
      break
    done
  fi
  [ -n "$PASSWORD" ] || { PASSWORD="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"; GENERATED=1; }
  umask 077
  cat > "$ENV_FILE" <<EOF
# Written by the tmp installer ${VERSION} on $(date -u +%Y-%m-%dT%H:%M:%SZ).
# Values are read literally: do not quote them.

APP_ORIGIN=https://${DOMAIN}
APP_ENV=prod

# Signs session cookies and pagination cursors. Changing it signs everyone out.
SESSION_SECRET=$(openssl rand -hex 32)

# Used only between the two containers.
POSTGRES_PASSWORD=$(openssl rand -hex 32)
APP_USER_PASSWORD=$(openssl rand -hex 32)

# The account you sign in with. Set a new password here and restart to rotate
# it; that is also how a forgotten password is recovered.
OWNER_USER=${OWNER}
OWNER_PASSWORD=${PASSWORD}

# Nobody but the owner can create an account on a self-hosted instance anyway.
SIGNUPS_ENABLED=0
EOF
  chmod 600 "$ENV_FILE"
  note "wrote ${ENV_FILE} (0600)"
fi

if [ "$WITH_CADDY" -eq 1 ]; then
  CADDYFILE=/etc/caddy/Caddyfile
  BLOCK=$(printf '# >>> tmp >>>\n%s {\n    reverse_proxy 127.0.0.1:%s\n}\n# <<< tmp <<<\n' "$DOMAIN" "$PORT")
  mkdir -p /etc/caddy
  if [ -f "$CADDYFILE" ] && grep -q '# >>> tmp >>>' "$CADDYFILE"; then
    # Replace our own block, leave everything else of theirs alone.
    awk -v block="$BLOCK" '
      /# >>> tmp >>>/ { print block; skip = 1; next }
      /# <<< tmp <<</ { skip = 0; next }
      !skip
    ' "$CADDYFILE" > "${CADDYFILE}.new" && mv "${CADDYFILE}.new" "$CADDYFILE"
    note "updated the tmp block in ${CADDYFILE}"
  else
    if [ -f "$CADDYFILE" ]; then
      CADDY_BACKUP="${CADDYFILE}.bak.$(date +%s)"
      cp "$CADDYFILE" "$CADDY_BACKUP"
      note "backed up the existing ${CADDYFILE}"
    fi
    printf '\n%s\n' "$BLOCK" >> "$CADDYFILE"
    note "added a tmp block to ${CADDYFILE}"
  fi
  if ! caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
    # Put back exactly what was there. A half-edited Caddyfile would take down
    # every other site on this host at the next reload.
    if [ -n "${CADDY_BACKUP:-}" ] && [ -f "$CADDY_BACKUP" ]; then
      mv "$CADDY_BACKUP" "$CADDYFILE"
      note "restored ${CADDYFILE}; nothing was changed"
    else
      rm -f "$CADDYFILE"
    fi
    die "the Caddyfile does not validate with a block for ${DOMAIN} added. A site for that name probably exists already: remove it, or re-run with --no-caddy and wire the proxy up yourself."
  fi
  systemctl reload caddy 2>/dev/null || systemctl restart caddy
  note "caddy reloaded; it will request a certificate for ${DOMAIN}"
fi

if [ "$WITH_FIREWALL" -eq 1 ] && command -v ufw >/dev/null; then
  step "Firewall"
  ufw allow 22,80,443/tcp >/dev/null 2>&1 || true
  note "allowed 22, 80 and 443; the app itself is bound to 127.0.0.1 and the database publishes no port"
fi

step "This script"
# curl|bash leaves nothing behind, so keep a copy where the install is. That
# copy is what you run later for update, status and logs.
#
# `-ef` compares inodes rather than paths: if this IS the installed copy,
# there is nothing to do. Writing it to itself truncates the file the shell
# is still reading, which empties the script mid-run.
# Piped from curl, "$0" is the string "bash". If the working directory happens
# to hold a file of that name, [ -f "$0" ] is true and the wrong file would be
# copied into place, so check that it really is this script first.
is_self() { [ -f "$0" ] && head -5 "$0" 2>/dev/null | grep -q 'tmp installer'; }

if is_self && [ "$0" -ef "${DIR}/install.sh" ]; then
  note "already running the copy at ${DIR}/install.sh"
elif is_self; then
  cp -f "$0" "${DIR}/install.sh"
  chmod 0755 "${DIR}/install.sh"
  note "kept a copy at ${DIR}/install.sh (run it with no arguments to see what it can do)"
elif curl -fsSL --max-time 20 "$RAW_URL" -o "${DIR}/install.sh.part" 2>/dev/null \
     && [ -s "${DIR}/install.sh.part" ] && bash -n "${DIR}/install.sh.part" 2>/dev/null; then
  # Piped from curl: there is no file to copy, so fetch one.
  mv -f "${DIR}/install.sh.part" "${DIR}/install.sh"
  chmod 0755 "${DIR}/install.sh"
  note "kept a copy at ${DIR}/install.sh (run it with no arguments to see what it can do)"
else
  rm -f "${DIR}/install.sh.part"
  note "could not save a copy here; fetch one with: curl -fsSL ${RAW_URL} -o ${DIR}/install.sh"
fi

step "Starting"
cd "$DIR"
docker compose pull -q 2>/dev/null || docker compose pull
docker compose up -d

printf '\n'
for i in $(seq 1 60); do
  if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/readyz" 2>/dev/null; then
    READY=1; break
  fi
  sleep 2
  printf '%s.%s' "$DIM" "$NC"
done
printf '\n'

if [ "${READY:-0}" -ne 1 ]; then
  say ""
  say "${RED}The server did not report ready within two minutes.${NC}"
  say "Look at what it said:  cd ${DIR} && docker compose logs app"
  exit 1
fi

step "Ready"
say ""
say "  ${GREEN}https://${DOMAIN}${NC}"
say ""
if [ "${GENERATED:-0}" -eq 1 ]; then
  say "  ${DIM}username${NC}  ${OWNER}"
  say "  ${DIM}password${NC}  ${PASSWORD}"
  say ""
  say "  ${DIM}That password is stored in ${ENV_FILE}. Change it by editing${NC}"
  say "  ${DIM}OWNER_PASSWORD there and running: cd ${DIR} && docker compose up -d${NC}"
elif [ "$EXISTING" -eq 1 ]; then
  say "  ${DIM}Existing install updated. Sign-in details are unchanged.${NC}"
else
  say "  ${DIM}username${NC}  ${OWNER}"
  say "  ${DIM}password as you set it${NC}"
fi
say ""
installer_line
say ""
say "  ${DIM}update${NC}   ${DIR}/install.sh update"
say "  ${DIM}status${NC}   ${DIR}/install.sh status"
say "  ${DIM}logs${NC}     ${DIR}/install.sh logs"
say "  ${DIM}backup   docs/DOCKER.md${NC}"
say ""
