# capsule-ledger-go design

Status: design in progress. This document must pass the required design
cross-review before implementation begins.

The completed design will define:

- AAC format-4 Capsule and Producer Envelope validation;
- ledger, storage, LogSource, CLL runner, checkpoint signer, witness client,
  and receipt-verifier contracts;
- JSONL and MySQL durability and recovery semantics;
- MMR leaf identity, proof, checkpoint, and cadence behavior;
- conformance with the current Go and Python AAC/CLL implementations;
- explicit current limitations and release gates.
