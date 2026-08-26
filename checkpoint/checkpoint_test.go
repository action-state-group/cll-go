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
	signer, err := NewEd25519Signer("peer-1", private)
	require.NoError(t, err)
	payload, err := (Payload{
		LogID: "alchemy", KeyID: signer.KeyID(), MMRSize: 7,
		Root:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Timestamp: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}).CanonicalJSON()
	require.NoError(t, err)
	require.Contains(t, string(payload), `"artifact_type":"mmr-checkpoint"`)
	require.Contains(t, string(payload), `"kind":"mmr_checkpoint"`)
	require.Contains(t, string(payload), `"v":1`)
	parsed, err := ParsePayload(payload)
	require.NoError(t, err)
	require.Equal(t, uint64(7), parsed.MMRSize)

	statement, err := signer.SignCheckpoint(t.Context(), payload)
	require.NoError(t, err)
	require.NoError(t, signer.VerifyCheckpoint(payload, statement))
	tampered := bytes.Replace(payload, []byte("alchemy"), []byte("changed"), 1)
	require.Error(t, signer.VerifyCheckpoint(tampered, statement))
}

func TestParsePayloadRejectsNonCanonicalOrUnknownFields(t *testing.T) {
	_, err := ParsePayload([]byte(`{"artifact_type":"mmr-checkpoint","unknown":true}`))
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
