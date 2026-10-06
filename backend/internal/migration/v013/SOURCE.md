# v0.13.0 conversion boundary

Source release: `v0.13.0`, commit `416b7fc909dda3ed9179bcdbaf4ba47b6b55779d`.
The standalone importer supports that source only. It does not infer a migration
chain, invoke current execution defaulting, or accept partially upgraded JSON.
The package and its fixtures can be removed together when this upgrade path is
retired. Normal service code must never import it.

## Retained historical fields

The source boundary includes known historical fields that v0.13.0 could still
read and ignore. `v0.8.0` stored execution strategy inside each translate round
at `rounds[*].translate.strategy`. Commit
`dd031a5096d3e9e9519b69ed809fe2d9a159bf15` moved strategy to the snapshot root;
the change is included in `v0.9.0` and did not rewrite saved job JSON.
In v0.13.0, `service.GetSnapshot` used ordinary `json.Unmarshal`, which ignored
the old round field, and the worker used only the top-level strategy.

`source_decode.go` preserves that behavior with a translate-only strict decoder.
It accepts the old `strategy` field as an uninterpreted JSON value and discards
it before source conversion. The field's contents never enter the target
snapshot, provenance, or diagnostics. Other unknown fields, including fields
elsewhere in translate settings or the top-level strategy, remain errors;
partially upgraded snapshots are still rejected.

A present top-level strategy retains its saved values, including explicit
zeroes, false flags, empty lists, and sub-options under disabled features.
An absent, null, or empty-object top-level strategy retains the source reader's
zero values. No strategy is recovered from a round, even if several rounds
agree, and no current profile defaults are applied. The existing frozen source
runtime fallbacks still supply missing QA checks and `char_weight` for an empty
QA length method without enabling QA or other strategy features.

## Frozen source material

- `source.go`: type definitions from `backend/internal/service/job.go`,
  `backend/internal/ent/schema/execution_profile_config.go`, and the retry type
  from `backend/internal/ent/schema/execution_plan_template.go`. Names are
  prefixed; YAML tags have no runtime effect. Slice `omitempty` tags are removed
  when transferring source values to the target so explicit empty lists survive.
  No type aliases point to current configuration types.
- `assets/{adjudication,semantic_qa,revise}.tmpl`: corresponding files from
  `backend/internal/templates/default/prompts`. The old loader removed trailing
  newlines; the importer does the same and normalizes checkout CRLF.
- `assets/ruby_{json,text}.tmpl`: the two `sys` raw string literals from
  `backend/internal/pipeline/ruby_restore.go`.
- `assets/retry_reminder.tmpl`: template representation of the exact text emitted
  by `backend/internal/repair/prompt.go:BuildRetryReminder`. Dynamic arguments
  continue to use the target renderer's reason, IDs and truncated previous head.
- Provider defaults in `frozen.go`: v0.13.0 `backend/{openai,anthropic,google}`
  factories and Anthropic thinking-budget calculation. Numeric options must be
  valid nonnegative integers. Duration strings are rejected because v0.13.0
  factories did not interpret them as durations. Empty response format uses the
  old `json_schema` fallback. Unsupported legacy values require repair with the
  source release rather than silently acquiring current semantics.
- QA selections come from v0.13.0 `qa/qa.go`. Explicit empty QA checks disable
  checks; empty adjudication and revision selections use the old handlers'
  defaults. Profile context and Ruby normalization follow the old profile type.
- Database schema fixtures were generated from the release's ent migration
  tables, not by deleting columns from the current schema.

## Explicit target compatibility

The supported target has execution schema **1**, execution defaults **1**,
initialization **1**, encrypted credential format **1**, and SQLite time format
**1**. Entry points check execution and initialization versions; conversion
checks the actual written encrypted rows; SQLite opens through the normal strict
database format check after conversion. Current execution validation is only
used to reject an incompatible result, never to supply source defaults.

`testdata/source-assets.json` pins normalized source template SHA-256 hashes.
`TestFrozenExecutionGolden` pins the entire canonical target snapshot for a
fixture containing all six rounds, Ruby retry, custom prompts and saved zeros.
Changes to target types, serialization, source defaults or assets must receive an
explicit compatibility review and update the fixtures, rather than blindly
regenerating the expected values from the latest resolver.

Backend and historical snapshot secrets intentionally use separate credentials.
An old job keeps its own saved secret and endpoint, even if the current backend
was changed. Missing secrets or uncertain ownership fail the complete migration.
The shared `credentialstore` owns persistence and authenticated encryption; this
package contains no alternate encryption format or authorization service.
