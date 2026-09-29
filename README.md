# Cellar Run Control

Cellar Run Control is a local Go HTTP service for coordinating a small
production cellar. It keeps formula approval, vessel availability, active
fermentation runs, gravity observations, conditioning, completion metrics, and
vessel cleaning in one deterministic process. Every successful business write
is recorded in a local append-only audit trail that survives restarts.

The repository is a healthy initialization baseline. It has no intentional
defects. Focused tests cover the audit trail and its HTTP surface; the
production workflow checks remain available for immediate build and behavior
verification.

## Requirements

- Go 1.23 or later
- A writable local data directory
- No database, network service, or third-party module

## Build And Run

Build every package:

```bash
go build ./...
```

Start the service:

```bash
go run ./cmd/fermentctl serve
```

The default address is `127.0.0.1:8088`. The default snapshot directory is
`./data`.

Environment variables:

- `CELLAR_CONTROL_ADDR`: HTTP listen address.
- `CELLAR_CONTROL_DATA_DIR`: directory containing `state.json` and
  `audit.jsonl`.

Example:

```bash
CELLAR_CONTROL_ADDR=127.0.0.1:9099 \
CELLAR_CONTROL_DATA_DIR=/tmp/cellar-control \
go run ./cmd/fermentctl serve
```

## Directory Structure

```text
cmd/fermentctl                 service and workflow-check executable
internal/audit                 append-only JSON-lines audit trail
internal/clock                 time boundary
internal/config                environment configuration
internal/domain                entities, validation, transitions, metrics
internal/httpapi               HTTP routes, JSON decoding, errors
internal/service               use-case orchestration
internal/storage               revisioned snapshot persistence
internal/workflowcheck         real HTTP production checks
```

## Public API

Health:

- `GET /healthz`

Formula workflow:

- `POST /v1/formulas`
- `GET /v1/formulas`
- `GET /v1/formulas/{id}`
- `PUT /v1/formulas/{id}/steps`
- `POST /v1/formulas/{id}/approve`
- `POST /v1/formulas/{id}/retire`

Vessel workflow:

- `POST /v1/vessels`
- `GET /v1/vessels`
- `GET /v1/vessels/{id}`
- `POST /v1/vessels/{id}/finish-cleaning`
- `POST /v1/vessels/{id}/retire`

Run workflow:

- `POST /v1/runs`
- `GET /v1/runs`
- `GET /v1/runs/{id}`
- `POST /v1/runs/{id}/reserve`
- `POST /v1/runs/{id}/start`
- `POST /v1/runs/{id}/observations`
- `POST /v1/runs/{id}/conditioning`
- `POST /v1/runs/{id}/complete`
- `POST /v1/runs/{id}/abort`

Audit (read-only):

- `GET /v1/audit-entries`

The audit endpoint returns entries of successful business writes only. Query
parameters are all optional and combine with AND semantics:

- `target_id`: exact aggregate identifier;
- `target_kind`: `formula`, `vessel`, or `run`;
- `operation`: one of the audit operations below;
- `from`, `to`: inclusive RFC3339 time bounds on the recorded time.

Audit operations are `formula_create`, `formula_set_steps`,
`formula_approve`, `formula_retire`, `vessel_create`,
`vessel_finish_cleaning`, `vessel_retire`, `run_create`, `run_reserve`,
`run_start`, `run_append_observation`, `run_begin_conditioning`,
`run_complete`, and `run_abort`.

Each entry carries a globally contiguous `sequence`, `operation`,
`target_kind`, `target_id`, optional `before_version`, `after_version`,
`recorded_at`, and a `summary` of the resulting aggregate state. Request
payload fields (names, notes, measured values, codes) are never recorded.

Writes accept an optional `X-Expected-Version` header. Errors use a stable
shape:

```json
{
  "error": {
    "code": "revision_conflict",
    "detail": "the aggregate version does not match X-Expected-Version",
    "field": ""
  }
}
```

## Workflow Checks

Run each production workflow check separately:

```bash
go run ./cmd/fermentctl check formula-approval
go run ./cmd/fermentctl check vessel-run-lifecycle
go run ./cmd/fermentctl check observations-completion
```

Each check creates a temporary snapshot directory, starts the real HTTP
handler on a loopback listener, issues JSON requests, verifies success and
failure behavior, and removes its temporary data.

## Persistence And Recovery

The service stores a complete revisioned snapshot at
`<data-directory>/state.json`. A write creates a temporary file in the same
directory, commits the encoded bytes durably, closes the handle, and renames
the result over the previous snapshot. The in-memory snapshot is replaced only
after that operation succeeds.

On startup, an existing snapshot is decoded and normalized. Unknown or
conflicting map indexes are not silently invented; invalid JSON or a negative
revision stops the process before HTTP serving begins.

## Audit Trail

Successful business writes append one JSON object per affected aggregate to
`<data-directory>/audit.jsonl`. Entries are appended while the snapshot write
lock is held immediately after the business commit, so audit sequence order is
the durable business commit order even under concurrent requests. The whole
batch for one operation is written with a single `O_APPEND` write and then
`fsync`ed; a mutex prevents interleaving, and sequence numbers are contiguous
starting at 1.

Audit recording is best effort and never changes the outcome of a business
write: if the trail cannot be appended, the write still returns success and the
failure is logged. A short or failed write fails the audit handle closed to
avoid duplicate or missing sequence numbers; the next process start reconciles
from disk. On startup the trail is read back, validated, and a corrupt file or
a sequence gap stops the process before serving. The query interface is
read-only; entries are never modified or deleted by the service.

## Repair-Ready Boundaries

The baseline intentionally keeps several independent behavior surfaces visible
for later engineering evaluation:

- formula name normalization and duplicate detection;
- target gravity and process-step range validation;
- aggregate version conflict checks;
- atomic vessel reservation and release;
- run state transition guards;
- observation sequence and time ordering;
- gravity movement and range checks;
- metric calculation and rounding;
- durable snapshot replacement and startup normalization.

These are production mechanisms, not injected defects. A later task can add
automated tests or request a focused repair without changing the public API.

## Test Boundary

The audit module ships with focused unit tests (contiguous sequences, filters,
restart recovery, corrupt-trail rejection, concurrent appends) and HTTP-level
tests (end-to-end recording, query validation, sensitive-field exclusion,
restart attribution, and concurrent writes). Run them with:

```bash
go test ./...
```

Broader systematic coverage of validation limits, state transitions, and
numeric edge cases outside the audit surface remains owned by a later test
task.

## Scope

The service does not control physical equipment, authenticate remote users,
send notifications, expose a browser interface, or replicate data across
processes. It is intended for a trusted local network or an internal adapter.
