# Findings and Decisions

## Requirements

- Remove the public caller-supplied ledger verifier boundary; AAC v4
  verification remains mandatory and internally owned.
- Rename the extension-only `Registries` input to `RegistryExtensions`.
- Make the complete Producer to Ledger to Checkpoint to external Witness
  example a prominent README integration section.
- Create `/Users/ezhang/Github/capsule-ledger-go` and publish it under the
  `ethanyzhang` GitHub account.
- Implement ledger plus CLL/MMR in Go.
- Keep durable storage behind an interface with JSONL, SQLite, and MySQL implementations.
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
- The finished repository must include GitHub Actions CI and the pushed
  implementation is not complete until required checks pass remotely.

## Research Findings

- `AACVerifier` was the only production implementation of the former ledger
  `Verifier` interface, and no service test used an injected fake. The public
  seam therefore weakened the mandatory format-4 invariant without supporting
  an actual alternate implementation.
- The standard registry baseline already lives in `registry_baseline.go` and
  is always merged before upstream verification. The public map is extensions
  only and is now named and copied accordingly.
- The `capsuleanchor` package's small receipt `Verifier` interface remains
  valid: it is
  a separate trust boundary used by the delivery runner and has both the real
  pinned-key implementation and test substitutes.
- Correctness review found that `Audit(MaxScanLimit)` requested
  `MaxScanLimit+1` records from stores that reject that limit. Audit now scans
  the requested bound and performs a one-record continuation probe only when
  the first scan fills the bound.
- Correctness review found that the design used conceptual witness interface
  names where the code block otherwise looked literal. It now names the actual
  `capsuleanchor.Submitter` and `capsuleanchor.Verifier` contracts and identifies
  the concrete `ReceiptVerifier` implementation.
- The quality `/simplify` pass produced six findings. All underlying concerns
  were accepted: the audit continuation probe now uses `ScanIDs`; registry key,
  copy, and additive rules are documented; one constructor owns the immutable
  standalone verifier registry set; duplicated registered-value filtering and
  test fixture mutation were consolidated; audit-bound tests are isolated and
  cover exact and exceeded bounds; and both `RunOnce` boolean contracts are
  documented and named accurately in the README example.

- Refreshed source baselines on August 25, 2026:
  - `agent-action-capsule` `origin/main` at
    `7dcef86634355c0d3335b3050b1bc18845716275`;
  - `ethanyzhang/capsule-producer-go` `origin/main` at
    `cb9de82a792c387e64f39f719221511b3fa27b49`;
  - `capsule-emit` `origin/main` at
    `d0ff0d725d8683a9e8f63c4980fc5fe3d7c5d443`;
  - `capsule-ledger` `origin/main` at
    `01f2ae6c4e7d54793ed308a624c367e702a8d089`;
  - `capsule-anchor` `origin/main` at
    `452d253c69bc9eb8ddb780347377c2115e6aa166`.
- The AAC Go module is nested at `agent-action-capsule/go/go.mod`. The
  authoritative upstream corpora include Capsule vectors under `test-vectors`
  and eight frozen Producer Envelope cases under `producer-envelope-vectors`.
- `capsule-producer-go` exposes separate `Build`, `Sign`, `Seal`,
  `VerifyCapsule`, and `VerifyEnvelope` paths plus evidence helpers; the ledger
  should reuse those or their upstream dependency rather than reimplementing a
  second format-4 profile.
- Upstream `agent-action-capsule/go/verify.Verify` accepts an optional complete
  store and registry map, returns structured findings plus recomputed
  `capsule_id`, and treats only error-severity findings as gating. The upstream
  envelope verifier authenticates the exact format-4 COSE profile and returns
  the raw signer public key as evidence, not authorization.
- The Alchemy profile uses private effect type
  `urn:alchemy:effect:github-issue-comment-publication:v1`. Its unknown registry
  value is intentionally informational, so ledger verification must preserve
  non-gating findings rather than reject or discard them.
- Alchemy's persistence design already assigns Capsule construction and
  Producer Envelope signing to `capsule-producer-go`, and requires an outbox
  worker to reverify, append, read back, and compare exact bytes. Its earlier
  single-envelope table simplification is application-local; this reusable
  ledger must support zero or many independent envelopes for one Capsule.
- The Alchemy publication profile requires AAC format 4, `jcs`, and exact
  canonical bytes from the producer. It also preserves upstream informational
  findings for the private publication effect type. `capsule-ledger-go` is a
  transport/storage dependency of that profile, not the schema reader or
  application-profile authority.
- The ledger depends directly on the upstream AAC verifier and envelope
  verifier through its concrete `AACVerifier` adapter. The public service
  constructor installs that adapter rather than accepting a replaceable
  verifier. It does not import producer construction APIs merely to verify
  stored records, and callers may only add registry extensions to the embedded
  upstream baseline.
- Current Python `LogSource` is a structural protocol with `append`, `scan`,
  `fetch`, `verify`, and `find_gaps`; CLL MMR indexing actually depends on a
  gapless 1-indexed `seq` plus `capsule_id`.
- Current Python `MmrLedger` rejects out-of-order or non-contiguous ledger
  sequence numbers and maps `leaf_index = seq - 1`.
- The Python production MMR uses `SHA-256(0x00 || capsule_id_bytes)` leaves,
  position-committed interior hashes
  `SHA-256(be64(parent_position + 1) || left || right)`, and right-to-left
  peak bagging `SHA-256(right || left)`. It identifies the interior algorithm
  as MMRIVER-draft-compatible and cites DataTrails' Go implementation.
- The cited DataTrails implementation is the nested Go module
  `github.com/datatrails/go-datatrails-merklelog/mmr` at commit
  `275103a34a08e56a376107246e04dc5819cc44cc`, requires Go 1.24, and uses the
  MIT license. Its `AddHashedLeaf`, bagged-root, inclusion-proof, and bagged
  consistency-proof APIs implement the same position commitment
  (`be64(position + 1)`) described by the Python CLL.
- `action-state-group/scitt-cose` `origin/main` at
  `36ec13992287a463481d1b00ac2098f032f229b5` includes an independent Go
  verifier under `scitt-cose-go-verify`. It uses `veraison/go-cose`, strict
  CBOR decoding, a protected VDS header, clean-room RFC 9162 inclusion-root
  reconstruction, and the pinned log key. It is a command module rather than
  an importable library, so this repository should port the small verifier core
  with Apache-2.0 attribution and run its vectors, not shell out at runtime.
- Inclusion proofs carry the sibling path plus peaks to the left and right;
  consistency proofs prove every old peak into a new peak and re-bag both
  roots. Verifiers are total functions over adversarial input and return false
  rather than panic.
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
- The current checkpoint-aware anchor API is
  `POST /transparency/register-statement` with JSON
  `{\"signed_statement_b64\": \"<COSE_Sign1>\"}`. Its embedded JSON payload must
  declare `artifact_type: \"mmr-checkpoint\"` and include non-empty `log_id`,
  `key_id`, `mmr_root`, positive `mmr_size`, non-negative `prev_size`, and
  `timestamp`. The response adds `entry_hash_scheme` and optional
  `checkpoint_witness` to the receipt, entry hash, leaf index, and tree size.
- `POST /v1/digest` remains a simple digest anchoring surface. Sending a
  checkpoint digest there returns a receipt but does not invoke the anchor's
  checkpoint continuity checks. The Go default must use the signed-statement
  endpoint; digest-only support, if exposed, must be explicitly lower assurance.
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
| Require CI with MySQL-backed integration coverage | JSONL-only unit tests cannot establish storage parity or MySQL transaction behavior. |
| Preserve complete structured AAC verification results | Alchemy requires informational findings such as unknown private registry values; reducing verification to a boolean loses evidence. |
| Reuse or verify against the cited DataTrails Go MMR implementation before porting | The Python implementation already grounds its position hashing in that Go source; duplicating the math without comparison creates avoidable interop risk. |
| Use Apache-2.0 for the repository | Matches the immediate Go producer and Python CLL/ledger implementation dependencies and permits an attributed port of Apache-licensed logic. |
| Declare Go 1.27 | Matches Ethan's installed toolchain and lets the new repository use current dependencies; Alchemy can update before integration instead of constraining this repository to its older Go baseline. |
| Make SQLite a formal third Store backend | It gives an embedded transactional option for Alchemy without weakening MySQL parity or replacing production MySQL integration coverage. |
| Use Testcontainers for MySQL tests | Matches Alchemy's established integration style and tests the actual MySQL 8 transaction semantics; local runs may skip without Docker, while CI must fail. |
| Version relational schemas from the first release | An explicit version-1 row prevents a newer binary from silently opening an unknown schema; actual migrations remain deferred until a released predecessor exists. |
| Copy immutable ledger and MMR data during rebaseline | The active log ID retains sequence and proof availability, while checkpoint and witness history remains frozen under the old ID; the exceptional operation has an explicit storage cost. |

## Design Cross-Review Adjudication

Claude correctness round 1 session `337fc44c-42f0-427d-b63f-1aa1edb5f670`
reported 12 actionable findings. All were accepted:

1. Embed and always pass the full upstream registry baseline.
2. Decode Capsules with upstream `DecodeCapsuleJSON` and `json.Number`.
3. Emit a checkpoint payload that is both Python CLL-readable and anchor-recognized.
4. Bind each store instance to one log ID.
5. Persist the first-uncheckpointed time across restarts.
6. Add a full-store `Audit` API backed by `VerifyStore`.
7. Pin the DataTrails bagged proof family and empty-root convention.
8. Require tagged, embedded-payload COSE and locally derived `sig_structure` entry hash.
9. Correct producer repository ownership to `ethanyzhang`.
10. Make anchor 409 a terminal continuity conflict requiring explicit re-baseline.
11. Match Python overdue semantics at more than 200 entries.
12. State JSONL non-tail corruption as a fatal open error.

Claude correctness round 2 found five remaining gaps. All were accepted:

1. Remove repeated log IDs from single-log store methods.
2. Bound `Audit` and return stored sequence and ID with each result.
3. Define re-baseline as a durable metadata migration with a first-seen checkpoint.
4. Generate and parity-test the embedded registry map through upstream `Load`.
5. Include the durable age anchor in the atomic CLL mutation.

Claude correctness round 3 found three internal consistency gaps. All were
accepted: rebaseline now atomically rebinds the live instance and has a JSONL
event, pending witness reads are per-witness, and every MySQL metadata,
checkpoint, and delivery key is explicitly log-scoped.

Claude correctness round 4 found one remaining shared-storage gap. It was
accepted: MMR nodes now have a `(log_id, position)` unique key and matching
JSONL write-once replay validation. CLL mutation retries are protected by the
indexed-sequence compare-and-swap and must reload before recomputing.

Claude quality review found six residual design issues. They were accepted:
the public snippets now match concrete interfaces and result fields; SQLite
uses immediate writer transactions and retryable lock classification; both
relational stores carry a schema-version guard; rebaseline documents its copy
scope and storage cost; local and remote witness status words are distinct;
and package/CI wording no longer promises nonexistent migration suites.

## Issues Encountered

| Issue | Resolution |
|---|---|
| Repository and local checkout did not exist | Confirmed absence before creation; bootstrap is Phase 1. |
| `agent-action-capsule` has no root `go.mod` | Locate and pin its nested Go module rather than assuming a root Go module. |
| zsh treats lowercase `path` as a special variable tied to `PATH` | Use `repo_dir` or another task-specific name in all repository loops. |
| `go-datatrails-merklelog` has no root `go.mod` | Locate its nested module boundaries and license before selecting a dependency path. |

## Implementation Cross-Review Adjudication

The driver correctness and simplification passes found and fixed these material
issues before publication:

1. JSONL CLL validation changed `ForceCheckpoint` before the journal commit;
   validation is now side-effect free and has a regression test.
2. Terminal witness outcomes could be overwritten and later checkpoints could
   leak past continuity conflicts; transitions are now validated and terminal.
3. Direct Store inputs, identifiers, timestamps, counts, scan sizes, checkpoint
   sizes, and JSONL event lines needed shared bounds and normalization.
4. SQLite was missing the MySQL/JSONL not-found behavior for envelope adds;
   shared contracts now cover it.
5. Returned JSONL and inserted relational values shared mutable verification
   maps and finding pointers; model-level deep copies now protect all backends.
6. Restored MMRs checked hash lengths but not complete size or interior hashes;
   restore now validates both before use.
7. Stored checkpoints were extended without revalidating their canonical
   payload, signature, predecessor, historical root, or leaf cursor; the runner
   now fails closed on every one of those checks.
8. MySQL deadlocks and SQLite busy errors were not consistently classified at
   write-method boundaries; runners now pause and retry typed contention.
9. The anchor client needed an additive-response policy, URL validation,
   default request timeout, redirect and response bounds, echo tests, and
   bounded persisted errors.

## Resources

- `/Users/ezhang/GitHub/agent-action-capsule`
- `/Users/ezhang/GitHub/capsule-producer-go`
- `/Users/ezhang/GitHub/capsule-emit`
- `/Users/ezhang/GitHub/capsule-ledger`
- `/Users/ezhang/GitHub/capsule-anchor`
- `/Users/ezhang/GitHub/go-datatrails-merklelog`
- `/Users/ezhang/GitHub/aac-contexts`
- `/Users/ezhang/GitHub/presto-performance/alchemy/wiki/docs/engprod/alchemy/developer-guide/aac-investigation-publication-profile.md`
- Slack Go CLL responsibility thread: `https://actionstategroup.slack.com/archives/C0BQHT6JQDP/p1787692471541509`
- Slack capsule-ledger-go packaging direction: `https://actionstategroup.slack.com/archives/C0BQHT6JQDP/p1787700973921179`
- capsule-anchor compatibility issue: `https://github.com/action-state-group/capsule-anchor/issues/27`

## Visual or Browser Findings

- None.
