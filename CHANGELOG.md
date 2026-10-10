# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added

- `archera-comparison` command: a read-only, default-off comparison of an
  Archera commitment plan (table or JSON). See `docs/cli/archera-comparison.md`
  (#2156).

### Fixed

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
