# GitHub Actions Workflows

This directory contains CI for the CUDly CLI: automated build, test, lint,
and security checks. There are no deployment workflows here -- this repo
ships a CLI binary, not a running service.

## Workflows Overview

| Workflow | Purpose | Trigger | Required secrets |
|----------|---------|---------|-------------------|
| [ci.yml](#ci-workflow) | Build, test, lint, and security-scan the CLI | PR to `main`/`develop`, push to `main`/`develop`, manual dispatch | `SNYK_TOKEN` (optional) |
| [pre-commit.yml](#pre-commit-workflow) | Run the same hooks as local `pre-commit` against the whole tree | PR to `main`, push to `main` | None |

## CI Workflow

**File:** `ci.yml`

### Purpose

Runs comprehensive quality checks on every pull request and push to `main`
or `develop`.

### Jobs

1. **lint** - golangci-lint, `go vet`, and a gocyclo complexity gate (>10)
2. **workflow-lint** - actionlint and zizmor against `.github/workflows/`
3. **unit-tests** - `go test -race -short ./...` with coverage upload to Codecov
4. **integration-tests** - `go test -race -tags=integration ./...`
5. **security-scan** - govulncheck, gosec (SARIF upload), Trivy filesystem scan
6. **snyk-scan** - Snyk dependency scan (skipped on forked PRs without the token)
7. **cli-build** - `make build`, then `./cudly --help` as a smoke check
8. **ci-success** - gate job that requires every job above to have succeeded

### Triggers

- Pull requests to `main` or `develop`
- Pushes to `main` or `develop`
- Manual dispatch

### Required secrets

- `SNYK_TOKEN` (optional -- Snyk scanning is skipped without it)

### Example

```bash
# Runs automatically on push/PR
git push origin feature-branch

# Or trigger manually
gh workflow run ci.yml
```

## pre-commit Workflow

**File:** `pre-commit.yml`

### Purpose

Runs the repository's `.pre-commit-config.yaml` hooks (gofmt, go vet,
go mod tidy, general file hygiene, hadolint, actionlint, zizmor,
markdownlint, gocyclo, git-secrets, gosec, Trivy config) against every file
on pushes and pull requests to `main`. This mirrors what `pre-commit run
--all-files` does locally, so a clean local run should also pass here.

### Triggers

- Pull requests to `main`
- Pushes to `main`

### Required secrets

None.

### Example

```bash
# Run the same checks locally before pushing
pip install 'pre-commit==4.0.1'
pre-commit install
pre-commit run --all-files
```

## Troubleshooting

**Lint or vet fails:**

```bash
make lint
make vet
```

**Unit tests fail:**

```bash
make test-unit
```

**Complexity gate fails:**

```bash
make complexity
```

**Security scan fails:**

```bash
make security-scan
```

**pre-commit fails:**

```bash
pre-commit run --all-files
```

## Additional Resources

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [golangci-lint](https://golangci-lint.run/)
- [pre-commit](https://pre-commit.com/)
