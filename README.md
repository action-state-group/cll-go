# capsule-ledger-go

`capsule-ledger-go` is an embeddable Go implementation of an Agent Action
Capsule (AAC) format-4 ledger with a Checkpointed Local Log (CLL).

It provides:

- signature-free Capsules and independent Producer Envelopes;
- a storage-neutral ledger with JSONL, SQLite, and MySQL implementations;
- a narrow `LogSource` projection for ordered CLL/MMR indexing;
- signed checkpoints with explicit external witness clients and offline receipt
  verification;
- conformance with the current AAC v4, Python CLL, and capsule-anchor contracts;
- explicit checkpoint and witness runners whose lifecycle belongs to the host;
- signed Ed25519 COSE checkpoints;
- a bounded capsule-anchor REST client;
- offline RFC 9162 receipt verification under a pinned authority key.

The CLL commits ordered Capsule IDs. Producer Envelopes remain independently
verified ledger associations and do not create CLL leaves.

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

## Host lifecycle

Importing the module starts no goroutines and makes no network calls. A host
such as Alchemy starts the runners under its server context:

```go
service, err := ledger.New(store, ledger.AACVerifier{}, nil)

checkpointRunner, err := checkpoint.NewRunner(
    checkpoint.DefaultRunnerConfig("alchemy-investigations"),
    store,
    checkpointSigner,
)
go func() {
    if err := checkpointRunner.Run(serverContext); err != nil &&
        !errors.Is(err, context.Canceled) {
        report(err)
    }
}()

deliveryRunner, err := anchor.NewDeliveryRunner(
    anchor.DefaultDeliveryConfig("production-anchor"),
    store,
    anchorClient,
    receiptVerifier,
)
go func() {
    if err := deliveryRunner.Run(serverContext); err != nil &&
        !errors.Is(err, context.Canceled) {
        report(err)
    }
}()
```

After a successful append, `checkpointRunner.Notify()` can reduce latency.
Notifications are optional; the durable sequence scan recovers lost signals.

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
