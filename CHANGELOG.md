# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-11-21

First public release.

- Reverse-SSH tunnel server and client (Go), with per-client port forwarding driven by `authorized_keys`.
- Web panel with live status, traffic history, speedtest, availability, sortable columns and name search.
- Host-based reverse proxy to client ports, taken from the URLs in `clients.json`.
- Prometheus `/metrics` endpoint and a Grafana dashboard.
- Optional UDP heartbeat, host key pinning, and Home Assistant add-on and OpenWrt packages.
- Signed multi-arch images (cosign keyless) and release assets.
