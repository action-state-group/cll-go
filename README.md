# capsule-ledger-go

`capsule-ledger-go` is a Go implementation of an Agent Action Capsule (AAC)
format-4 ledger with a Checkpointed Local Log (CLL).

The project is being designed around these boundaries:

- signature-free Capsules and independent Producer Envelopes;
- a storage-neutral ledger with JSONL and MySQL implementations;
- a narrow `LogSource` projection for ordered CLL/MMR indexing;
- signed checkpoints with explicit external witness clients and offline receipt
  verification;
- conformance with the current AAC v4, Python CLL, and capsule-anchor contracts.

The architecture is under review before implementation. See [DESIGN.md](DESIGN.md)
once Phase 2 lands and [task_plan.md](task_plan.md) for the active work plan.

## Status

Pre-implementation design phase. No release or interoperability claim exists
yet.

## License

Apache-2.0.
