# Contributing

## Build and test

Everything is Go (standard library, plus `golang.org/x/crypto` in the server).
Without a local Go toolchain, use Docker:

```bash
cd core
docker run --rm -v "$PWD":/src -w /src golang:1.23 sh -c \
  'gofmt -l . && go vet ./... && go test -race ./...'
```

`gofmt -l .` must print nothing. CI runs `go vet` and `go test -race` on every pull
request and builds the images and the OpenWrt binaries without publishing them.

## Pull requests

- Branch from `main` and open a PR against `main`.
- Keep code, comments, docs and UI text in English; use neutral names in examples
  and tests (`example.com`, `tk-site-web`), not names from a real deployment.
- Add or update tests for behaviour changes, and update the docs and the
  [CHANGELOG](CHANGELOG.md) under `[Unreleased]`.
- Packaging that talks to a real SSH implementation (client options, known_hosts,
  dbclient/ssh arguments) should be checked against a real server with both
  OpenSSH and Dropbear, not only with unit tests.

## Releases

A release is a tag `vX.Y.Z` on `main`. Before tagging, set the same version in
`tunnelkit-client/config.yaml` (`version`) and `openwrt/Makefile` (`PKG_VERSION`):
Home Assistant pulls `tunnelkit-client:<version>` and CI fails if the three
differ. Move the `[Unreleased]` entries of the CHANGELOG under the new version.
