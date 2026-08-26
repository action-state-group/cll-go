package ledger

import "context"

// LogSource exposes only the ordered projection needed for CLL indexing.
type LogSource interface {
	ScanIDs(context.Context, uint64, int) ([]LogEntry, error)
}

// CheckpointStore combines the narrow index projection with durable CLL state.
type CheckpointStore interface {
	LogSource
	CLLStore
}

// Store combines the application data path and durable CLL state.
type Store interface {
	CheckpointStore
	Append(context.Context, AppendInput) (Record, AppendOutcome, error)
	AddEnvelope(context.Context, EnvelopeInput) (Envelope, AddOutcome, error)
	Get(context.Context, CapsuleID) (Record, error)
	Scan(context.Context, uint64, int) ([]Record, error)
	FindChainGaps(context.Context) ([]ChainGap, error)
	Close() error
}
