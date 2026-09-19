# Running tmp with Docker

Two containers: the server and PostgreSQL. The server applies its own
migrations, creates its own restricted database role and provisions the owner
account on first boot, so a new instance needs nothing but a compose file and
an `.env`.

## Install

```bash
mkdir tmp && cd tmp
curl -O https://raw.githubusercontent.com/remarqable/tmpio/main/docker-compose.yml
curl -o .env https://raw.githubusercontent.com/remarqable/tmpio/main/config/docker.env.example
```

Edit `.env`. Four values have no default and the stack will not start without
them:

| Variable | What to put there |
|---|---|
| `APP_ORIGIN` | The public URL you will reach this instance at, `https://` in production. It must match what your reverse proxy serves, because cookies, CSRF checks and OAuth redirects are all bound to it. |
| `SESSION_SECRET` | `openssl rand -hex 32`. Changing it later signs everyone out. |
| `POSTGRES_PASSWORD` | `openssl rand -hex 32`. The database owner, used for migrations and backups. |
| `APP_USER_PASSWORD` | `openssl rand -hex 32`. The role the server runs as, which cannot bypass row-level security. |

Then set `OWNER_EMAIL` and `OWNER_PASSWORD` to the account you will sign in
with, and:

```bash
docker compose up -d
```

Values in `.env` are read literally, so a password with spaces needs no quotes
and quotes would become part of the password.

The server listens on `127.0.0.1:8000`. Put a reverse proxy in front for TLS —
Caddy needs two lines:

```
tmp.example.com {
    reverse_proxy 127.0.0.1:8000
}
```

`docs/SETUP.md` has the equivalent for Nginx, and the note about keeping
sharing links out of proxy logs, which matters: `/s/<token>` URLs are
credentials.

## A first deployment, end to end

A fresh Debian 12 or Ubuntu 24.04 server with a domain pointed at it. Two
vCPUs and 2GB of memory is comfortable; 1GB is enough to run the published
image, and only building from source needs more.

The short version, which does everything below for you:

```bash
curl -fsSL https://raw.githubusercontent.com/remarqable/tmpio/main/install.sh \
  | sudo bash -s -- --domain tmp.example.com --email you@example.com
```

`install.sh` installs Docker and Caddy, writes `/opt/tmp/docker-compose.yml`,
`/opt/tmp/.env` and a Caddy site block, opens 22, 80 and 443, starts the stack
and waits until it reports ready. It generates the session secret, both
database passwords and the owner password, and prints the owner password at
the end. Re-running it keeps your secrets and data, so it doubles as the
updater. `--no-caddy` leaves your existing proxy alone, `--port` moves it off 8000 when
something else is already there, `--dry-run` says what it would do, and
`--print-compose` shows the compose file it would write.

If a site for your domain already exists in the Caddyfile, the installer
restores the file untouched and tells you rather than leaving a half-edited
config that would take down every other site on the host at the next reload.

The rest of this section is the same thing by hand.

**Before you start**, point an A record at the server's IP address and wait
until `dig +short your-domain` answers with it. Caddy asks for a certificate
the moment it starts, and a name that does not resolve yet is the single most
common reason a first deployment fails.

```bash
# 1. Docker
curl -fsSL https://get.docker.com | sh

# 2. A reverse proxy for TLS
apt-get update && apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
  | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
  | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update && apt-get install -y caddy

# 3. The stack
mkdir -p /opt/tmp && cd /opt/tmp
curl -O https://raw.githubusercontent.com/remarqable/tmpio/main/docker-compose.yml
curl -o .env https://raw.githubusercontent.com/remarqable/tmpio/main/config/docker.env.example

# 4. Fill in .env. These three want real randomness:
printf 'SESSION_SECRET=%s\n'    "$(openssl rand -hex 32)" >> .env
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 32)" >> .env
printf 'APP_USER_PASSWORD=%s\n' "$(openssl rand -hex 32)" >> .env
# then edit .env and set APP_ORIGIN, OWNER_EMAIL and OWNER_PASSWORD,
# deleting the empty placeholders the example file ships with.
$EDITOR .env

# 5. TLS
cat > /etc/caddy/Caddyfile <<EOF
your-domain {
    reverse_proxy 127.0.0.1:8000
}
EOF
systemctl reload caddy

# 6. Up
docker compose up -d
docker compose logs -f app
```

The logs should reach `tmp listening` within a few seconds of the database
becoming healthy. Open `https://your-domain` and sign in with `OWNER_EMAIL`
and `OWNER_PASSWORD`.

### Firewall

Open 80 and 443 only. The application is published on `127.0.0.1:8000` so it
cannot be reached except through the proxy, and the database publishes no port
at all — it is reachable only from the other container. Anything that reaches
port 8000 directly would bypass the proxy, so do not publish it.

```bash
ufw allow 22,80,443/tcp && ufw enable
```

### Building from source instead

Until an image is published for the version you want, or if you would rather
compile it yourself:

```bash
apt-get install -y git
git clone https://github.com/remarqable/tmpio.git /opt/tmp-src && cd /opt/tmp-src
cp /opt/tmp/.env .env
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

The first build takes a few minutes and wants about 2GB of memory. On a 1GB
server, add swap first (`fallocate -l 2G /swapfile && chmod 600 /swapfile &&
mkswap /swapfile && swapon /swapfile`) or build the image elsewhere and push it
to your own registry.

### Connecting an AI client

With a public HTTPS origin the MCP endpoint is reachable by hosted clients.
The URL is `https://your-domain/mcp`, and `docs/MCP.md` has the per-client
steps. `/admin/connections` in the browser shows the same URL and lists every
client that has connected.

## First boot

The logs tell you what happened:

```
INF schema up to date version=10
INF local owner account ready created=true email=you@example.com
INF tmp listening addr=0.0.0.0:8000 origin=https://tmp.example.com
```

Open `APP_ORIGIN`, sign in with the address and password from `.env`, and the
site is there with its starting pages.

Once the account exists you can remove `OWNER_PASSWORD` from `.env`. Keep
`OWNER_EMAIL`: it is what keeps the password form on. Setting a new
`OWNER_PASSWORD` and restarting rotates the password, which is also how you
recover from forgetting it.

## Updating

```bash
docker compose pull
docker compose up -d
```

The new container migrates the schema itself on the way up. Back up first; the
whole point of a backup is the update that goes wrong.

The compose file pins `ghcr.io/remarqable/tmpio:1`, the major version. Patches
and features arrive on a `docker compose pull`; a change that would break an
existing instance never does, because it would be version 2 and you would have
to ask for it. Other tags:

| Tag | What it is |
|---|---|
| `:1` | The major version. What you want. |
| `:1.4` | A minor series, if you want to hold back a feature release. |
| `:1.4.2` | Exactly one build, for reproducibility. |
| `:latest` | The newest release, major changes included. Not recommended. |
| `:edge` | The tip of `main`. For people who want to help find the bugs. |

Migrations only ever go forward, and no migration in a major series destroys
data. That is what makes a rollback to the previous image tag safe: an older
binary against a newer schema is the case that is not supported, so if you
roll back across a migration, restore the dump from before the update.

Automatic updaters such as Watchtower work, but for something holding your
writing it is better to pull deliberately, after a backup.

## Publishing your own front page

An instance shows a plain sign-in page to strangers. Put static files in
`/opt/tmp/web` and they are served instead, at the same address:

```bash
rsync -av --delete ./site/ root@your-server:/opt/tmp/web/
```

Nothing restarts; the server reads from disk per request. `index.html` answers
`/`, `about.html` or `about/index.html` answers `/about`, and `robots.txt`,
`sitemap.xml`, `favicon.ico` and `llms.txt` are served from here when present.

What is never shadowed, however you name a file: `/login`, `/s/<token>`,
`/mcp`, `/api`, `/admin`, `/.well-known`, and anything at all for a visitor who
is signed in — an owner always gets their own site at `/`.

Two things follow from these files sharing an origin with the application:

- **Script here runs as your instance** and can act on behalf of a signed-in
  visitor. Write access to that directory is equivalent to deploy access to the
  application. Do not paste third-party snippets in without deciding that.
- **The application's strict Content-Security-Policy applies**, which blocks
  inline scripts and third-party origins. Build without them, or set
  `PUBLIC_SITE_CSP` deliberately.

Leave the directory empty and the built-in sign-in page answers, which is what
a private instance wants.

## Backup and restore

The database volume holds everything: content, revisions, images, tokens,
sessions. Back it up with `pg_dump`, encrypted, and keep the key somewhere
other than the server:

```bash
# once: age-keygen -o tmp-backup.key, then keep the "public key:" line handy
docker compose exec -T db pg_dump --format=custom --no-owner --no-privileges \
  -U app_owner tmp | age -r "$TMP_BACKUP_RECIPIENT" -o "tmp-$(date +%F).dump.age"
```

Restore into a stopped stack:

```bash
age -d -i tmp-backup.key tmp-2026-09-19.dump.age > tmp.dump
docker compose up -d db
docker compose exec -T db psql -U app_owner -d postgres \
  -c 'DROP DATABASE IF EXISTS tmp' -c 'CREATE DATABASE tmp OWNER app_owner'
docker compose exec -T db pg_restore -U app_owner -d tmp --no-owner < tmp.dump
docker compose up -d
rm tmp.dump
```

`docs/RUNBOOK.md` covers the same ground for a non-container deployment,
including the restore drill you should do once before you have data you care
about.

## Building from source

```bash
git clone https://github.com/remarqable/tmpio.git && cd tmpio
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

The blueprint submodule is not needed to build, so a plain `git clone` is
enough. `--recurse-submodules` is for contributors who want the architecture
blueprint and the boundary checks.

## Using your own PostgreSQL

Point `DATABASE_URL` and `DATABASE_OWNER_URL` at it and delete the `db`
service. Two requirements:

- The server connects as a role with `NOBYPASSRLS`. Tenant isolation is
  enforced by the database, and a role that bypasses row-level security
  silently removes it. With `AUTO_MIGRATE=1` the server creates that role
  itself if the owner has permission to; otherwise create it by hand with the
  SQL in `docs/SETUP.md`.
- Over a public network both URLs need `sslmode=require` or stricter, and the
  server refuses to start in production without it. A database on a private or
  loopback address is exempt, which is what makes the compose stack above
  work.

## What the container does at startup

In order, and only with `AUTO_MIGRATE=1`, which the compose file sets:

1. Creates the role named in `DATABASE_URL` if it is missing, `NOBYPASSRLS`,
   and grants it the privileges the server needs — including the default
   privileges that cover tables later migrations have not created yet.
2. Takes a PostgreSQL advisory lock, applies every pending migration as the
   owner role, and releases it. Two instances starting together cannot race;
   the second waits and finds nothing to do.
3. Creates the owner account from `OWNER_EMAIL` and `OWNER_PASSWORD`, or
   rotates the password if one is set and different.
4. Drops the owner connection and serves everything else as the restricted
   role.

With `AUTO_MIGRATE` off, none of it happens and migrations are the operator's
job (`make migrate`). That is how tmp.io itself runs.
