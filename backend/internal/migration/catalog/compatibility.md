# Migration compatibility register

Baseline rechecked 2026-10-23: v0.15.0, commit 6b264ad5d8b88295c397204eacaa0838cfc97a69.
The initial target release number is not assigned. Review checkpoints never
authorize automatic deletion or removal of source data, objects or keys.

| COMPAT ID | First published source / contract | Review | Removal condition | Retained recovery |
| --- | --- | --- | --- | --- |
| migration-v013-route | v0.14 tool tree, v0.13 source and switch journals | First unified-tool release | Minimum directly supported source documented; exact archive available and interrupted recovery verified, or support explicitly ended with recovery obligations resolved | Fixed v0.14 tool tree 4bc4577cd669d9ccbbc4980f19e259b5beff2b18 and exact recovery guide |
| migration-storage-import | v0.14/v0.15 storage_path, manifest=1, legacy_migration | Minimum source/tool format change | All promised sources no longer need import or frozen verified importer covers them; interrupted operations remain recoverable | Domain generation checks, legacy references and manifest recovery |
| migration-command-names | v0.14/v0.15 old source binary and Task names | First unified-tool release | Unified commands and documentation exist, repository scripts updated, release note records name removal | History/journal formats are independent of executable names |
| migration-v015-auth1 | v0.15 source; converter first release unassigned | First support-window review after release | v0.15 leaves promised support or verified fixed replacement is available | Pre-write rejection, target barrier and original backup restore |
| migration-journal-readers | v0.14/v0.15 manifests and v013 journals; new operation version=1 | Before journal/tool format change | Supported incomplete operations converged or exact verified tool can recover | Read-only recognition and safe instructions |
| storage-legacy-runtime | Published legacy locations, unknown source, historical jobs/backups | Before removing legacy driver/fields/enums | Live/history/delivery/pin/backup/deletion references proven absent and supported backups recoverable | Current generations, ciphertext and source histories |
| storage-backup-manifest-v1 | v0.14/v0.15 storagebackup.Manifest version 1, backup records and BackupPin | First B0 split release; then backup format or minimum restore version changes | Promised version 1 recovery obligations resolved or a verified replacement covers them; required object/key/pin references no longer need the reader | Manifest identity/expiry semantics, reference/key checks, exact object versions and executable recovery instructions |

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

The first official unified-tool release and archive publication are unassigned.
These implementation dispositions do not claim that a release has been shipped.

## COMPAT(storage-backup-manifest-v1)

The published backup manifest is distinct from the storage-import manifest and
the new unpublished migration/backup recovery.json. Preserve version 1 status,
identity, expiry, object/location/provider-version and required-key semantics;
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

Review at the first B0 release and any later backup-format/minimum-restore-version
change. Remove this reader only after promised recovery obligations are resolved
or a tested replacement covers them and runtime references no longer require it;
a date, expired pin or locally completed migration alone is not sufficient.

The full scope and B0/B1/B2 acceptance plan are in the
[instance backup design](../../../docs/design/instance-backup.md). Current
CLI/route status is recorded in [README.md](README.md) and acceptance.json;
the general backup route was removed in B0 and requires its own later acceptance.
