# Changelog

All notable changes to CUDly are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Notices

- **Federation IaC bundles downloaded before 2026-04-22 need to be
  re-downloaded** to get zero-touch registration. Older bundles silently
  skip auto-registration unless manually edited (Terraform `registration.tf`
  gated `do_register` on `cudly_api_url`; CLI shell scripts included the
  registration call only when `CUDlyAPIURL` was present at render time;
  CloudFormation deploy scripts had no registration call at all).
  Re-download the bundle from the CUDly UI and the new copy will register
  your account automatically with no manual edits required.

### Fixed

- Remove debug console.log from frontend recommendation handler
- Align pre-commit gocyclo threshold (10) with CI pipeline
- Pin tool versions in GitHub Actions for reproducible builds
- Update README Go version badge to match go.mod (1.25+)

## [0.9.0] - 2026-03-06

### Added

- RI Exchange feature: reshape analysis with normalization factors, API
  endpoints, and frontend page for managing convertible Reserved Instances
- RI utilization tracking from Cost Explorer with pagination support
- Convertible RI listing in EC2 client
- Security headers on all Lambda responses
- Admin password resolution from cloud secret managers

### Fixed

- Harden RI exchange handlers with validation and error sanitization
- Fix async race conditions and input validation in RI exchange frontend
- Fix base64 encoding for saveProfile and resetPassword
- Remove duplicate logout event handler
- Guard DNS zone outputs against missing resources (GCP, Azure)
- Fix Azure CDN redirect type and SPA routing
- Add network policies and resource quotas to AKS module
- Add security headers to Azure Front Door and GCP load balancer
- Wire admin password secrets through all cloud environment root modules

## [0.8.0] - 2026-02-01

### Added

- Deployment health check blocks for AWS, Azure, and GCP Terraform modules
- GCP self-signed cert for dev HTTPS
- Azure Front Door API routing and custom domain support
- Cross-provider deployment test harness script
- Azure ACR resource and registry authentication

### Fixed

- Enforce SSL-only connections on GCP Cloud SQL
- Migrate GCP load balancer to EXTERNAL_MANAGED with SPA routing
- Fix Azure Container Apps config and CDN delivery rule names
- Fix GCP frontend build trigger and database password generation
- Expand frontend CSP connect-src for Azure and GCP API origins
- Fix Fargate EventBridge container name
- Capture migration exit code correctly in entrypoint.sh

### Changed

- Convert AWS database from Aurora Serverless v2 to standalone RDS
- Move GCP Secret Manager out of database module
- Replace Azure Container App Jobs with Logic Apps scheduled tasks
- Simplify Azure database module

## [0.7.0] - 2026-01-15

### Added

- Full Terraform infrastructure for AWS (Fargate, Lambda, CloudFront, RDS),
  Azure (Container Apps, AKS, Front Door, PostgreSQL), and GCP (Cloud Run,
  GKE, Cloud SQL) with CI-specific tfvars
- PostgreSQL database with connection pool, migrations, and secret resolvers
- Authentication service with RBAC and API key support
- REST API with rate limiting, CORS, and middleware stack
- Email service with SMTP sender and cloud credential resolution
- Analytics collector, purchase execution, and scheduled task runner
- Docker containerization with multi-stage builds and compose configs
- GitHub Actions CI/CD pipeline (lint, test, security scan, Docker build,
  Terraform validate, E2E tests, Infracost)
- Frontend web dashboard with TypeScript, webpack, Chart.js

### Fixed

- Sanitize user input in dashboard and recommendations (XSS prevention)
- Add connection pool limits and graceful shutdown to server
- Add nil checks across Azure service clients
- Enforce 12-char minimum password with complexity requirements
- Use hidden-source-map for production frontend builds
- Use rightmost X-Forwarded-For IP for client identification
- Add SHA256 checksum verification for migrate binary in Docker
- Tighten git-secrets patterns to reduce false positives

## [0.6.0] - 2025-11-01

### Added

- Database Savings Plans support and SP type filtering
- OSL-3.0 license and contributing guidelines

### Fixed

- RDS RI purchase failing on details assertion and invalid reservation ID
- OpenSearch RI resource type and offering lookup
- Deduplicate reservation ID sanitization into pkg/common

### Changed

- Refactor internal packages to providers (aws, azure, gcp)
- Add provider-specific mocking infrastructure and tests

## [0.5.0] - 2025-09-01

### Added

- Multi-cloud support (Azure experimental, GCP experimental)
- API-based RDS extended support detection
- Instance type validation system
- CSV reader for recommendation import
- Duplicate RI purchase prevention
- Account alias lookup
- Confirmation prompt and instance limit features

### Changed

- Replace global variables with Config struct pattern
- Improve rate limiting and test performance
- Refactor all purchase clients with enhanced error handling

## [0.4.0] - 2025-07-01

### Added

- Multi-service RI support: EC2, ElastiCache, MemoryDB, OpenSearch, Redshift
- Multi-service orchestration and CLI
- Comprehensive test coverage (80%+ across packages)

### Fixed

- CSV pricing calculations to use AWS-provided cost data

## [0.3.0] - 2025-05-01

### Added

- Initial CLI tool for RDS Reserved Instance purchasing
- Recommendations fetching from AWS Cost Explorer
- CSV output for analysis results
- Go module setup with AWS SDK v2
