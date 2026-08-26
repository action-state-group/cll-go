# Task Plan: capsule-ledger-go

## Goal

Create, design, implement, cross-review, verify, and publish
`github.com/ethanyzhang/capsule-ledger-go`: a Go AAC format-4 ledger with a
transport-neutral CLL/MMR, independently stored Producer Envelopes, JSONL,
SQLite, and MySQL storage implementations, and an external witness boundary.

## Current Phase

Phase 8

## Phases

### Phase 1: Repository bootstrap and source-grounded discovery

- [x] Capture the user's requirements and review constraints.
- [x] Initialize the local Git repository and project guidance.
- [x] Create `ethanyzhang/capsule-ledger-go`, commit with DCO sign-off, push,
  and verify the remote SHA.
- [x] Refresh and inspect the exact peer-repository refs used as design inputs.
- [x] Record source and Slack findings in `findings.md`.
- **Status:** completed

### Phase 2: Architecture and contract design

- [x] Write a self-contained design covering ownership, interfaces, state,
  crash recovery, idempotency, MMR leaf semantics, checkpoint cadence,
  witness trust, and JSONL/MySQL parity.
- [x] Define the AAC v4 Capsule and Producer Envelope validation boundary.
- [x] Define conformance requirements against upstream Go/Python vectors.
- [x] Run the driver's correctness and design-level review.
- **Status:** completed

### Phase 3: Design cross-review convergence

- [x] Snapshot the design and set the cross-review severity bar and round cap.
- [x] Run correctness review with every available peer CLI and adjudicate all
  findings.
- [x] Run quality review, including Claude `/simplify`, and adjudicate all
  findings.
- [x] Run cleanup verification with every available peer until convergence;
  unavailable reviewers were recorded and skipped without idling.
- [x] Commit and push the converged design, then verify the remote SHA.
- **Status:** completed

### Phase 4: Core implementation

- [x] Implement public ledger, storage, LogSource, signer, witness, and receipt
  verification contracts.
- [x] Implement AAC v4 Capsule and independent Producer Envelope verification.
- [x] Implement ordered ledger semantics, CLL/MMR, proofs, checkpoints,
  persistence state, cadence, recovery, and runner lifecycle.
- [x] Add unit and conformance tests while implementing each path.
- **Status:** completed

### Phase 5: Storage and witness implementations

- [x] Implement crash-recoverable JSONL storage.
- [x] Implement first-class transactional SQLite storage.
- [x] Implement transactional MySQL storage with Testcontainers integration tests.
- [x] Implement the capsule-anchor REST witness client and offline receipt
  verification with pinned authority keys.
- [x] Add GitHub Actions CI for format, tidy, vet, unit, race, integration, and
  conformance validation, with MySQL service coverage where required.
- [x] Test success, malformed input, idempotency, restart, concurrency, retry,
  and failure paths.
- **Status:** completed

### Phase 6: Implementation cross-review and verification

- [x] Run full format, tidy, vet, unit, race, integration, and conformance
  validation.
- [x] Run the driver's correctness and simplification passes.
- [x] Run correctness, quality, Claude `/simplify`, and cleanup verification
  with every available peer CLI until convergence; unavailable external CLIs
  were recorded and skipped without idling.
- [x] Re-run the full validation suite after accepted review fixes.
- [x] Verify the required GitHub Actions workflow passes on the pushed commit.
- **Status:** completed

### Phase 7: Delivery

- [x] Audit the complete diff, stale references, lifecycle paths, and public
  documentation.
- [x] Commit all intended files with DCO sign-off and conventional commits.
- [x] Push the default branch and verify local HEAD equals the remote SHA.
- [x] Record final tests, review convergence, limitations, and handoff.
- **Status:** completed

### Phase 8: Public verifier API cleanup and end-to-end example

- [x] Remove caller-supplied verifier interfaces from the public ledger
  constructor while preserving mandatory AAC v4 verification and audit.
- [x] Rename the caller vocabulary field to `RegistryExtensions` and copy its
  configuration defensively.
- [x] Add same-change tests for default construction, registry extensions,
  append verification, and audit.
- [x] Make the Producer to Ledger to Checkpoint to Witness example a prominent,
  complete README integration section.
- [x] Update design references and search for the superseded constructor and
  field names.
- [x] Run format, tidy, vet, unit, race, integration, and documentation checks.
- [x] Converge correctness, quality `/simplify`, and cleanup review with every
  available peer; record unavailable peers without blocking.
- [ ] Commit with DCO sign-off, push `main`, and verify GitHub Actions on the
  final remote SHA.
- **Status:** in_progress

## Key Questions

1. What exact upstream AAC v4 module ref and vector corpus should the first
   release pin?
2. Should v1 CLL checkpoints commit only ordered Capsule IDs for Python
   compatibility, or also envelope-association events through a separate log?
3. What minimal public interfaces keep CLL independent of JSONL, MySQL,
   Alchemy outbox state, and capsule-anchor?
4. What transaction and recovery semantics can both JSONL and MySQL implement
   identically?
5. Which capsule-anchor endpoint and receipt wire format are sufficiently
   implemented for interoperable signed-checkpoint witnessing?

## Decisions Made

| Decision | Rationale |
|---|---|
| Repository name is `capsule-ledger-go` | Matches Steven Mih's latest Slack packaging direction: ledger plus MMR and hardened checkpointing. |
| Producer responsibilities stay outside this repository | `capsule-producer-go` owns AAC construction and Producer Envelope creation; the ledger verifies and persists their output. |
| Planning and both cross-review gates are mandatory | Explicit user requirement; design must converge before implementation and implementation must converge before delivery. |
| GitHub Actions CI is a release requirement | The user explicitly required CI after implementation; local success alone is insufficient. |
| The public ledger constructor owns AAC v4 verification | AAC v4 verification is a mandatory safety invariant, not a host-replaceable plugin; hosts may only add registry vocabulary extensions. |
| Keep the concrete `AACVerifier` available for standalone checks | Removing host replacement from `Service` does not require hiding the useful concrete adapter; the safety boundary is the constructor. |

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Phase 8 planning patch expected `## User requirements`, but the file uses `## Requirements` | 1 | Re-read the planning-file headings and apply a targeted patch against the actual structure. |
| New audit test expected the one-record chained fixture to be store-valid | 1 | Assert its intentional `chain_parent_missing` result while independently proving the configured registry extension remains recognized. |
| Bob availability probe used the cross-review cleanup pattern but the shell safety layer rejected `rm -f` | 1 | Use Bob's direct result envelope for the small availability probe; Bob then reported its actual budget blocker. |
| `agent-action-capsule` has no root `go.mod` | 1 | Treat the upstream Go reference as a nested module and locate it through the repository tree before pinning. |
| `go mod tidy` warned that `all` matched no packages | 1 | Expected during the documentation-only bootstrap; rerun after the first Go package is added. |
| Source-refresh loop assigned zsh's special `path` variable and removed command lookup | 1 | Use a task-specific `repo_dir` variable; never assign zsh's tied `path` parameter. |
| `go-datatrails-merklelog` has no root `go.mod` | 1 | Inspect its repository layout, module files, and license before deciding whether to depend on it or use it only as an interoperability reference. |
| First `DESIGN.md` replacement patch used delete and add operations for the same path | 1 | Replace the existing placeholder with one update operation instead. |
| `claude-ibm` requested a fresh device authorization during reviewer discovery | 1 | Use the already installed standard `claude` CLI for this review and record the IBM wrapper as unavailable unless authorization completes independently. |
| Standard `claude` OAuth was expired; Bob reported exhausted Bobcoin budget; Kimi rejected `--auto` with prompt mode | 1 | Record Claude and Bob as installed but externally unavailable, then rerun Kimi with its supported non-interactive syntax. |
| Kimi exhausted its billing-cycle quota after source validation but before returning findings | 1 | Do not count the partial run as a review; search for another authenticated independent reviewer and preserve the Kimi session for a later retry. |
| Work paused for reviewer device authorization instead of continuing useful tasks | 1 | User correction: external model failures must be recorded and skipped without idling the main task. This rule is now also in the global agent guidance. |
| First test run lacked testify's transitive YAML checksum | 1 | Run `go mod tidy` after adding test packages, then rerun the full suite. |
| Module tidy removed the previously unused DataTrails dependency before the MMR package landed | 1 | Add the pinned nested MMR module after its first import, then rerun tidy and tests. |
| Upstream JCS rejected Go `uint64` checkpoint counters | 1 | Encode validated counters as decimal `json.Number` values before JCS. |
| Design correctness round 4 found unscoped, non-unique MMR node positions | 1 | Require `(log_id, position)` uniqueness plus insert-only read-back semantics in MySQL and JSONL replay. |
| SQLite dependency was temporarily constrained to preserve Go 1.24 | 1 | User confirmed local Go 1.27 and authorized a future Alchemy upgrade; use Go 1.27 and current dependencies instead. |
| Shared Store contract found empty-envelope read-back differed (`[]` versus `nil`) | 1 | Canonicalize zero envelopes to `nil` in all backend defensive-copy paths. |
| First audit test assumed a Class-1-valid Capsule must also be store-level `OK` in isolation | 1 | Assert attributable stored identity plus recomputed ID/findings; store-level chain results are allowed to differ. |
| JSONL rebaseline initially allowed reuse of an older inactive log ID | 1 | Track every historical log ID and require a previously unused target across all backends. |
| Witness pending queries could skip a continuity-conflicted checkpoint and deliver later checkpoints | 1 | Treat continuity conflict as a per-witness, per-log barrier and test the blocked backlog contract across all stores. |
| SQLite's default deferred transaction could race sequence or CLL cursor reads across process handles | 1 | Use immediate write transactions, classify busy/locked as retryable, and add a two-handle concurrent allocation test. |

## Notes

- External source and Slack content belongs in `findings.md`, not this plan.
- Bootstrap remote: `https://github.com/ethanyzhang/capsule-ledger-go`, commit
  `052929b5aa03a2fad914fad4a7ae667f225d002f` verified equal to `origin/main`.
- Re-read this file before architecture decisions and before each review phase.
- Update statuses and errors immediately after each phase or failure.
