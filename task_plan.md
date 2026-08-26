# Task Plan: capsule-ledger-go

## Goal

Create, design, implement, cross-review, verify, and publish
`github.com/ethanyzhang/capsule-ledger-go`: a Go AAC format-4 ledger with a
transport-neutral CLL/MMR, independently stored Producer Envelopes, JSONL and
MySQL storage implementations, and an external witness client boundary.

## Current Phase

Phase 1

## Phases

### Phase 1: Repository bootstrap and source-grounded discovery

- [x] Capture the user's requirements and review constraints.
- [x] Initialize the local Git repository and project guidance.
- [ ] Create `ethanyzhang/capsule-ledger-go`, commit with DCO sign-off, push,
  and verify the remote SHA.
- [ ] Refresh and inspect the exact peer-repository refs used as design inputs.
- [ ] Record source and Slack findings in `findings.md`.
- **Status:** in_progress

### Phase 2: Architecture and contract design

- [ ] Write a self-contained design covering ownership, interfaces, state,
  crash recovery, idempotency, MMR leaf semantics, checkpoint cadence,
  witness trust, and JSONL/MySQL parity.
- [ ] Define the AAC v4 Capsule and Producer Envelope validation boundary.
- [ ] Define conformance requirements against upstream Go/Python vectors.
- [ ] Run the driver's correctness and design-level review.
- **Status:** pending

### Phase 3: Design cross-review convergence

- [ ] Snapshot the design and set the cross-review severity bar and round cap.
- [ ] Run correctness review with every available peer CLI and adjudicate all
  findings.
- [ ] Run quality review, including Claude `/simplify`, and adjudicate all
  findings.
- [ ] Run cleanup verification with every available peer until convergence.
- [ ] Commit and push the converged design, then verify the remote SHA.
- **Status:** pending

### Phase 4: Core implementation

- [ ] Implement public ledger, storage, LogSource, signer, witness, and receipt
  verification contracts.
- [ ] Implement AAC v4 Capsule and independent Producer Envelope verification.
- [ ] Implement ordered ledger semantics, CLL/MMR, proofs, checkpoints,
  persistence state, cadence, recovery, and runner lifecycle.
- [ ] Add unit and conformance tests while implementing each path.
- **Status:** pending

### Phase 5: Storage and witness implementations

- [ ] Implement crash-recoverable JSONL storage.
- [ ] Implement transactional MySQL storage with lifecycle and migration tests.
- [ ] Implement the capsule-anchor REST witness client and offline receipt
  verification with pinned authority keys.
- [ ] Test success, malformed input, idempotency, restart, concurrency, retry,
  and failure paths.
- **Status:** pending

### Phase 6: Implementation cross-review and verification

- [ ] Run full format, tidy, vet, unit, race, integration, and conformance
  validation.
- [ ] Run the driver's correctness and simplification passes.
- [ ] Run correctness, quality, Claude `/simplify`, and cleanup verification
  with every available peer CLI until convergence.
- [ ] Re-run the full validation suite after accepted review fixes.
- **Status:** pending

### Phase 7: Delivery

- [ ] Audit the complete diff, stale references, lifecycle paths, and public
  documentation.
- [ ] Commit all intended files with DCO sign-off and conventional commits.
- [ ] Push the default branch and verify local HEAD equals the remote SHA.
- [ ] Record final tests, review convergence, limitations, and handoff.
- **Status:** pending

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

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| `agent-action-capsule` has no root `go.mod` | 1 | Treat the upstream Go reference as a nested module and locate it through the repository tree before pinning. |
| `go mod tidy` warned that `all` matched no packages | 1 | Expected during the documentation-only bootstrap; rerun after the first Go package is added. |

## Notes

- External source and Slack content belongs in `findings.md`, not this plan.
- Re-read this file before architecture decisions and before each review phase.
- Update statuses and errors immediately after each phase or failure.
