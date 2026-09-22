APP=tmpio
PG_BIN?=/opt/homebrew/opt/postgresql@16/bin
PG_DATA?=data/pg16
PG_PORT?=5433
ENV?=config/local.env

.DEFAULT_GOAL := help


help: # this
	@tty -s <&1 && { B=$$(printf '\033[1m'); C=$$(printf '\033[36m'); D=$$(printf '\033[2m'); N=$$(printf '\033[0m'); } || { B=; C=; D=; N=; }; \
	printf '\n  %stmp%s  %smake <target>%s\n' "$$B" "$$N" "$$D" "$$N"; \
	awk -v c="$$C" -v d="$$D" -v n="$$N" ' \
	  match($$0, /^[a-zA-Z0-9_-]+:.*##@/) { \
	    split($$0, a, ":.*##@"); split(a[2], b, " "); \
	    sec = b[1]; sub(/^[A-Za-z]+ /, "", a[2]); \
	    rows[sec] = rows[sec] sprintf("  %s%-12s%s %s\n", c, a[1], n, a[2]) \
	  } \
	  END { \
	    split("Develop Check Database Ship", order, " "); \
	    for (i = 1; i <= 4; i++) \
	      if (rows[order[i]]) printf "\n  %s%s%s\n%s", d, order[i], n, rows[order[i]] \
	  }' $(MAKEFILE_LIST); \
	printf '\n  %sHOST=root@example.com make update%s\n\n' "$$D" "$$N"

.PHONY: help release installer-smoke update reset kill deploy deploy-status deploy-logs tunnel tunnel-stop run build test test-unit fmt vet migrate migrate-status migrate-down db-init db-start db-stop db-reset check ci

release: ##@Ship publish a version (make release V=1.4.1)
	@test -n "$(V)" || { echo "usage: make release V=1.4.1"; exit 1; }
	@case "$(V)" in v*) echo "leave the v off: make release V=$${V#v}"; exit 1 ;; esac
	@git diff --quiet && git diff --cached --quiet || { echo "working tree is dirty; commit first"; exit 1; }
	@test -z "$$(git log origin/main..HEAD --oneline)" || { echo "unpushed commits; git push first"; exit 1; }
	@git rev-parse -q --verify "refs/tags/v$(V)" >/dev/null && { echo "v$(V) already exists"; exit 1; } || true
	@printf 'tagging v%s at %s\n' "$(V)" "$$(git rev-parse --short HEAD)"
	@git tag -a "v$(V)" -m "v$(V)" && git push -q origin "v$(V)"
	@echo "pushed. building the image (arm64 is emulated, so this takes a while)…"
	@until [ "$$(gh run list --workflow release --branch v$(V) --limit 1 --json status --jq '.[0].status' 2>/dev/null)" = completed ]; do sleep 20; done; \
	 r=$$(gh run list --workflow release --branch v$(V) --limit 1 --json conclusion --jq '.[0].conclusion'); \
	 if [ "$$r" = success ]; then echo "ghcr.io/remarqable/tmpio:$(V) published, and :1 now points at it"; \
	   echo "deploy it:  make update"; \
	 else echo "the build did not succeed: $$r"; exit 1; fi

update: ##@Ship update a host to the current release
	@scp -q install.sh $${HOST:-root@tmp.io}:/opt/tmp/install.sh
	@ssh $${HOST:-root@tmp.io} 'chmod 0755 /opt/tmp/install.sh && /opt/tmp/install.sh update'




tunnel: ##@Ship expose the dev server over public HTTPS
	scripts/tunnel.sh

tunnel-stop: ##@Ship stop the tunnel
	scripts/tunnel.sh stop

run: ##@Develop start the dev server
	@set -a && . ./$(ENV) && set +a && go run -ldflags="$(LDFLAGS)" ./cmd/api

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%d)
LDFLAGS = -X github.com/remarqable/tmpio/internal/version.Version=$(VERSION) -X github.com/remarqable/tmpio/internal/version.Date=$(BUILD_DATE)

build: ##@Develop build the binaries into bin/
	go build -ldflags="$(LDFLAGS)" -o bin/ ./cmd/...

kill: ##@Develop stop it
	@p=$$(grep -sE '^PORT=' $(ENV) | cut -d= -f2 | tr -d '"'); p=$${p:-8000}; \
	 pids=$$(lsof -ti tcp:$$p -sTCP:LISTEN 2>/dev/null || true); \
	 if [ -z "$$pids" ]; then echo "nothing is listening on $$p"; exit 0; fi; \
	 for pid in $$pids; do \
	   name=$$(basename "$$(ps -o comm= -p $$pid 2>/dev/null)"); \
	   case "$$name" in \
	     api|tmpio|main|go) kill $$pid && echo "stopped $$name ($$pid) on port $$p" ;; \
	     *) echo "left $$name ($$pid) on port $$p alone: that is not a tmp server" ;; \
	   esac; \
	 done



test: ##@Check full suite against PostgreSQL
	@set -a && . ./$(ENV) && set +a && go test ./... -race -count=1 -p 1


migrate: ##@Database apply migrations
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$DATABASE_OWNER_URL" up && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" up



db: ##@Database start PostgreSQL, creating it the first time
	@if [ ! -d $(PG_DATA) ]; then \
	  echo "creating a project-local PostgreSQL 16 cluster in $(PG_DATA) on port $(PG_PORT)"; \
	  $(PG_BIN)/initdb -D $(PG_DATA) -U app_owner --auth=trust --auth-host=scram-sha-256 -E UTF8 >/dev/null; \
	  printf "port = $(PG_PORT)\nunix_socket_directories = '/tmp'\nlisten_addresses = '127.0.0.1'\n" >> $(PG_DATA)/postgresql.conf; \
	  $(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start; \
	  sleep 2; \
	  $(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d postgres -v ON_ERROR_STOP=1 -c "ALTER ROLE app_owner PASSWORD 'app';" -c "CREATE ROLE app_user LOGIN PASSWORD 'app' NOBYPASSRLS;" -c "CREATE DATABASE tmp OWNER app_owner;" -c "CREATE DATABASE tmp_test OWNER app_owner;"; \
	  for d in tmp tmp_test; do $(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d $$d -v ON_ERROR_STOP=1 -c "GRANT USAGE ON SCHEMA public TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT USAGE ON SEQUENCES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO app_user;"; done; \
	else \
	  $(PG_BIN)/pg_ctl -D $(PG_DATA) status >/dev/null 2>&1 && echo "postgres already running on port $(PG_PORT)" || $(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start; \
	fi

db-stop: ##@Database stop it
	$(PG_BIN)/pg_ctl -D $(PG_DATA) stop

reset: ##@Database empty the dev database
	@test -f $(ENV) || { echo "no $(ENV); copy config/local.env.example first"; exit 1; }
	@set -a && . "$(ENV)" && set +a && \
	if [ "$$FORCE" != "1" ]; then \
		printf 'Delete every page, revision, account and token in the development database.\nType "reset" to continue: '; \
		read answer; [ "$$answer" = "reset" ] || { echo "cancelled"; exit 1; }; \
	fi; \
	$(PG_BIN)/psql "$$DATABASE_OWNER_URL" -v ON_ERROR_STOP=1 -q -c "\
		DO \$$\$$ DECLARE r record; BEGIN \
			FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' LOOP \
				EXECUTE format('TRUNCATE TABLE %I RESTART IDENTITY CASCADE', r.tablename); \
			END LOOP; \
		END \$$\$$;" \
		-c "INSERT INTO instance_setting (id) VALUES (1) ON CONFLICT (id) DO NOTHING;" && \
	echo "development database emptied. Sign in again and you get a new site with the welcome page."


check: ##@Check boundary and installer checks
	@! grep -rn "db.Unscoped()" internal/controllers internal/models internal/mcp || { echo "owner connection used in request code"; exit 1; }
	@! grep -rln "db.EnterTenantScope(" internal | grep -v "internal/models/user.go\|internal/platform/db/" || { echo "EnterTenantScope is for tenant provisioning only"; exit 1; }
	@! grep -rln "lookup_share_grant\|lookup_api_token\|lookup_oauth_token\|lookup_oauth_code" internal | grep -v "internal/models/credentials.go\|internal/models/share.go" || { echo "SECURITY DEFINER lookups may only be called from the credential models"; exit 1; }
	@! grep -rn "db\.Get()\|db\.WithTenant\|gorm\.DB" internal/controllers internal/mcp --include=*.go | grep -v _test.go || { echo "database access outside models"; exit 1; }
	@echo "boundary checks passed"
	@bash install.sh --print-compose | diff -u docker-compose.yml - >/dev/null && echo "installer compose matches"
	@$(MAKE) --no-print-directory installer-smoke



ci: ##@Check everything CI runs
	gofmt -l -w cmd internal
	go vet ./...
	@$(MAKE) --no-print-directory build check test

installer-smoke: # The installer must survive being run as the installed copy
	@set -e; \
	T=$$(mktemp -d); cp install.sh $$T/install.sh; chmod 0755 $$T/install.sh; \
	before=$$(wc -c < $$T/install.sh); \
	TMP_NO_SELF_UPDATE=1 bash $$T/install.sh --help >/dev/null; \
	after=$$(wc -c < $$T/install.sh); \
	[ "$$before" = "$$after" ] || { echo "install.sh changed size when run ($$before -> $$after)"; exit 1; }; \
	[ "$$after" -gt 0 ] || { echo "install.sh truncated itself"; exit 1; }; \
	TMP_NO_SELF_UPDATE=1 bash $$T/install.sh --no-self-update >/dev/null 2>&1 || true; \
	[ "$$(wc -c < $$T/install.sh)" -gt 0 ] || { echo "install.sh truncated itself on a flag-only run"; exit 1; }; \
	rm -rf $$T; echo "installer survives running as the installed copy"
