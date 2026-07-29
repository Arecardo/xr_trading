.PHONY: security-check fmt fmt-check tidy-check vet test test-race test-adapters test-integration test-integration-existing smoke-bybit smoke-longbridge coverage build check db-start db-stop migrate-up

COVERAGE_MIN ?= 80.0
COVERAGE_FILE ?= coverage.out
BIN_DIR ?= bin
MARKET_INFO_DATABASE_URL ?= postgres://postgres:local-only-database-password@localhost:5432/xr_trading?sslmode=disable&options=-c%20role%3Dxr_market_data_runtime
MARKET_INFO_MIGRATION_DATABASE_URL ?= postgres://postgres:local-only-database-password@localhost:5432/xr_trading?sslmode=disable&options=-c%20role%3Dxr_market_data_owner
MARKET_INFO_TEST_ADMIN_DATABASE_URL ?= postgres://postgres:local-only-database-password@localhost:5432/xr_trading?sslmode=disable
export MARKET_INFO_DATABASE_URL
export MARKET_INFO_MIGRATION_DATABASE_URL
export MARKET_INFO_TEST_ADMIN_DATABASE_URL

security-check:
	../scripts/security-check.sh

fmt:
	go fmt ./...

fmt-check:
	@files="$$(gofmt -l .)"; \
	if test -n "$$files"; then \
		printf 'gofmt is required for:\n%s\n' "$$files" >&2; \
		exit 1; \
	fi

tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-adapters:
	go test ./internal/providers/...

test-integration:
	./scripts/test-integration.sh

# Escape hatch for an already prepared disposable database. The default target
# above owns creation, migration and cleanup and is the command used by CI.
test-integration-existing:
	@test "$(MARKET_INFO_ALLOW_EXISTING_INTEGRATION)" = "1" || { \
		echo "set MARKET_INFO_ALLOW_EXISTING_INTEGRATION=1 and provide disposable database URLs" >&2; \
		exit 1; \
	}
	go test -tags=integration ./...

smoke-bybit:
	@test "$(BYBIT_SMOKE)" = "1" || { \
		echo "set BYBIT_SMOKE=1 to acknowledge a real Bybit network request" >&2; \
		exit 1; \
	}
	go test -count=1 -tags=smoke ./internal/providers/bybit -run TestSmokePublicMarketData

smoke-longbridge:
	@test "$(LONGBRIDGE_SMOKE)" = "1" || { \
		echo "set LONGBRIDGE_SMOKE=1 and inject quote-only credentials" >&2; \
		exit 1; \
	}
	go test -count=1 -tags=smoke ./internal/providers/longbridge -run TestSmokeMarketData

coverage:
	go test ./... -covermode=atomic -coverprofile=$(COVERAGE_FILE)
	@total="$$(go tool cover -func=$(COVERAGE_FILE) | awk '/^total:/ { gsub(/%/, "", $$3); print $$3 }')"; \
	awk -v total="$$total" -v minimum="$(COVERAGE_MIN)" 'BEGIN { \
		printf "total coverage: %.1f%% (minimum %.1f%%)\n", total, minimum; \
		if (total + 0 < minimum + 0) exit 1; \
	}'

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -o $(BIN_DIR)/market-info ./cmd/market-info
	CGO_ENABLED=0 go build -trimpath -o $(BIN_DIR)/market-info-migrate ./cmd/market-info-migrate

check: security-check fmt-check tidy-check vet coverage test-race

db-start:
	docker compose --env-file .env.example up -d postgres

db-stop:
	docker compose down

migrate-up:
	go run ./cmd/market-info-migrate
