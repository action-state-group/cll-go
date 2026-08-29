// Package storetest defines the behavior shared by every ledger Store backend.
package storetest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/action-state-group/agent-action-capsule/go/canonical"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	producer "github.com/ethanyzhang/capsule-producer-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validCapsule = `{"action_id":"v4-chain","action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"not_applicable","ledger_mode":"chained"},"canonicalization_id":"jcs","capsule_id":"862024869f00481bb4f59d9528a45c2d4885f64c5222a9324a38ac2c2cd119f2","chain":{"parent_capsule_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","relation":"confirms"},"constraints":[],"developer":"agent@v1","disposition":{"approver":"policy","decision":"accept","human_disposed":false,"verdict_class":"executed"},"format_version":"4","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-04","timestamp":"2026-08-24T00:00:00Z"}`

type Factory func(t *testing.T, logID string) ledger.Store
type CLLFactory func(t *testing.T, logID string) ledger.CLLStore
type RebaselineStore interface {
	ledger.Store
	ledger.Rebaseliner
}
type RebaselineFactory func(t *testing.T, logID string) RebaselineStore

func Run(t *testing.T, open Factory) {
	t.Helper()
	t.Run("append read idempotency and conflict", func(t *testing.T) {
		store := open(t, "contract-basic")
		input := appendInput(1)
		record, outcome, err := store.Append(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, ledger.AppendInserted, outcome)
		assert.Equal(t, uint64(1), record.Seq)
		stored, err := store.Get(t.Context(), input.CapsuleID)
		require.NoError(t, err)
		assert.Equal(t, record, stored)

		_, outcome, err = store.Append(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, ledger.AppendIdempotent, outcome)
		conflict := input
		conflict.Capsule = []byte("different")
		_, _, err = store.Append(t.Context(), conflict)
		assert.ErrorIs(t, err, ledger.ErrConflict)

		stored.Capsule[0] ^= 0xff
		stored.Verification.Assurance["effect_mode"] = "tampered"
		*stored.Verification.Findings[0].Check = 99
		again, err := store.Get(t.Context(), input.CapsuleID)
		require.NoError(t, err)
		assert.Equal(t, input.Capsule, again.Capsule)
		assert.Equal(t, "confirmed", again.Verification.Assurance["effect_mode"])
		assert.Equal(t, 1, *again.Verification.Findings[0].Check)
	})

	t.Run("envelope scan projections and gaps", func(t *testing.T) {
		store := open(t, "contract-query")
		first := appendInput(1)
		first.ParentID = capsuleID(99)
		_, _, err := store.Append(t.Context(), first)
		require.NoError(t, err)
		_, _, err = store.Append(t.Context(), appendInput(2))
		require.NoError(t, err)

		envelope := ledger.Envelope{Digest: "4c503ca67761e5c4aaecfe996244c25d8c0b40902d1085c85b4468bd567548c6", Bytes: []byte("envelope"), AddedAt: first.AppendedAt}
		_, outcome, err := store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: first.CapsuleID, Envelope: envelope})
		require.NoError(t, err)
		assert.Equal(t, ledger.EnvelopeInserted, outcome)
		retry := envelope
		retry.AddedAt = envelope.AddedAt.Add(time.Hour)
		retry.Verification.OK = true
		storedEnvelope, outcome, err := store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: first.CapsuleID, Envelope: retry})
		require.NoError(t, err)
		assert.Equal(t, ledger.EnvelopeIdempotent, outcome)
		assert.Equal(t, envelope, storedEnvelope)
		invalidEnvelope := envelope
		invalidEnvelope.Digest = ledger.EnvelopeDigest(fmt.Sprintf("%064x", 10))
		_, _, err = store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: first.CapsuleID, Envelope: invalidEnvelope})
		assert.ErrorIs(t, err, ledger.ErrInvalid)
		_, _, err = store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: capsuleID(99), Envelope: envelope})
		assert.ErrorIs(t, err, ledger.ErrNotFound)
		replayed, appendOutcome, err := store.Append(t.Context(), first)
		require.NoError(t, err)
		assert.Equal(t, ledger.AppendIdempotent, appendOutcome)
		require.Len(t, replayed.Envelopes, 1)

		records, err := store.Scan(t.Context(), 0, 1)
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Equal(t, uint64(1), records[0].Seq)
		entries, err := store.ScanIDs(t.Context(), 1, 10)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, uint64(2), entries[0].Seq)
		assert.Equal(t, appendInput(2).CapsuleID, entries[0].CapsuleID)
		gaps, err := store.FindChainGaps(t.Context())
		require.NoError(t, err)
		require.Len(t, gaps, 1)
		assert.Equal(t, first.CapsuleID, gaps[0].CapsuleID)
		_, err = store.Scan(t.Context(), 0, 0)
		assert.ErrorIs(t, err, ledger.ErrInvalid)
		_, err = store.ScanIDs(t.Context(), 0, 0)
		assert.ErrorIs(t, err, ledger.ErrInvalid)
	})

	t.Run("concurrent sequence allocation", func(t *testing.T) {
		store := open(t, "contract-concurrent")
		const count = 16
		var wg sync.WaitGroup
		errors := make(chan error, count)
		for index := 1; index <= count; index++ {
			input := appendInput(index)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := store.Append(t.Context(), input)
				errors <- err
			}()
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			require.NoError(t, err)
		}
		entries, err := store.ScanIDs(t.Context(), 0, count+1)
		require.NoError(t, err)
		require.Len(t, entries, count)
		for index, entry := range entries {
			assert.Equal(t, uint64(index+1), entry.Seq)
		}
	})

	t.Run("portable timestamp precision", func(t *testing.T) {
		store := open(t, "contract-time")
		input := appendInput(1)
		input.AppendedAt = input.AppendedAt.Add(1234 * time.Nanosecond)
		record, _, err := store.Append(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, ledger.NormalizeTime(input.AppendedAt), record.AppendedAt)
		envelope := ledger.Envelope{Digest: "4c503ca67761e5c4aaecfe996244c25d8c0b40902d1085c85b4468bd567548c6", Bytes: []byte("envelope"), AddedAt: input.AppendedAt}
		stored, _, err := store.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: input.CapsuleID, Envelope: envelope})
		require.NoError(t, err)
		assert.Equal(t, ledger.NormalizeTime(input.AppendedAt), stored.AddedAt)
	})

	t.Run("close is idempotent and terminal", func(t *testing.T) {
		store := open(t, "contract-close")
		require.NoError(t, store.Close())
		require.NoError(t, store.Close())
		_, err := store.Get(t.Context(), capsuleID(1))
		assert.ErrorIs(t, err, ledger.ErrClosed)
	})
}

// RunAdmission applies the five shared Python/Go admission cases to a Store
// backend through the public Service boundary.
func RunAdmission(t *testing.T, open Factory) {
	t.Helper()
	capsule := []byte(validCapsule)
	validEnvelope := producerEnvelope(t, capsule)
	wrongCapsule := mutateCapsule(t, capsule, "v4-chain-wrong-envelope")
	wrongEnvelope := producerEnvelope(t, wrongCapsule)

	t.Run("declared unsigned records explicit state", func(t *testing.T) {
		store := open(t, "admission-unsigned")
		service, err := ledger.New(store, ledger.Config{})
		require.NoError(t, err)
		record, err := service.Append(t.Context(), ledger.AdmissionUnsigned, capsule)
		require.NoError(t, err)
		assert.Equal(t, ledger.AuthenticityUnsigned, record.Authenticity)
		assert.Empty(t, record.Envelopes)
	})

	t.Run("declared signed missing envelope rejects before mutation", func(t *testing.T) {
		store := open(t, "admission-missing")
		service, err := ledger.New(store, ledger.Config{})
		require.NoError(t, err)
		_, err = service.Append(t.Context(), ledger.AdmissionSigned, capsule)
		require.ErrorIs(t, err, ledger.ErrAdmission)
		assertEmptyStore(t, store)
	})

	t.Run("declared signed invalid envelope rejects before mutation", func(t *testing.T) {
		store := open(t, "admission-invalid")
		service, err := ledger.New(store, ledger.Config{})
		require.NoError(t, err)
		_, err = service.Append(t.Context(), ledger.AdmissionSigned, capsule, wrongEnvelope)
		require.ErrorIs(t, err, ledger.ErrAdmission)
		assertEmptyStore(t, store)
	})

	t.Run("declared signed mixed valid and invalid envelopes rejects before mutation", func(t *testing.T) {
		store := open(t, "admission-mixed")
		service, err := ledger.New(store, ledger.Config{})
		require.NoError(t, err)
		_, err = service.Append(t.Context(), ledger.AdmissionSigned, capsule, validEnvelope, wrongEnvelope)
		require.ErrorIs(t, err, ledger.ErrAdmission)
		require.ErrorIs(t, err, ledger.ErrInvalid)
		assertEmptyStore(t, store)
	})

	t.Run("declared signed valid envelope persists and reverifies", func(t *testing.T) {
		const logID = "admission-valid"
		store := open(t, logID)
		service, err := ledger.New(store, ledger.Config{})
		require.NoError(t, err)
		record, err := service.Append(t.Context(), ledger.AdmissionSigned, capsule, validEnvelope)
		require.NoError(t, err)
		assert.Equal(t, ledger.AuthenticitySigned, record.Authenticity)
		require.Len(t, record.Envelopes, 1)
		assert.Equal(t, validEnvelope, record.Envelopes[0].Bytes)
		require.NoError(t, store.Close())
		reopened := open(t, logID)
		stored, err := reopened.Get(t.Context(), record.CapsuleID)
		require.NoError(t, err)
		require.Len(t, stored.Envelopes, 1)
		verification, err := (ledger.AACVerifier{}).VerifyEnvelope(stored.CapsuleID, stored.Envelopes[0].Bytes)
		require.NoError(t, err)
		assert.True(t, verification.OK)
		assert.Equal(t, stored.Envelopes[0].Verification.PublicKey, verification.PublicKey)
	})

}

func assertEmptyStore(t *testing.T, store ledger.Store) {
	t.Helper()
	entries, err := store.ScanIDs(t.Context(), 0, 1)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func producerEnvelope(t *testing.T, capsule []byte) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	identity, err := producer.NewEd25519SigningIdentity(privateKey)
	require.NoError(t, err)
	_, capsuleID, err := (ledger.AACVerifier{}).VerifyCapsule(capsule)
	require.NoError(t, err)
	envelope, err := producer.Sign(producer.BuiltPayload{CapsuleID: string(capsuleID), JSON: capsule}, identity)
	require.NoError(t, err)
	return envelope
}

func mutateCapsule(t *testing.T, capsule []byte, actionID string) []byte {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(capsule, &value))
	value["action_id"] = actionID
	id, err := canonical.ComputeCapsuleID(value)
	require.NoError(t, err)
	value["capsule_id"] = id
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func RunCLL(t *testing.T, open CLLFactory) {
	t.Helper()
	store := open(t, "contract-cll")
	created := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	cp := ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: fmt.Sprintf("%064x", 1), Payload: []byte("payload"), SignedCheckpoint: []byte("statement"), CreatedAt: created}
	err := store.CommitCLL(t.Context(), ledger.CLLMutation{ExpectedIndexedSeq: 0, IndexedSeq: 1, Nodes: []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}}, Checkpoint: &cp, WitnessIDs: []string{"a", "b"}})
	require.NoError(t, err)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), state.IndexedSeq)
	require.Len(t, state.Nodes, 1)
	require.Len(t, state.Checkpoints, 1)

	err = store.CommitCLL(t.Context(), ledger.CLLMutation{ExpectedIndexedSeq: 0, IndexedSeq: 2, Nodes: []ledger.MMRNode{{Position: 1, Hash: make([]byte, 32)}}})
	assert.ErrorIs(t, err, ledger.ErrConflict)
	state, err = store.LoadCLL(t.Context())
	require.NoError(t, err)
	require.Len(t, state.Nodes, 1)

	pending, err := store.PendingWitnesses(t.Context(), "a", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "a", MMRSize: 1, State: ledger.WitnessVerified, Receipt: []byte("receipt"), AttemptedAt: created}))
	delivery, err := store.GetWitness(t.Context(), "a", 1)
	require.NoError(t, err)
	assert.Equal(t, ledger.WitnessVerified, delivery.State)
	assert.Equal(t, uint64(1), delivery.Attempts)
	assert.Equal(t, created, delivery.LastAttemptAt)
	assert.Equal(t, []byte("receipt"), delivery.Receipt)
	pending, err = store.PendingWitnesses(t.Context(), "a", 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
	pending, err = store.PendingWitnesses(t.Context(), "b", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	secondCheckpoint := ledger.CheckpointRecord{IndexedSeq: 2, MMRSize: 3, Root: fmt.Sprintf("%064x", 2), Payload: []byte("payload-2"), SignedCheckpoint: []byte("statement-2"), CreatedAt: created.Add(time.Minute)}
	require.NoError(t, store.CommitCLL(t.Context(), ledger.CLLMutation{ExpectedIndexedSeq: 1, IndexedSeq: 2, Nodes: []ledger.MMRNode{{Position: 1, Hash: make([]byte, 32)}, {Position: 2, Hash: make([]byte, 32)}}, Checkpoint: &secondCheckpoint, WitnessIDs: []string{"b"}}))
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "b", MMRSize: 1, State: ledger.WitnessContinuityConflict, Error: "fork", AttemptedAt: created}))
	pending, err = store.PendingWitnesses(t.Context(), "b", 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
	err = store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "b", MMRSize: 1, State: ledger.WitnessVerified, Receipt: []byte("late"), AttemptedAt: created.Add(time.Minute)})
	assert.ErrorIs(t, err, ledger.ErrConflict)
}

func RunRebaseline(t *testing.T, open RebaselineFactory) {
	t.Helper()
	store := open(t, "old-log")
	input := appendInput(1)
	_, _, err := store.Append(t.Context(), input)
	require.NoError(t, err)
	cp := ledger.CheckpointRecord{IndexedSeq: 1, MMRSize: 1, Root: fmt.Sprintf("%064x", 1), Payload: []byte("payload"), SignedCheckpoint: []byte("statement"), CreatedAt: input.AppendedAt}
	require.NoError(t, store.CommitCLL(t.Context(), ledger.CLLMutation{ExpectedIndexedSeq: 0, IndexedSeq: 1, Nodes: []ledger.MMRNode{{Position: 0, Hash: make([]byte, 32)}}, Checkpoint: &cp, WitnessIDs: []string{"anchor", "anchor-conflict"}}))
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "anchor", MMRSize: 1, State: ledger.WitnessVerified, Receipt: []byte("receipt"), AttemptedAt: input.AppendedAt}))
	require.NoError(t, store.CommitWitness(t.Context(), ledger.WitnessResult{WitnessID: "anchor-conflict", MMRSize: 1, State: ledger.WitnessContinuityConflict, Error: "fork", AttemptedAt: input.AppendedAt}))
	record, err := store.Rebaseline(t.Context(), ledger.RebaselineInput{NewLogID: "new-log", Reason: "continuity conflict", At: input.AppendedAt.Add(time.Hour), MigrationID: "migration-1"})
	require.NoError(t, err)
	assert.Equal(t, "old-log", record.OldLogID)
	assert.Equal(t, "new-log", record.NewLogID)
	assert.Equal(t, uint64(1), record.LastWitnessedSize)
	history, err := store.Rebaselines(t.Context(), 10)
	require.NoError(t, err)
	require.Equal(t, []ledger.RebaselineRecord{record}, history)
	_, err = store.Rebaselines(t.Context(), 0)
	assert.ErrorIs(t, err, ledger.ErrInvalid)
	state, err := store.LoadCLL(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "new-log", state.LogID)
	assert.True(t, state.ForceCheckpoint)
	assert.Empty(t, state.Checkpoints)
	require.Len(t, state.Nodes, 1)
	stored, err := store.Get(t.Context(), input.CapsuleID)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), stored.Seq)
	second := appendInput(2)
	stored, _, err = store.Append(t.Context(), second)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), stored.Seq)
	_, err = store.Rebaseline(t.Context(), ledger.RebaselineInput{NewLogID: "old-log", Reason: "must collide", At: input.AppendedAt.Add(2 * time.Hour), MigrationID: "migration-2"})
	assert.ErrorIs(t, err, ledger.ErrInvalid)
}

func appendInput(number int) ledger.AppendInput {
	check := 1
	return ledger.AppendInput{
		CapsuleID:    capsuleID(number),
		Capsule:      []byte(fmt.Sprintf(`{"capsule":%d}`, number)),
		Authenticity: ledger.AuthenticityUnsigned,
		Verification: ledger.VerificationResult{OK: true, CapsuleID: capsuleID(number), Assurance: map[string]string{"effect_mode": "confirmed"}, Findings: []ledger.Finding{{Code: "test", Check: &check}}},
		AppendedAt:   time.Date(2026, 8, 25, 1, 2, number%60, 0, time.UTC),
	}
}

func capsuleID(number int) ledger.CapsuleID {
	return ledger.CapsuleID(fmt.Sprintf("%064x", number))
}
