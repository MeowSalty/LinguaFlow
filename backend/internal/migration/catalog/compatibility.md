# Migration compatibility register

Baseline rechecked 2026-10-24: v0.16.0, commit e2284dea6ccbc69513e7b38aa0755d19271e5af7,
released 2026-10-23 22:16:55 +08:00. The upgrade source remains v0.15.0,
commit 6b264ad5d8b88295c397204eacaa0838cfc97a69. Auth1/data3 and the unified
operation version 1 are published source contracts; dedicated migration binary
assets are still absent. Review checkpoints never authorize automatic deletion
or removal of source data, objects or keys.

| COMPAT ID | First published source / contract | Review | Removal condition | Retained recovery |
| --- | --- | --- | --- | --- |
| migration-v013-route | v0.14 tool tree, v0.13 source and switch journals | v0.16 checked: archive unverified; next strategy release | Minimum directly supported source documented; exact archive available and interrupted recovery verified, or support explicitly ended with recovery obligations resolved | Fixed v0.14 tool tree 4bc4577cd669d9ccbbc4980f19e259b5beff2b18 and exact recovery guide |
| migration-storage-import | v0.14/v0.15 storage_path, manifest=1, legacy_migration | Minimum source/tool format change | All promised sources no longer need import or frozen verified importer covers them; interrupted operations remain recoverable | Domain generation checks, legacy references and manifest recovery |
| migration-command-names | v0.14/v0.15 old source binary and Task names | v0.16 names removed; next tool binary publication | Unified commands and documentation exist, repository scripts updated, release note records name removal | History/journal formats are independent of executable names |
| migration-v015-auth1 | v0.15 source; converter source first released in v0.16 | Next support-window review | v0.15 leaves promised support or verified fixed replacement is available | Pre-write rejection, target barrier and original backup restore |
| migration-journal-readers | v0.14/v0.15 manifests, v013 journals and v0.16 operation/journal=1 | Before strategy or journal/tool format change | Supported incomplete operations converged or exact verified tool can recover | Read-only recognition and safe instructions |
| storage-legacy-runtime | Published legacy locations, unknown source, historical jobs/backups | Before removing legacy driver/fields/enums | Live/history/delivery/pin/backup/deletion references proven absent and supported backups recoverable | Current generations, ciphertext and source histories |
| storage-backup-manifest-v1 | v0.14/v0.15 storagebackup.Manifest version 1, backup records and BackupPin | B0/v0.16 checked: restore partly unverified; next strategy or backup-format review | Promised version 1 recovery obligations resolved or a verified replacement covers them; required object/key/pin references no longer need the reader | Manifest identity/expiry semantics, reference/key checks, exact object versions and executable recovery instructions |

Historical policy: archived_unverified. No complete v0.13/v0.14 upgrade chain is
promised. Current v013 conversion remains rejected until archive and recovery
requirements permit retiring its body. Git history alone is not a usable binary.
General instance backup belongs to long-term maintenance. Its creation capability
may ship after the migration boundary split; migration snapshots and published
backup recovery obligations remain required. B0 is implemented: the migration
CLI, Task and catalog no longer expose general backup creation, and no replacement
creation command is claimed as available.

The implementation checkpoint retains the following concrete artifacts:

| COMPAT ID | 2026-10-22 disposition | Retained path or next evidence |
| --- | --- | --- |
| migration-v013-route | archived_unverified; body and refusal retained | `../upgrade/v013/SOURCE.md`, source, journals and golden fixtures; obtain the exact runnable archive and verify interrupted restore before body removal |
| migration-storage-import | maintained; SQLite serve/local accepted for local objects | `../storage/manifest.go`, deterministic resource receipts and constrained rollback; PostgreSQL acceptance remains unverified |
| migration-command-names | old binary, packages and Task names removed in this implementation | `../../../cmd/linguaflow-migrate/main.go` and unified `backend:migrate:*` Tasks; downloadable artifact and release workflow handed off separately |
| migration-v015-auth1 | maintained; SQLite serve/local independently accepted | `../upgrade/v015/source/SOURCE.md`, fixed release schema/DTO and comparison fixtures; PostgreSQL live dump/restore remains unverified |
| migration-journal-readers | maintained; new operation/journal version 1 and explicit manifest 1 takeover | `../operation/plan.go`, `../storage/manifest.go`, startup barrier and frozen v013 readers; changing formats never implies accepting unrelated incomplete operations |
| storage-legacy-runtime | retained; no live/history/pin reference removal authorized | `../../../internal/api/storage_setup.go` legacy driver and domain retention paths; prove all enumerated references absent before runtime removal |
| storage-backup-manifest-v1 | B0 boundary implemented 2026-10-23; recovery handoff remains partly unverified | `../../storagebackup/backup.go`, `../../storagebackup/offline.go` and domain pin consumers; record exact runnable recovery steps/artifacts and verified DB/provider coverage before claiming handoff complete |

The unified source contract shipped in v0.16.0. Dedicated tool binary assets and
verified historical recovery archives are still unavailable; source publication
never proves that an exact recovery executable or a new database/provider cell
has been validated. The table above records historical implementation evidence.

## COMPAT(storage-backup-manifest-v1)

The published backup manifest is distinct from the storage-import manifest and
the former unpublished migration/backup recovery.json removed before v0.16.
Preserve version 1 status, identity, expiry, object/location/provider-version and
required-key semantics;
do not relabel an old complete manifest as proof of an independent recovery set.

B0 removes the unpublished general backup creation commands, dedicated
migration/backup orchestration and backup-current catalog entry. No replacement
creation command, scheduler or UI is required in that phase. It retains migration
source snapshots, storage reference/key checks and unexpired BackupPin consumers.
Removing a command never authorizes deleting backup records, changing pinned
locations, expiring protection early or removing necessary keys.

Supported old backups still require executable read/check and verified restore
instructions. The current source keeps the version 1 reader/check implementation
and its tests in `internal/storagebackup`; it does not expose a new maintenance
command. Restore backups requiring an exact old model in an isolated
matching-version environment, then use the supported upgrade route. Record the
actual tools, hashes, recovery steps and verified DB/provider cells; missing
runnable recovery keeps the cell unverified.
The historical v0.14/v0.15 releases have no dedicated migration binary asset.

B0 shipped in v0.16.0. Review at the first strategy optimization release and any
later backup-format/minimum-restore-version change. Remove this reader only after
promised recovery obligations are resolved or a tested replacement covers them
and runtime references no longer require it;
a date, expired pin or locally completed migration alone is not sufficient.

The full scope and B0/B1/B2 acceptance plan are in the
[instance backup design](../../../docs/design/instance-backup.md). Current
CLI/route status is recorded in [README.md](README.md) and acceptance.json;
the general backup route was removed in B0 and requires its own later acceptance.

## Strategy implementation (2026-10-24)

The [implemented design](../../../docs/design/migration-tooling.md) adds default
inplace upgrades, optional clone targets, and explicit PostgreSQL external backup
declarations for new version 2 operations. The business target remains Auth1/data3
and the startup barrier remains version 1. New nullable baseline_digest and
result_digest step fields bind v2 receipts without changing the frozen v1 DTO or
its field order and digest algorithm. Acceptance is recorded per strategy and
protection mode, independently of historical clone results.

Version 1 upgrade always means clone, and version 1 storage retains its original
import and protection contract. Preserve the frozen DTO, digest, target and exact
build constraints. Read-only status may identify an old operation and direct the
operator to its matching original executable; it must not rewrite the old plan,
accept a new backup declaration for an incomplete operation, or claim that a new
binary can resume it. The exact preserved v0.16 original binary has now passed
interrupted clone/status/resume/verify acceptance on Windows SQLite. The genuine
unresumed fixture and original hashes remain under backend/bin/legacy-v016; other
historical executable recovery remains unverified.

Existing target arguments continue to select clone when no strategy is supplied.
The new default applies only when creating a new operation without a target.
Operator-declared protection is reported separately from captured or restore-
verified material; published backup-manifest complete semantics remain unchanged.
New inplace support requires its own SQLite/PostgreSQL failure and restore checks,
while live/history/BackupPin/key recovery obligations continue to apply.
