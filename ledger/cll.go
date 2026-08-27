package ledger

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"
)

// MMRNode is an immutable position-addressed CLL node.
type MMRNode struct {
	Position uint64 `json:"position"`
	Hash     []byte `json:"hash"`
}

// CheckpointRecord preserves exact signed checkpoint bytes.
type CheckpointRecord struct {
	IndexedSeq       uint64    `json:"indexed_seq"`
	MMRSize          uint64    `json:"mmr_size"`
	Root             string    `json:"root"`
	Payload          []byte    `json:"payload"`
	SignedCheckpoint []byte    `json:"signed_checkpoint"`
	CreatedAt        time.Time `json:"created_at"`
}

// CLLState is the complete restartable state for one active log.
type CLLState struct {
	LogID               string             `json:"log_id"`
	IndexedSeq          uint64             `json:"indexed_seq"`
	Nodes               []MMRNode          `json:"nodes"`
	FirstUncheckpointed time.Time          `json:"first_uncheckpointed,omitempty"`
	Checkpoints         []CheckpointRecord `json:"checkpoints,omitempty"`
	ForceCheckpoint     bool               `json:"force_checkpoint,omitempty"`
}

// CLLMutation atomically advances nodes, cursor, checkpoint, and deliveries.
type CLLMutation struct {
	ExpectedIndexedSeq  uint64
	IndexedSeq          uint64
	Nodes               []MMRNode
	FirstUncheckpointed time.Time
	Checkpoint          *CheckpointRecord
	WitnessIDs          []string
}

// Normalized returns a mutation with portable persisted timestamp precision.
func (m CLLMutation) Normalized() CLLMutation {
	m.FirstUncheckpointed = NormalizeTime(m.FirstUncheckpointed)
	if m.Checkpoint != nil {
		checkpoint := *m.Checkpoint
		checkpoint.CreatedAt = NormalizeTime(checkpoint.CreatedAt)
		m.Checkpoint = &checkpoint
	}
	return m
}

// Validate checks backend-independent mutation invariants.
func (m CLLMutation) Validate() error {
	if m.IndexedSeq < m.ExpectedIndexedSeq {
		return fmt.Errorf("%w: CLL cursor moved backwards", ErrInvalid)
	}
	for _, node := range m.Nodes {
		if len(node.Hash) != 32 {
			return fmt.Errorf("%w: MMR node hash must be 32 bytes", ErrInvalid)
		}
	}
	if m.Checkpoint == nil {
		if len(m.WitnessIDs) != 0 {
			return fmt.Errorf("%w: witnesses require a checkpoint", ErrInvalid)
		}
		return nil
	}
	cp := m.Checkpoint
	root, err := hex.DecodeString(cp.Root)
	if cp.MMRSize == 0 || cp.IndexedSeq != m.IndexedSeq || err != nil || len(root) != 32 || hex.EncodeToString(root) != cp.Root || len(cp.Payload) == 0 || len(cp.Payload) > MaxCheckpointPayloadBytes || len(cp.SignedCheckpoint) == 0 || len(cp.SignedCheckpoint) > MaxSignedCheckpointBytes || cp.CreatedAt.IsZero() || !m.FirstUncheckpointed.IsZero() {
		return fmt.Errorf("%w: invalid checkpoint mutation", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(m.WitnessIDs))
	if len(m.WitnessIDs) > MaxWitnesses {
		return fmt.Errorf("%w: too many witnesses", ErrInvalid)
	}
	for _, id := range m.WitnessIDs {
		if err := ValidateIdentifier(id); err != nil {
			return fmt.Errorf("%w: invalid witness id", err)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate witness id", ErrInvalid)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// WitnessState classifies one durable delivery attempt.
type WitnessState string

const (
	WitnessPending            WitnessState = "pending"
	WitnessRetryable          WitnessState = "retryable"
	WitnessVerified           WitnessState = "verified"
	WitnessContinuityConflict WitnessState = "continuity_conflict"
	WitnessPermanentFailure   WitnessState = "permanent_failure"
)

// PendingWitness describes a checkpoint's per-witness delivery state.
type PendingWitness struct {
	WitnessID     string
	Checkpoint    CheckpointRecord
	State         WitnessState
	Attempts      uint64
	LastAttemptAt time.Time
	NextAttemptAt time.Time
	LastError     string
	Receipt       []byte
}

// WitnessResult commits one classified delivery attempt.
type WitnessResult struct {
	WitnessID     string
	MMRSize       uint64
	State         WitnessState
	Receipt       []byte
	AttemptedAt   time.Time
	NextAttemptAt time.Time
	Error         string
}

// Normalized returns a witness result with portable timestamp precision.
func (r WitnessResult) Normalized() WitnessResult {
	r.AttemptedAt = NormalizeTime(r.AttemptedAt)
	r.NextAttemptAt = NormalizeTime(r.NextAttemptAt)
	return r
}

// Validate checks a durable witness outcome before a backend commits it.
func (r WitnessResult) Validate() error {
	if ValidateIdentifier(r.WitnessID) != nil || r.MMRSize == 0 || r.AttemptedAt.IsZero() {
		return fmt.Errorf("%w: witness id, MMR size, and attempt time are required", ErrInvalid)
	}
	switch r.State {
	case WitnessRetryable:
		if r.Error == "" || len(r.Error) > MaxReasonBytes || len(r.Receipt) != 0 || r.NextAttemptAt.IsZero() || !r.NextAttemptAt.After(r.AttemptedAt) {
			return fmt.Errorf("%w: invalid retryable witness result", ErrInvalid)
		}
	case WitnessVerified:
		if len(r.Receipt) == 0 || len(r.Receipt) > MaxWitnessReceiptBytes || r.Error != "" || !r.NextAttemptAt.IsZero() {
			return fmt.Errorf("%w: invalid verified witness result", ErrInvalid)
		}
	case WitnessContinuityConflict, WitnessPermanentFailure:
		if r.Error == "" || len(r.Error) > MaxReasonBytes || len(r.Receipt) != 0 || !r.NextAttemptAt.IsZero() {
			return fmt.Errorf("%w: invalid terminal witness result", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: invalid witness state", ErrInvalid)
	}
	return nil
}

// CLLStore persists CLL and independent witness-delivery state.
type CLLStore interface {
	LoadCLL(context.Context) (CLLState, error)
	CommitCLL(context.Context, CLLMutation) error
	PendingWitnesses(context.Context, string, int) ([]PendingWitness, error)
	GetWitness(context.Context, string, uint64) (PendingWitness, error)
	CommitWitness(context.Context, WitnessResult) error
}

// RebaselineInput authorizes one explicit continuity migration.
type RebaselineInput struct {
	NewLogID    string
	Reason      string
	At          time.Time
	MigrationID string
}

// Normalized returns rebaseline metadata with portable timestamp precision.
func (r RebaselineInput) Normalized() RebaselineInput {
	r.At = NormalizeTime(r.At)
	return r
}

// Validate checks explicit operator rebaseline metadata.
func (r RebaselineInput) Validate() error {
	if ValidateIdentifier(r.NewLogID) != nil || ValidateIdentifier(r.MigrationID) != nil || len(r.Reason) == 0 || len(r.Reason) > MaxReasonBytes || r.At.IsZero() {
		return fmt.Errorf("%w: invalid rebaseline metadata", ErrInvalid)
	}
	return nil
}

// RebaselineRecord is the durable audit record of a log-ID migration.
type RebaselineRecord struct {
	OldLogID          string    `json:"old_log_id"`
	NewLogID          string    `json:"new_log_id"`
	Reason            string    `json:"reason"`
	At                time.Time `json:"at"`
	MigrationID       string    `json:"migration_id"`
	LastWitnessedSize uint64    `json:"last_witnessed_size"`
}

// Rebaseliner exposes the optional operator-only migration capability.
type Rebaseliner interface {
	Rebaseline(context.Context, RebaselineInput) (RebaselineRecord, error)
	Rebaselines(context.Context, int) ([]RebaselineRecord, error)
}
