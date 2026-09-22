APP=tmpio
PG_BIN?=/opt/homebrew/opt/postgresql@16/bin
PG_DATA?=data/pg16
PG_PORT?=5433
ENV?=config/local.env

.DEFAULT_GOAL := help

help-all: # Every target, including the ones help leaves out
	@tty -s <&1 && { C=$$(printf '\033[36m'); D=$$(printf '\033[2m'); N=$$(printf '\033[0m'); } || { C=; D=; N=; }; \
	printf '\n  %severy target%s\n\n' "$$D" "$$N"; \
	awk -v c="$$C" -v n="$$N" ' \
	  /^[a-zA-Z0-9_-]+:.*#/ { split($$0, a, ":.*#+ *"); \
	    printf "  %s%-16s%s %s\n", c, a[1], n, a[2] }' $(MAKEFILE_LIST) | sort -u; \
	printf '\n'

help: ## this
	@tty -s <&1 && { B=$$(printf '\033[1m'); C=$$(printf '\033[36m'); D=$$(printf '\033[2m'); N=$$(printf '\033[0m'); } || { B=; C=; D=; N=; }; \
	printf '\n  %stmp%s  %smake <target>%s\n\n' "$$B" "$$N" "$$D" "$$N"; \
	awk -v c="$$C" -v n="$$N" ' \
	  /^[a-zA-Z0-9_-]+:.*##/ { split($$0, a, ":.*## "); \
	    printf "  %s%-9s%s %s\n", c, a[1], n, a[2] }' $(MAKEFILE_LIST); \
	printf '\n  %sHOST=root@example.com make update   ·   make help-all for the rest%s\n\n' "$$D" "$$N"

.PHONY: help help-all installer-smoke update reset kill deploy deploy-status deploy-logs tunnel tunnel-stop run build test test-unit fmt vet migrate migrate-status migrate-down db-init db-start db-stop db-reset check ci

update: ## deploy to a host
	@scp -q install.sh $${HOST:-root@tmp.io}:/opt/tmp/install.sh
	@ssh $${HOST:-root@tmp.io} 'chmod 0755 /opt/tmp/install.sh && /opt/tmp/install.sh update'

deploy: # Legacy bare-binary deploy; refuses a host running the image
	@test -f config/deploy.env || { echo "create config/deploy.env from config/deploy.env.example"; exit 1; }
	@set -a && . ./config/deploy.env && set +a && scripts/deploy.sh

deploy-status: # Service, health and recent errors on the server
	@ssh $${HOST:-root@tmp.io} 'systemctl is-active tmp; systemctl status tmp --no-pager | sed -n 1,5p; curl -s 127.0.0.1:8100/readyz; echo; journalctl -u tmp --since "1 hour ago" --no-pager | grep -c ERR || true'

deploy-logs: # Tail the server logs
	@ssh $${HOST:-root@tmp.io} 'journalctl -u tmp -f --no-pager'

tunnel: # Expose the server over public HTTPS with ngrok (MCP clients need https)
	scripts/tunnel.sh

tunnel-stop: # Stop the ngrok tunnel
	scripts/tunnel.sh stop

run: ## start the dev server
	@set -a && . ./$(ENV) && set +a && go run -ldflags="$(LDFLAGS)" ./cmd/api

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +%Y-%m-%d)
LDFLAGS = -X github.com/remarqable/tmpio/internal/version.Version=$(VERSION) -X github.com/remarqable/tmpio/internal/version.Date=$(BUILD_DATE)

build: # Build the binaries into bin/
	go build -ldflags="$(LDFLAGS)" -o bin/ ./cmd/...

kill: ## stop it
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

fmt: # gofmt the tree
	gofmt -l -w cmd internal

vet: # go vet the tree
	go vet ./...

test: ## full suite against PostgreSQL
	@set -a && . ./$(ENV) && set +a && go test ./... -race -count=1 -p 1

test-unit: # Unit tests only (no database)
	go test ./internal/platform/render/... ./internal/models/ -run 'Path|Config|Principal|Token|Scopes' -race -count=1

migrate: ## apply migrations
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$DATABASE_OWNER_URL" up && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" up

migrate-status: # Show which migrations have been applied
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$DATABASE_OWNER_URL" status

migrate-down: # Roll back one migration on the test database (validation only)
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" down

db-init: # Create a project-local PostgreSQL 16 cluster, roles and databases
	$(PG_BIN)/initdb -D $(PG_DATA) -U app_owner --auth=trust --auth-host=scram-sha-256 -E UTF8
	@printf "port = $(PG_PORT)\nunix_socket_directories = '/tmp'\nlisten_addresses = '127.0.0.1'\n" >> $(PG_DATA)/postgresql.conf
	$(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start
	sleep 2
	$(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d postgres -v ON_ERROR_STOP=1 -c "ALTER ROLE app_owner PASSWORD 'app';" -c "CREATE ROLE app_user LOGIN PASSWORD 'app' NOBYPASSRLS;" -c "CREATE DATABASE tmp OWNER app_owner;" -c "CREATE DATABASE tmp_test OWNER app_owner;"
	for d in tmp tmp_test; do $(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d $$d -v ON_ERROR_STOP=1 -c "GRANT USAGE ON SCHEMA public TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT USAGE ON SEQUENCES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO app_user;"; done

db-start: # Start the local cluster (no-op if it is already running)
	@$(PG_BIN)/pg_ctl -D $(PG_DATA) status >/dev/null 2>&1 && echo "postgres already running on port $(PG_PORT)" || $(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start

db-stop: # Stop the local cluster
	$(PG_BIN)/pg_ctl -D $(PG_DATA) stop

reset: ## empty the dev database
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

db-reset: # Drop and recreate the test database schema
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" down-to 0 && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" up

check: ## boundary checks
	@! grep -rn "db.Unscoped()" internal/controllers internal/models internal/mcp || { echo "owner connection used in request code"; exit 1; }
	@! grep -rln "db.EnterTenantScope(" internal | grep -v "internal/models/user.go\|internal/platform/db/" || { echo "EnterTenantScope is for tenant provisioning only"; exit 1; }
	@! grep -rln "lookup_share_grant\|lookup_api_token\|lookup_oauth_token\|lookup_oauth_code" internal | grep -v "internal/models/credentials.go\|internal/models/share.go" || { echo "SECURITY DEFINER lookups may only be called from the credential models"; exit 1; }
	@! grep -rn "db\.Get()\|db\.WithTenant\|gorm\.DB" internal/controllers internal/mcp --include=*.go | grep -v _test.go || { echo "database access outside models"; exit 1; }
	@echo "boundary checks passed"

installer-check: # The installer's embedded files must match the ones in the repo
	@bash install.sh --print-compose | diff -u docker-compose.yml - && echo "installer compose matches"

vulncheck: # Report known vulnerabilities in the dependency graph that this code reaches
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

ci: fmt vet build check test # Everything CI runs

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
