# AGENTS.md

## Project purpose

`cll-go` implements storage-neutral CLL entry, MMR, checkpoint, and
external-witness primitives, plus an AAC format-4 ledger binding with
independent Producer Envelope persistence.

Go module: `github.com/ethanyzhang/cll-go`.

## Source grounding

Before reading a peer repository to make a behavioral or compatibility claim:

1. confirm its branch with `git -C <repo> rev-parse --abbrev-ref HEAD`;
2. update the intended ref with `git pull --ff-only` on a clean target branch,
   or `git fetch` plus an explicit `origin/<branch>` read when the worktree is
   on another branch;
3. record a compatibility-critical source revision in `DESIGN.md` when that
   revision is part of the implementation contract.

Peer checkouts normally live beside this repository under `~/GitHub`.

## Required compatibility reviews

Design and implementation reviews must compare:

- `agent-action-capsule` AAC format 4 and its Go/Python vectors;
- `capsule-emit-go` Capsule and Producer Envelope behavior;
- `capsule-emit/checkpoint` CLL/MMR and checkpoint behavior;
- `capsule-ledger` ledger semantics and current gaps;
- `capsule-anchor` request, receipt, and witness behavior;
- the Alchemy investigation publication profile;
- the ownership and integration decisions captured in `DESIGN.md`.

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
- Do not commit credentials, `.env`, temporary files, editor state, task-local
  plans, progress logs, review transcripts, or command history. Git, CI, and
  the task own execution history; `DESIGN.md` and `README.md` own durable
  contracts and usage guidance.
- Preserve unrelated user changes.
