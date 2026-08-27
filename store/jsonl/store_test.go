package jsonl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/internal/storetest"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/stretchr/testify/require"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T, logID string) ledger.Store {
		store, err := Open(t.TempDir(), logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestCLLValidationHasNoStateEffectsBeforeJournalCommit(t *testing.T) {
	store, err := Open(t.TempDir(), "force-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	store.cll.ForceCheckpoint = true
	mutation := ledger.CLLMutation{
		IndexedSeq: 1,
		Nodes:      []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}},
		Checkpoint: &ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: fmt.Sprintf("%064x", 1), Payload: []byte("payload"), SignedCheckpoint: []byte("statement"), CreatedAt: time.Now().UTC()},
	}
	require.NoError(t, store.validateCLL(mutation))
	require.True(t, store.cll.ForceCheckpoint)
}

func TestCLLStoreContract(t *testing.T) {
	storetest.RunCLL(t, func(t *testing.T, logID string) ledger.CLLStore {
		store, err := Open(t.TempDir(), logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestRebaselineContract(t *testing.T) {
	storetest.RunRebaseline(t, func(t *testing.T, logID string) storetest.RebaselineStore {
		store, err := Open(t.TempDir(), logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

const testID = ledger.CapsuleID("862024869f00481bb4f59d9528a45c2d4885f64c5222a9324a38ac2c2cd119f2")

func TestStoreAppendEnvelopeRestartAndGap(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "test-log")
	require.NoError(t, err)

	input := ledger.AppendInput{
		CapsuleID: testID, Capsule: []byte(`{"capsule_id":"one"}`),
		ParentID:   ledger.CapsuleID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		AppendedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	record, outcome, err := store.Append(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, ledger.AppendInserted, outcome)
	require.Equal(t, uint64(1), record.Seq)

	_, outcome, err = store.Append(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, ledger.AppendIdempotent, outcome)

	conflict := input
	conflict.Capsule = []byte(`{"capsule_id":"different"}`)
	_, _, err = store.Append(t.Context(), conflict)
	require.ErrorIs(t, err, ledger.ErrConflict)

	envelope := ledger.Envelope{Digest: "4c503ca67761e5c4aaecfe996244c25d8c0b40902d1085c85b4468bd567548c6", Bytes: []byte("envelope"), AddedAt: input.AppendedAt}
	_, addOutcome, err := store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: testID, Envelope: envelope})
	require.NoError(t, err)
	require.Equal(t, ledger.EnvelopeInserted, addOutcome)
	_, addOutcome, err = store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: testID, Envelope: envelope})
	require.NoError(t, err)
	require.Equal(t, ledger.EnvelopeIdempotent, addOutcome)

	gaps, err := store.FindChainGaps(t.Context())
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	require.NoError(t, store.Close())

	reopened, err := Open(root, "test-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	restored, err := reopened.Get(t.Context(), testID)
	require.NoError(t, err)
	require.Equal(t, record.Capsule, restored.Capsule)
	require.Len(t, restored.Envelopes, 1)
	entries, err := reopened.ScanIDs(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Equal(t, []ledger.LogEntry{{Seq: 1, CapsuleID: testID, AppendedAt: input.AppendedAt}}, entries)

	restored.Capsule[0] = 'X'
	again, err := reopened.Get(t.Context(), testID)
	require.NoError(t, err)
	require.NotEqual(t, restored.Capsule, again.Capsule)
}

func TestStoreRejectsSecondWriter(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root, "test-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })

	second, err := Open(root, "test-log")
	require.Error(t, err)
	require.Nil(t, second)
}

func TestStoreTruncatesUnterminatedTailButRejectsEarlierCorruption(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "test-log")
	require.NoError(t, err)
	_, _, err = store.Append(t.Context(), ledger.AppendInput{CapsuleID: testID, Capsule: []byte("capsule"), AppendedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	path := filepath.Join(root, journalName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(`{"version":1`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	recovered, err := Open(root, "test-log")
	require.NoError(t, err)
	require.NoError(t, recovered.Close())

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	corrupt := append([]byte("not-json\n"), content...)
	require.NoError(t, os.WriteFile(path, corrupt, 0o600))
	failed, err := Open(root, "test-log")
	require.ErrorIs(t, err, ledger.ErrCorrupt)
	require.Nil(t, failed)
}

func TestOpenRejectsVersionOneJournal(t *testing.T) {
	root := t.TempDir()
	record := []byte("{\"version\":1,\"type\":\"log.init\",\"log_id\":\"test-log\"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, journalName), record, 0o600))

	store, err := Open(root, "test-log")
	require.ErrorIs(t, err, ledger.ErrCorrupt)
	require.Nil(t, store)
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	store, err := Open(t.TempDir(), "test-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Get(ctx, testID)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestStorePersistsCLLAndWitnessState(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "test-log")
	require.NoError(t, err)
	created := time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC)
	checkpoint := ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: fmt.Sprintf("%064x", 1), Payload: []byte("payload"), SignedCheckpoint: []byte("statement"), CreatedAt: created}
	err = store.CommitCLL(t.Context(), ledger.CLLMutation{
		ExpectedIndexedSeq: 0, IndexedSeq: 1,
		Nodes:      []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}},
		Checkpoint: &checkpoint, WitnessIDs: []string{"anchor-a", "anchor-b"},
	})
	require.NoError(t, err)
	pending, err := store.PendingWitnesses(t.Context(), "anchor-a", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "anchor-a", MMRSize: 1, State: ledger.WitnessVerified, AttemptedAt: created, Receipt: []byte("receipt")}))
	require.NoError(t, store.Close())

	reopened, err := Open(root, "test-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	state, err := reopened.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(1), state.IndexedSeq)
	require.Len(t, state.Nodes, 1)
	require.Len(t, state.Checkpoints, 1)
	pending, err = reopened.PendingWitnesses(t.Context(), "anchor-a", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	pending, err = reopened.PendingWitnesses(t.Context(), "anchor-b", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

func TestStoreRebaselineRebindsJournal(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "old-log")
	require.NoError(t, err)
	created := time.Now().UTC()
	_, _, err = store.Append(t.Context(), ledger.AppendInput{CapsuleID: testID, Capsule: []byte("capsule"), AppendedAt: created})
	require.NoError(t, err)
	checkpoint := ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: fmt.Sprintf("%064x", 1), Payload: []byte("payload"), SignedCheckpoint: []byte("statement"), CreatedAt: created}
	require.NoError(t, store.CommitCLL(t.Context(), ledger.CLLMutation{IndexedSeq: 1, Nodes: []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}}, Checkpoint: &checkpoint, WitnessIDs: []string{"anchor"}}))
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "anchor", MMRSize: 1, State: ledger.WitnessContinuityConflict, Error: "fork", AttemptedAt: created}))
	_, err = store.Rebaseline(t.Context(), ledger.RebaselineInput{NewLogID: "new-log", Reason: "anchor continuity conflict", At: time.Now().UTC(), MigrationID: "migration-1"})
	require.NoError(t, err)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Equal(t, "new-log", state.LogID)
	require.True(t, state.ForceCheckpoint)
	require.NoError(t, store.Close())

	reopened, err := Open(root, "new-log")
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	old, err := Open(root, "old-log")
	require.Error(t, err)
	require.Nil(t, old)
}
