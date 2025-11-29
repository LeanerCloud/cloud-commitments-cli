# Contributing to CUDly

Thank you for your interest in contributing to CUDly! This document provides guidelines and instructions for contributing.

## Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment. Be kind, constructive, and professional in all interactions.

## How to Contribute

### Reporting Bugs

1. **Search existing issues** - Check if the bug has already been reported
2. **Create a detailed report** including:
   - CUDly version (`./cudly --version`)
   - Go version (`go version`)
   - Operating system and architecture
   - Cloud provider and service affected
   - Steps to reproduce
   - Expected vs actual behavior
   - Relevant logs (with sensitive data removed)

### Suggesting Features

1. **Search existing issues** - Your idea may already be proposed
2. **Open a feature request** with:
   - Clear description of the feature
   - Use case and benefits
   - Proposed implementation (if applicable)
   - Any potential drawbacks

### Submitting Code

1. **Fork the repository**
2. **Create a feature branch** from `main`:
   ```bash
   git checkout -b feature/your-feature-name
   ```
3. **Make your changes** following our coding standards
4. **Write or update tests** for your changes
5. **Run the test suite** to ensure everything passes
6. **Commit with clear messages** following our commit conventions
7. **Push to your fork** and submit a Pull Request

## Development Setup

### Prerequisites

- Go 1.23 or later
- AWS/Azure/GCP credentials for integration testing
- Git

### Getting Started

```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/CUDly.git
cd CUDly

# Add upstream remote
git remote add upstream https://github.com/LeanerCloud/CUDly.git

# Install dependencies
go mod download

# Build the project
go build -o cudly cmd/*.go

# Run tests
go test ./...
```

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests for a specific package
go test ./providers/aws/...

# Run tests with verbose output
go test -v ./...

# Run a specific test
go test -run TestFunctionName ./path/to/package
```

### Test Coverage Goals

We aim to maintain the following minimum test coverage:

| Package | Minimum Coverage |
|---------|-----------------|
| Service clients | 80% |
| Provider implementations | 70% |
| Common/shared packages | 80% |
| CLI/cmd | 60% |

## Coding Standards

### Go Style

- Follow the [Effective Go](https://golang.org/doc/effective_go) guidelines
- Use `gofmt` to format code
- Use `golint` and `go vet` to catch issues
- Keep functions focused and reasonably sized
- Write clear, self-documenting code

### Naming Conventions

- Use CamelCase for exported names, camelCase for unexported
- Use meaningful, descriptive names
- Interfaces describing behavior should end in `-er` (e.g., `Reader`, `Writer`)
- Test files: `*_test.go`
- Mock implementations: prefix with `mock`

### Documentation

- All exported functions, types, and packages must have doc comments
- Use complete sentences starting with the name being documented
- Include usage examples for complex functionality
- Keep comments up to date with code changes

### Error Handling

- Always handle errors explicitly
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use custom error types for domain-specific errors
- Never ignore errors silently

### Testing

- Write table-driven tests where appropriate
- Use interfaces and dependency injection for testability
- Mock external dependencies (AWS/Azure/GCP SDKs)
- Test both success and error paths
- Include edge cases in test coverage

## Project Structure

```
CUDly/
├── cmd/                      # CLI entry point
├── pkg/                      # Shared packages
│   ├── common/              # Cloud-agnostic types
│   └── provider/            # Provider abstraction
├── providers/               # Cloud implementations
│   ├── aws/                 # AWS provider
│   │   ├── services/        # Service clients
│   │   └── internal/        # Internal packages
│   ├── azure/               # Azure provider
│   └── gcp/                 # GCP provider
└── internal/                # Private packages
```

### Adding a New Service

1. Create the service client in `providers/<cloud>/services/`
2. Implement the `ServiceClient` interface from `pkg/provider`
3. Register the service in the provider's `GetServiceClient` method
4. Add recommendations support if applicable
5. Write comprehensive tests
6. Update documentation

### Adding a New Cloud Provider

1. Create a new directory under `providers/`
2. Implement the `Provider` interface from `pkg/provider`
3. Implement required service clients
4. Register the provider using `provider.RegisterProvider()` in `init()`
5. Add authentication documentation
6. Write comprehensive tests
7. Update README with new provider information

## Commit Guidelines

### Commit Message Format

```
type(scope): brief description

Longer description if needed. Explain what and why,
not how (the code shows how).

Fixes #123
```

### Types

- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation only
- `style`: Formatting, missing semicolons, etc.
- `refactor`: Code change that neither fixes a bug nor adds a feature
- `perf`: Performance improvement
- `test`: Adding or updating tests
- `chore`: Build process, dependencies, etc.

### Examples

```
feat(aws): add MemoryDB reserved node support

Implements purchase and recommendation fetching for
Amazon MemoryDB reserved nodes.

Fixes #42
```

```
fix(azure): handle subscription pagination correctly

The previous implementation missed subscriptions after
the first page. Now properly iterates all pages.
```

## Pull Request Process

1. **Update documentation** for any user-facing changes
2. **Add or update tests** for your changes
3. **Ensure all tests pass** before submitting
4. **Fill out the PR template** completely
5. **Request review** from maintainers
6. **Address feedback** promptly and constructively

### PR Checklist

- [ ] Code follows project style guidelines
- [ ] Tests added/updated and passing
- [ ] Documentation updated
- [ ] Commit messages follow conventions
- [ ] No sensitive data in code or commits
- [ ] Changes are backwards compatible (or breaking changes documented)

## Security

### Reporting Vulnerabilities

**Do not report security vulnerabilities through public issues.**

Instead, please email security concerns to the maintainers directly. Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Security Best Practices

- Never commit credentials or secrets
- Use environment variables for sensitive configuration
- Validate all external input
- Follow least-privilege principles
- Keep dependencies updated

## Areas for Contribution

We welcome contributions in these areas:

### High Priority
- Additional AWS services (Lambda, DynamoDB, etc.)
- Azure service implementations
- GCP service implementations
- Improved error messages and user experience

### Medium Priority
- Enhanced reporting and analytics
- Terraform/CloudFormation integration
- Web UI dashboard
- Performance optimizations

### Documentation
- Usage tutorials and guides
- Architecture documentation
- API documentation
- Translation to other languages

## Getting Help

- **Issues**: Open a GitHub issue for bugs or features
- **Discussions**: Use GitHub Discussions for questions
- **Documentation**: Check the README and code comments

## License

By contributing to CUDly, you agree that your contributions will be licensed under the Open Software License 3.0 (OSL-3.0).

This means:
- Your contributions can be used commercially
- Derivative works must also be OSL-3.0 licensed
- You grant a patent license for your contributions
- Attribution must be maintained

## Acknowledgments

Thank you to all contributors who help make CUDly better! Your time and expertise are greatly appreciated.
