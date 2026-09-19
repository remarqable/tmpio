#!/usr/bin/env bash
# Bounded load check (spec §13): seeds N pages through the REST API, then
# measures p50/p95 for 10 concurrent users (10 separate API tokens, so the
# per-principal rate limit applies per user as in production) reading HTML, raw
# Markdown and search, plus 100 text writes. Usage: scripts/loadcheck.sh [origin] [pages]
set -u
exec python3 - "${1:-http://localhost:8000}" "${2:-1000}" <<'PY'
import sys, json, time, uuid, re, statistics, urllib.request, urllib.parse, http.cookiejar
from concurrent.futures import ThreadPoolExecutor
H, N = sys.argv[1], int(sys.argv[2])
def req(method, url, data=None, headers=None, opener=None):
    r = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    t = time.perf_counter()
    try:
        resp = opener.open(r, timeout=30) if opener else urllib.request.urlopen(r, timeout=30); code = resp.status; body = resp.read()
    except urllib.error.HTTPError as e:
        code, body = e.code, e.read()
    return code, body, (time.perf_counter() - t) * 1000
def mint(op, csrf, name):
    r = urllib.request.Request(H + "/admin/tokens", data=urllib.parse.urlencode([("csrf_token", csrf), ("name", name), ("scopes", "content:read"), ("scopes", "content:write")]).encode(), method="POST", headers={"Origin": H, "Content-Type": "application/x-www-form-urlencoded"})
    try: op.open(r); loc = None
    except urllib.error.HTTPError as e: loc = e.headers.get("Location")
    return urllib.parse.parse_qs(urllib.parse.urlparse(loc).query)["token"][0]
def user(i):
    jar = http.cookiejar.CookieJar(); op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    op.addheaders = []
    class NoRedir(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **k): return None
    op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedir())
    req("POST", H + "/auth/dev", urllib.parse.urlencode({"email": f"load{i}-{int(time.time())}@example.com", "name": "Load"}).encode(), {"Origin": H, "Content-Type": "application/x-www-form-urlencoded"}, op)
    code, body, _ = req("GET", H + "/admin", None, None, op); body = body.decode()
    org = re.search(r"o:([A-Z0-9]{8})", body).group(1); csrf = re.search(r'data-csrf="([^"]+)"', body).group(1)
    # Measurement uses its own token; seeding uses separate tokens so the 60 writes/min
    # per-credential limit applies per token exactly as in production.
    tok = mint(op, csrf, "load-measure")
    seeders = [mint(op, csrf, f"load-seed-{k}") for k in range(20 if i == 0 else 1)]
    return {"org": org, "tok": tok, "op": op, "seed": seeders}
users = [user(i) for i in range(10)]
u0 = users[0]
def seed(i):
    body = json.dumps({"content": f"---\ntitle: Page {i}\ntags: [load]\n---\n\n# Page {i}\n\n## Section\n\nLorem ipsum dolor sit amet, consectetur adipiscing elit number {i}. Community platform research text.\n\n- one\n- two\n\n:::callout type=\"note\"\nnote {i}\n:::\n"}).encode()
    return req("PUT", f"{H}/api/v1/orgs/{u0['org']}/entries?path=/load/d{i%25}/page-{i}.md", body, {"Authorization": "Bearer " + u0["seed"][i % len(u0["seed"])], "If-None-Match": "*", "Idempotency-Key": str(uuid.uuid4()), "Content-Type": "application/json"})[0]
t0 = time.perf_counter()
with ThreadPoolExecutor(8) as ex: codes = list(ex.map(seed, range(1, N + 1)))
print(f"seeded {N} pages in {time.perf_counter()-t0:.1f}s codes={ {c: codes.count(c) for c in set(codes)} }")
# make the other users able to read: seed their own org with the same page set? No: each user reads their OWN site,
# as in production. Seed 100 pages per other user.
def seed_user(u, n=100):
    def one(i):
        body = json.dumps({"content": f"# Page {i}\n\nLorem ipsum research text {i}.\n"}).encode()
        return req("PUT", f"{H}/api/v1/orgs/{u['org']}/entries?path=/load/page-{i}.md", body, {"Authorization": "Bearer " + u["seed"][0], "If-None-Match": "*", "Idempotency-Key": str(uuid.uuid4()), "Content-Type": "application/json"})[0]
    with ThreadPoolExecutor(4) as ex: return list(ex.map(one, range(1, n + 1)))
for u in users[1:]: seed_user(u, 50)
def summarize(name, rows):
    t = sorted(r[1] for r in rows); codes = {}
    for r in rows: codes[r[0]] = codes.get(r[0], 0) + 1
    p = lambda q: t[min(len(t) - 1, int(q * len(t)))]
    print(f"{name:14s} n={len(t):4d} p50={p(0.5):7.1f}ms p95={p(0.95):7.1f}ms max={t[-1]:7.1f}ms codes={codes}")
def run(name, fn, per_user=30):
    jobs = [(u, i) for u in users for i in range(per_user)]
    with ThreadPoolExecutor(10) as ex: rows = list(ex.map(lambda j: fn(*j), jobs))
    summarize(name, rows)
def html(u, i):
    p = f"/load/d{i%25}/page-{(i%N)+1}" if u is u0 else f"/load/page-{(i%50)+1}"
    c, _, ms = req("GET", H + p, None, {"Authorization": "Bearer " + u["tok"], "Accept": "text/html"}); return c, ms
def raw(u, i):
    p = f"/o:{u['org']}/load/d{i%25}/page-{(i%N)+1}.md" if u is u0 else f"/o:{u['org']}/load/page-{(i%50)+1}.md"
    c, _, ms = req("GET", H + p, None, {"Authorization": "Bearer " + u["tok"]}); return c, ms
words = ["lorem", "community", "research", "page", "platform", "note", "text"]
def search(u, i):
    c, _, ms = req("GET", f"{H}/search.json?q={words[i%len(words)]}", None, {"Authorization": "Bearer " + u["tok"]}); return c, ms
def write(u, i):
    body = json.dumps({"content": f"# W {i}\n\nwrite test {i}\n"}).encode()
    c, _, ms = req("PUT", f"{H}/api/v1/orgs/{u['org']}/entries?path=/loadw/w-{i}.md", body, {"Authorization": "Bearer " + u["tok"], "If-None-Match": "*", "Idempotency-Key": str(uuid.uuid4()), "Content-Type": "application/json"}); return c, ms
print("10 concurrent users, each against their own site (user 1 holds the 1000-page site):")
run("html", html); run("raw", raw); run("search", search); run("write", write, 10)
PY
