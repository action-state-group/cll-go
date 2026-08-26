# Findings and Decisions

## Requirements

- Create `/Users/ezhang/Github/capsule-ledger-go` and publish it under the
  `ethanyzhang` GitHub account.
- Implement ledger plus CLL/MMR in Go.
- Keep durable storage behind an interface with JSONL and MySQL implementations.
- Expose a CLL-facing `LogSource` boundary without allowing CLL to read the
  Alchemy outbox or concrete database internals.
- Persist signature-free AAC format-4 Capsules and independently deduplicated
  Producer Envelopes.
- Provide an external witness callout interface and a capsule-anchor REST
  implementation with local receipt verification.
- Use `planning-with-files` throughout.
- Cross-review the design to convergence before implementation.
- Cross-review the completed implementation to convergence before delivery.
- Both reviews must compare current peer repositories, AAC v4 requirements,
  relevant Action State Group Slack discussions, and this Codex conversation.

## Research Findings

- Current Python `LogSource` is a structural protocol with `append`, `scan`,
  `fetch`, `verify`, and `find_gaps`; CLL MMR indexing actually depends on a
  gapless 1-indexed `seq` plus `capsule_id`.
- Current Python `MmrLedger` rejects out-of-order or non-contiguous ledger
  sequence numbers and maps `leaf_index = seq - 1`.
- Current Python `find_gaps` finds missing AAC `chain.parent_capsule_id`
  references, not missing integer sequence numbers.
- Current `capsule-ledger` stores Capsule content but has no Producer Envelope
  persistence or COSE verification API. This is an implementation gap relative
  to the Alchemy AAC v4 target.
- Current Python CLL defaults to a checkpoint after 100 new entries or 15
  minutes since the first uncheckpointed entry, whichever comes first, with no
  empty checkpoint. It declares a 200-entry maximum lag.
- Current Python neutral MMR core/index/store live in `capsule_emit.checkpoint`;
  `capsule-ledger` imports them and retains ledger-specific hardened checkpoint
  signing, persistence, CLI, and Transparency Service integration.
- Steven Mih first agreed that Action State Group would provide a Go CLL, then
  stated that because it is MMR plus hardened checkpointing inside the ledger,
  he favors the `capsule-ledger-go` repository/package shape.
- `capsule-anchor` is an external push-based Transparency Service. It does not
  pull ledger data or read an Alchemy outbox. The Go process must submit a
  signed checkpoint and verify the returned receipt locally.
- The current capsule-anchor signed-statement witness checks checkpoint shape,
  exact `prev_size`, and increasing `mmr_size`; it does not verify the producer
  signature, previous root, or an MMR consistency proof. Client-side validation
  must not overstate this service behavior.
- `capsule-producer-go`, `capsule-emit`, and `capsule-ledger` use Apache-2.0;
  `agent-action-capsule` uses BSD-3-Clause. A new implementation inspired by
  the Apache-licensed CLL/ledger sources should use Apache-2.0 and preserve
  attribution for any ported code.

## Technical Decisions

| Decision | Rationale |
|---|---|
| Keep `LogSource` as a narrow CLL projection rather than adding envelopes to it | MMR ordering and proofs need ordered Capsule commitments; envelope lifecycle is a ledger concern and may have multiple signers over time. |
| Add a separate application-facing `Ledger` API for Capsule and envelope operations | Alchemy needs atomic initial persistence, later envelope association, query, and independent verification. |
| Add a lower-level `Store` interface | JSONL and MySQL must implement identical durable semantics without leaking backend details into ledger or CLL code. |
| Separate `WitnessClient` from offline `ReceiptVerifier` | Network success is not receipt validity; trust must use pinned authority keys rather than keys fetched through the same untrusted connection. |
| Host starts the CLL runner explicitly | Library import must not start hidden goroutines; Alchemy owns lifecycle and cancellation. |
| New-record notification is an optimization only | Restart recovery must scan persisted ledger state after the last indexed sequence so a lost notification cannot lose a Capsule. |
| Default cadence is 100 entries or 15 minutes with no empty checkpoints | Aligns with current Python CLL behavior while a Go timer can enforce the age leg during idle-after-write periods. |
| Use Apache-2.0 for the repository | Matches the immediate Go producer and Python CLL/ledger implementation dependencies and permits an attributed port of Apache-licensed logic. |
| Declare Go 1.24 initially | Matches `capsule-producer-go` and enables `t.Context()` while remaining compatible with the installed Go 1.26 toolchain. |

## Issues Encountered

| Issue | Resolution |
|---|---|
| Repository and local checkout did not exist | Confirmed absence before creation; bootstrap is Phase 1. |
| `agent-action-capsule` has no root `go.mod` | Locate and pin its nested Go module rather than assuming a root Go module. |

## Resources

- `/Users/ezhang/GitHub/agent-action-capsule`
- `/Users/ezhang/GitHub/capsule-producer-go`
- `/Users/ezhang/GitHub/capsule-emit`
- `/Users/ezhang/GitHub/capsule-ledger`
- `/Users/ezhang/GitHub/capsule-anchor`
- `/Users/ezhang/GitHub/aac-contexts`
- `/Users/ezhang/GitHub/presto-performance/alchemy/wiki/docs/engprod/alchemy/developer-guide/aac-investigation-publication-profile.md`
- Slack Go CLL responsibility thread: `https://actionstategroup.slack.com/archives/C0BQHT6JQDP/p1787692471541509`
- Slack capsule-ledger-go packaging direction: `https://actionstategroup.slack.com/archives/C0BQHT6JQDP/p1787700973921179`
- capsule-anchor compatibility issue: `https://github.com/action-state-group/capsule-anchor/issues/27`

## Visual or Browser Findings

- None.
