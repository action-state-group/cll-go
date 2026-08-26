package checkpoint_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/checkpoint"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/store/jsonl"
	"github.com/stretchr/testify/require"
)

func TestRunnerCreatesDurableCheckpointAtEntryCadence(t *testing.T) {
	store, signer := runnerFixture(t, "entry-log")
	started := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	appendCapsules(t, store, started, 2)
	config := checkpoint.DefaultRunnerConfig("entry-log")
	config.Cadence.CadenceEntries = 2
	config.WitnessIDs = []string{"anchor"}
	runner, err := checkpoint.NewRunner(config, store, signer)
	require.NoError(t, err)
	changed, err := runner.RunOnce(t.Context(), started.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, changed)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(2), state.IndexedSeq)
	require.Len(t, state.Checkpoints, 1)
	require.NoError(t, signer.VerifyCheckpoint(state.Checkpoints[0].Payload, state.Checkpoints[0].SignedStatement))
	pending, err := store.PendingWitnesses(t.Context(), "anchor", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

func TestRunnerForcesFirstSeenCheckpointAfterRebaseline(t *testing.T) {
	store, signer := runnerFixture(t, "old-log")
	started := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)
	appendCapsules(t, store, started, 1)
	oldConfig := checkpoint.DefaultRunnerConfig("old-log")
	oldConfig.Cadence.CadenceEntries = 1
	oldConfig.WitnessIDs = []string{"anchor"}
	oldRunner, err := checkpoint.NewRunner(oldConfig, store, signer)
	require.NoError(t, err)
	_, err = oldRunner.RunOnce(t.Context(), started)
	require.NoError(t, err)
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "anchor", MMRSize: 1, State: ledger.WitnessContinuityConflict, Error: "fork", AttemptedAt: started}))
	_, err = store.Rebaseline(t.Context(), ledger.RebaselineInput{NewLogID: "new-log", Reason: "continuity conflict", At: started.Add(time.Hour), MigrationID: "migration-1"})
	require.NoError(t, err)
	changed, err := oldRunner.RunOnce(t.Context(), started.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, changed)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Len(t, state.Checkpoints, 1)
	var payload struct {
		PrevSize uint64 `json:"prev_size"`
		PrevRoot string `json:"prev_root"`
		LogID    string `json:"log_id"`
	}
	require.NoError(t, json.Unmarshal(state.Checkpoints[0].Payload, &payload))
	require.Zero(t, payload.PrevSize)
	require.Empty(t, payload.PrevRoot)
	require.Equal(t, "new-log", payload.LogID)
}

func TestRunnerPersistsAndEnforcesAgeCadenceWithoutNewEntry(t *testing.T) {
	store, signer := runnerFixture(t, "age-log")
	started := time.Date(2026, 8, 25, 5, 0, 0, 0, time.UTC)
	appendCapsules(t, store, started, 1)
	config := checkpoint.DefaultRunnerConfig("age-log")
	runner, err := checkpoint.NewRunner(config, store, signer)
	require.NoError(t, err)
	changed, err := runner.RunOnce(t.Context(), started)
	require.NoError(t, err)
	require.True(t, changed)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Empty(t, state.Checkpoints)
	require.Equal(t, started, state.FirstUncheckpointed)

	changed, err = runner.RunOnce(t.Context(), started.Add(15*time.Minute))
	require.NoError(t, err)
	require.True(t, changed)
	state, err = store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Len(t, state.Checkpoints, 1)
	require.True(t, state.FirstUncheckpointed.IsZero())
}

func TestRunnerRejectsTamperedPersistedCheckpoint(t *testing.T) {
	store, signer := runnerFixture(t, "tamper-log")
	started := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	appendCapsules(t, store, started, 1)
	config := checkpoint.DefaultRunnerConfig("tamper-log")
	config.Cadence.CadenceEntries = 1
	runner, err := checkpoint.NewRunner(config, store, signer)
	require.NoError(t, err)
	_, err = runner.RunOnce(t.Context(), started)
	require.NoError(t, err)

	corrupt := tamperedCheckpointStore{CheckpointStore: store}
	tamperedRunner, err := checkpoint.NewRunner(config, corrupt, signer)
	require.NoError(t, err)
	_, err = tamperedRunner.RunOnce(t.Context(), started.Add(time.Minute))
	require.ErrorIs(t, err, ledger.ErrCorrupt)
}

type tamperedCheckpointStore struct{ ledger.CheckpointStore }

func (s tamperedCheckpointStore) LoadCLL(ctx context.Context) (ledger.CLLState, error) {
	state, err := s.CheckpointStore.LoadCLL(ctx)
	if err == nil && len(state.Checkpoints) > 0 {
		state.Checkpoints[0].SignedStatement[0] ^= 1
	}
	return state, err
}

func runnerFixture(t *testing.T, logID string) (*jsonl.Store, *checkpoint.Ed25519Signer) {
	t.Helper()
	store, err := jsonl.Open(t.TempDir(), logID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := checkpoint.NewEd25519Signer("test-key", private)
	require.NoError(t, err)
	return store, signer
}

func appendCapsules(t *testing.T, store ledger.Store, started time.Time, count int) {
	t.Helper()
	for number := 1; number <= count; number++ {
		_, _, err := store.Append(t.Context(), ledger.AppendInput{
			CapsuleID:  ledger.CapsuleID(fmt.Sprintf("%064x", number)),
			Capsule:    []byte(fmt.Sprintf(`{"capsule":%d}`, number)),
			AppendedAt: started.Add(time.Duration(number-1) * time.Second),
		})
		require.NoError(t, err)
	}
}
