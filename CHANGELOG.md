# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

---

## [v1.2.0] - 2025-11-01

### Added

- Library support via new `cmd/netx_lib` C-compatible shared library exposing `Netx` and `NetxInterrupt` with callback hooks for stdout/stderr. (PR #9)
- Refactored CLI into reusable `internal/cli` package to run programmatically with configurable I/O and args. (PR #9)
- ICMP transport support: `icmp` can now be used in URIs and through `Listen`/`Dial` (includes ICMP listener and client/server wrappers). (PR #9)
- End-to-end helpers/tests for the library interface under `internal/tools/e2e/lib`. (PR #9)
- Build tasks and CI steps to produce shared libraries for Linux, Windows, and Android; optional static archives for macOS/iOS. (PR #9)

### Changed

- `cmd/netx/main.go` now delegates to `internal/cli.Run`. (PR #9)
- Taskfile restructured: added `test:e2e:*` tasks, library build tasks, and unique artifact names. (PR #9)
- GitHub Actions updated: consolidated lint/test/build, added library builds, and automated release publishing. (PR #9)
- Dependency upgrades: `refraction-networking/utls` v1.8.1, `spf13/cobra` v1.10.1, `klauspost/compress` v1.18.1, and `spf13/pflag` v1.0.10. (PR #9)

### Fixed

- Correct half-duplex relay direction and error messages in `Tun.Relay`. (PR #9)
- Improved error logging in CLI and in tun dial path. (PR #9)

## [v1.1.0] - 2025-10-16

### Added

- `uri` package to marshal/unmarshal chain URIs for programmatic dial/listen support.
- SSH connection wrapper.
- `netx` CLI command.
- `netx tun` subcommand for composing listener/dialer chains with transports and wrappers.

## [v1.0.0] - 2025-09-20

### Added

- Initial release
