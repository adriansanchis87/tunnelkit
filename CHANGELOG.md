# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Readable names in the panel: optional `clients.json` (`TK_SERVER_CLIENTS`) with a
  client `title`, a `name` per port and an optional expected `url`. The **ON**
  dialog now lists the client's ports and has a **Test** button that checks each
  port through the tunnel (HTTP request to the expected URL, or a TCP check that
  tells "service down behind the tunnel" from "service up"). New endpoint
  `/api/probe`; `/api/status` rows gain `title` and `port_info`.
- Panel text coming from the operator is now HTML-escaped.
- `docs/`: getting started, operations, key rotation and troubleshooting guides;
  `SECURITY.md` and `CONTRIBUTING.md`. Diagnostics sections moved out of the README.
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
- The example server compose file publishes the monitor panel on `127.0.0.1` only.
- `main` now publishes the `edge` image tag; `latest` only moves on releases.
- Home Assistant add-on renamed to **TunnelKit Client**, slug changed from
  `autossh_plus` to `tunnelkit_client` and directory to `tunnelkit-client/`.
  Existing installs must uninstall the old add-on and install the new one;
  options are not carried over.
- Examples and tests use neutral names; all comments are in English.
