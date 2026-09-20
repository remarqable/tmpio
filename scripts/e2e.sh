#!/usr/bin/env bash
# End-to-end demonstration of the tmp core proof (spec §17) against a running
# development server with DEV_LOGIN_BYPASS=1 and the `local-test` OAuth client
# configured (see config/local.env.example). Usage: scripts/e2e.sh [origin]
set -u
H="${1:-http://localhost:8000}"
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT
J="$W/cookies"; PASS=0; FAIL=0
ok()   { echo "PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "FAIL  $1 -- $2"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected $3 got $2"; fi; }
uuid() { python3 -c "import uuid;print(uuid.uuid4())"; }
jq_()  { python3 -c "import json,sys;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1"; }

echo "== 1. Sign in without naming anything (dev bypass)"
code=$(curl -s -c "$J" -o /dev/null -w "%{http_code}" -H "Origin: $H" -d "email=e2e-$(date +%s)@example.com&name=E2E" "$H/auth/dev")
check "dev sign-in redirects" "$code" "302"
curl -s -b "$J" -o "$W/dash.html" "$H/admin"
ORG=$(grep -o 'o:[A-Z0-9]\{8\}' "$W/dash.html" | head -1 | cut -c3-); CSRF=$(grep -o 'data-csrf="[^"]*"' "$W/dash.html" | head -1 | cut -d'"' -f2)
[ -n "$ORG" ] && ok "organization created silently ($ORG)" || bad "organization" "no code on dashboard"
grep -q "Notebook" "$W/dash.html" && ok "no organization name required (shows the unnamed-site label)" || bad "site name fallback" "missing"

echo "== 2. Connect an AI client (OAuth code+PKCE) and create a document through MCP"
VER=$(python3 -c "import secrets;print(secrets.token_urlsafe(48))"); CH=$(python3 -c "import hashlib,base64,sys;print(base64.urlsafe_b64encode(hashlib.sha256(sys.argv[1].encode()).digest()).rstrip(b'=').decode())" "$VER")
Q="response_type=code&client_id=local-test&redirect_uri=http%3A%2F%2F127.0.0.1%3A9876%2Fcb&scope=content%3Aread%20content%3Awrite%20content%3Adelete&state=xyz&code_challenge=$CH&code_challenge_method=S256"
loc=$(curl -s -b "$J" -o /dev/null -w "%{redirect_url}" -H "Origin: $H" -X POST "$H/oauth/authorize" -d "csrf_token=$CSRF&decision=allow&$Q")
CODE=$(echo "$loc" | sed 's/.*code=\([^&]*\).*/\1/')
[ -n "$CODE" ] && ok "consent issued an authorization code" || bad "consent" "$loc"
curl -s -X POST "$H/oauth/token" -d "grant_type=authorization_code&client_id=local-test&code=$CODE&code_verifier=$VER&redirect_uri=http://127.0.0.1:9876/cb" > "$W/tok.json"
AT=$(jq_ "['access_token']" < "$W/tok.json" 2>/dev/null); [ -n "$AT" ] && ok "access token issued" || bad "token" "$(cat "$W/tok.json")"
mcp() { curl -s -X POST "$H/mcp" -H "Authorization: Bearer $AT" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "MCP-Protocol-Version: 2025-11-25" -d "$1" | sed -n 's/^data: //p'; }
mcp '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}' | grep -q '"tools"' && ok "MCP initialize" || bad "MCP initialize" ""
R=$(mcp "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"tmp_write\",\"arguments\":{\"path\":\"auto\",\"content\":\"---\\ntitle: Circle\\ntags: [community]\\n---\\n\\n# Circle\\n\\n## Overview\\n\\nResearch text with [sources](https://example.com).\\n\\n:::callout type=\\\"note\\\"\\nWorking draft.\\n:::\\n\",\"expected_revision\":0,\"request_id\":\"$(uuid)\"}}}")
echo "$R" | grep -q '\\"revision\\": 1' && ok "tmp_write created the page at revision 1" || bad "tmp_write" "$R"
# tmp files new pages itself, so the proof follows where it put this one.
PAGE=$(printf '%s' "$R" | python3 -c "
import json,sys
d = json.load(sys.stdin)
sc = d.get('result', {}).get('structuredContent')
if not sc:
    txt = d['result']['content'][0]['text']
    sc = json.loads(txt)
print(sc.get('path',''))")
[ -n "$PAGE" ] && ok "tmp filed it at $PAGE" || bad "no path in the write reply" "$R"
PAGE_HTML="${PAGE%.md}"
code=$(curl -s -b "$J" -o /dev/null -w "%{http_code}" "$H/o:$ORG$PAGE_HTML"); check "canonical HTML URL renders for the owner" "$code" "200"
code=$(curl -s -o /dev/null -w "%{http_code}" "$H/o:$ORG$PAGE"); check "private read without credentials is refused (404)" "$code" "404"

echo "== 3. Create a writable secret link (owner browser only)"
curl -s -b "$J" -H "Origin: $H" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -X POST "$H/api/v1/orgs/$ORG/share-links" -d "{\"path\":\"$PAGE\",\"label\":\"Reviewer A\"}" > "$W/share.json"
SURL=$(jq_ "['url']" < "$W/share.json"); GID=$(jq_ "['link']['id']" < "$W/share.json")
[[ "$SURL" == "$H/s/"* ]] && ok "edit link created ($SURL)" || bad "share create" "$(cat "$W/share.json")"

echo "== 4. Edit in a signed-out browser (form flow)"
curl -s -o "$W/edit.html" "$SURL/edit"; NONCE=$(grep -o 'name="nonce" value="[^"]*"' "$W/edit.html" | cut -d'"' -f4)
code=$(curl -s -o /dev/null -w "%{http_code}" -H "Origin: $H" -X POST "$SURL/edit" --data-urlencode "content=---
title: Circle
tags: [community]
---

# Circle

## Overview

Edited from a signed-out browser.
" -d "expected_revision=1&request_id=$(uuid)&nonce=$NONCE&author_name=Browser+Reviewer")
check "signed-out editor saved revision 2" "$code" "303"

echo "== 5. Read and update through the documented HTTP link API"
curl -s -D "$W/h.txt" -o "$W/raw.md" "$SURL/raw"; ET=$(grep -i '^etag' "$W/h.txt" | cut -d' ' -f2 | tr -d '\r')
grep -q "signed-out browser" "$W/raw.md" && ok "GET raw returns revision 2 with ETag $ET" || bad "GET raw" "$(head -c 100 "$W/raw.md")"
printf -- "---\ntitle: Circle\ntags: [community]\n---\n\n# Circle\n\n## Overview\n\nUpdated over HTTP with If-Match.\n" > "$W/v3.md"
code=$(curl -s -o "$W/put.json" -w "%{http_code}" -X PUT -H "Content-Type: text/markdown" -H "If-Match: $ET" -H "Idempotency-Key: $(uuid)" -H "X-Author-Name: HTTP Client" --data-binary @"$W/v3.md" "$SURL/raw")
check "PUT raw with If-Match -> revision 3" "$code" "200"

echo "== 6. Read that revision through the owner-connected AI"
R=$(mcp "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"tmp_read\",\"arguments\":{\"path\":\"$PAGE\"}}}")
echo "$R" | grep -q 'Updated over HTTP' && ok "tmp_read sees the anonymous HTTP update (revision 3)" || bad "tmp_read" "$R"

echo "== 7. Concurrent stale edit is rejected"
R=$(mcp "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"tmp_write\",\"arguments\":{\"path\":\"$PAGE\",\"content\":\"# Stale\\n\",\"expected_revision\":2,\"request_id\":\"$(uuid)\"}}}")
echo "$R" | grep -q 'revision_conflict' && ok "stale tmp_write -> revision_conflict with current_revision" || bad "stale write" "$R"
code=$(curl -s -o /dev/null -w "%{http_code}" -X PUT -H "Content-Type: text/markdown" -H "If-Match: $ET" -H "Idempotency-Key: $(uuid)" --data-binary @"$W/v3.md" "$SURL/raw"); check "stale PUT raw -> 412" "$code" "412"

echo "== 8. Restore a previous revision as owner"
curl -s -b "$J" -H "Origin: $H" -H "X-CSRF-Token: $CSRF" "$H/api/v1/orgs/$ORG/history?path=$PAGE" > "$W/hist.json"
python3 -c "import json;d=json.load(open('$W/hist.json'));print(' / '.join(f\"r{i['revision']} {i['actor']}\"+(f\" ({i['author_name']}, unverified)\" if i.get('author_name') else '') for i in d['items']))"
CUR=$(python3 -c "import json;print(json.load(open('$W/hist.json'))['entry']['revision'])"); EID=$(python3 -c "import json;print(json.load(open('$W/hist.json'))['entry']['id'])")
ETAG=$(python3 -c "import sys;print('\"e%s-r%s\"' % (format(int(sys.argv[1]),'x'), sys.argv[2]))" "$EID" "$CUR")
# base36 in python: format(int, 'x') is hex; compute base36 properly
ETAG=$(python3 -c "
import sys
n=int(sys.argv[1]); s=''
while n: n,r=divmod(n,36); s='0123456789abcdefghijklmnopqrstuvwxyz'[r]+s
print('\"e%s-r%s\"' % (s or '0', sys.argv[2]))" "$EID" "$CUR")
code=$(curl -s -b "$J" -o "$W/restore.json" -w "%{http_code}" -H "Origin: $H" -H "X-CSRF-Token: $CSRF" -H "If-Match: $ETAG" -H "Idempotency-Key: $(uuid)" -H "Content-Type: application/json" -X POST "$H/api/v1/orgs/$ORG/restores" -d "{\"path\":\"$PAGE\",\"revision\":1}")
check "restore revision 1 -> new revision" "$code" "200"
curl -s "$SURL/raw" | grep -q "Research text with" && ok "link holders see the restored content" || bad "restored content" ""

echo "== 9. Move the page; the share link keeps working; old address redirects"
NEWREV=$(jq_ "['entry']['revision']" < "$W/restore.json"); ETAG=$(echo "$ETAG" | sed "s/-r[0-9]*\"/-r$NEWREV\"/")
code=$(curl -s -b "$J" -o /dev/null -w "%{http_code}" -H "Origin: $H" -H "X-CSRF-Token: $CSRF" -H "If-Match: $ETAG" -H "Idempotency-Key: $(uuid)" -H "Content-Type: application/json" -X POST "$H/api/v1/orgs/$ORG/moves" -d "{\"from\":\"$PAGE\",\"to\":\"/notes/circle.md\"}")
check "move $PAGE -> /notes/circle.md" "$code" "200"
code=$(curl -s -o /dev/null -w "%{http_code}" "$SURL/raw"); check "share link still reads after the move" "$code" "200"
loc=$(curl -s -b "$J" -o /dev/null -w "%{redirect_url}" "$H$PAGE_HTML"); check "old personal address redirects (form preserved)" "$loc" "$H/notes/circle"
loc=$(curl -s -b "$J" -o /dev/null -w "%{redirect_url}" "$H/o:$ORG$PAGE"); check "old explicit raw address redirects (form preserved)" "$loc" "$H/o:$ORG/notes/circle.md"

echo "== 10. Revoke the link: reads, writes and assets stop; owner access continues"
code=$(curl -s -b "$J" -o /dev/null -w "%{http_code}" -H "Origin: $H" -H "X-CSRF-Token: $CSRF" -X DELETE "$H/api/v1/orgs/$ORG/share-links/$GID"); check "revoke link" "$code" "204"
for p in "" /raw /json /edit /assets/1; do code=$(curl -s -o /dev/null -w "%{http_code}" "$SURL$p"); check "after revoke GET $SURL$p -> 404" "$code" "404"; done
code=$(curl -s -o /dev/null -w "%{http_code}" -X PUT -H "Content-Type: text/markdown" -H "If-Match: $ET" -H "Idempotency-Key: $(uuid)" --data-binary @"$W/v3.md" "$SURL/raw"); check "after revoke PUT -> 404" "$code" "404"
code=$(curl -s -b "$J" -o /dev/null -w "%{http_code}" "$H/notes/circle"); check "owner still reads the page" "$code" "200"
R=$(mcp '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"tmp_read","arguments":{"path":"/notes/circle.md"}}}'); echo "$R" | grep -q '"revision' && ok "connected AI still reads the page" || bad "AI read after revoke" "$R"

echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" -eq 0 ]
