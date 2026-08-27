package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckpointCanonicalSignAndVerify(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := NewEd25519Signer(private)
	require.NoError(t, err)
	payload, err := (Payload{
		LogID: "alchemy", KeyID: signer.KeyID(), MMRSize: 7,
		Root:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Timestamp: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}).CanonicalJSON()
	require.NoError(t, err)
	require.NotContains(t, string(payload), `"artifact_type"`)
	require.Contains(t, string(payload), `"kind":"mmr_checkpoint"`)
	require.Contains(t, string(payload), `"v":1`)
	parsed, err := ParsePayload(payload)
	require.NoError(t, err)
	require.Equal(t, uint64(7), parsed.MMRSize)

	statement, err := signer.SignCheckpoint(t.Context(), payload)
	require.NoError(t, err)
	record, err := ParseRecord(statement)
	require.NoError(t, err)
	require.Equal(t, signer.KeyID(), record.KeyID)
	require.NoError(t, record.VerifySignature())
	require.NoError(t, signer.VerifyCheckpoint(payload, statement))
	tampered := bytes.Replace(payload, []byte("alchemy"), []byte("changed"), 1)
	require.Error(t, signer.VerifyCheckpoint(tampered, statement))
}

func TestCapsuleEmitCheckpointVector(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index)
	}
	signer, err := NewEd25519Signer(ed25519.NewKeyFromSeed(seed))
	require.NoError(t, err)
	require.Equal(t, "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8", signer.KeyID())

	payload, err := (Payload{
		LogID: "interop-log", KeyID: signer.KeyID(), MMRSize: 7,
		Root:     "abababababababababababababababababababababababababababababababab",
		PrevSize: 3, PrevRoot: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd",
		Timestamp: time.Date(2026, 8, 27, 12, 34, 56, 0, time.UTC),
	}).CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, `{"key_id":"03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8","kind":"mmr_checkpoint","log_id":"interop-log","mmr_size":7,"prev_root":"cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd","prev_size":3,"root":"abababababababababababababababababababababababababababababababab","timestamp":"2026-08-27T12:34:56Z","v":1}`, string(payload))
	parsed, err := ParsePayload(payload)
	require.NoError(t, err)
	digest, err := parsed.DigestHex()
	require.NoError(t, err)
	require.Equal(t, "5974370796021d48c883dd778f5bcbb2a25fc0db8132acce2ace2e9c5014d8f9", digest)

	statement, err := signer.SignCheckpoint(t.Context(), payload)
	require.NoError(t, err)
	require.Equal(t, `{"key_id":"03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8","kind":"mmr_checkpoint","log_id":"interop-log","mmr_size":7,"prev_root":"cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd","prev_size":3,"root":"abababababababababababababababababababababababababababababababab","signature":"9b89ecbab0d9c03f259dde645f5a2ece2cb24f737646742733eff2997386e041556a8278b4f1dd2554c00ddceca0d000cd746dc1d8447d3ca2d3bb0497fd9602","timestamp":"2026-08-27T12:34:56Z","v":1}`, string(statement))
}

func TestParsePayloadRejectsNonCanonicalOrUnknownFields(t *testing.T) {
	_, err := ParsePayload([]byte(`{"kind":"mmr_checkpoint","unknown":true}`))
	require.Error(t, err)
}

func TestCadenceBoundaries(t *testing.T) {
	config := DefaultConfig()
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	require.False(t, config.Due(0, now.Add(-time.Hour), now))
	require.True(t, config.Due(100, now, now))
	require.True(t, config.Due(1, now.Add(-15*time.Minute), now))
	require.False(t, config.Overdue(200))
	require.True(t, config.Overdue(201))
}
