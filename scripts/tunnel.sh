#!/usr/bin/env bash
# Expose the local server over public HTTPS with ngrok, point APP_ORIGIN at the
# tunnel URL, rebuild and restart the API. MCP clients (Claude Code, Claude,
# ChatGPT) require an https origin for OAuth. Usage: scripts/tunnel.sh [stop]
#
# Set NGROK_DOMAIN in config/local.env to a static domain from the ngrok
# dashboard (one is free) and the URL stays the same between runs, so a client
# is added once. With it set, running this again while the tunnel is up only
# rebuilds and restarts the server: the quick way to try a change.
# WARNING: while the tunnel is up, the dev sign-in bypass is reachable from the
# internet at a random URL. Stop it when you are done: scripts/tunnel.sh stop
set -eu
cd "$(dirname "$0")/.."
if [ "${1:-}" = "stop" ]; then
  pkill -f "ngrok http" 2>/dev/null || true
  sed -i '' "s|^APP_ORIGIN=.*|APP_ORIGIN=http://localhost:8000|" config/local.env
  PID=$(lsof -nP -tiTCP:8000 -sTCP:LISTEN || true); [ -n "$PID" ] && kill "$PID" && sleep 2
  set -a && . ./config/local.env && set +a && (./bin/api > data/api.log 2>&1 &)
  echo "tunnel stopped; APP_ORIGIN reset to http://localhost:8000 and server restarted"; exit 0
fi
command -v ngrok >/dev/null || { echo "ngrok is required (brew install ngrok; ngrok config add-authtoken ...)"; exit 1; }
DOMAIN=$(grep -sE '^NGROK_DOMAIN=' config/local.env | cut -d= -f2- | tr -d '"' || true)
DOMAIN=${DOMAIN#https://}; DOMAIN=${DOMAIN%/}
current() { curl -s 127.0.0.1:4040/api/tunnels 2>/dev/null | python3 -c "import json,sys;t=[x for x in json.load(sys.stdin)['tunnels'] if x['public_url'].startswith('https')];print(t[0]['public_url'])" 2>/dev/null || true; }
URL=""
# A tunnel already serving the static domain is kept, so only the server restarts.
if [ -n "$DOMAIN" ] && [ "$(current)" = "https://$DOMAIN" ]; then
  URL="https://$DOMAIN"; echo "tunnel already up at $URL; rebuilding and restarting the server"
else
  pkill -f "ngrok http" 2>/dev/null || true
  mkdir -p data
  if [ -n "$DOMAIN" ]; then
    (nohup ngrok http 8000 --url="https://$DOMAIN" --log=stdout > data/ngrok.log 2>&1 &)
  else
    (nohup ngrok http 8000 --log=stdout > data/ngrok.log 2>&1 &)
  fi
  for i in $(seq 1 20); do URL=$(current); [ -n "$URL" ] && break; sleep 1; done
  [ -n "$URL" ] || { echo "ngrok did not report a tunnel; see data/ngrok.log"; exit 1; }
fi
sed -i '' "s|^APP_ORIGIN=.*|APP_ORIGIN=$URL|" config/local.env
make -s build
PID=$(lsof -nP -tiTCP:8000 -sTCP:LISTEN || true); [ -n "$PID" ] && kill "$PID" && sleep 2
set -a && . ./config/local.env && set +a && (./bin/api > data/api.log 2>&1 &)
sleep 2
echo "$URL" > data/tunnel_url.txt
echo "tmp is reachable at $URL"
echo "MCP URL for clients: $URL/mcp"
echo "Claude Code: claude mcp add --transport http tmpio $URL/mcp"
if [ -z "$DOMAIN" ]; then
  echo "Note: the URL changes on every ngrok restart. Set NGROK_DOMAIN in config/local.env to keep it."
fi
