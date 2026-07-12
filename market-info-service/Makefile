.PHONY: fmt vet test test-race coverage check db-start db-stop migrate-up

COVERAGE_MIN ?= 80.0
COVERAGE_FILE ?= coverage.out

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	go test ./... -covermode=atomic -coverprofile=$(COVERAGE_FILE)
	@total="$$(go tool cover -func=$(COVERAGE_FILE) | awk '/^total:/ { gsub(/%/, "", $$3); print $$3 }')"; \
	awk -v total="$$total" -v minimum="$(COVERAGE_MIN)" 'BEGIN { \
		printf "total coverage: %.1f%% (minimum %.1f%%)\n", total, minimum; \
		if (total + 0 < minimum + 0) exit 1; \
	}'

check: fmt vet coverage test-race

db-start:
	docker compose up -d postgres

db-stop:
	docker compose down

migrate-up:
	go run ./cmd/market-info-migrate
