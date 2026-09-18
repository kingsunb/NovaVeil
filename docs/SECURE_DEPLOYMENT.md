# Secure deployment

This document defines the supported Docker security posture for NovaVeil. It is an
operator runbook, not a replacement for application configuration documentation.

## Container identity and data ownership

The image runs as the fixed numeric identity `10001:10001`. The image does not
change ownership recursively at startup. Only `/app/data` is writable; the binary,
licenses, and root filesystem are read-only in the supplied Compose files.

For a host bind mount, prepare a dedicated directory before creating the external
Docker volume:

```bash
sudo install -d -o 10001 -g 10001 -m 0700 /var/lib/novaveil
docker volume create --driver local \
  --opt type=none \
  --opt o=bind \
  --opt device=/var/lib/novaveil \
  novaveil-data
```

Do not use `/tmp` for production data. `/tmp` is volatile, commonly cleaned by the
host, and has broader access semantics than a dedicated `0700` directory.

The image entrypoint sets `umask 0077`. New configuration, SQLite, WAL, and
conversation files therefore default to owner-only access. Confirm host-side
ownership after storage migrations; a wrong UID/GID causes startup to fail rather
than silently weakening permissions.

## Image pinning

The canonical image is `ghcr.io/kingsunb/novaveil-api`. Production Compose requires
`NOVAVEIL_IMAGE`; no default `latest` or `dev` tag is accepted. Docker Hub synchronization
is discontinued and Docker Hub images must be treated as unsupported/stale. Migrate by
changing only the image reference to a reviewed GHCR version/digest while retaining the
same `/app/data` volume.

> No versioned tag has been published yet: pushes to `main` auto-publish
> `ghcr.io/kingsunb/novaveil:latest` and `:sha-<short>`; the `novaveil-api` versioned
> image is produced by the manual `release` / `build` workflows. Pin the reviewed digest
> rather than a version string until a tag is released.

Prefer a digest-qualified reference:

```bash
export NOVAVEIL_IMAGE='ghcr.io/kingsunb/novaveil-api@sha256:<manifest-digest>'
docker compose pull
docker compose up -d
```

A version tag without a digest is easier to operate but can be republished. Record
the resolved digest in the change ticket before deployment:

```bash
docker buildx imagetools inspect ghcr.io/kingsunb/novaveil-api:<version>
```

The runtime base is pinned to the Alpine 3.21.7 multi-platform OCI index digest
`sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d`.
Every platform build therefore resolves through the reviewed immutable index rather
than a mutable tag. Release binaries use Go 1.26.7 or newer patch releases so the
image vulnerability gate does not ship known fixed standard-library CVEs.

## Runtime restrictions

The supplied Compose files enforce:

- fixed non-root UID/GID `10001:10001`;
- read-only root filesystem;
- writable `/app/data` volume only;
- bounded, `noexec,nosuid,nodev` `/tmp` tmpfs;
- `cap_drop: [ALL]` and `no-new-privileges:true`;
- CPU, memory, and PID limits;
- bounded `json-file` log rotation;
- a 90-second container stop grace period for the sequential HTTP/task/conversation/stat/log/database shutdown hooks;
- an HTTP healthcheck using the image's BusyBox `wget`.

The healthcheck calls `/`, the existing embedded frontend root. A dedicated
`/healthz` endpoint is not present in the current backend. If one is added later,
it should be unauthenticated, side-effect free, and should replace `/` in both the
Dockerfile and Compose healthchecks.

## Network and HTTPS

The production Compose template binds `127.0.0.1:8888:8080` by default, so only a
reverse proxy on the same host can reach the backend. Set `NOVAVEIL_BIND_ADDRESS`
explicitly only when another trusted host must connect, and protect that port with
host firewall rules. The two supplied Compose files name this host-bind variable
differently: the production template uses `NOVAVEIL_BIND_ADDRESS`, while
`docker-compose.local.yml` uses `NOVAVEIL_BIND`.

Terminate TLS at a reverse proxy, preserve the original `Host` header, and set the
application cookie flag when TLS terminates at the proxy:

```bash
export NOVAVEIL_SECURITY_COOKIE_SECURE=true
```

Keep `server.host=0.0.0.0` inside the container; restrict exposure at the host,
reverse proxy, and firewall. Same-origin checks support HTTPS termination when the
proxy preserves `Host`, without trusting `X-Forwarded-*`. The server intentionally
trusts no proxy source by default, so IP-based rate limiting behind a proxy still
requires an explicit trusted-proxy configuration in application code.

## Updates

Docker deployments must upgrade by changing `NOVAVEIL_IMAGE` to a reviewed version
or digest and recreating the container. The supplied read-only root filesystem and
non-root user prevent the process from replacing `/app/novaveil` in place.

For non-Docker installations, download the archive and `SHA256SUMS` from the same
GitHub Release, then verify before extracting or replacing any executable:

```bash
scripts/verify-release-archive.sh novaveil-linux-amd64.zip SHA256SUMS
```

SHA-256 detects download corruption and a mismatched release asset, but it does not
authenticate the publisher when both files are replaced by an attacker. Release
asset signing/Sigstore verification is not implemented; this is a remaining risk.
The built-in updater now requires and verifies the same Release `SHA256SUMS`; Docker
images disable in-container self-update and must be upgraded by replacing the pinned image.

## Secrets and logs

The first-run admin password is written once to `/app/data/initial-admin-password`
with mode `0600`; it is never written to normal logs. Read it from the protected data
volume, change it immediately, and confirm the bootstrap file is removed after the
password change. The Compose log driver limits disk usage but does not encrypt logs.

Application export files contain channel and API keys in plaintext. Conversation
retention, when enabled, stores request content below `/app/data/conversations`.
Treat both as production secrets, encrypt backups at rest, and restrict access to the
fixed container identity and backup operator only.

Release archives and images include `THIRD_PARTY_LICENSES.csv` (Go) and
`THIRD_PARTY_LICENSES.frontend.json` (frontend production dependencies). The Go
inventory has one fail-closed generation path based on `go list` module metadata;
missing files, unknown/custom classifications, insecure URLs, or duplicate rows fail
the build. Each exact per-platform Docker archive is vulnerability-scanned, converted
to a CycloneDX SBOM, and runtime-smoke-tested before it can enter a publish artifact.
The publish jobs load and push only those checksum-verified archives and never rebuild
them. The frontend license JSON remains because the tested CycloneDX npm generator
could not correctly resolve this repository's pnpm virtual store.
