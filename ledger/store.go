package ledger

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/action-state-group/cll-go/cll"
)

// ProjectCLLEntries decodes verified AAC Capsule IDs into the generic CLL leaf
// values consumed by checkpointing.
func ProjectCLLEntries(entries []LogEntry) ([]cll.Entry, error) {
	result := make([]cll.Entry, 0, len(entries))
	for _, entry := range entries {
		if !validID(entry.CapsuleID) {
			return nil, fmt.Errorf("%w: invalid Capsule ID for CLL", ErrCorrupt)
		}
		value, err := hex.DecodeString(string(entry.CapsuleID))
		if err != nil {
			return nil, fmt.Errorf("%w: decode Capsule ID for CLL: %v", ErrCorrupt, err)
		}
		result = append(result, cll.Entry{
			Seq:        entry.Seq,
			Value:      value,
			AppendedAt: entry.AppendedAt,
		})
	}
	return result, nil
}

// LogSource exposes only the ordered projection needed for CLL indexing.
type LogSource interface {
	ScanIDs(context.Context, uint64, int) ([]LogEntry, error)
	cll.Source
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
