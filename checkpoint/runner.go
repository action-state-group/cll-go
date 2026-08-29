package checkpoint

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/mmr"
)

// Signer signs canonical checkpoint payloads.
type Signer interface {
	KeyID() string
	SignCheckpoint(context.Context, []byte, [][]byte, [][]byte, *mmr.ConsistencyProof) ([]byte, error)
	VerifyCheckpoint([]byte, []byte) error
}

// RunnerConfig controls durable indexing and checkpoint cadence.
type RunnerConfig struct {
	LogID        string
	Cadence      Config
	ScanLimit    int
	PollInterval time.Duration
	WitnessIDs   []string
}

// DefaultRunnerConfig returns the interoperable initial cadence.
func DefaultRunnerConfig(logID string) RunnerConfig {
	return RunnerConfig{LogID: logID, Cadence: DefaultConfig(), ScanLimit: ledger.DefaultScanLimit, PollInterval: time.Minute}
}

// Runner advances the durable CLL when explicitly run by the host.
type Runner struct {
	mu     sync.Mutex
	config RunnerConfig
	store  ledger.CheckpointStore
	signer Signer
	notify chan struct{}
}

// NewRunner validates dependencies without starting background work.
func NewRunner(config RunnerConfig, store ledger.CheckpointStore, signer Signer) (*Runner, error) {
	if ledger.ValidateIdentifier(config.LogID) != nil || store == nil || signer == nil || config.ScanLimit <= 0 || config.ScanLimit > ledger.MaxScanLimit || config.PollInterval <= 0 {
		return nil, fmt.Errorf("%w: invalid runner configuration", ledger.ErrInvalid)
	}
	if err := config.Cadence.Validate(); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(config.WitnessIDs))
	if len(config.WitnessIDs) > ledger.MaxWitnesses {
		return nil, fmt.Errorf("%w: too many witnesses", ledger.ErrInvalid)
	}
	for _, id := range config.WitnessIDs {
		if err := ledger.ValidateIdentifier(id); err != nil {
			return nil, fmt.Errorf("%w: invalid witness id", err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate witness id", ledger.ErrInvalid)
		}
		seen[id] = struct{}{}
	}
	return &Runner{config: config, store: store, signer: signer, notify: make(chan struct{}, 1)}, nil
}

func (r *Runner) Notify() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.catchUp(ctx, time.Now().UTC()); err != nil && !errors.Is(err, ledger.ErrRetryable) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.notify:
		case <-ticker.C:
		}
	}
}

// RunOnce advances durable CLL state by at most one scan batch. Its boolean is
// true for any committed progress, whether or not that progress included a
// checkpoint.
func (r *Runner) RunOnce(ctx context.Context, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now = ledger.NormalizeTime(now)
	state, err := r.store.LoadCLL(ctx)
	if err != nil {
		return false, err
	}
	if state.LogID != r.config.LogID {
		if !state.ForceCheckpoint {
			return false, fmt.Errorf("%w: store log id mismatch", ledger.ErrInvalid)
		}
		// Rebaseline atomically changes the live store binding and sets the force
		// marker. Following it here lets the explicitly running host lifecycle
		// continue without replacing the runner in a second, racy operation.
		r.config.LogID = state.LogID
	}
	nodes := make([][]byte, len(state.Nodes))
	for index, node := range state.Nodes {
		if node.Position != uint64(index) {
			return false, fmt.Errorf("%w: non-contiguous MMR nodes", ledger.ErrCorrupt)
		}
		nodes[index] = node.Hash
	}
	tree, err := mmr.New(nodes)
	if err != nil {
		return false, fmt.Errorf("restore MMR: %w", err)
	}
	leaves, ok := mmr.LeafCount(tree.Size())
	if !ok || leaves != state.IndexedSeq {
		return false, fmt.Errorf("%w: MMR size does not match indexed sequence", ledger.ErrCorrupt)
	}
	if err := r.verifyCheckpoints(state, nodes); err != nil {
		return false, err
	}
	entries, err := r.store.ScanIDs(ctx, state.IndexedSeq, r.config.ScanLimit)
	if err != nil {
		return false, err
	}
	firstUncheckpointed := state.FirstUncheckpointed
	if firstUncheckpointed.IsZero() && len(entries) > 0 {
		firstUncheckpointed = entries[0].AppendedAt
	}
	before := tree.Size()
	indexed := state.IndexedSeq
	for _, entry := range entries {
		if entry.Seq != indexed+1 {
			return false, fmt.Errorf("%w: non-contiguous ledger projection", ledger.ErrCorrupt)
		}
		if _, err := tree.AppendCapsuleID(string(entry.CapsuleID)); err != nil {
			return false, err
		}
		indexed = entry.Seq
	}
	allNodes := tree.Nodes()
	newNodes := make([]ledger.MMRNode, 0, uint64(len(allNodes))-before)
	for position := before; position < uint64(len(allNodes)); position++ {
		newNodes = append(newNodes, ledger.MMRNode{Position: position, Hash: allNodes[position]})
	}
	lastSeq := uint64(0)
	var previous *ledger.CheckpointRecord
	if len(state.Checkpoints) > 0 {
		previous = &state.Checkpoints[len(state.Checkpoints)-1]
		lastSeq = previous.IndexedSeq
	}
	uncheckpointed := indexed - lastSeq
	var cp *ledger.CheckpointRecord
	if tree.Size() > 0 && (state.ForceCheckpoint || r.config.Cadence.Due(uncheckpointed, firstUncheckpointed, now)) {
		root, err := tree.Root()
		if err != nil {
			return false, err
		}
		payload := Payload{LogID: state.LogID, KeyID: r.signer.KeyID(), MMRSize: tree.Size(), Root: hex.EncodeToString(root), Timestamp: now.UTC()}
		if previous != nil {
			payload.PrevSize = previous.MMRSize
			payload.PrevRoot = previous.Root
		}
		body, err := payload.CanonicalJSON()
		if err != nil {
			return false, err
		}
		newPeaks, err := tree.PeakHashesAt(tree.Size())
		if err != nil {
			return false, err
		}
		var prevPeaks [][]byte
		var proof *mmr.ConsistencyProof
		if previous != nil {
			prevPeaks, err = tree.PeakHashesAt(previous.MMRSize)
			if err != nil {
				return false, err
			}
			consistency, err := tree.ConsistencyProof(previous.MMRSize)
			if err != nil {
				return false, err
			}
			proof = &consistency
		}
		statement, err := r.signer.SignCheckpoint(ctx, body, newPeaks, prevPeaks, proof)
		if err != nil {
			return false, err
		}
		cp = &ledger.CheckpointRecord{IndexedSeq: indexed, MMRSize: tree.Size(), Root: payload.Root, Payload: body, SignedCheckpoint: statement, CreatedAt: now.UTC()}
		firstUncheckpointed = time.Time{}
	}
	if len(entries) == 0 && cp == nil {
		return false, nil
	}
	mutation := ledger.CLLMutation{ExpectedIndexedSeq: state.IndexedSeq, IndexedSeq: indexed, Nodes: newNodes, FirstUncheckpointed: firstUncheckpointed, Checkpoint: cp}
	if cp != nil {
		mutation.WitnessIDs = r.config.WitnessIDs
	}
	if err := r.store.CommitCLL(ctx, mutation); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Runner) verifyCheckpoints(state ledger.CLLState, nodes [][]byte) error {
	var previous *ledger.CheckpointRecord
	for index := range state.Checkpoints {
		checkpoint := &state.Checkpoints[index]
		leaves, ok := mmr.LeafCount(checkpoint.MMRSize)
		if !ok || checkpoint.MMRSize > uint64(len(nodes)) || leaves != checkpoint.IndexedSeq || checkpoint.IndexedSeq > state.IndexedSeq {
			return fmt.Errorf("%w: invalid stored checkpoint position", ledger.ErrCorrupt)
		}
		historical, err := mmr.New(nodes[:checkpoint.MMRSize])
		if err != nil {
			return fmt.Errorf("%w: restore checkpoint MMR: %v", ledger.ErrCorrupt, err)
		}
		root, err := historical.Root()
		if err != nil || hex.EncodeToString(root) != checkpoint.Root {
			return fmt.Errorf("%w: checkpoint root mismatch", ledger.ErrCorrupt)
		}
		payload, err := ParsePayload(checkpoint.Payload)
		if err != nil || payload.LogID != state.LogID || payload.KeyID != r.signer.KeyID() || payload.MMRSize != checkpoint.MMRSize || payload.Root != checkpoint.Root || !ledger.NormalizeTime(payload.Timestamp).Equal(ledger.NormalizeTime(checkpoint.CreatedAt)) {
			return fmt.Errorf("%w: checkpoint payload mismatch", ledger.ErrCorrupt)
		}
		if previous == nil {
			if payload.PrevSize != 0 || payload.PrevRoot != "" {
				return fmt.Errorf("%w: first checkpoint has a predecessor", ledger.ErrCorrupt)
			}
		} else if payload.PrevSize != previous.MMRSize || payload.PrevRoot != previous.Root {
			return fmt.Errorf("%w: checkpoint predecessor mismatch", ledger.ErrCorrupt)
		}
		if err := r.signer.VerifyCheckpoint(checkpoint.Payload, checkpoint.SignedCheckpoint); err != nil {
			return fmt.Errorf("%w: stored checkpoint signature: %v", ledger.ErrCorrupt, err)
		}
		previous = checkpoint
	}
	return nil
}

func (r *Runner) catchUp(ctx context.Context, now time.Time) error {
	for {
		changed, err := r.RunOnce(ctx, now)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
}
