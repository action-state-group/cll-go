# Progress Log

## Session: 2026-08-25

### Phase 1: Repository bootstrap and source-grounded discovery

- **Status:** completed
- **Started:** 2026-08-25 19:04 CDT
- Actions taken:
  - Read the complete `planning-with-files` and `cross-review` skill rules.
  - Confirmed the local path and GitHub repository did not already exist.
  - Ran planning session catch-up; no prior state was present.
  - Created the persistent plan, findings, and progress files.
  - Refreshed the producer, AAC, CLL, and ledger source branches before reading
    their licenses; selected Apache-2.0 for the new repository.
  - Initialized `main`, added self-contained project guidance, README, design
    placeholder, Go module, license, and ignore rules.
  - Created the public `ethanyzhang/capsule-ledger-go` repository, pushed the
    signed bootstrap commit `052929b5aa03a2fad914fad4a7ae667f225d002f`, and
    verified local HEAD equals `origin/main`.
  - Added GitHub Actions CI, including MySQL integration coverage, as an
    explicit implementation and release gate.
  - Refreshed and recorded exact AAC, producer, Python CLL/ledger, anchor,
    DataTrails MMR, and scitt-cose Go verifier baselines.
- Files created:
  - `task_plan.md`
  - `findings.md`
  - `progress.md`

### Phase 2: Architecture and contract design

- **Status:** in_progress
- Actions taken:
  - Wrote the source-grounded implementation contract in `DESIGN.md`.
  - Defined ownership, interfaces, AAC verification, CLL leaf semantics,
    checkpoint and witness wire behavior, store parity, crash recovery, CI,
    and deferred scope.
  - Corrected the witness design to use the current checkpoint-aware
    `/transparency/register-statement` path and the independent Go receipt
    verifier baseline.
- Files created or modified:
  - `DESIGN.md`

### Phase 3: Design cross-review convergence

- **Status:** in_progress
- Actions taken:
  - Snapshotted the design in the Git index.
  - Claude correctness round 1 returned 12 actionable findings in session
    `337fc44c-42f0-427d-b63f-1aa1edb5f670`; all were accepted and applied.
  - Claude correctness round 2 returned five residual findings in the same
    session; all were accepted and applied.
  - Claude correctness round 3 returned three internal consistency findings;
    all were accepted and applied.
  - Claude correctness round 4 returned one MMR node identity and retry
    finding; it was accepted and applied.
  - Claude quality review returned six contract and clarity findings. Applied
    the interface alignment, SQLite writer model, schema-version boundary,
    rebaseline storage-cost disclosure, and CI/package wording fixes.
  - Final Claude cleanup verification could not authenticate because its OAuth
    session had expired. Recorded and skipped it per the no-idling rule, then
    completed the driver cleanup audit against concrete interfaces and tests.
  - Kimi and Bob were unavailable because of quota. They were recorded and
    skipped without stopping useful work.

### Phase 4: Core implementation

- **Status:** completed
- Actions taken:
  - Implemented AAC v4 decoding and verification with a pinned embedded
    registry baseline and upstream parity test.
  - Implemented initial ledger service contracts and exact-byte envelope
    association.
  - Implemented the DataTrails-compatible bagged MMR root and inclusion proof
    path with multi-peak tests.
  - Implemented canonical checkpoint payloads and tagged Ed25519 COSE_Sign1
    statements with cadence boundary tests.
  - Added bagged consistency proofs, attributed Python/DataTrails known-answer
    vectors, bounded store audit, durable cadence recovery, and forced
    first-seen checkpoints after rebaseline.

### Phase 5: Storage and witness implementations

- **Status:** completed
- Actions taken:
  - Implemented the first JSONL ledger backend with exclusive writer locking,
    append and envelope events, exact-byte idempotency, replay, torn-tail
    recovery, corruption refusal, sequence scans, and chain gap discovery.
  - Added a first-class pure-Go SQLite backend with WAL, transactional append,
    restart persistence, and log isolation tests.
  - Added the MySQL 8 backend with Testcontainers, transactional sequence and
    CLL allocation, per-witness queues, rebaseline, and retry classification.
  - Added shared data, CLL, witness, lifecycle, concurrency, and rebaseline
    contract suites for all three backends.
  - Added capsule-anchor submission, bounded response validation, offline
    pinned-key RFC 9162 receipt verification, and durable delivery retries.
  - Added schema version 1 guards to SQLite and MySQL and two-handle SQLite
    writer coverage.
  - Added pinned, read-only GitHub Actions CI with full tests and race tests;
    MySQL is started by Testcontainers and cannot skip in CI.
  - Raised the new repository baseline to Go 1.27 at the user's direction;
    Alchemy will update its Go baseline before integration.

### Phase 6: Implementation cross-review and verification

- **Status:** pending
- Actions taken:
  - None.

### Phase 7: Delivery

- **Status:** pending
- Actions taken:
  - None.

## Test Results

| Test | Input | Expected | Actual | Status |
|---|---|---|---|---|
| Planning catch-up | Empty new project directory | No prior unsynced state | No report returned | Pass |
| Go package suite | `go test ./...` | All current packages pass | AAC verifier, checkpoint, MMR, and JSONL store pass | Pass |
| Initial three-backend contract run | `go test ./...` | Identical Store behavior | Exposed empty-envelope `nil` versus empty-slice mismatch | Fail, fixed |
| Full local validation before final cleanup | fmt, tidy, vet, unit, MySQL integration | All packages pass | All packages passed, including three Testcontainers suites | Pass |

## Error Log

| Timestamp | Error | Attempt | Resolution |
|---|---|---|---|
| 2026-08-25 19:09 CDT | `git show origin/main:go.mod` failed for `agent-action-capsule` because the Go module is not at repository root | 1 | Record the layout and locate the nested module before dependency selection. |
| 2026-08-25 19:13 CDT | `go mod tidy` warned that `all` matched no packages | 1 | Expected for the documentation-only initial commit; rerun after implementation packages exist. |
| 2026-08-25 19:17 CDT | A source-refresh loop assigned zsh's special `path` variable, causing `git`, `rg`, and other commands to disappear from lookup | 1 | Changed the loop variable to `repo_dir`; do not assign zsh's tied `path` parameter. |
| 2026-08-25 19:31 CDT | `go-datatrails-merklelog` has no `go.mod` at repository root | 1 | Inspect its nested modules and licensing before choosing between direct reuse and conformance-only comparison. |
| 2026-08-25 19:52 CDT | The first design replacement patch targeted `DESIGN.md` with both delete and add operations | 1 | Use a single update patch against the placeholder contents. |
| 2026-08-25 20:02 CDT | `claude-ibm` paused for device authorization during cross-review discovery | 1 | Continue with the installed standard `claude` reviewer and do not count the IBM wrapper as a completed review. |
| 2026-08-25 20:23 CDT | Correctness round launch found expired standard Claude OAuth, exhausted Bob budget, and an unsupported Kimi flag combination | 1 | Mark Claude/Bob unavailable for this round and rerun Kimi using `kimi -p`; neither unavailable reviewer counts as clean. |
| 2026-08-25 20:26 CDT | Kimi validated all named source SHAs, then hit its billing-cycle quota before reporting findings | 1 | Preserve session `54325`, do not count it as a completed review, and discover another authenticated reviewer. |
| 2026-08-25 21:02 CDT | Main work paused while waiting for an external reviewer authorization | 1 | Resumed immediately after user correction. Future auth, quota, service, and network failures are logged and skipped while useful main work continues. |
| 2026-08-25 21:24 CDT | Initial package test run was missing testify's transitive YAML checksum | 1 | Run module tidy now that test imports exist, then rerun all tests. |
| 2026-08-25 21:32 CDT | DataTrails MMR dependency had been removed by the earlier tidy while unused | 1 | Restore the pinned nested module now that `mmr/` imports it. |
| 2026-08-25 21:41 CDT | Checkpoint canonicalization rejected Go `uint64` counters | 1 | Encode them as decimal `json.Number` values before JCS and rerun tests. |
| 2026-08-25 21:33 CDT | Switching Capsule parent parsing to the upstream decoder left its `interface{}` result untyped | 1 | Require an object root before reading `chain`; rerun all packages. |
| 2026-08-25 21:38 CDT | Final Claude design cleanup could not refresh its expired OAuth session | 1 | Record the reviewer as unavailable and continue the driver verification; do not idle on external models. |
| 2026-08-25 21:38 CDT | A diagnostic search included Markdown backticks in a shell argument and zsh attempted command substitution | 1 | Repeat such searches with literal-safe quoting and keep shell arguments free of executable substitutions. |

## 5-Question Reboot Check

| Question | Answer |
|---|---|
| Where am I? | Final design cleanup verification, followed by implementation cross-review. |
| Where am I going? | Design, design cross-review, implementation, implementation cross-review, and delivery. |
| What's the goal? | Publish a reviewed Go AAC v4 ledger plus CLL with JSONL, MySQL, and witness support. |
| What have I learned? | See `findings.md`. |
| What have I done? | Bootstrapped and pushed the repo, converged four correctness rounds, implemented all three stores, CLL/checkpointing, anchor witnessing, tests, and CI. |
