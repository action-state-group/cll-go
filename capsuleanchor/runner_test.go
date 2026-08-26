package capsuleanchor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/store/jsonl"
	"github.com/stretchr/testify/require"
)

type fakeSubmitter struct {
	receipt Receipt
	err     error
}

func (f fakeSubmitter) Submit(context.Context, []byte) (Receipt, error) { return f.receipt, f.err }

type fakeVerifier struct{ err error }

func (f fakeVerifier) Verify([]byte, Receipt) error { return f.err }

func TestDeliveryRunnerPersistsRetryAndContinuityStates(t *testing.T) {
	store := pendingStore(t, "delivery-log")
	config := DefaultDeliveryConfig("anchor")
	config.Jitter = false
	runner, err := NewDeliveryRunner(config, store, fakeSubmitter{err: &HTTPError{StatusCode: http.StatusTooManyRequests, Body: "slow"}}, fakeVerifier{})
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 6, 0, 0, 0, time.UTC)
	changed, err := runner.RunOnce(t.Context(), now)
	require.NoError(t, err)
	require.True(t, changed)
	pending, err := store.PendingWitnesses(t.Context(), "anchor", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, ledger.WitnessRetryable, pending[0].State)
	require.Equal(t, now.Add(time.Second), pending[0].NextAttemptAt)

	runner, err = NewDeliveryRunner(config, store, fakeSubmitter{err: &HTTPError{StatusCode: http.StatusConflict, Body: "fork"}}, fakeVerifier{})
	require.NoError(t, err)
	changed, err = runner.RunOnce(t.Context(), now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	pending, err = store.PendingWitnesses(t.Context(), "anchor", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestDeliveryRunnerVerifiesBeforeCompletion(t *testing.T) {
	store := pendingStore(t, "verified-log")
	config := DefaultDeliveryConfig("anchor")
	runner, err := NewDeliveryRunner(config, store, fakeSubmitter{receipt: Receipt{Bytes: []byte("receipt")}}, fakeVerifier{})
	require.NoError(t, err)
	changed, err := runner.RunOnce(t.Context(), time.Now().UTC())
	require.NoError(t, err)
	require.True(t, changed)
	pending, err := store.PendingWitnesses(t.Context(), "anchor", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestDeliveryRunnerPersistsInvalidReceiptAsPermanentFailure(t *testing.T) {
	store := pendingStore(t, "invalid-receipt-log")
	config := DefaultDeliveryConfig("anchor")
	runner, err := NewDeliveryRunner(config, store,
		fakeSubmitter{receipt: Receipt{Bytes: []byte("untrusted")}}, fakeVerifier{err: errors.New("bad authority signature")})
	require.NoError(t, err)
	changed, err := runner.RunOnce(t.Context(), time.Now().UTC())
	require.NoError(t, err)
	require.True(t, changed)
	stored, err := store.GetWitness(t.Context(), "anchor", 1)
	require.NoError(t, err)
	require.Equal(t, ledger.WitnessPermanentFailure, stored.State)
	require.ErrorContains(t, errors.New(stored.LastError), "bad authority signature")
}

func pendingStore(t *testing.T, logID string) *jsonl.Store {
	t.Helper()
	store, err := jsonl.Open(t.TempDir(), logID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	cp := ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: strings.Repeat("0", 64), Payload: []byte("payload"), SignedStatement: []byte("statement"), CreatedAt: time.Now().UTC()}
	err = store.CommitCLL(t.Context(), ledger.CLLMutation{ExpectedIndexedSeq: 0, IndexedSeq: 1, Nodes: []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}}, Checkpoint: &cp, WitnessIDs: []string{"anchor"}})
	require.NoError(t, err)
	return store
}
