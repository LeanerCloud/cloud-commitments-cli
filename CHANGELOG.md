# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added

- `archera-comparison` command: a read-only, default-off comparison of an
  Archera commitment plan (table or JSON). See `docs/cli/archera-comparison.md`
  (#2156).

### Fixed

- Ctrl-C now cancels the whole invocation instead of only setting a flag that
  was polled between purchases. The engine-version lifecycle queries, region
  discovery, recommendation fetch, coverage fetch and duplicate checks treat a
  canceled or expired context as terminal (the run stops and buys nothing)
  instead of continuing with empty lifecycle data, "no recommendations" or
  un-deduplicated counts. A purchase already in flight is not aborted: it
  finishes, is written to the audit log (before the delay between purchases,
  which the `--input-csv` path used to run first), and the report and an
  "interrupted: N of M attempted" summary are still produced. Errored
  purchases in an interrupted run are flagged "verify in AWS before
  re-running". Further Ctrl-Cs are absorbed; Ctrl-\ force-quits and may lose
  the in-flight audit record (#2151).
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
