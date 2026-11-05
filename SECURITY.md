# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Use GitHub's private
reporting: the repository's **Security** tab → **Report a vulnerability**. Include
the affected version (image tag), what you can do with it and, if possible, how
to reproduce it. You will get an answer as soon as the maintainer can read it;
this is a small project, so there is no guaranteed response time.

## Supported versions

Only the latest release receives fixes. Pin an exact version in production and
watch the [CHANGELOG](CHANGELOG.md).

## What to keep in mind when deploying

- The SSH port only accepts the keys in `authorized_keys`, and only the ports each
  key lists with `permitlisten`.
- By default a client trusts the server's host key on first use. Set `host_key` on
  every client to pin it.
- The monitor panel has **no authentication**. Keep it on loopback or behind an
  authenticating reverse proxy (see [Operations](docs/operations.md#exposing-the-monitor-panel)).
- Back up and protect `host_key`; it is the server's identity.
- Released images are signed with cosign and carry an SBOM and provenance (see the
  README); verify them before deploying.
