# cll-go

`cll-go` is an embeddable Go implementation of a Checkpointed Local Log (CLL).
It treats an Agent Action Capsule (AAC) format-4 ledger as one application
binding rather than as the definition of the log.

It provides:

- application-neutral ordered 32-byte record identities consumed by
  checkpointing;
- an AAC binding for signature-free Capsules and independent Producer Envelopes;
- a storage-neutral ledger with JSONL, SQLite, and MySQL implementations;
- a narrow application-neutral `cll.Source` projection for CLL/MMR indexing;
- signed checkpoints with explicit external witness clients and offline receipt
  verification;
- conformance with the current AAC v4, Python CLL, and capsule-anchor contracts;
- explicit checkpoint and witness runners whose lifecycle belongs to the host;
- signed Ed25519 checkpoint records interoperable with `capsule-emit`;
- a bounded checkpoint-only witness REST client;
- offline RFC 9162 receipt verification under a pinned authority key.

The generic CLL commits ordered 32-byte record identities. Fixed width preserves
leaf/interior domain separation in the MMR. The included AAC binding projects
each verified Capsule ID into one entry. Producer Envelopes remain
independently verified ledger associations and do not create CLL leaves.

## Install

The end-to-end integration uses both the producer and ledger modules:

```sh
go get github.com/ethanyzhang/capsule-emit-go@latest
go get github.com/ethanyzhang/cll-go@latest
```

## Storage

Choose one backend:

```go
jsonlStore, err := jsonl.Open("./ledger-data", "alchemy-investigations")
sqliteStore, err := sqlite.Open("./ledger.db", "alchemy-investigations")
mysqlStore, err := mysql.Open(ctx, dsn, "alchemy-investigations")
```

SQLite is the simplest transactional choice for one server process. JSONL is
an inspectable append-only journal. MySQL supports shared infrastructure and is
tested against a real MySQL 8 container.

## End-to-end: Producer to Ledger to Witness

The producer key signs one independent Producer Envelope. The checkpoint key
signs the local CLL checkpoint. The pinned witness authority public key verifies
the external service's receipt. These are three separate trust roles, and the
host must provision and authorize their keys outside this library.

This synchronous example sets the checkpoint cadence to one Capsule so the
whole path is visible in one call. `witnessBaseURL` identifies an already-running
checkpoint-only `capsule-anchor` deployment; this library does not start it.

```go
package integration

import (
    "context"
    "crypto/ed25519"
    "errors"
    "fmt"
    "time"

    "github.com/ethanyzhang/capsule-emit-go"

    "github.com/ethanyzhang/cll-go/capsuleanchor"
    "github.com/ethanyzhang/cll-go/checkpoint"
    "github.com/ethanyzhang/cll-go/ledger"
    "github.com/ethanyzhang/cll-go/store/sqlite"
)

func PublishInvestigation(
    ctx context.Context,
    databasePath string,
    witnessBaseURL string,
    producerPrivateKey ed25519.PrivateKey,
    checkpointPrivateKey ed25519.PrivateKey,
    witnessAuthorityPublicKey ed25519.PublicKey,
) (finalErr error) {
    const (
        logID     = "alchemy-investigations-prod"
        witnessID = "production-checkpoint-witness"
    )

    // Open one durable ledger stream. Reuse logID across process restarts.
    store, err := sqlite.Open(databasePath, logID)
    if err != nil {
        return err
    }
    defer func() { finalErr = errors.Join(finalErr, store.Close()) }()

    // AAC format-4 verification is mandatory and owned by ledger.New. The
    // host may add application vocabulary but cannot replace verification.
    service, err := ledger.New(store, ledger.Config{
        RegistryExtensions: map[string]map[string]bool{
            "effect.type": {
                "urn:alchemy:effect:github-issue-comment-publication:v1": true,
            },
        },
    })
    if err != nil {
        return err
    }

    // Build a signature-free Capsule and one independent Producer Envelope.
    producerIdentity, err := emit.NewEd25519SigningIdentity(producerPrivateKey)
    if err != nil {
        return err
    }
    produced, err := emit.Seal(emit.Input{
        ActionID:   "investigation-123",
        ActionType: emit.ActionTypeDecide,
        Operator:   "alchemy",
        Developer:  "alchemy@1.0.0",
        Timestamp:  time.Now().UTC(),
        Disposition: &emit.Disposition{
            Decision:      emit.DecisionAccept,
            Approver:      emit.ApproverPolicy,
            VerdictClass:  emit.VerdictExecuted,
            HumanDisposed: false,
        },
    }, producerIdentity)
    if err != nil {
        return err
    }

    // Verify exact Capsule and Envelope bytes, allocate seq, and persist them.
    record, err := service.Append(ctx, ledger.AdmissionSigned, produced.Payload, produced.Envelope)
    if err != nil {
        return err
    }
    fmt.Printf("stored seq=%d capsule_id=%s\n", record.Seq, record.CapsuleID)

    // Read new Capsule IDs from the same Store, advance the CLL/MMR, sign a
    // checkpoint, and atomically create its pending witness-delivery row.
    checkpointSigner, err := checkpoint.NewEd25519Signer(checkpointPrivateKey)
    if err != nil {
        return err
    }
    checkpointConfig := checkpoint.DefaultRunnerConfig(logID)
    checkpointConfig.Cadence.CadenceEntries = 1 // demonstration only
    checkpointConfig.WitnessIDs = []string{witnessID}
    checkpointRunner, err := checkpoint.NewRunner(
        checkpointConfig,
        store,
        checkpointSigner,
    )
    if err != nil {
        return err
    }
    advanced, err := checkpointRunner.RunOnce(ctx, time.Now().UTC())
    if err != nil {
        return err
    }
    if !advanced {
        return fmt.Errorf("checkpoint runner made no progress")
    }

    // POST the signed checkpoint to the external witness and verify its receipt
    // locally under trust material provisioned independently of that server.
    witnessClient, err := capsuleanchor.NewClient(witnessBaseURL, nil, 0)
    if err != nil {
        return err
    }
    receiptVerifier, err := capsuleanchor.NewReceiptVerifier(witnessAuthorityPublicKey)
    if err != nil {
        return err
    }
    deliveryRunner, err := capsuleanchor.NewDeliveryRunner(
        capsuleanchor.DefaultDeliveryConfig(witnessID),
        store,
        witnessClient,
        receiptVerifier,
    )
    if err != nil {
        return err
    }
    attempted, err := deliveryRunner.RunOnce(ctx, time.Now().UTC())
    if err != nil {
        return err
    }
    if !attempted {
        return fmt.Errorf("no checkpoint was ready for a delivery attempt")
    }

    state, err := store.LoadCLL(ctx)
    if err != nil {
        return err
    }
    if len(state.Checkpoints) == 0 {
        return fmt.Errorf("checkpoint is missing")
    }
    latest := state.Checkpoints[len(state.Checkpoints)-1]
    witnessed, err := store.GetWitness(ctx, witnessID, latest.MMRSize)
    if err != nil {
        return err
    }
    if witnessed.State != ledger.WitnessVerified {
        return fmt.Errorf("witness delivery ended in state %s", witnessed.State)
    }
    return nil
}
```

The call path is:

```text
emit.Seal
    -> Capsule + Producer Envelope
ledger.Service.Append
    -> durable Capsule record with seq
checkpoint.Runner.RunOnce
    -> durable CLL/MMR checkpoint + pending delivery
capsuleanchor.DeliveryRunner.RunOnce
    -> POST /checkpoints (raw COSE_Sign1)
    -> offline receipt verification
    -> durable verified witness result
```

The client accepts only the deployed checkpoint-only request and response
profile. The witness verifies the checkpoint's self-contained Ed25519
signature and returns an RFC 9162 receipt. It is stateless across checkpoints,
so the receipt proves checkpoint registration and inclusion, not stream
continuity or an independently attested time.
See the [witness contract](DESIGN.md#witness-rest-and-trust) for the exact hash
rules.

In a long-running Alchemy server, construct these objects once during startup
and run `checkpointRunner.Run(serverContext)` and
`deliveryRunner.Run(serverContext)` in server-owned goroutines. Request handlers
only call the producer and `service.Append`. Calling `checkpointRunner.Notify()`
after a successful append reduces latency; polling and durable cursors recover
lost notifications and process restarts.

See [DESIGN.md](DESIGN.md) for wire contracts, persistence guarantees, trust
boundaries, and pinned interoperability baselines.

## Verification

```sh
go test ./...
go test -race ./...
go vet ./...
```

MySQL tests use Testcontainers. They skip locally when Docker is unavailable,
but Docker failure is a CI failure.

## License

Apache-2.0.
