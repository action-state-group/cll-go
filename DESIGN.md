# capsule-ledger-go design

Status: implementation contract for the first release. This document separates
verified upstream behavior from choices made by this repository.

## Purpose and ownership

`capsule-ledger-go` is an embeddable Go library containing an append-only AAC
format-4 Capsule ledger and a Checkpointed Local Log (CLL). The CLL commits the
ordered Capsule stream to an MMR, signs checkpoints, and optionally pushes them
to external witnesses.

Alchemy owns its investigation workflow, GitHub publication, application
profile, MySQL outbox, retries, and signer authorization.
`capsule-producer-go` constructs Capsules and Producer Envelopes. This library
verifies and stores their output. `capsule-anchor` is an external Transparency
Service. It receives signed checkpoint statements; it does not pull records or
read Alchemy's outbox.

This is Ethan Zhang's Go implementation, aligned with Steven Mih's Slack
direction that CLL belongs with the ledger because it is the ledger's MMR and
hardened checkpointing layer. This does not assign repository ownership to
Action State Group.

## Source baseline

The design was checked against these refreshed `origin/main` commits on
2026-08-25:

| Repository | Commit | Use |
|---|---|---|
| `action-state-group/agent-action-capsule` | `7dcef86634355c0d3335b3050b1bc18845716275` | AAC format-4 verifier, JCS Capsule ID, Producer Envelope verifier, vectors |
| `ethanyzhang/capsule-producer-go` | `cb9de82a792c387e64f39f719221511b3fa27b49` | producer/ledger boundary and Go version |
| `action-state-group/capsule-emit` | `d0ff0d725d8683a9e8f63c4980fc5fe3d7c5d443` | CLL hashing, cadence, retry, and trust behavior |
| `action-state-group/capsule-ledger` | `01f2ae6c4e7d54793ed308a624c367e702a8d089` | sequence, chain-gap, and checkpoint persistence behavior |
| `action-state-group/capsule-anchor` | `452d253c69bc9eb8ddb780347377c2115e6aa166` | current REST and receipt contract |
| `action-state-group/capsule-anchor` v0.1.1 | `e6ba56e88a9199d09f111061edce357b120a472c` | older hosted response shape and exact-statement entry hash |
| `datatrails/go-datatrails-merklelog` | `275103a34a08e56a376107246e04dc5819cc44cc` | MMRIVER-compatible Go reference and KATs |
| `action-state-group/scitt-cose` | `36ec13992287a463481d1b00ac2098f032f229b5` | independent Go COSE/receipt verifier and conformance vectors |

The Alchemy mapping `urn:alchemy:aac:investigation-publication:v1` is a separate
private application profile. The ledger does not interpret it.

## Invariants

- A Capsule is immutable and identified by its verified lowercase 64-hex
  `capsule_id`.
- Re-appending the same ID with identical bytes is idempotent. The same ID with
  different bytes is a conflict.
- A successful new-Capsule append allocates one gapless, 1-based sequence.
  Sequence is local ordering metadata, not part of the Capsule ID.
- A Capsule has zero or more immutable Producer Envelopes. They are
  independently verified and deduplicated by SHA-256 of their exact bytes.
- A verified envelope public key is evidence, not application authorization.
- Adding an envelope allocates no Capsule sequence and adds no CLL leaf.
- Initial Capsule plus initial envelopes is one atomic operation.
- CLL reads only `LogSource`; it knows nothing about Alchemy's outbox or a
  concrete database.
- JSONL, SQLite, and MySQL expose the same success, idempotency, conflict,
  ordering, and read-back semantics.
- Network success is not witness success. A receipt is verified offline under
  a caller-pinned authority key.
- Importing the library starts no goroutine and performs no network call. The
  host explicitly runs the checkpoint runner and owns its context.

## Public model and application API

```go
type CapsuleID string
type EnvelopeDigest string

type Record struct {
    Seq          uint64
    CapsuleID    CapsuleID
    Capsule      []byte
    Envelopes    []Envelope
    Verification VerificationResult
    ParentID     CapsuleID
    AppendedAt   time.Time
}

type Envelope struct {
    Digest       EnvelopeDigest
    Bytes        []byte
    Verification EnvelopeVerification
    AddedAt      time.Time
}

type Ledger interface {
    Append(ctx context.Context, capsule []byte, envelopes ...[]byte) (Record, error)
    AddEnvelope(ctx context.Context, id CapsuleID, envelope []byte) (Envelope, error)
    Get(ctx context.Context, id CapsuleID) (Record, error)
    Scan(ctx context.Context, after uint64, limit int) ([]Record, error)
    FindChainGaps(ctx context.Context) ([]ChainGap, error)
}

type Auditor interface {
    Audit(ctx context.Context, maxRecords int) ([]RecordVerification, error)
}
```

Returned byte slices are defensive copies. `Append` verifies the Capsule and
all provided envelopes before the durable write. Duplicate input returns the
stored record after exact-byte comparison of the Capsule and every supplied
Envelope. Envelopes added after the original append may be present in that
stored result and do not make an older append retry conflict. `Get` requires an
exact ID; prefix lookup is deliberately absent from the integrity API.

`VerificationResult` preserves the upstream `OK`, structured findings,
severity, numbered check, derived assurance, and recomputed Capsule ID.
Envelope results preserve findings and raw signer public key. Informational
findings remain visible and non-gating.

## Durable store and CLL boundaries

```go
type Store interface {
    CheckpointStore
    Append(ctx context.Context, in AppendInput) (Record, AppendOutcome, error)
    AddEnvelope(ctx context.Context, in EnvelopeInput) (Envelope, AddOutcome, error)
    Get(ctx context.Context, id CapsuleID) (Record, error)
    Scan(ctx context.Context, after uint64, limit int) ([]Record, error)
    FindChainGaps(ctx context.Context) ([]ChainGap, error)
    Close() error
}

type Rebaseliner interface {
    Rebaseline(ctx context.Context, in RebaselineInput) (RebaselineRecord, error)
    Rebaselines(ctx context.Context, limit int) ([]RebaselineRecord, error)
}

type LogSource interface {
    ScanIDs(ctx context.Context, afterSeq uint64, limit int) ([]LogEntry, error)
}

type CheckpointStore interface {
    LogSource
    CLLStore
}

type CLLStore interface {
    LoadCLL(ctx context.Context) (CLLState, error)
    CommitCLL(ctx context.Context, mutation CLLMutation) error
    PendingWitnesses(ctx context.Context, witnessID string, limit int) ([]PendingWitness, error)
    GetWitness(ctx context.Context, witnessID string, mmrSize uint64) (PendingWitness, error)
    CommitWitness(ctx context.Context, result WitnessResult) error
}

type LogEntry struct {
    Seq        uint64
    CapsuleID  CapsuleID
    AppendedAt time.Time
}
```

Each store instance is bound to one non-empty `logID` at construction. Methods
therefore do not repeat `logID`; any CLL input carrying another log is invalid.
Relational stores retain `log_id` columns so multiple single-log instances can
share tables safely. The store uses repository types, not SQL handles or file
offsets. Outcomes distinguish inserted and idempotent results. Typed errors
distinguish not-found, invalid input, exact-byte conflict, corruption,
closed stores, and retryable contention.

`CommitCLL` atomically persists new MMR nodes, the indexed Capsule cursor, the
first-uncheckpointed timestamp, an optional new checkpoint, and one pending row
per configured witness. This prevents a durable checkpoint from becoming
separated from retry obligations. `CLLMutation.ExpectedIndexedSeq` is a
compare-and-swap guard. A stale writer receives `ErrConflict`, reloads
`CLLState`, and recomputes instead of treating a second mutation as a blind
idempotent write.
The `LogSource` implementation uses the narrow `Store.ScanIDs` projection so it
never loads Capsule or envelope bodies. The checkpoint runner rejects a batch
whose first sequence is not `afterSeq+1` or whose entries are not contiguous.

```go
type Signer interface {
    KeyID() string
    SignCheckpoint(ctx context.Context, payload []byte) ([]byte, error)
    VerifyCheckpoint(payload, statement []byte) error
}

// capsuleanchor.Submitter
type Submitter interface {
    Submit(ctx context.Context, signedStatement []byte) (Receipt, error)
}

// capsuleanchor.Verifier; ReceiptVerifier is its pinned-key implementation.
type Verifier interface {
    Verify(statement []byte, receipt Receipt) error
}
```

The wire signer emits CBOR tag 18 with exactly four COSE_Sign1 elements. Its
protected map contains algorithm label `1` with EdDSA value `-8`, its
unprotected map is empty, its attached payload is the exact checkpoint JSON,
and its signature covers the RFC 9052 `Sig_structure` with empty external AAD.
Private key loading and authorization remain outside the library; a helper
accepts an already-held Ed25519 key. The helper verifies its own output before
the checkpoint is committed. On restart, the runner also verifies every stored
checkpoint signature, canonical payload, predecessor, historical MMR root, and
cursor-to-leaf-count relationship before extending the log.

## AAC format-4 verification

The mandatory ledger verifier calls the upstream Go functions at the recorded
baseline:

- `verify.Verify(parsedCapsule, completeStoreOrNil, registries)`; and
- `envelope.Verify(capsuleID, exactEnvelopeBytes)`.

Capsule bytes are decoded only through upstream `verify.DecodeCapsuleJSON`,
which preserves integers as `json.Number`. Plain `json.Unmarshal` is forbidden
because it produces `float64`, breaks JCS recomputation, and bypasses upstream
float and unsafe-integer guards.

The library vendors the baseline `spec/REGISTRY.md` with BSD-3-Clause
attribution and embeds its parsed map as Go data. A test loads the vendored file
with upstream `registries.Load` and asserts exact equality with the embedded map
at the pinned commit. Runtime always passes a non-nil complete map and never
relies on the host working directory or `AAC_REGISTRY_PATH`. The public ledger
constructor always installs this verifier and accepts only caller registry
extensions, which it defensively copies and merges into the baseline. The host
cannot replace AAC format-4 verification. An informational unknown registry
value never becomes an append failure.

Append performs Class 1 per-Capsule verification with caller-configurable
registry extensions. Chain existence changes as later records arrive, so
`FindChainGaps` and `Audit`, backed by `verify.VerifyStore`, provide store-level
results. `Audit` returns `RecordVerification{Seq, CapsuleID, Result, Error}` so
a result remains attributable when recomputation fails. It is operator-only and
requires a positive `maxRecords`; it fails before decoding if the ledger
exceeds that bound.

The verifier recomputes the Capsule ID. Missing, malformed, mismatched, or
error-gated records are rejected. Exact input bytes are retained and never
repaired by re-marshalling. Envelope bytes are also retained exactly.

Conformance tests keep attributed frozen cases from the upstream Capsule corpus
and all eight Producer Envelope cases at the recorded commit. CI never fetches
mutable test data from the network. Single-Capsule cases exercise format-4 JCS,
tampering, canonicalization declarations, float, and unsafe-integer behavior.
Ledger-shaped cases run through `VerifyStore`, including missing-parent and
concurrent-supersedes findings.

## CLL and MMR

The CLL commits only ordered Capsule IDs, preserving current Python behavior:

```text
leaf = SHA256(0x00 || bytes.fromhex(capsule_id))
parent = SHA256(be64(parent_position + 1) || left || right)
root = bag peaks right-to-left with SHA256(right || left)
```

`mmr_size` counts MMR nodes, not Capsules and not ledger sequences. Envelope
associations are not covered by this MMR. If that is later required, it gets a
separately versioned association-event log rather than a changed leaf preimage.

The implementation uses DataTrails' bagged API only: `AddHashedLeaf`,
`GetRoot`, `InclusionProofBagged`, `VerifyInclusionBagged`, and the bagged
consistency functions. Peak-accumulator proof APIs are not interoperable with
the Python root and are forbidden. This repository applies the leaf prefix
before `AddHashedLeaf` and defines the empty root as 32 zero bytes. Because the
upstream bagged API is marked as potentially removable, the pinned dependency
and vectors are a release gate; replacement requires identical KAT results.
This repository owns strict boundary checks
and stable proof DTOs. Inclusion and consistency verification must not panic on
malformed sizes, indices, hash lengths, or proof shapes.

`find gaps` means a Capsule's `chain.parent_capsule_id` is absent. It does not
search for missing numeric sequences because successful storage cannot commit
a sequence gap.

## Checkpoints and runner

The anchor-facing payload is a canonical JSON superset of the ratified CLL
record and the anchor recognition fields:

```json
{
  "artifact_type": "mmr-checkpoint",
  "key_id": "<stable signer id>",
  "kind": "mmr_checkpoint",
  "log_id": "<non-empty configured id>",
  "mmr_root": "<64 lowercase hex>",
  "mmr_size": 250,
  "prev_root": "<64 lowercase hex or empty for first>",
  "prev_size": 100,
  "root": "<same value as mmr_root>",
  "timestamp": "2026-08-25T00:00:00Z",
  "v": 1
}
```

`v`, `kind`, `root`, and `prev_root` preserve the Python record shape;
`artifact_type` and `mmr_root` activate current anchor recognition. Duplicate
root fields must match. The canonical ratified signing body and digest remain a
separate persisted projection for Python tooling. The Ed25519 COSE signature
covers the complete superset payload and is not claimed to interoperate with
the Python HMAC helper.

The local record also stores exact payload bytes, exact COSE bytes, creation
time, and witness states. Before signing, the runner recomputes the
previous root, requires the same log, requires increasing MMR size, and binds
`prev_size` and local `prev_root` to the prior checkpoint.

Defaults are 100 newly indexed Capsules or 15 minutes since the first
uncheckpointed Capsule, whichever comes first. Empty and unchanged logs are
silent. The first-uncheckpointed time is durable, derived from
`LogEntry.AppendedAt` and persisted in `CLLState`, so restart cannot reset the
age leg. A lag exceeding 200 Capsules is exposed as overdue health, not a
reason to discard records. Values are configurable, positive, and maximum lag
must be at least the entry cadence.

The host calls `Runner.Run(ctx)` and may call `Runner.Notify()` after append.
Notifications coalesce and are only a latency optimization. Each wake scans
after the durable indexed sequence. A timer enforces the age leg even when
Alchemy has no later request traffic.

### Rebaseline

An anchor 409 requires an explicit operator rebaseline. The store rejects the
operation unless a durable `continuity_conflict` witness outcome exists for the
active log. `Rebaseline` atomically:

1. freezes the old log scope as read-only;
2. preserves Capsule sequences, IDs, envelopes, MMR nodes, and size;
3. binds the live store to a new, previously unused `log_id`; and
4. records the old and new IDs, last witnessed size, reason, actor-supplied
   time, and migration ID.

Checkpoint and witness-delivery history remains under the old log ID. The new
scope has no prior checkpoint and forces an immediate first checkpoint with
`prev_size = 0` and empty `prev_root`. The anchor therefore grades that first
checkpoint `first-seen`, while the migration record preserves local
discontinuity evidence.

The relational backends physically copy immutable Capsule rows, envelope rows,
and MMR nodes into the new log scope. They do not copy checkpoint or witness
history. This preserves sequence and proof availability under the active ID,
but doubles the copied ledger and MMR storage. JSONL instead appends one
rebaseline event and rebinds the replayed immutable projection without copying
body events. Rebaseline is an exceptional operator action, not a routine
rollover mechanism.
On its next pass, the existing runner recognizes the durable force marker,
adopts the store's new active log ID, and emits the forced checkpoint.
`Rebaselines` exposes the bounded durable lineage after restart; the migration
record is not write-only evidence.

## Witness REST and trust

The default client calls:

```text
POST {baseURL}/transparency/register-statement
Content-Type: application/json
{"signed_statement_b64":"..."}
```

It accepts bounded current and older-hosted 2xx JSON response profiles. The
current profile contains `receipt_b64`, `entry_hash`, `entry_hash_scheme`,
`leaf_index`, `tree_size`, and a non-null `checkpoint_witness`. Echoed fields
must match the submitted payload, status must be `first-seen`, `witnessed`, or
`already-registered`, and the scheme must be `sig_structure` or the explicit
`legacy` value returned by the current migration dual-lookup path. The older
hosted deployment omits both additive fields and defines `entry_hash` as
SHA-256 of the exact COSE_Sign1 bytes. The client records that exact older shape
locally as `statement_bytes`; it never interprets an unknown or ambiguous
response as compatibility mode. A verified omitted-field compatibility receipt
proves checkpoint-statement inclusion, not server-side checkpoint continuity.

HTTP 409 is a permanent continuity conflict. Timeouts, 429, and 5xx are
retryable with capped exponential backoff and jitter. `/v1/digest` is not the
default because it cannot invoke checkpoint continuity behavior and currently
misnames a generic digest as `capsule_id` (capsule-anchor issue 27).

Offline receipt verification binds the receipt structure and authority
signature, caller-pinned Ed25519 authority key, entry hash, submitted signed
statement, tree coordinates, and inclusion path. The key is configuration,
not fetched during verification from the server being judged. Rotation is an
explicit configuration change with an optional declared overlap.

The verifier recomputes `entry_hash` according to the explicit local scheme:
SHA-256 of the submitted statement's RFC 9052 `Sig_structure` for the current
profile, or SHA-256 of the exact submitted COSE_Sign1 bytes for an explicit
`legacy` migration response or omitted-field hosted compatibility. It compares
the derived value with the response, then uses those bytes for RFC 9162 leaf
and inclusion-root reconstruction. It never trusts the echoed entry hash as the
binding source.

The implementation ports the small profile-opaque verifier core from
`scitt-cose-go-verify` with Apache-2.0 attribution. It uses
`veraison/go-cose`, requires VDS in the protected header, reconstructs the RFC
9162 root from the entry plus inclusion proof, and then verifies the log's
COSE signature. It does not shell out or invoke Python at runtime.

Each witness has an independent durable delivery row. Pending reads are scoped
by witness ID and exclude terminal rows, so the limit applies independently to
each witness. The runner retries that witness's oldest retryable checkpoint
first and stops its pass on first retryable failure. A
409 becomes terminal `continuity_conflict`, stops all later delivery for that
witness and log, and exposes unhealthy state. The runner never skips the
conflict automatically; recovery uses the explicit Rebaseline operation above.
A permanent client or receipt-validation failure becomes `permanent_failure`.
The local delivery row becomes `verified` only after offline receipt
verification. The word `witnessed` is reserved for the anchor's remote status
value. Per-witness backlog remains visible.

The anchor checks only payload shape and `prev_size` continuity against what it
last witnessed. It does not verify the producer signature, `prev_root`, MMR
root, or a consistency proof. Documentation and status must not overstate it.

## JSONL storage

The JSONL backend uses one append-only journal whose events contain a version,
type, and complete rebuild data. New Capsule plus initial envelopes is one
`capsule.append` event. Later envelopes, CLL commits, witness outcomes, and the
one-shot `log.rebaseline` migration use separate events with idempotency keys.

Writes are serialized, bounded to one line, written completely, flushed, and
`fsync`ed before success. On open, a final unterminated or invalid line is
treated as a torn tail only when it is the last line and is truncated to the
last valid byte boundary. Corruption anywhere else is a fatal open error and
the store refuses to serve the journal. Replay validates
sequence continuity, IDs, digests, checkpoint continuity, idempotency, and that
each MMR position is written once with immutable bytes.
Derived indexes are rebuildable and never authoritative.

An exclusive lock rejects cross-process concurrent writers. Concurrent
goroutines are safe.

## MySQL storage

MySQL 8 tables cover ledger metadata, Capsules, envelopes, MMR nodes,
checkpoints, and witness deliveries. Ledger metadata uses `log_id` as its
primary key and that row is locked `FOR UPDATE` for sequence allocation or CLL
commit. Capsule plus initial
envelopes is one transaction. Unique keys cover `(log_id, seq)`,
`(log_id, capsule_id)`, `(log_id, capsule_id, envelope_digest)`, checkpoints
`(log_id, mmr_size)`, MMR nodes `(log_id, position)`, and witness deliveries
`(log_id, witness_id, mmr_size)`.

MMR node writes are insert-only. A position collision inside a mutation is a
conflict. The cursor compare-and-swap prevents blind replay of stale CLL
mutations; callers reload state and recompute after contention. Deadlock and
lock-timeout failures are retryable. Every row loop checks `rows.Err()`.
The first release initializes schema version 1 from empty storage and rejects
an unknown version. There is no prior released schema to migrate; version-to-
version migration tooling is deliberately deferred. `Close` is idempotent for
all stores.

## SQLite storage

The SQLite backend is a first-class embedded relational store, not a MySQL
test substitute. It uses a pure-Go driver, WAL mode, foreign-key enforcement,
a bounded busy timeout, immediate write transactions, and the same log-scoped
schema and unique constraints as MySQL. `SQLITE_BUSY` and `SQLITE_LOCKED` are
classified as retryable contention. Sequence allocation and CLL mutations run
in write transactions that acquire the writer reservation before reading the
next sequence or CLL cursor. Two-handle tests exercise concurrent allocation.
SQLite, JSONL, and MySQL run the same data-path contract suite; backend-specific
tests cover locking, restart, and corruption behavior. SQLite also initializes
schema version 1 and rejects an unknown version; future migrations are deferred.

## Failure and recovery

| Scenario | Required result |
|---|---|
| Stop before store commit | No visible record; caller retries. |
| Store commit before Alchemy outbox completion | Query by ID, compare exact bytes, then mark delivered. |
| Lost notification | Durable cursor scan discovers the Capsule. |
| Stop during MMR indexing | Atomic CLL mutation is absent or complete; restart resumes. |
| Stop after checkpoint commit, before network | Pending witness row survives. |
| Anchor accepted, response lost | Exact statement resubmission is idempotent. |
| Malformed receipt or unpinned signature | Do not grade witnessed; persist classified failure. |
| Anchor 409 | Mark permanent continuity conflict; do not skip forward. |
| Stop during rebaseline | Migration and rebinding are absent or complete; bindings follow the durable record. |
| Stored bytes fail verification | Stop the integrity path; never replace bytes. |

## Security and limits

- Parameterize SQL and bound HTTP bodies, JSONL events, Capsules, Envelopes,
  scan batches, proofs, and operational error text.
- Use portable identifier syntax, at most 64 Envelopes per Capsule, at most 32
  witnesses per checkpoint, and microsecond UTC timestamp precision across all
  three stores.
- Reject anchor redirects by default.
- Never log Capsule content, envelopes, receipts, private keys, or credentials.
- Keep authentication separate from authorization.
- Validate integers before conversion and reject overflow.

## Package layout

```text
ledger/             service, models, errors, verification adapters
store/jsonl/        JSONL journal backend
store/sqlite/       embedded SQLite backend
store/mysql/        MySQL backend
mmr/                CLL hashing, proofs, DataTrails adapter
checkpoint/         payload, COSE signer, cadence, runner
capsuleanchor/      capsule-anchor client and receipt verifier
internal/storetest/ shared three-backend contract suites
ledger/testdata/    attributed AAC registry and Capsule vectors
```

No package imports Alchemy. Storage, checkpoint signing, and external witness
dependencies use narrow interfaces. Mandatory AAC format-4 verification is a
concrete service invariant rather than a replaceable host plugin. Package
import has no side effects.

## CI and release gates

GitHub Actions runs on pull requests and pushes to `main` with Go 1.27:

- formatting and clean `go mod tidy` checks;
- `go vet ./...`;
- `go test ./...` and `go test -race ./...`;
- AAC, Producer Envelope, MMR, checkpoint, and receipt conformance tests;
- SQLite integration tests against temporary database files; and
- MySQL integration tests using `testcontainers-go` with the
  same pinned MySQL image used by Alchemy.

The workflow uses read-only repository permissions, pins actions to immutable
commit SHAs, and contains no secrets. Release is incomplete until the pushed
commit's required workflow is green.

## Acceptance criteria

1. All three stores pass the same contract suite for append, idempotency,
   conflict, envelopes, ordering, gaps, concurrency, restart, CLL state,
   per-witness delivery, and rebaseline.
2. Upstream AAC format-4 and Producer Envelope vectors match expected outcomes.
3. Python/DataTrails known-answer vectors match roots and proofs, including
   multi-peak and adversarial cases.
4. Checkpoint signatures and local chain consistency verify after restart.
5. Fake-anchor tests cover bounds, 409, retry classification, echo checks, and
   pinned-key receipt validation.
6. Lost-notification and crash-boundary tests demonstrate recovery.
7. Examples show Alchemy-style append and explicit runner lifecycle.
8. Local checks and pushed GitHub Actions pass after implementation review.
9. CI must fail, rather than skip, if its Docker provider cannot run the MySQL
   Testcontainer; local runs may skip that suite when Docker is unavailable.
10. The embedded registry drift test equals upstream `registries.Load` at the
    pinned commit.
11. Both relational stores enforce `(log_id, position)` node uniqueness, and
    JSONL replay enforces the same write-once rule.
12. One unavailable witness cannot hide or block another witness's pending
    checkpoints.
13. Rebaseline rejects an existing new log ID and forces a `first-seen`
    checkpoint with no prior checkpoint fields.

## Deliberately deferred

- Alchemy manifest/profile evaluation and signer authorization.
- Assessment Capsules, compiler/engine folds, and AI effectiveness scoring.
- A separately versioned envelope-association transparency log.
- Multi-primary JSONL writers or distributed sequence allocation.
- Ledger pruning or compaction; records and proof material are intentionally
  append-only, so operators must size and monitor durable storage.
- Operating a witness, automatic key discovery, or trust-on-first-use.
- Any claim that anchor independently verified MMR roots or proofs.
