#!/usr/bin/env bash
#
# tmp installer — https://github.com/remarqable/tmpio
#
#   curl -fsSL https://raw.githubusercontent.com/remarqable/tmpio/main/install.sh \
#     | sudo bash -s -- --domain tmp.example.com --email you@example.com
#
# Read this before running it. It is short on purpose: it installs Docker and
# Caddy, writes three files, and hands off to `docker compose`. It does not
# supervise anything, phone home, or manage the lifecycle of your data. Updates
# are `docker compose pull && docker compose up -d`, which this script does not
# need to be involved in.
#
# Re-running is safe. Existing secrets and data are kept.

set -euo pipefail

VERSION="1.0.0"

DIR="/opt/tmp"
TAG="1"
PORT="8000"
DOMAIN=""
EMAIL=""
PASSWORD=""
WITH_CADDY=1
WITH_FIREWALL=1
DRY_RUN=0

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; DIM=$'\033[2m'; BOLD=$'\033[1m'; NC=$'\033[0m'

say()  { printf '%s\n' "$*"; }
step() { printf '\n%s==>%s %s%s%s\n' "$GREEN" "$NC" "$BOLD" "$*" "$NC"; }
note() { printf '%s    %s%s\n' "$DIM" "$*" "$NC"; }
die()  { printf '\n%serror:%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
tmp installer ${VERSION}

  --domain <host>     the public name this instance answers on (required)
  --email <address>   the owner account to create (required)
  --password <pass>   owner password (default: generated and printed)
  --dir <path>        install directory (default: ${DIR})
  --tag <tag>         image tag to run (default: ${TAG})
  --port <port>       loopback port to publish on (default: ${PORT}); change it
                      when something else already uses that port
  --no-caddy          do not install or configure Caddy; bring your own proxy
  --no-firewall       do not touch ufw
  --print-compose     print the compose file this script would write, and exit
  --dry-run           say what would happen, change nothing
  -h, --help          this

The install directory ends up holding docker-compose.yml and .env. Your data
lives in the Docker volume tmp_pgdata, not in that directory.
EOF
}

compose_file() {
  cat <<'COMPOSE'
# tmp, self-hosted. Two containers: the server and its database.
#
#   curl -O https://raw.githubusercontent.com/remarqable/tmpio/main/docker-compose.yml
#   curl -o .env https://raw.githubusercontent.com/remarqable/tmpio/main/config/docker.env.example
#   $EDITOR .env          # at minimum: APP_ORIGIN, SESSION_SECRET, OWNER_EMAIL, OWNER_PASSWORD
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
      # Bound to loopback: put a reverse proxy in front for TLS. Change to
      # "8000:8000" only if something else already terminates TLS for you.
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

while [ $# -gt 0 ]; do
  case "$1" in
    --domain)        DOMAIN="${2:-}"; shift 2 ;;
    --email)         EMAIL="${2:-}"; shift 2 ;;
    --password)      PASSWORD="${2:-}"; shift 2 ;;
    --dir)           DIR="${2:-}"; shift 2 ;;
    --tag)           TAG="${2:-}"; shift 2 ;;
    --port)          PORT="${2:-}"; shift 2 ;;
    --no-caddy)      WITH_CADDY=0; shift ;;
    --no-firewall)   WITH_FIREWALL=0; shift ;;
    --print-compose) compose_file; exit 0 ;;
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
  [ -n "$EMAIL" ]  || die "--email is required (the owner account to create)"
  case "$DOMAIN" in *.*) ;; *) die "--domain should be a hostname such as tmp.example.com" ;; esac
  case "$EMAIL" in *@*.*) ;; *) die "--email should be an email address" ;; esac
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
  PASSWORD=""
else
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

# The owner account. Set a new password here and restart to rotate it; that is
# also how a forgotten password is recovered. OWNER_EMAIL must stay, because it
# is what keeps the password sign-in form on.
OWNER_EMAIL=${EMAIL}
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
say "  ${BOLD}https://${DOMAIN}${NC}"
say ""
if [ "${GENERATED:-0}" -eq 1 ]; then
  say "  owner     ${EMAIL}"
  say "  password  ${BOLD}${PASSWORD}${NC}"
  say ""
  say "  ${DIM}That password is stored in ${ENV_FILE}. Change it by editing${NC}"
  say "  ${DIM}OWNER_PASSWORD there and running: cd ${DIR} && docker compose up -d${NC}"
elif [ "$EXISTING" -eq 1 ]; then
  say "  ${DIM}Existing install updated. Sign-in details are unchanged.${NC}"
else
  say "  owner     ${EMAIL}"
  say "  ${DIM}password as supplied on the command line${NC}"
fi
say ""
say "  ${DIM}update   cd ${DIR} && docker compose pull && docker compose up -d${NC}"
say "  ${DIM}logs     cd ${DIR} && docker compose logs -f app${NC}"
say "  ${DIM}backup   docs/DOCKER.md${NC}"
say ""
