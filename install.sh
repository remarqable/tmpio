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

DIR="/opt/tmp"
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
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; DIM=$'\033[2m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; DIM=""; NC=""
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
  [ "${TMP_NO_SELF_UPDATE:-0}" = "1" ] && return 0
  [ -f "$0" ] || return 0
  dir=$(cd "$(dirname "$0")" && pwd)
  [ -d "${dir}/.git" ] && return 0
  [ -w "$0" ] || return 0
  command -v curl >/dev/null 2>&1 || return 0

  tmpf="${dir}/.install.sh.new"
  if ! curl -fsSL --max-time 20 "$RAW_URL" -o "$tmpf" 2>/dev/null; then
    rm -f "$tmpf"; return 0          # offline, or GitHub is having a day
  fi
  # Never replace this script with something that is not a working script.
  if [ ! -s "$tmpf" ] \
     || ! head -1 "$tmpf" | grep -q '^#!/usr/bin/env bash' \
     || ! grep -q '^VERSION=' "$tmpf" \
     || ! bash -n "$tmpf" 2>/dev/null; then
    rm -f "$tmpf"; return 0
  fi
  if cmp -s "$tmpf" "$0"; then
    rm -f "$tmpf"; return 0          # already current
  fi

  newv=$(grep -m1 '^VERSION=' "$tmpf" | cut -d'"' -f2)
  chmod 0755 "$tmpf"
  # A rename, not a copy: the running shell keeps reading the old inode, so
  # overwriting the file it is executing cannot corrupt this run.
  mv -f "$tmpf" "$0" || { rm -f "$tmpf"; return 0; }
  printf '  %sthis script updated to %s (was %s); restarting%s\n' \
    "${DIM}" "${newv:-newer}" "${VERSION}" "${NC}"
  TMP_NO_SELF_UPDATE=1 exec "$0" "$@"
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

manage_update() {
  local before after
  before=$(running_version)
  say "  ${DIM}running${NC}   ${before}"
  say "  ${DIM}pulling…${NC}"
  docker compose pull -q 2>/dev/null || docker compose pull

  # `up -d` leaves the container alone when the image has not changed, so it
  # is safe either way and the version afterwards tells the truth.
  docker compose up -d >/dev/null
  wait_healthy || {
    say "  ${RED}the app did not come up healthy. Recent log:${NC}"
    docker compose logs --tail 40 app >&2
    exit 1
  }

  after=$(running_version)
  if [ "$before" = "$after" ]; then
    say "  already on ${after} ${DIM}— nothing to do${NC}"
  else
    say "  ${DIM}now on${NC}    ${after} ${DIM}(was ${before})${NC}"
  fi
}

manage_status() {
  say "  ${DIM}version${NC}   $(running_version)"
  say "  directory ${DIR}"
  say ""
  docker compose ps
}

# What a bare run prints when tmp is already installed here.
menu() {
  cd "$DIR"
  say ""
  say "  tmp is installed in ${DIR}"
  say ""
  manage_status
  say ""
  say "  $0 update     ${DIM}pull the current image and restart onto it${NC}"
  say "  $0 status     ${DIM}running, healthy, which version${NC}"
  say "  $0 logs       ${DIM}follow the server log${NC}"
  say "  $0 restart    ${DIM}restart without changing version${NC}"
  say ""
  say "  ${DIM}To reconfigure (domain, port, proxy) pass the flags: --help${NC}"
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
  update|status|version|logs|restart) SUBCOMMAND="$1"; shift ;;
esac

if [ -n "$SUBCOMMAND" ]; then
  [ "$(id -u)" -eq 0 ] || die "run this as root (prefix it with sudo)"
  [ -f "${DIR}/docker-compose.yml" ] || die "no install in ${DIR} — run this with --domain <host> first"
  cd "$DIR"
  case "$SUBCOMMAND" in
    update)  manage_update ;;
    status)  manage_status ;;
    version) running_version ;;
    logs)    docker compose logs -f --tail "${1:-100}" app ;;
    restart) docker compose restart app && wait_healthy && say "restarted $(running_version)" ;;
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
    -h|--help)       usage; exit 0 ;;
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
if [ -f "$0" ] && [ "$0" -ef "${DIR}/install.sh" ]; then
  note "already running the copy at ${DIR}/install.sh"
elif [ -f "$0" ]; then
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
say "  ${DIM}update${NC}   ${DIR}/install.sh update"
say "  ${DIM}status${NC}   ${DIR}/install.sh status"
say "  ${DIM}logs${NC}     ${DIR}/install.sh logs"
say "  ${DIM}backup   docs/DOCKER.md${NC}"
say ""
