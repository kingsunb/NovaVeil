# Backup and restore

NovaVeil has two distinct backup mechanisms. Choose based on the recovery objective;
the application export is not a full disaster-recovery snapshot.

## Application export

`POST /api/v1/setting/export` returns a JSON file containing channels, groups,
channel-model mappings, group items, API keys, usage buckets, client statistics, and
non-secret settings. Channel keys and API keys are plaintext, even though the live
database stores them as `nv1:` ciphertext. Proxy URLs, custom header values, and header
template values are replaced with `****` and are not restored from this file.

It does **not** include users/password hashes, the JWT signing secret, global
`proxy_url` / `proxy_pool`, login-attempt counters, error logs, or conversation
archives. Import is incremental: existing rows may remain and some rows are upserted
rather than replacing the entire database. Channel keys and API keys in this file are
plaintext on purpose, so a restore can call upstreams again. A key whose value is
exactly `****` is rejected. A proxy, custom header, or header template whose value is
exactly `****` is omitted and does not overwrite a live secret.

Channel-page text export is a separate file. `POST /api/v1/channel/export` writes every channel in the cache, including builtin channels and channels with no key. Each block is `# name`, the plaintext base URL, then one plaintext key per line. A channel with no key is still present, with only the name and URL. Models, type, group, and enabled state are not in that file. Importing it creates custom channels; builtin identity is not preserved. See [REQ-003](FEATURES.md).

Use the authenticated Web UI export/import controls when available. The equivalent
HTTP endpoints are:

```text
POST /api/v1/setting/export
POST /api/v1/setting/import
```

The POST endpoint accepts the exported JSON body or a multipart field named `file`.
Store exports encrypted and with mode `0600`; they are credentials, not ordinary
configuration files. The plaintext keys in exports are an intentional, accepted
design decision — they will not be changed: an encrypted export would defeat the
offline-restore purpose, and the export endpoint already enforces POST + NoStore +
an audit-log IP warning. The security boundary is the operator's handling of the
export file, not the export format.

## Full SQLite disaster-recovery backup

For the default SQLite deployment, back up the entire `/app/data` directory. This
includes `data.db`, any `data.db-wal`/`data.db-shm`, `config.json`, and optional
conversation archives. Do not copy only `data.db` while the service is writing; that
can produce an inconsistent snapshot.

A safe operational sequence is:

1. Put the service into a maintenance window and stop writes at the reverse proxy.
2. Create a database-consistent SQLite backup using the SQLite backup API/tool, or
   stop the application cleanly and wait for it to exit.
3. Copy the complete data directory into encrypted backup storage.
4. Record the deployed image digest, NovaVeil version, backup timestamp, checksum,
   and file owner (`10001:10001`).
5. Restart only after the backup checksum and required files are verified.

Do not use `kill -9`; normal termination flushes in-memory queues and database state.
Do not place the backup under `/tmp`.

For MySQL or PostgreSQL, use the database vendor's transaction-consistent backup
mechanism and separately back up `/app/data/config.json` plus conversation archives.
The application JSON export can be retained as a portable configuration recovery aid,
but not as the only backup.

## Restore into a new deployment

1. Select and pin the exact image version or digest that produced the backup.
2. Create an empty host directory owned by `10001:10001` with mode `0700`.
3. Keep the service stopped while restoring a full SQLite data directory.
4. Restore all SQLite files from the same snapshot together; do not mix DB, WAL, and
   SHM files from different timestamps.
5. Validate owner/mode, then start the service and wait for the Compose healthcheck.
6. Log in, change/verify the admin password, and verify channels, groups, API keys,
   settings, and a test relay request.
7. For an application JSON import instead of full restore, first create a fresh
   instance/admin account, then import the JSON through the authenticated endpoint.
   Users and historical operational data must be recreated or restored separately.

## Recovery test

At least quarterly, restore the latest backup into an isolated host or project. Do
not attach the test instance to production upstream credentials unless egress is
blocked. Verify:

- the image digest and application version match the backup record;
- the service becomes healthy without permission changes;
- login and required forced-password flow work;
- expected channel/group/API-key counts match the backup inventory;
- the SQLite integrity check or vendor database check succeeds;
- a newly generated export can be parsed and stored with owner-only permissions.

A backup is not considered valid until this restore test passes.
