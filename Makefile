.PHONY: all build clean test test-unit test-integration test-coverage full-test fmt vet lint \
	complexity complexity-report security-scan security-scan-go security-scan-snyk \
	security-scan-all pre-commit setup-git-secrets install-dev-tools help ci tidy-check

VERSION?=dev
GOLANGCI_LINT_VERSION?=v2.10.1
GOSEC_VERSION?=v2.28.0
GOCYCLO_VERSION?=v0.6.0
STATICCHECK_VERSION?=v0.7.0

all: build

help:
	@echo "Available targets:"
	@echo "  build              - Build the CLI"
	@echo "  test-unit          - Run unit tests"
	@echo "  test-integration   - Run integration tests with testcontainers"
	@echo "  test-coverage      - Run tests with coverage report"
	@echo "  clean              - Remove CLI build artifacts"
	@echo "  fmt                - Format Go code"
	@echo "  lint               - Run golangci-lint"
	@echo "  complexity         - Check cyclomatic complexity"
	@echo "  security-scan      - Run Go security scanners"
	@echo "  security-scan-snyk - Run Snyk"
	@echo "  ci                 - Run CI pipeline locally"

build:
	go build -o cudly ./cmd

test: test-unit

test-unit:
	@echo "Running unit tests..."
	go test -v -race -short ./...

test-integration:
	@echo "Running integration tests..."
	go test -v -race -tags=integration ./...

test-coverage:
	@echo "Generating coverage report..."
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | grep total

full-test: test-unit test-integration test-coverage

clean:
	rm -f cudly coverage.out coverage.html gosec-report.json complexity-report.txt
	go clean

fmt:
	go fmt ./...

tidy-check:
	@version=$$(awk '/^[[:space:]]*go([[:space:]]|$$)/ { if (NF != 2) { print "__malformed__"; next } print $$2 }' go.mod); \
	count=$$(printf '%s\n' "$$version" | awk 'NF { n++ } END { print n + 0 }'); \
	if [ "$$count" -ne 1 ] || ! printf '%s\n' "$$version" | awk '$$0 !~ /^[0-9]+\.[0-9]+\.[0-9]+$$/ { exit 1 }'; then \
		echo "expected exactly one patch-level Go version in go.mod" >&2; exit 1; \
	fi; \
	if ! GOTOOLCHAIN="go$$version" GOWORK=off go mod tidy -diff; then \
		echo "go mod tidy check failed for module ." >&2; exit 1; \
	fi

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed. Install: make install-dev-tools" >&2; exit 1; }
	golangci-lint run --timeout=5m

complexity:
	@command -v gocyclo >/dev/null || { echo "gocyclo not installed. Install: make install-dev-tools" >&2; exit 1; }
	@if ! issues="$$(gocyclo -over 10 -ignore '.*_test\.go' .)"; then echo "gocyclo failed" >&2; exit 1; fi; \
	if [ -n "$$issues" ]; then \
		echo "Found functions with cyclomatic complexity over 10:" >&2; \
		echo "$$issues" >&2; \
		exit 1; \
	fi

complexity-report:
	@command -v gocyclo >/dev/null || { echo "gocyclo not installed. Install: make install-dev-tools" >&2; exit 1; }
	gocyclo -top 20 -ignore '.*_test\.go' . > complexity-report.txt && cat complexity-report.txt

security-scan: security-scan-go

security-scan-go:
	@command -v gosec >/dev/null || { echo "gosec not installed. Install: make install-dev-tools" >&2; exit 1; }
	gosec -fmt=json -out=gosec-report.json -exclude=G101,G104,G115,G204,G301,G304,G402,G505 ./...

security-scan-snyk:
	@command -v snyk >/dev/null || { echo "snyk not installed. Install: npm install -g snyk" >&2; exit 1; }
	snyk test --severity-threshold=high

security-scan-all: security-scan security-scan-snyk

ci: fmt vet complexity test-unit security-scan

pre-commit: fmt vet complexity test-unit

setup-git-secrets:
	@echo "Setting up git-secrets..."
	@bash scripts/setup-git-secrets.sh

install-dev-tools:
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@echo "Installing gosec $(GOSEC_VERSION)..."
	@go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	@echo "Installing staticcheck $(STATICCHECK_VERSION)..."
	@go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	@echo "Installing gocyclo $(GOCYCLO_VERSION)..."
	@go install github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION)
