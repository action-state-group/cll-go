# AGENTS.md

## Project purpose

`capsule-ledger-go` implements an AAC format-4 ledger, independent Producer
Envelope persistence, a storage-neutral CLL/MMR, checkpointing, and external
witness integration.

Go module: `github.com/ethanyzhang/capsule-ledger-go`.

## Source grounding

Before reading a peer repository to make a behavioral or compatibility claim:

1. confirm its branch with `git -C <repo> rev-parse --abbrev-ref HEAD`;
2. update the intended ref with `git pull --ff-only` on a clean target branch,
   or `git fetch` plus an explicit `origin/<branch>` read when the worktree is
   on another branch;
3. record the exact commit used in `findings.md` or the design.

Peer checkouts normally live beside this repository under `~/GitHub`.

## Required compatibility reviews

Design and implementation reviews must compare:

- `agent-action-capsule` AAC format 4 and its Go/Python vectors;
- `capsule-producer-go` Capsule and Producer Envelope behavior;
- `capsule-emit/checkpoint` CLL/MMR and checkpoint behavior;
- `capsule-ledger` ledger semantics and current gaps;
- `capsule-anchor` request, receipt, and witness behavior;
- the Alchemy investigation publication profile;
- the Action State Group Slack decisions recorded in `findings.md`;
- the user decisions captured in `DESIGN.md` and `task_plan.md`.

Run the `cross-review` workflow to convergence after the design and again after
implementation. Peer findings are hypotheses; verify and adjudicate each before
editing.

## Architecture invariants

- Capsule content is immutable and idempotent by `capsule_id`.
- Producer Envelopes are independent, many-to-one evidence associated with a
  Capsule and deduplicated by exact bytes.
- Adding an envelope does not allocate a Capsule sequence or silently change an
  existing CLL commitment.
- CLL never reads Alchemy outbox state or concrete storage internals.
- Storage implementations expose equivalent transactional, recovery, ordering,
  and error semantics.
- Witness network success is not receipt validity. Verify receipts locally
  against configured trust material.
- Package initialization must not start background goroutines. Hosts explicitly
  own runner lifecycle and cancellation.

## Go requirements

Before every commit containing Go changes, run:

```text
go fmt ./...
go vet ./...
go mod tidy
go test ./...
go test -race ./...
```

- Use `github.com/stretchr/testify/assert` and `require` in tests.
- Use `t.Context()` for context-blocked goroutines.
- Do not discard errors silently.
- Check map lookups and pointers before use.
- Use parameterized MySQL queries and check `rows.Err()` after iteration.
- New functions and logic paths require success and edge-case tests.
- Public identifiers and non-trivial packages require useful comments.
- Keep application orchestration, storage, CLL, and witness transport in
  separate packages without cyclic dependencies.

## Git requirements

- Use conventional commit prefixes.
- Sign every commit with `git commit -s` and verify the `Signed-off-by` trailer.
- Do not commit credentials, `.env`, temporary files, editor state, or generated
  review scratch outside the explicitly tracked planning files.
- Preserve unrelated user changes.
