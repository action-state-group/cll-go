package anchor

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/ethanyzhang/capsule-ledger-go/checkpoint"
	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
	"github.com/veraison/go-cose"
)

func TestReceiptVerifierBindsStatementProofAndPinnedKey(t *testing.T) {
	_, statementPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	statementSigner, err := checkpoint.NewEd25519Signer("checkpoint-key", statementPrivate)
	require.NoError(t, err)
	statement, err := statementSigner.SignCheckpoint(t.Context(), []byte(`{"artifact_type":"mmr-checkpoint"}`))
	require.NoError(t, err)
	entry, err := EntryHash(statement)
	require.NoError(t, err)

	logPublic, logPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := cbor.Marshal([]any{int64(1), int64(0), [][]byte{}})
	require.NoError(t, err)
	leaf := sha256.Sum256(append([]byte{0}, entry...))
	receiptMessage := cose.NewSign1Message()
	receiptMessage.Headers.Protected.SetAlgorithm(cose.AlgorithmEdDSA)
	receiptMessage.Headers.Protected[headerVDS] = vdsRFC9162
	receiptMessage.Headers.Unprotected[headerVDP] = map[any]any{vdpInclusion: []any{proof}}
	receiptMessage.Payload = leaf[:]
	logSigner, err := cose.NewSigner(cose.AlgorithmEdDSA, logPrivate)
	require.NoError(t, err)
	require.NoError(t, receiptMessage.Sign(rand.Reader, nil, logSigner))
	receiptMessage.Payload = nil
	receiptBytes, err := receiptMessage.MarshalCBOR()
	require.NoError(t, err)

	verifier, err := NewReceiptVerifier(logPublic)
	require.NoError(t, err)
	receipt := Receipt{Bytes: receiptBytes, EntryHash: hex.EncodeToString(entry), EntryHashScheme: "sig_structure", LeafIndex: 0, TreeSize: 1}
	require.NoError(t, verifier.Verify(statement, receipt))
	receipt.EntryHash = hex.EncodeToString(make([]byte, 32))
	require.Error(t, verifier.Verify(statement, receipt))
	receipt.EntryHash = hex.EncodeToString(entry)
	receipt.EntryHashScheme = "legacy"
	require.Error(t, verifier.Verify(statement, receipt))
}
