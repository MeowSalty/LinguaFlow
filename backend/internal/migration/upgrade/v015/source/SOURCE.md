# Frozen v0.15.0 source

Release: v0.15.0. Peeled commit: `6b264ad5d8b88295c397204eacaa0838cfc97a69`.
`schema.go` is copied from that commit's `backend/internal/ent/migrate/schema.go`;
only checkout newlines are normalized. Dependencies: ent v0.14.6, modernc SQLite
v1.59.0. It must never alias the current ORM tables or be regenerated from HEAD.

Source initialization version is 1, data_version is 2, SQLite time user_version
is 1, and the account format marker is absent. Original user IDs, hashes,
credentials, ownership and exact storage references are preservation contracts.
Tests create source fixtures with these release tables in isolated databases.

This source is retired only under COMPAT(migration-v015-auth1), recorded in
`internal/migration/catalog/compatibility.md`. A support-window review is not
automatic deletion authorization.
