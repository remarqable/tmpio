#!/usr/bin/env bash
# Deploy tmp to a single Ubuntu host behind Caddy, with a managed PostgreSQL.
#
# What it does (idempotent, each step prints what it changed):
#   1. Creates app_owner / app_user roles and the "tmp" database (first run only)
#   2. Runs goose migrations as app_owner from this machine
#   3. Builds the linux/amd64 binary and uploads it to /opt/tmp/bin/api
#   4. Writes /etc/tmp/tmp.env (secrets, mode 600) and a systemd unit; restarts tmp
#   5. Adds a tmp.io site block to /etc/caddy/Caddyfile (backup kept) and reloads Caddy
#   6. Checks https://tmp.io/healthz and /readyz
#
# Inputs (environment): DOADMIN_URL   doadmin connection URL to defaultdb (sslmode=require)
#                       BASICAUTH_PW  password protecting /login and /auth/dev (dev sign-in)
#                       GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET (optional; enables real sign-in)
# State: data/prod-secrets.env (gitignored) holds generated DB passwords and the session secret.
set -euo pipefail
cd "$(dirname "$0")/.."
HOST=${HOST:-root@tmp.io}; ORIGIN=${ORIGIN:-https://tmp.io}; PORT=8100
# This is the original bare-binary path. A host that runs tmp from the
# container image already owns the port, so this script would install a
# systemd unit that can never bind and leave it restarting forever. Refuse
# before touching the database or the host.
if ssh "$HOST" 'docker ps --format "{{.Image}}" 2>/dev/null | grep -q "ghcr.io/remarqable/tmpio"'; then
  echo "error: $HOST runs tmp from the container image, not a bare binary." >&2
  echo "  release:  git tag -a vX.Y.Z -m ... && git push origin vX.Y.Z" >&2
  echo "  deploy:   ssh $HOST 'cd /opt/tmp && docker compose pull && docker compose up -d'" >&2
  echo "  (FORCE_SYSTEMD=1 runs this legacy path anyway)" >&2
  [ "${FORCE_SYSTEMD:-}" = "1" ] || exit 1
fi
PSQL=${PSQL:-/opt/homebrew/opt/postgresql@16/bin/psql}
: "${DOADMIN_URL:?set DOADMIN_URL}"; : "${BASICAUTH_PW:?set BASICAUTH_PW}"
mkdir -p data
if [ ! -f data/prod-secrets.env ]; then
  DBHOST=$(python3 -c "import urllib.parse,sys;u=urllib.parse.urlparse(sys.argv[1]);print(f'{u.hostname}:{u.port}')" "$DOADMIN_URL")
  OWNER_PW=$(openssl rand -hex 24); USER_PW=$(openssl rand -hex 24)
  cat > data/prod-secrets.env <<ENV
DATABASE_OWNER_URL=postgres://app_owner:$OWNER_PW@$DBHOST/tmp?sslmode=require
DATABASE_URL=postgres://app_user:$USER_PW@$DBHOST/tmp?sslmode=require
SESSION_SECRET=$(openssl rand -hex 32)
ENV
  echo "1. creating roles and database"
  $PSQL "$DOADMIN_URL" -v ON_ERROR_STOP=1 -v owner_pw="$OWNER_PW" -v user_pw="$USER_PW" -f scripts/deploy/prod-db.sql
  $PSQL "${DOADMIN_URL/defaultdb/tmp}" -v ON_ERROR_STOP=1 -f scripts/deploy/prod-db-grants.sql
else
  echo "1. roles/database already created (data/prod-secrets.env exists)"
fi
set -a; . ./data/prod-secrets.env; set +a
echo "2. migrations"; goose -dir migrations postgres "$DATABASE_OWNER_URL" up
echo "3. build + upload"; CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X github.com/remarqable/tmpio/internal/version.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X github.com/remarqable/tmpio/internal/version.Date=$(date -u +%Y-%m-%d)" -o bin/api-linux-amd64 ./cmd/api
ssh "$HOST" 'id tmp >/dev/null 2>&1 || useradd --system --home /opt/tmp --shell /usr/sbin/nologin tmp; mkdir -p /opt/tmp/bin /etc/tmp; chown -R tmp:tmp /opt/tmp'
scp -q bin/api-linux-amd64 "$HOST:/opt/tmp/bin/api.new"
echo "4. env + systemd"
HASH=$(ssh "$HOST" "caddy hash-password --plaintext '$BASICAUTH_PW'")
ssh "$HOST" "cat > /etc/tmp/tmp.env" <<ENV
APP_ENV=${APP_ENV:-staging}
PORT=$PORT
APP_ORIGIN=$ORIGIN
DATABASE_URL=$DATABASE_URL
DATABASE_OWNER_URL=$DATABASE_OWNER_URL
SESSION_SECRET=$SESSION_SECRET
GOOGLE_CLIENT_ID=${GOOGLE_CLIENT_ID:-}
GOOGLE_CLIENT_SECRET=${GOOGLE_CLIENT_SECRET:-}
GOOGLE_REDIRECT_URL=$ORIGIN/auth/google/callback
DEV_LOGIN_BYPASS=${DEV_LOGIN_BYPASS:-1}
SIGNUPS_ENABLED=${SIGNUPS_ENABLED:-0}
SOURCE_URL=${SOURCE_URL:-https://github.com/remarqable/tmpio}
METRICS_TOKEN=$(openssl rand -hex 16)
ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}
AI_WORKSPACE_ID=${AI_WORKSPACE_ID:-}
AI_MODEL=${AI_MODEL:-claude-haiku-4-5-20251001}
ENV
scp -q scripts/deploy/tmp.service "$HOST:/etc/systemd/system/tmp.service"; scp -q scripts/deploy/Caddyfile.tmp.io "$HOST:/tmp/Caddyfile.tmp.io"
ssh "$HOST" "chmod 600 /etc/tmp/tmp.env; mv /opt/tmp/bin/api.new /opt/tmp/bin/api; chown tmp:tmp /opt/tmp/bin/api; systemctl daemon-reload; systemctl enable --now tmp >/dev/null; systemctl restart tmp; sleep 2; systemctl is-active tmp; curl -s 127.0.0.1:$PORT/healthz; echo"
echo "5. caddy"
# Credentials are inlined into the Caddyfile through files, never through shell
# expansion: bcrypt hashes contain "$" sequences that a shell would mangle.
printf %s "$BASICAUTH_PW" | ssh "$HOST" 'cat > /root/.tmp-pw; chmod 600 /root/.tmp-pw'
ssh "$HOST" 'set -e
HASH=$(caddy hash-password --plaintext "$(cat /root/.tmp-pw)"); rm -f /root/.tmp-pw
python3 - "'"${BASICAUTH_USER:-admin}"'" "$HASH" <<'"'"'PY'"'"'
import re, sys, os, shutil, time
user, h = sys.argv[1], sys.argv[2]
p = "/etc/caddy/Caddyfile"; s = open(p).read()
block = open("/tmp/Caddyfile.tmp.io").read().replace("{$TMP_BASICAUTH_USER} {$TMP_BASICAUTH_HASH}", user + " " + h)
# The site block may be the first thing in the file, so anchor on a line
# start rather than a leading newline.
if not re.search(r"(?m)^tmp\.io\b", s):
    shutil.copy(p, p + ".bak." + str(int(time.time())))
    anchor = "\nhttps:// {"
    if anchor in s:
        i = s.index(anchor); s = s[:i] + "\n" + block + s[i:]
    else:
        s = s.rstrip() + "\n\n" + block + "\n"
else:
    s = re.sub(r"basic_auth \{\n\s*\S+ \S+\n\s*\}", "basic_auth {\n            " + user + " " + h + "\n        }", s)
open(p, "w").write(s)
PY
caddy validate --config /etc/caddy/Caddyfile >/dev/null && systemctl reload caddy && echo "caddy reloaded"'
echo "6. checks (certificate issuance can take a minute on first deploy)"
for i in $(seq 1 12); do curl -fsS "$ORIGIN/healthz" >/dev/null 2>&1 && break; sleep 5; done
curl -s "$ORIGIN/healthz"; echo; curl -s "$ORIGIN/readyz"; echo; curl -s -o /dev/null -w "landing %{http_code}\n" "$ORIGIN/"; curl -s -o /dev/null -w "login (basic auth expected 401) %{http_code}\n" "$ORIGIN/login"
echo "MCP URL: $ORIGIN/mcp"
