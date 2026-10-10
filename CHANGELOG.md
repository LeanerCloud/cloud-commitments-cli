# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Fixed

- Stop silently disabling the extended-support exclusion when AWS queries
  fail. A failed engine lifecycle query or region listing now aborts the run
  before any purchase; an RDS recommendation in a region whose instance
  inventory could not be read aborts it too, while other regions proceed with
  a warning. The queries are skipped entirely under `--include-extended-support`
  or when no RDS recommendations are in scope (#2147).
- Reject NaN and infinite values for `--coverage`, `--target-coverage`,
  `--min-pool-size` and `--min-savings-pct` before any API call. NaN used to
  pass validation and silently switch `--target-coverage` runs to
  `--coverage` sizing (#2136).

### Changed

- Split the CLI out of the CUDly monorepo into its own module,
  `github.com/LeanerCloud/cloud-commitments-cli`. The shared client and
  provider code it depends on now lives in
  `github.com/LeanerCloud/cloud-commitments-go`, pinned in `go.mod`.
  History predating the split lives in the monorepo's changelog.
