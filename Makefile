.PHONY: build clean test deploy help all build-server build-lambda test-unit test-integration \
        test-coverage full-test security-scan terraform-validate docker-build \
        fmt vet lint complexity complexity-report security-scan-go security-scan-docker \
        security-scan-terraform terraform-fmt terraform-fmt-check docker-test pre-commit \
        setup-git-secrets security-scan-snyk security-scan-all cost-estimate docker-compose-test \
        install-dev-tools

# Variables
VERSION?=dev
BUILD_TIME=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# Default target
all: build

help: ## Display available targets
	@echo "Available targets:"
	@echo "  build              - Build the CLI"
	@echo "  build-server       - Build the unified server"
	@echo "  build-lambda       - Build for AWS Lambda"
	@echo "  test               - Run all unit tests"
	@echo "  test-unit          - Run unit tests only"
	@echo "  test-integration   - Run integration tests with testcontainers"
	@echo "  test-coverage      - Run tests with coverage report"
	@echo "  clean              - Remove build artifacts"
	@echo "  fmt                - Format Go code"
	@echo "  lint               - Run golangci-lint"
	@echo "  complexity         - Check cyclomatic complexity"
	@echo "  complexity-report  - Generate detailed complexity report"
	@echo "  security-scan      - Run security scanners (gosec, trivy, tfsec)"
	@echo "  security-scan-all  - Run all security scanners including Snyk"
	@echo "  setup-git-secrets  - Set up git-secrets for preventing credential leaks"
	@echo "  terraform-validate - Validate Terraform configurations"
	@echo "  cost-estimate      - Estimate infrastructure costs with Infracost"
	@echo "  docker-build       - Build Docker image"
	@echo "  docker-compose-test - Run E2E tests with docker-compose"
	@echo "  ci                 - Run CI pipeline locally"

# Build the CLI
build:
	go build -o cudly ./cmd

# Build the unified server
build-server:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/cudly-server ./cmd/server

# Build for Lambda (backward compatible)
build-lambda:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bootstrap ./cmd/lambda

# Run unit tests
test: test-unit

test-unit:
	@echo "Running unit tests..."
	go test -v -race -short ./...

# Run integration tests (requires testcontainers)
test-integration:
	@echo "Running integration tests..."
	go test -v -race -tags=integration ./...

# Run tests with coverage
test-coverage:
	@echo "Generating coverage report..."
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | grep total

# Run full test suite
full-test: test-unit test-integration test-coverage

# Clean build artifacts
clean:
	rm -f cudly bootstrap bin/cudly-server
	rm -f coverage.out coverage.html
	rm -f gosec-report.json trivy-report.json tfsec-report.json
	go clean

# Deploy (requires AWS credentials and terraform profiles)
deploy:
	./scripts/tf-deploy.sh aws dev

# Format code
fmt:
	go fmt ./...
	terraform fmt -recursive terraform/

# Lint code
lint:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "golangci-lint not installed. Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Go vet
vet:
	go vet ./...

# Check cyclomatic complexity
complexity:
	@echo "Checking cyclomatic complexity (threshold: 10)..."
	@if command -v gocyclo > /dev/null; then \
		COMPLEXITY_ISSUES=$$(gocyclo -over 10 . 2>&1 || true); \
		if [ -n "$$COMPLEXITY_ISSUES" ]; then \
			echo "❌ Found functions with cyclomatic complexity over 10:"; \
			echo "$$COMPLEXITY_ISSUES"; \
			echo ""; \
			echo "⚠️  Please refactor these functions to reduce complexity."; \
			echo "📖 Tip: Extract helper functions, use early returns, or simplify logic."; \
			exit 1; \
		else \
			echo "✅ All functions have acceptable cyclomatic complexity (≤10)"; \
		fi \
	else \
		echo "gocyclo not installed. Install: go install github.com/fzipp/gocyclo/cmd/gocyclo@latest"; \
		exit 1; \
	fi

# Generate detailed complexity report
complexity-report:
	@echo "Generating cyclomatic complexity report..."
	@if command -v gocyclo > /dev/null; then \
		gocyclo -top 20 . | tee complexity-report.txt; \
		echo ""; \
		echo "📊 Top 20 most complex functions saved to: complexity-report.txt"; \
	else \
		echo "gocyclo not installed. Install: go install github.com/fzipp/gocyclo/cmd/gocyclo@latest"; \
	fi

# Security scanning
security-scan: security-scan-go security-scan-docker security-scan-terraform

security-scan-go:
	@echo "Running gosec..."
	@if command -v gosec > /dev/null; then \
		gosec -fmt=json -out=gosec-report.json -exclude=G101,G104,G115,G204,G301,G304,G402,G505 ./...; \
		echo "✓ Go security scan complete: gosec-report.json"; \
	else \
		echo "gosec not installed. Install: go install github.com/securego/gosec/v2/cmd/gosec@latest"; \
	fi

security-scan-docker:
	@echo "Running trivy..."
	@if command -v trivy > /dev/null; then \
		trivy fs --security-checks vuln,config . --format json --output trivy-report.json; \
		echo "✓ Container security scan complete: trivy-report.json"; \
	else \
		echo "trivy not installed. Install: https://aquasecurity.github.io/trivy/"; \
	fi

security-scan-terraform:
	@echo "Running tfsec..."
	@if command -v tfsec > /dev/null; then \
		tfsec terraform/ --format json --out tfsec-report.json; \
		echo "✓ Terraform security scan complete: tfsec-report.json"; \
	else \
		echo "tfsec not installed. Install: https://aquasecurity.github.io/tfsec/"; \
	fi

# Terraform validation
terraform-validate:
	@echo "Validating Terraform configurations..."
	@for dir in terraform/environments/*/dev; do \
		echo "Validating $$dir..."; \
		(cd $$dir && terraform init -backend=false && terraform validate) || exit 1; \
	done
	@echo "✓ Terraform validation complete"

terraform-fmt:
	terraform fmt -recursive terraform/

terraform-fmt-check:
	terraform fmt -check -recursive terraform/

# Docker
docker-build:
	@echo "Building Docker image..."
	docker build -t cudly:$(VERSION) -t cudly:latest --build-arg VERSION=$(VERSION) .
	@echo "✓ Docker image built: cudly:$(VERSION)"

docker-test: docker-build
	@echo "Testing Docker image..."
	docker run --rm cudly:$(VERSION) /app/cudly --help || true

# CI pipeline
ci: fmt vet complexity test-unit security-scan terraform-validate
	@echo "✓ CI pipeline complete"

# Pre-commit checks
pre-commit: fmt vet complexity test-unit
	@echo "✓ Pre-commit checks complete"

# Git secrets setup
setup-git-secrets:
	@echo "Setting up git-secrets..."
	@bash scripts/setup-git-secrets.sh

# Snyk security scanning
security-scan-snyk:
	@echo "Running Snyk security scan..."
	@if command -v snyk > /dev/null; then \
		snyk test --severity-threshold=high; \
		echo "✓ Snyk scan complete"; \
	else \
		echo "snyk not installed. Install: npm install -g snyk"; \
	fi

# Run all security scanners including Snyk
security-scan-all: security-scan security-scan-snyk
	@echo "✓ All security scans complete"

# Cost estimation with Infracost
cost-estimate:
	@echo "Estimating infrastructure costs..."
	@bash scripts/cost-estimate.sh

# Docker Compose E2E tests
docker-compose-test:
	@echo "Running E2E tests with docker-compose..."
	docker-compose -f docker-compose.test.yml up --abort-on-container-exit --exit-code-from test-runner
	docker-compose -f docker-compose.test.yml down -v

# Install development dependencies
install-dev-tools:
	@echo "Installing development tools..."
	@echo "Installing golangci-lint..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "Installing gosec..."
	@go install github.com/securego/gosec/v2/cmd/gosec@latest
	@echo "Installing staticcheck..."
	@go install honnef.co/go/tools/cmd/staticcheck@latest
	@echo "Installing gocyclo..."
	@go install github.com/fzipp/gocyclo/cmd/gocyclo@latest
	@echo "Installing golang-migrate..."
	@go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	@echo "✓ Development tools installed"
	@echo ""
	@echo "Additional tools to install manually:"
	@echo "  - trivy: https://aquasecurity.github.io/trivy/"
	@echo "  - tfsec: https://aquasecurity.github.io/tfsec/"
	@echo "  - infracost: https://www.infracost.io/docs/"
	@echo "  - git-secrets: https://github.com/awslabs/git-secrets"
	@echo "  - snyk: npm install -g snyk"
	@echo "  - pre-commit: pip install pre-commit"
