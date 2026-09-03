# cll-go

`cll-go` is an embeddable Go implementation of a Checkpointed Local Log
(CLL). It stores a dense sequence of opaque 32-byte identities, commits those
identities to an MMR, signs checkpoints, and can submit checkpoints to external
witnesses.

The library is application-neutral. It does not understand record bodies,
signatures, authorization, application indexes, or business workflows. An
application verifies and stores its complete record first, then appends the
record's 32-byte identity to CLL.

## Install

Requires Go 1.27 or newer.

```bash
go get github.com/action-state-group/cll-go@latest
```

## Append identities

Choose a backend and append the identity of an already-persisted record:

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/action-state-group/cll-go/cll"
	"github.com/action-state-group/cll-go/store/memory"
)

func main() {
	ctx := context.Background()
	store := memory.New()
	defer func() {
		if err := store.Close(); err != nil {
			panic(err)
		}
	}()

	// The application owns and persists the complete record.
	record := []byte(`{"order":"A-123","status":"approved"}`)
	identity := sha256.Sum256(record)

	result, err := store.Append(ctx, cll.AppendInput{
		Value:      identity[:],
		AppendedAt: time.Now().UTC(),
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("seq=%d outcome=%s\n", result.Entry.Seq, result.Outcome)
}
```

Appending the same identity again is idempotent. It returns the original entry
and timestamp with `cll.AppendIdempotent`.

## Backends

The same `cll.Backend` contract is implemented by:

```go
memoryStore := memory.New()
jsonlStore, err := jsonl.Open("./cll.jsonl")
sqliteStore, err := sqlite.Open("./cll.sqlite", "default")
mysqlStore, err := mysql.Open(ctx, dsn, "default")
```

- Memory is process-local and non-durable.
- JSONL is an inspectable, single-writer append-only journal.
- SQLite uses WAL transactions and supports multiple handles in one process.
- MySQL uses transactional row locking and supports multiple processes.

SQLite and MySQL accept an explicit log ID. The TypeScript default is
`"default"`; use that value when both runtimes must open the same log. A
different ID selects a different logical log in the same database. JSONL holds
one logical log per file.

### Relational schema

Go and TypeScript use the same four logical tables and column names:

| Table | Purpose | Key |
| --- | --- | --- |
| `cll_meta` | CLL cursor and latest checkpoint metadata | `log_id` |
| `cll_entries` | Dense 1-based identities and append times | `log_id, seq`; identity is unique per log |
| `cll_nodes` | Ordered MMR nodes | `log_id, position` |
| `cll_witnesses` | Pending or completed witness delivery state | `log_id, witness_id, checkpoint_size` |

SQLite uses `TEXT`, `BLOB`, and `INTEGER`. MySQL uses corresponding
`VARCHAR(191)`, `LONGBLOB` or `BINARY(32)`, and unsigned integer types. Stored
metadata, timestamps, decimal counters, base64 values, and witness JSON use the
same portable encodings in both runtimes. See [DESIGN.md](DESIGN.md) for the
exact DDL and transition contract.

The current generic schema intentionally refuses the identifiable pre-1.0
application-specific schema. Applications must migrate their business records
and initialize a fresh generic CLL explicitly.

### JSONL format

JSONL v4 writes newline-delimited compact JSON events:

- `cll.init`
- `entry.append`
- `cll.commit`
- `witness.commit`

Opening a v3 file returns `cll.ErrCorrupt`. A final incomplete line is removed
on recovery, while a malformed complete line is rejected. The file is locked
for the lifetime of the backend so a second writer receives
`cll.ErrContention`.

## Checkpoints

`checkpoint.Runner` incrementally scans entries, updates the MMR, and persists
signed checkpoints through the same backend:

```go
_, privateKey, err := ed25519.GenerateKey(rand.Reader)
if err != nil {
	return err
}
signer, err := checkpoint.NewEd25519Signer(privateKey)
if err != nil {
	return err
}

config := checkpoint.DefaultRunnerConfig("orders-prod")
config.Cadence.CadenceEntries = 100
config.WitnessIDs = []string{"witness.example"}

runner, err := checkpoint.NewRunner(config, store, signer)
if err != nil {
	return err
}
changed, err := runner.RunOnce(ctx, time.Now().UTC())
```

For a server, construct the runner once and call `runner.Run(serverContext)` in
a host-owned goroutine. `runner.Notify()` reduces latency after an append;
durable cursors and polling recover lost notifications and restarts.

Checkpoint COSE records remain byte-compatible with the TypeScript and Python
CLL implementations.

## External witnesses

The `witness` package contains a bounded HTTP client, offline RFC 9162 receipt
verification under a pinned Ed25519 authority key, and a durable retry runner:

```go
client, err := witness.NewClient(witnessBaseURL, nil, 0)
if err != nil {
	return err
}
verifier, err := witness.NewReceiptVerifier(witnessAuthorityPublicKey)
if err != nil {
	return err
}

delivery, err := witness.NewDeliveryRunner(
	witness.DefaultDeliveryConfig(),
	store,
	map[string]witness.Submitter{"witness.example": client},
	map[string]witness.Verifier{"witness.example": verifier},
)
if err != nil {
	return err
}
_, err = delivery.RunOnce(ctx, time.Now().UTC(), cll.MaxWitnesses)
```

The host provisions checkpoint keys, witness endpoints, and pinned authority
keys. Receipt verification proves registration and inclusion of the submitted
checkpoint. It does not independently prove application-record semantics.

## Add a backend

TypeScript-style structural interfaces are expressed as Go interfaces in
[`cll/types.go`](cll/types.go). A backend implements `cll.Backend`:

```go
type Backend interface {
	EntryStore
	CheckpointStateStore
	WitnessStateStore
	Close() error
}
```

A new backend must preserve:

- exact 32-byte identities and dense 1-based sequence allocation;
- duplicate append idempotence with the original timestamp;
- defensive copies at public boundaries;
- compare-and-set behavior for CLL and witness state;
- append-only nodes, checkpoint monotonicity, and stable error classes;
- millisecond UTC timestamps and portable integer bounds;
- bounded scans, witnesses, checkpoint bytes, and reason text;
- safe concurrent access and idempotent close.

For a backend contributed to this repository, reuse `internal/backend` for
portable encoding and state transitions, then run `internal/storetest.Run` in
its tests. Persistent multi-handle backends should also run
`internal/storetest.CrossHandle`. Backend-specific tests must cover reopen,
corruption or legacy-format refusal, and crash recovery.

## Continuous integration

GitHub Actions exposes separate checks for quality, unit tests, race tests,
checkpoint wire interoperability, JSONL continuation, SQLite continuation, and
MySQL continuation. Interoperability jobs build `cll-ts/main` and the Python
CLL `main` at run time. The checkout SHAs in each job log make the moving-main
comparison traceable.

## Development

```bash
gofmt -w .
go mod tidy
go vet ./...
go test ./...
go test -race ./...
```

MySQL tests use a real MySQL 8 Testcontainer. They skip locally when Docker is
unavailable, but Docker failure is a CI failure.

## License

Apache-2.0.
