# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Server pinning: new client option `host_key` (`TK_HOST_KEY`, `--host-key`,
  add-on and UCI option). The client refuses any other server key, with OpenSSH
  and Dropbear. The server prints the value to use at startup. Empty keeps the
  previous trust-on-first-use behaviour.
- Release workflow: semver-tagged images (`X.Y.Z`, `X.Y`, `latest`), OpenWrt
  binaries and `SHA256SUMS` attached to each GitHub release.
- Images are signed with cosign (keyless) and carry an SBOM and SLSA provenance.
- `go vet` and `go test -race` run on every pull request.
- Dependabot for Go modules, base images and GitHub Actions.

### Changed
- `main` now publishes the `edge` image tag; `latest` only moves on releases.
- Home Assistant add-on renamed to **TunnelKit Client**, slug changed from
  `autossh_plus` to `tunnelkit_client` and directory to `tunnelkit-client/`.
  Existing installs must uninstall the old add-on and install the new one;
  options are not carried over.
- Examples and tests use neutral names; all comments are in English.
