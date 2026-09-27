# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Changed

- Split the CLI out of the CUDly monorepo into its own module,
  `github.com/LeanerCloud/cloud-commitments-cli`. The shared client and
  provider code it depends on now lives in
  `github.com/LeanerCloud/cloud-commitments-go`, pinned in `go.mod`.
  History predating the split lives in the monorepo's changelog.
