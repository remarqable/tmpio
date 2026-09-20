APP=tmpio
PG_BIN?=/opt/homebrew/opt/postgresql@16/bin
PG_DATA?=data/pg16
PG_PORT?=5433
ENV?=config/local.env

.PHONY: reset kill deploy deploy-status deploy-logs tunnel tunnel-stop run build test test-unit fmt vet migrate migrate-status migrate-down db-init db-start db-stop db-reset check ci

deploy: ## Build, migrate and deploy to tmp.io (reads config/deploy.env for DOADMIN_URL, BASICAUTH_PW, optional GOOGLE_*)
	@test -f config/deploy.env || { echo "create config/deploy.env from config/deploy.env.example"; exit 1; }
	@set -a && . ./config/deploy.env && set +a && scripts/deploy.sh

deploy-status: ## Service, health and recent errors on the server
	@ssh $${HOST:-root@tmp.io} 'systemctl is-active tmp; systemctl status tmp --no-pager | sed -n 1,5p; curl -s 127.0.0.1:8100/readyz; echo; journalctl -u tmp --since "1 hour ago" --no-pager | grep -c ERR || true'

deploy-logs: ## Tail the server logs
	@ssh $${HOST:-root@tmp.io} 'journalctl -u tmp -f --no-pager'

tunnel: ## Expose the server over public HTTPS with ngrok (MCP clients need https)
	scripts/tunnel.sh

tunnel-stop:
	scripts/tunnel.sh stop

run: ## Run the API with config/local.env
	@set -a && . ./$(ENV) && set +a && go run ./cmd/api

build:
	go build -o bin/ ./cmd/...

kill: ## Stop a server started by make run
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

fmt:
	gofmt -l -w cmd internal

vet:
	go vet ./...

test: ## Full suite: unit + PostgreSQL integration (needs tmp_test database)
	@set -a && . ./$(ENV) && set +a && go test ./... -race -count=1 -p 1

test-unit: ## Unit tests only (no database)
	go test ./internal/platform/render/... ./internal/models/ -run 'Path|Config|Principal|Token|Scopes' -race -count=1

migrate: ## Apply migrations as the owner role
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$DATABASE_OWNER_URL" up && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" up

migrate-status:
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$DATABASE_OWNER_URL" status

migrate-down: ## Roll back one migration on the test database (validation only)
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" down

db-init: ## Create a project-local PostgreSQL 16 cluster, roles and databases
	$(PG_BIN)/initdb -D $(PG_DATA) -U app_owner --auth=trust --auth-host=scram-sha-256 -E UTF8
	@printf "port = $(PG_PORT)\nunix_socket_directories = '/tmp'\nlisten_addresses = '127.0.0.1'\n" >> $(PG_DATA)/postgresql.conf
	$(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start
	sleep 2
	$(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d postgres -v ON_ERROR_STOP=1 -c "ALTER ROLE app_owner PASSWORD 'app';" -c "CREATE ROLE app_user LOGIN PASSWORD 'app' NOBYPASSRLS;" -c "CREATE DATABASE tmp OWNER app_owner;" -c "CREATE DATABASE tmp_test OWNER app_owner;"
	for d in tmp tmp_test; do $(PG_BIN)/psql -h /tmp -p $(PG_PORT) -U app_owner -d $$d -v ON_ERROR_STOP=1 -c "GRANT USAGE ON SCHEMA public TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT USAGE ON SEQUENCES TO app_user; ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO app_user;"; done

db-start: ## Start the local cluster (no-op if it is already running)
	@$(PG_BIN)/pg_ctl -D $(PG_DATA) status >/dev/null 2>&1 && echo "postgres already running on port $(PG_PORT)" || $(PG_BIN)/pg_ctl -D $(PG_DATA) -l data/pg16.log start

db-stop:
	$(PG_BIN)/pg_ctl -D $(PG_DATA) stop

reset: ## Empty the development database: next sign-in starts a fresh site
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

db-reset: ## Drop and recreate the test database schema
	@set -a && . ./$(ENV) && set +a && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" down-to 0 && goose -dir migrations postgres "$$TEST_DATABASE_OWNER_URL" up

check: ## The blueprint's boundary checks
	@! grep -rn "db.Unscoped()" internal/controllers internal/models internal/mcp || { echo "owner connection used in request code"; exit 1; }
	@! grep -rln "db.EnterTenantScope(" internal | grep -v "internal/models/user.go\|internal/platform/db/" || { echo "EnterTenantScope is for tenant provisioning only"; exit 1; }
	@! grep -rln "lookup_share_grant\|lookup_api_token\|lookup_oauth_token\|lookup_oauth_code" internal | grep -v "internal/models/credentials.go\|internal/models/share.go" || { echo "SECURITY DEFINER lookups may only be called from the credential models"; exit 1; }
	@! grep -rn "db\.Get()\|db\.WithTenant\|gorm\.DB" internal/controllers internal/mcp --include=*.go | grep -v _test.go || { echo "database access outside models"; exit 1; }
	@echo "boundary checks passed"

installer-check: ## The installer's embedded compose file must match docker-compose.yml
	@bash install.sh --print-compose | diff -u docker-compose.yml - && echo "installer compose matches"

vulncheck: ## Report known vulnerabilities in the dependency graph that this code reaches
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

ci: fmt vet build check test
